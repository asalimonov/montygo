package worker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"github.com/asalimonov/montygo/internal/wire"
)

// WasmSpawner instantiates the embedded wasip1 worker under wazero. The module
// is compiled once and shared by every instance.
type WasmSpawner struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	Stderr   io.Writer
}

type wasmKey struct {
	sum      string
	cacheDir string
}

var (
	wasmMu       sync.Mutex
	wasmSpawners = map[wasmKey]*WasmSpawner{}
)

// SharedWasmSpawner returns a process-wide spawner for the blob, compiling it on first use.
func SharedWasmSpawner(ctx context.Context, blob []byte, sum, cacheDir string) (*WasmSpawner, error) {
	wasmMu.Lock()
	defer wasmMu.Unlock()
	key := wasmKey{sum: sum, cacheDir: cacheDir}
	if s, ok := wasmSpawners[key]; ok {
		return s, nil
	}
	s, err := NewWasmSpawner(ctx, blob, cacheDir)
	if err != nil {
		return nil, err
	}
	wasmSpawners[key] = s
	return s, nil
}

// NewWasmSpawner compiles blob; cacheDir enables wazero's on-disk compilation cache.
func NewWasmSpawner(ctx context.Context, blob []byte, cacheDir string) (*WasmSpawner, error) {
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o755); err == nil {
			if cache, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
				cfg = cfg.WithCompilationCache(cache)
			}
		}
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, err
	}
	compiled, err := rt.CompileModule(ctx, blob)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("failed to compile the embedded monty worker: %w", err)
	}
	return &WasmSpawner{rt: rt, compiled: compiled}, nil
}

func (s *WasmSpawner) Kind() Kind { return KindWasm }

// Close is a no-op for the shared spawner; the runtime lives for the process.
func (s *WasmSpawner) Close(context.Context) error { return nil }

func (s *WasmSpawner) Spawn(ctx context.Context) (Worker, error) {
	return s.SpawnWithStderr(ctx, s.Stderr)
}

// SpawnWithStderr instantiates a worker writing diagnostics to stderr.
func (s *WasmSpawner) SpawnWithStderr(ctx context.Context, stderr io.Writer) (Worker, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	runCtx, cancel := context.WithCancel(context.Background())
	w := &wasmWorker{stdin: inW, stdinR: inR, queue: newFrameQueue(), cancel: cancel, done: make(chan struct{})}
	if stderr == nil {
		stderr = os.Stderr
	}
	cfg := wazero.NewModuleConfig().
		WithStdin(inR).
		WithStdout(outW).
		WithStderr(stderr).
		WithArgs("monty", "subprocess").
		WithSysNanotime().
		WithSysWalltime().
		WithRandSource(rand.Reader).
		WithName("")
	go func() {
		mod, err := s.rt.InstantiateModule(runCtx, s.compiled, cfg)
		if mod != nil {
			_ = mod.Close(context.Background())
		}
		w.status = wasmStatus(err, w.killed.Load())
		_ = outW.Close()
		_ = inR.Close()
		close(w.done)
	}()
	go pumpFrames(wire.NewFrameReader(outR), w.queue)
	return w, nil
}

func wasmStatus(err error, killed bool) Status {
	if err == nil {
		return Status{Known: true, Exited: true, Code: 0}
	}
	var exit *sys.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case sys.ExitCodeContextCanceled, sys.ExitCodeDeadlineExceeded:
			return Status{Known: true, Killed: true}
		}
		return Status{Known: true, Exited: true, Code: int(exit.ExitCode())}
	}
	if killed {
		return Status{Known: true, Killed: true}
	}
	return Status{}
}

type wasmWorker struct {
	stdin  *io.PipeWriter
	stdinR *io.PipeReader
	queue  *frameQueue
	cancel context.CancelFunc
	done   chan struct{}
	status Status
	sendMu sync.Mutex
	kill   sync.Once
	killed atomicBool
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) Store(v bool) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomicBool) Load() bool   { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

func (w *wasmWorker) Send(ctx context.Context, payload []byte) error {
	frame, err := wire.AppendFrameHeader(payload)
	if err != nil {
		return err
	}
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	stop := context.AfterFunc(ctx, w.Kill)
	defer stop()
	if _, err := w.stdin.Write(frame); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

func (w *wasmWorker) Recv(ctx context.Context) ([]byte, error) { return w.queue.pop(ctx) }

func (w *wasmWorker) Kill() {
	w.kill.Do(func() {
		w.killed.Store(true)
		w.cancel()
		_ = w.stdin.CloseWithError(io.EOF)
		_ = w.stdinR.CloseWithError(io.EOF)
	})
}

func (w *wasmWorker) Close() { w.Kill() }

func (w *wasmWorker) Wait(ctx context.Context) (Status, bool) {
	select {
	case <-w.done:
		return w.status, true
	case <-ctx.Done():
		return Status{}, false
	}
}

func (w *wasmWorker) PID() (int, bool) { return 0, false }
func (w *wasmWorker) Kind() Kind       { return KindWasm }

func (w *wasmWorker) Alive() bool {
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}
