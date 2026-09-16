package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/telemetry"
	"github.com/asalimonov/montygo/internal/wasmblob"
	"github.com/asalimonov/montygo/internal/wire"
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
	// BackendDocker reaches monty-server in a container montygo runs; see NewDocker.
	BackendDocker
)

func (b Backend) String() string {
	switch b {
	case BackendNative:
		return "native"
	case BackendWasm:
		return "wasm"
	case BackendWebSocket:
		return "websocket"
	case BackendDocker:
		return "docker"
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
	// MaxPendingBytes bounds worker output buffered by the parent per native or
	// wasm worker: 0 means 64 MiB, UnlimitedPendingBytes disables the bound.
	MaxPendingBytes int64
	// Telemetry selects this pool's telemetry; nil uses the process-wide installation.
	Telemetry *TelemetryComponents
	// Stop is the default stop policy of this pool's sessions; zero fields inherit DefaultStopPolicy.
	Stop StopPolicy
}

// UnlimitedPendingBytes disables the buffered frame bound.
const UnlimitedPendingBytes int64 = -1

const defaultMaxPendingBytes int64 = 64 << 20

// PoolStats counts a pool's workers by state.
type PoolStats struct {
	Starting, Active, Idle, Retiring int
}

// Pool is an elastic set of sandbox workers.
type Pool struct {
	inner   *pool.Pool
	backend Backend
	binary  string
	closed  atomic.Bool
	rec     *telemetry.Recorder
	metered bool
	// connectHeaders supplies per-checkout WebSocket upgrade headers.
	connectHeaders func(ctx context.Context) (map[string]string, error)
	sessionsMu     sync.Mutex
	sessions       map[*Session]struct{}
	stop           StopPolicy
	// recovery retries a supervised dial; supervised marks a pool whose endpoint
	// comes from a ServerSupervisor rather than a fixed URL.
	recovery   *recoverer
	supervised bool
	// rotation moves a session to a fresh connection before the server's deadline.
	rotation *rotationPolicy
	// owned is the supervisor this pool closes with itself; nil for a pool whose
	// supervisor belongs to the caller.
	owned     interface{ Close(context.Context) error }
	ownedOnce sync.Once
	ownedStop time.Duration
}

// New creates a pool.
func New(ctx context.Context, opts Options) (*Pool, error) {
	rec := resolveRecorder(opts.Telemetry)
	spawner, backend, binary, err := resolveSpawner(ctx, opts, rec)
	if err != nil {
		return nil, err
	}
	return newPool(ctx, opts, spawner, backend, binary, false, rec)
}

func pendingBytes(opts Options) int64 {
	switch {
	case opts.MaxPendingBytes == 0:
		return defaultMaxPendingBytes
	case opts.MaxPendingBytes < 0:
		return 0
	}
	return opts.MaxPendingBytes
}

func pendingObserver(rec *telemetry.Recorder) worker.PendingBytesObserver {
	if !rec.Metering() {
		return nil
	}
	m := telemetry.NewPoolMetrics(rec)
	return m.PendingBytes
}

func newPool(ctx context.Context, opts Options, spawner worker.Spawner, backend Backend, binary string, singleUse bool, rec *telemetry.Recorder) (*Pool, error) {
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
	stop, err := effectivePolicy(DefaultStopPolicy, []StopPolicy{opts.Stop})
	if err != nil {
		return nil, err
	}
	metrics := poolMetrics(rec)
	p := &Pool{backend: backend, binary: binary, rec: rec, metered: metrics != nil, sessions: map[*Session]struct{}{}, stop: stop}
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
		MontyVersion:          montyVersion,
		ProtocolVersion:       protocolVersion,
		Metrics:               metrics,
	}
	inner, err := pool.New(ctx, cfg)
	if err != nil {
		return nil, spawnError(err)
	}
	p.inner = inner
	return p, nil
}

func resolveSpawner(ctx context.Context, opts Options, rec *telemetry.Recorder) (worker.Spawner, Backend, string, error) {
	pending, observe := pendingBytes(opts), pendingObserver(rec)
	switch opts.Backend {
	case BackendNative:
		bin, err := FindMontyBinary(opts.BinaryPath)
		if err != nil {
			return nil, 0, "", err
		}
		return newSubprocessSpawner(bin, opts.WorkerStderr, pending, observe), BackendNative, bin, nil
	case BackendWasm:
		s, err := wasmSpawner(ctx, opts, pending, observe)
		return s, BackendWasm, "", err
	case BackendAuto:
		if bin, err := FindMontyBinary(opts.BinaryPath); err == nil && nativeSupported {
			return newSubprocessSpawner(bin, opts.WorkerStderr, pending, observe), BackendNative, bin, nil
		} else if opts.BinaryPath != "" {
			return nil, 0, "", err
		}
		s, err := wasmSpawner(ctx, opts, pending, observe)
		return s, BackendWasm, "", err
	}
	if opts.Backend == BackendDocker {
		return nil, 0, "", &OptionError{Message: "use NewDocker for the Docker backend"}
	}
	return nil, 0, "", &OptionError{Message: "use NewWebSocket for the WebSocket backend"}
}

func wasmSpawner(ctx context.Context, opts Options, pending int64, observe worker.PendingBytesObserver) (worker.Spawner, error) {
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
	return &poolWasmSpawner{WasmSpawner: s, stderr: opts.WorkerStderr, pending: pending, observe: observe}, nil
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

// OwnSupervisor makes the pool responsible for closing c: Shutdown stops it
// after closing the sessions, and Close stops it once the last session closes.
// A supervisor the caller keeps is never closed by the pool.
func (p *Pool) OwnSupervisor(c interface{ Close(context.Context) error }, stopTimeout time.Duration) {
	p.owned = c
	p.ownedStop = stopTimeout
}

// observe builds this pool's per-checkout telemetry observer.
func (p *Pool) observe(parent context.Context) func(pid int, hasPID bool) pool.Observer {
	rec, metered := p.rec, p.metered
	return func(pid int, hasPID bool) pool.Observer {
		if o := telemetry.NewCheckout(rec, parent, pid, hasPID, metered); o != nil {
			return o
		}
		return nil
	}
}

// Backend reports the transport the pool uses.
func (p *Pool) Backend() Backend { return p.backend }

// BinaryPath is the native worker binary, or "" for other backends.
func (p *Pool) BinaryPath() string { return p.binary }

// Close shuts down idle workers; it is idempotent.
func (p *Pool) Close(ctx context.Context) error {
	p.sessionsMu.Lock()
	alreadyClosed := p.closed.Swap(true)
	open := len(p.sessions)
	p.sessionsMu.Unlock()
	if alreadyClosed {
		return nil
	}
	err := p.inner.Close(ctx)
	// Open sessions keep their server: the last one to close stops it.
	if open == 0 {
		err = errors.Join(err, p.closeOwned(ctx))
	}
	return err
}

// Shutdown ends admission, closes every open session with the stop policy
// and waits for the workers to exit. ctx bounds only the caller's wait.
func (p *Pool) Shutdown(ctx context.Context, policy ...StopPolicy) error {
	if len(policy) > 1 {
		return &OptionError{Message: "at most one stop policy"}
	}
	p.sessionsMu.Lock()
	p.closed.Store(true)
	open := make([]*Session, 0, len(p.sessions))
	for s := range p.sessions {
		open = append(open, s)
	}
	p.sessionsMu.Unlock()
	errs := make([]error, len(open))
	var wg sync.WaitGroup
	for i, s := range open {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Close(ctx, policy...)
		}()
	}
	wg.Wait()
	if err := p.inner.Shutdown(ctx); err != nil {
		return errors.Join(err, p.closeOwned(ctx))
	}
	if err := p.closeOwned(ctx); err != nil {
		return err
	}
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return ctx.Err()
}

// Run checks out a session, feeds code once and closes the session.
func (p *Pool) Run(ctx context.Context, code string, opts *RunOptions) (any, error) {
	if opts == nil {
		opts = &RunOptions{}
	}
	s, err := p.Checkout(ctx, opts.CheckoutOptions)
	if err != nil {
		return nil, err
	}
	v, err := s.FeedRun(ctx, code, &opts.FeedOptions)
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.limits.stop.Timeout+s.limits.stop.Join)
	defer cancel()
	_ = s.Close(cctx)
	return v, err
}

// RunOptions configure a one-shot run.
type RunOptions struct {
	CheckoutOptions
	FeedOptions
}

// Stats counts the pool's workers by state.
func (p *Pool) Stats() PoolStats {
	s := p.inner.Stats()
	return PoolStats{Starting: s.Starting, Active: s.Active, Idle: s.Idle, Retiring: s.Retiring}
}

func (p *Pool) track(s *Session) error {
	p.sessionsMu.Lock()
	defer p.sessionsMu.Unlock()
	if p.closed.Load() {
		return ErrPoolClosed
	}
	if err := s.Err(); err != nil {
		return err
	}
	p.sessions[s] = struct{}{}
	return nil
}

func (p *Pool) untrack(s *Session) {
	p.sessionsMu.Lock()
	delete(p.sessions, s)
	last := p.closed.Load() && len(p.sessions) == 0
	p.sessionsMu.Unlock()
	if last && p.owned != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), p.ownedStop+5*time.Second)
			defer cancel()
			_ = p.closeOwned(ctx)
		}()
	}
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
	limits, err := opts.sessionLimits(p.stop)
	if err != nil {
		return nil, err
	}
	s := newSession(p, cfg, limits)
	co, dialStart, err := p.dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.attach(co, dialStart)
	if opts.Host != nil {
		if err := opts.Host.Register(s.store); err != nil {
			_ = s.Close(ctx, KillNow)
			return nil, err
		}
		s.host = opts.Host
	}
	if err := p.track(s); err != nil {
		_ = s.Close(ctx, KillNow)
		return nil, err
	}
	return s, nil
}

// dial opens one session's connection. A supervised pool resolves its endpoint
// per attempt and retries under the recovery policy; every other pool dials once.
// The returned time is taken before the dial, so a session deadline derived from
// it is never later than the server's.
func (p *Pool) dial(ctx context.Context, cfg wire.Configure) (*pool.Checkout, time.Time, error) {
	if !p.supervised {
		headers := traceContextHeaders(p.rec, ctx)
		if p.connectHeaders != nil {
			extra, err := p.connectHeaders(ctx)
			if err != nil {
				return nil, time.Time{}, err
			}
			headers = append(headers[:len(headers):len(headers)], sortedHeaders(extra)...)
		}
		if len(headers) > 0 {
			ctx = pool.WithConnectHeaders(ctx, headers)
		}
		dialStart := time.Now()
		co, err := p.inner.Checkout(ctx, cfg, pool.CheckoutOptions{Observe: p.observe(ctx)})
		if err != nil {
			return nil, time.Time{}, checkoutError(err)
		}
		return co, dialStart, nil
	}
	res, err := p.inner.Reserve(ctx)
	if err != nil {
		return nil, time.Time{}, checkoutError(err)
	}
	co, dialStart, err := p.bind(ctx, res, cfg, nil)
	if err != nil {
		res.Release()
		if ctx.Err() != nil {
			return nil, time.Time{}, ctx.Err()
		}
		return nil, time.Time{}, &SpawnError{Message: "monty-server unreachable " + err.Error()}
	}
	return co, dialStart, nil
}

// bind runs the recovery policy over one reservation: a fresh session with a nil
// state, or a rotated one carrying its dump.
func (p *Pool) bind(ctx context.Context, res *pool.Reservation, cfg wire.Configure, state []byte) (*pool.Checkout, time.Time, error) {
	var dialStart time.Time
	co, attempts, err := p.recovery.Do(ctx, func(actx context.Context, _ ServerEndpoint) (*pool.Checkout, error) {
		dialStart = time.Now()
		return p.inner.Bind(actx, res, cfg, state, pool.CheckoutOptions{Observe: p.observe(ctx)})
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("after %d attempts: %w", attempts, err)
	}
	return co, dialStart, nil
}

// closeOwned closes the supervisor this pool owns, exactly once.
func (p *Pool) closeOwned(ctx context.Context) error {
	if p.owned == nil {
		return nil
	}
	var err error
	p.ownedOnce.Do(func() { err = p.owned.Close(ctx) })
	return err
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
