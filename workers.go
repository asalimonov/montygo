package montygo

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"time"

	itel "github.com/asalimonov/montygo/internal/telemetry"
	"github.com/asalimonov/montygo/internal/wasmblob"
	"github.com/asalimonov/montygo/internal/worker"
	"github.com/asalimonov/montygo/monterr"
)

// WorkerKind says how a pool reaches its workers.
type WorkerKind int

const (
	// WorkerNative runs `monty subprocess` children; unix only.
	WorkerNative WorkerKind = iota + 1
	// WorkerWasm runs the embedded wasip1 worker under wazero.
	WorkerWasm
	// WorkerRemote dials one WebSocket connection per session to a supervised monty-server.
	WorkerRemote
)

func (k WorkerKind) String() string {
	switch k {
	case WorkerNative:
		return "native"
	case WorkerWasm:
		return "wasm"
	case WorkerRemote:
		return "websocket"
	}
	return "unknown"
}

// NoRequestTimeout disables the per-turn deadline.
const NoRequestTimeout time.Duration = -1

// NoDurationLimitGrace disables the max-duration backstop.
const NoDurationLimitGrace time.Duration = -1

// UnlimitedPendingBytes disables the buffered frame bound.
const UnlimitedPendingBytes int64 = -1

const (
	defaultMaxPendingBytes      int64 = 64 << 20
	defaultRemoteRequestTimeout       = 10 * time.Second
	defaultDialTimeout                = 10 * time.Second
)

// WorkerSource says where a pool's workers come from. Values come from Native,
// Wasm, Remote and Auto.
type WorkerSource interface {
	kind() WorkerKind
	// spawner builds the pool's spawner; binary is the native worker path and
	// remote is set for remote workers.
	spawner(ctx context.Context, opts PoolOptions, rec *itel.Recorder) (spawner worker.Spawner, binary string, remote *remoteSource, err error)
}

// NativeOptions configure `monty subprocess` workers.
type NativeOptions struct {
	// BinaryPath is the worker binary; "" resolves MONTY_BIN, PATH, then a cargo target directory.
	BinaryPath string
}

// WasmOptions configure the embedded wasm worker.
type WasmOptions struct {
	// CacheDir holds wazero's compilation cache: "" means the user cache dir.
	CacheDir string
	// DisableCache compiles in memory only.
	DisableCache bool
}

// RemoteOptions configure sessions dialed to a supervised monty-server.
type RemoteOptions struct {
	// DialTimeout bounds DNS, TCP, TLS and the upgrade of one attempt: 0 means 10s.
	DialTimeout time.Duration
	// TLSConfig configures wss:// dials; nil uses the system roots. An endpoint's
	// own TLSConfig overrides it. It is cloned per dial.
	TLSConfig *tls.Config
	// DialContext opens TCP connections; nil uses a net.Dialer.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// Recovery bounds retries of a dial and allows a supervisor restart.
	Recovery RecoveryPolicy
	// RotateSessions moves each session to a fresh connection before the server's
	// session timeout closes it. It reads GET /info once; a server without the
	// endpoint, or without usable timeouts, leaves rotation off.
	RotateSessions bool
	// RotationMargin is the lead time of a rotation: 0 means 30s.
	RotationMargin time.Duration
}

func (o RemoteOptions) validate() error {
	switch {
	case o.Recovery.Attempts < 0:
		return &monterr.OptionError{Message: "recovery.attempts must not be negative"}
	case o.Recovery.AttemptTimeout < 0:
		return &monterr.OptionError{Message: "recovery.attemptTimeout must not be negative"}
	case o.RotationMargin < 0:
		return &monterr.OptionError{Message: "rotationMargin must not be negative"}
	case o.DialTimeout < 0:
		return &monterr.OptionError{Message: "dialTimeout must not be negative"}
	}
	return nil
}

func (o RemoteOptions) dialTimeout() time.Duration {
	if o.DialTimeout == 0 {
		return defaultDialTimeout
	}
	return o.DialTimeout
}

// dialer builds the transport of one endpoint. url is "" for a pool, whose
// endpoint travels per attempt through the context.
func (o RemoteOptions) dialer(url string, tlsConfig *tls.Config) *worker.WebSocketDialer {
	if tlsConfig == nil {
		tlsConfig = o.TLSConfig
	}
	return &worker.WebSocketDialer{
		URL:         url,
		DialTimeout: o.dialTimeout(),
		UserAgent:   userAgent(),
		TLSConfig:   tlsConfig,
		DialContext: o.DialContext,
	}
}

// Native selects `monty subprocess` workers.
func Native(opts NativeOptions) WorkerSource { return nativeSource{opts: opts} }

// Wasm selects the embedded wasm worker.
func Wasm(opts WasmOptions) WorkerSource { return wasmSource{opts: opts} }

// Remote selects sessions dialed to the server sup supervises. The pool never
// owns sup: the application closes it after the pool.
func Remote(sup ServerSupervisor, opts RemoteOptions) WorkerSource {
	return &remoteSource{sup: sup, opts: opts}
}

// Auto picks Native when a worker binary resolves and Wasm otherwise. It never dials.
func Auto() WorkerSource { return autoSource{} }

type nativeSource struct{ opts NativeOptions }

func (nativeSource) kind() WorkerKind { return WorkerNative }

func (s nativeSource) spawner(_ context.Context, opts PoolOptions, rec *itel.Recorder) (worker.Spawner, string, *remoteSource, error) {
	bin, err := FindMontyBinary(s.opts.BinaryPath)
	if err != nil {
		return nil, "", nil, err
	}
	return newSubprocessSpawner(bin, opts.WorkerStderr, opts.pendingBytes(), pendingObserver(rec)), bin, nil, nil
}

type wasmSource struct{ opts WasmOptions }

func (wasmSource) kind() WorkerKind { return WorkerWasm }

func (s wasmSource) spawner(ctx context.Context, opts PoolOptions, rec *itel.Recorder) (worker.Spawner, string, *remoteSource, error) {
	sp, err := wasmSpawner(ctx, s.opts, opts.WorkerStderr, opts.pendingBytes(), pendingObserver(rec))
	return sp, "", nil, err
}

type autoSource struct{}

func (autoSource) kind() WorkerKind { return WorkerNative }

func (autoSource) spawner(ctx context.Context, opts PoolOptions, rec *itel.Recorder) (worker.Spawner, string, *remoteSource, error) {
	if bin, err := FindMontyBinary(""); err == nil && nativeSupported {
		return newSubprocessSpawner(bin, opts.WorkerStderr, opts.pendingBytes(), pendingObserver(rec)), bin, nil, nil
	}
	sp, err := wasmSpawner(ctx, WasmOptions{}, opts.WorkerStderr, opts.pendingBytes(), pendingObserver(rec))
	return sp, "", nil, err
}

type remoteSource struct {
	sup  ServerSupervisor
	opts RemoteOptions
}

func (*remoteSource) kind() WorkerKind { return WorkerRemote }

func (r *remoteSource) spawner(context.Context, PoolOptions, *itel.Recorder) (worker.Spawner, string, *remoteSource, error) {
	if r.sup == nil {
		return nil, "", nil, &monterr.OptionError{Message: "remote workers need a supervisor"}
	}
	if err := r.opts.validate(); err != nil {
		return nil, "", nil, err
	}
	return r.opts.dialer("", nil), "", r, nil
}

func (o PoolOptions) pendingBytes() int64 {
	switch {
	case o.MaxPendingBytes == 0:
		return defaultMaxPendingBytes
	case o.MaxPendingBytes < 0:
		return 0
	}
	return o.MaxPendingBytes
}

func pendingObserver(rec *itel.Recorder) worker.PendingBytesObserver {
	if !rec.Metering() {
		return nil
	}
	m := itel.NewPoolMetrics(rec)
	return m.PendingBytes
}

func wasmSpawner(ctx context.Context, opts WasmOptions, stderr io.Writer, pending int64, observe worker.PendingBytesObserver) (worker.Spawner, error) {
	blob, err := wasmblob.Bytes()
	if err != nil {
		return nil, err
	}
	cacheDir := opts.CacheDir
	if cacheDir == "" && !opts.DisableCache {
		cacheDir, _ = wasmblob.DefaultCacheDir()
	}
	if opts.DisableCache {
		cacheDir = ""
	}
	s, err := worker.SharedWasmSpawner(ctx, blob, wasmblob.SHA256(), cacheDir)
	if err != nil {
		return nil, err
	}
	return &poolWasmSpawner{WasmSpawner: s, stderr: stderr, pending: pending, observe: observe}, nil
}

// poolWasmSpawner applies one pool's stderr and frame bound to the shared runtime.
type poolWasmSpawner struct {
	*worker.WasmSpawner
	stderr  io.Writer
	pending int64
	observe worker.PendingBytesObserver
}

func (s *poolWasmSpawner) Spawn(ctx context.Context) (worker.Worker, error) {
	return s.SpawnWith(ctx, s.stderr, s.pending, s.observe)
}
