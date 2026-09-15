package monty

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wasmblob"
	"github.com/asalimonov/montygo/internal/worker"
)

// Backend selects how workers are reached.
type Backend int

const (
	// BackendAuto uses a native worker binary when one resolves, else the embedded wasm worker.
	BackendAuto Backend = iota
	BackendNative
	BackendWasm
	BackendWebSocket
)

func (b Backend) String() string {
	switch b {
	case BackendNative:
		return "native"
	case BackendWasm:
		return "wasm"
	case BackendWebSocket:
		return "websocket"
	}
	return "auto"
}

// NoDurationLimitGrace disables the max-duration backstop.
const NoDurationLimitGrace time.Duration = -1

// Options configure a pool.
type Options struct {
	Backend    Backend
	BinaryPath string
	// MinProcesses prewarmed workers: 0 means 1, a negative value means none.
	MinProcesses int
	// MaxProcesses caps live workers: 0 means runtime.NumCPU().
	MaxProcesses int
	// CheckoutTimeout bounds waiting for a free worker: 0 waits forever.
	CheckoutTimeout time.Duration
	// RequestTimeout is the hard per-turn deadline: 0 disables it.
	RequestTimeout time.Duration
	// DurationLimitGrace pads the max-duration backstop: 0 means 1s.
	DurationLimitGrace    time.Duration
	MaxCheckoutsPerWorker int
	// WasmCacheDir holds wazero's compilation cache: "" means the user cache dir.
	WasmCacheDir     string
	DisableWasmCache bool
	// WorkerStderr receives worker diagnostics: nil means os.Stderr.
	WorkerStderr io.Writer
}

// Pool is an elastic set of sandbox workers.
type Pool struct {
	inner   *pool.Pool
	backend Backend
	binary  string
	closed  atomic.Bool
	metered bool
	// connectHeaders supplies per-checkout WebSocket upgrade headers.
	connectHeaders func(ctx context.Context) (map[string]string, error)
}

// New creates a pool.
func New(ctx context.Context, opts Options) (*Pool, error) {
	spawner, backend, binary, err := resolveSpawner(ctx, opts)
	if err != nil {
		return nil, err
	}
	return newPool(ctx, opts, spawner, backend, binary, false)
}

func newPool(ctx context.Context, opts Options, spawner worker.Spawner, backend Backend, binary string, singleUse bool) (*Pool, error) {
	minProcs := opts.MinProcesses
	switch {
	case minProcs == 0:
		minProcs = 1
	case minProcs < 0:
		minProcs = 0
	}
	maxProcs := opts.MaxProcesses
	if maxProcs == 0 {
		maxProcs = runtime.NumCPU()
	}
	if maxProcs < 1 {
		return nil, &OptionError{Message: "maxProcesses must be at least 1"}
	}
	if !singleUse && minProcs > maxProcs {
		return nil, &OptionError{Message: "minProcesses cannot exceed maxProcesses"}
	}
	grace := opts.DurationLimitGrace
	if grace == 0 {
		grace = time.Second
	}
	metrics := currentMetrics()
	cfg := pool.Config{
		Spawner:               spawner,
		MinProcesses:          minProcs,
		MaxProcesses:          maxProcs,
		CheckoutTimeout:       opts.CheckoutTimeout,
		RequestTimeout:        opts.RequestTimeout,
		DurationLimitGrace:    grace,
		GraceDisabled:         opts.DurationLimitGrace == NoDurationLimitGrace,
		MaxCheckoutsPerWorker: opts.MaxCheckoutsPerWorker,
		SingleUse:             singleUse,
		MontyVersion:          Version,
		ProtocolVersion:       ProtocolVersion,
		Metrics:               metrics,
	}
	inner, err := pool.New(ctx, cfg)
	if err != nil {
		return nil, spawnError(err)
	}
	return &Pool{inner: inner, backend: backend, binary: binary, metered: metrics != nil}, nil
}

func resolveSpawner(ctx context.Context, opts Options) (worker.Spawner, Backend, string, error) {
	switch opts.Backend {
	case BackendNative:
		bin, err := FindMontyBinary(opts.BinaryPath)
		if err != nil {
			return nil, 0, "", err
		}
		return newSubprocessSpawner(bin, opts.WorkerStderr), BackendNative, bin, nil
	case BackendWasm:
		s, err := wasmSpawner(ctx, opts)
		return s, BackendWasm, "", err
	case BackendAuto:
		if bin, err := FindMontyBinary(opts.BinaryPath); err == nil && nativeSupported {
			return newSubprocessSpawner(bin, opts.WorkerStderr), BackendNative, bin, nil
		} else if opts.BinaryPath != "" {
			return nil, 0, "", err
		}
		s, err := wasmSpawner(ctx, opts)
		return s, BackendWasm, "", err
	}
	return nil, 0, "", &OptionError{Message: "use NewWebSocket for the WebSocket backend"}
}

func wasmSpawner(ctx context.Context, opts Options) (worker.Spawner, error) {
	blob, err := wasmblob.Bytes()
	if err != nil {
		return nil, err
	}
	cacheDir := opts.WasmCacheDir
	if cacheDir == "" && !opts.DisableWasmCache {
		cacheDir, _ = wasmblob.DefaultCacheDir()
	}
	if opts.DisableWasmCache {
		cacheDir = ""
	}
	s, err := worker.SharedWasmSpawner(ctx, blob, wasmblob.SHA256(), cacheDir)
	if err != nil {
		return nil, err
	}
	if opts.WorkerStderr != nil {
		return &stderrWasmSpawner{WasmSpawner: s, stderr: opts.WorkerStderr}, nil
	}
	return s, nil
}

type stderrWasmSpawner struct {
	*worker.WasmSpawner
	stderr io.Writer
}

func (s *stderrWasmSpawner) Spawn(ctx context.Context) (worker.Worker, error) {
	return s.SpawnWithStderr(ctx, s.stderr)
}

// Backend reports the transport the pool uses.
func (p *Pool) Backend() Backend { return p.backend }

// BinaryPath is the native worker binary, or "" for other backends.
func (p *Pool) BinaryPath() string { return p.binary }

// Close shuts down idle workers; it is idempotent.
func (p *Pool) Close(ctx context.Context) error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	return p.inner.Close(ctx)
}

// Checkout dedicates a worker to a new session.
func (p *Pool) Checkout(ctx context.Context, opts CheckoutOptions) (*Session, error) {
	if p.closed.Load() {
		return nil, ErrPoolClosed
	}
	cfg, err := opts.configure()
	if err != nil {
		return nil, err
	}
	s := &Session{pool: p, store: newInstanceStore(), scriptName: cfg.ScriptName}
	headers := traceContextHeaders(ctx)
	if p.connectHeaders != nil {
		extra, err := p.connectHeaders(ctx)
		if err != nil {
			return nil, err
		}
		headers = append(headers[:len(headers):len(headers)], sortedHeaders(extra)...)
	}
	if len(headers) > 0 {
		ctx = pool.WithConnectHeaders(ctx, headers)
	}
	co, err := p.inner.Checkout(ctx, cfg, pool.CheckoutOptions{Observe: p.observe(ctx)})
	if err != nil {
		return nil, checkoutError(err)
	}
	s.co = co
	return s, nil
}

func sortedHeaders(headers map[string]string) [][2]string {
	pairs := make([][2]string, 0, len(headers))
	for k, v := range headers {
		pairs = append(pairs, [2]string{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	return pairs
}

func spawnError(err error) error {
	var perr *pool.Error
	if errors.As(err, &perr) && perr.Kind == pool.KindSpawn {
		return &SpawnError{Message: perr.Error()}
	}
	return err
}

// SpawnError reports a worker that could not be started or dialed.
type SpawnError struct {
	Message string
}

func (e *SpawnError) Error() string { return e.Message }

func checkoutError(err error) error {
	var perr *pool.Error
	if !errors.As(err, &perr) {
		return err
	}
	switch perr.Kind {
	case pool.KindExhausted:
		return ErrCheckoutTimeout
	case pool.KindClosed:
		return ErrPoolClosed
	case pool.KindSpawn:
		return &SpawnError{Message: perr.Error()}
	case pool.KindCrashed:
		return &CrashedError{Message: perr.Error(), ExitStatus: perr.Status.String()}
	case pool.KindTimeout:
		return &CrashedError{Message: perr.Error(), TimedOut: true}
	case pool.KindDisconnected:
		return &DisconnectError{Message: perr.Error()}
	case pool.KindShutdown:
		return &ShutdownError{Message: perr.Error(), Dump: perr.Dump}
	case pool.KindRuntime:
		return errorFromException(perr.Exception)
	case pool.KindCancelled:
		if perr.Cause != nil {
			return perr.Cause
		}
	}
	return &ProtocolError{Message: perr.Error()}
}
