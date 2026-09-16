package montygo

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
	itel "github.com/asalimonov/montygo/internal/telemetry"
	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/telemetry"
)

// PoolOptions configure a pool. Workers is Auto() when nil.
type PoolOptions struct {
	Workers WorkerSource
	// MinWorkers prewarmed workers: 0 means 1, a negative value means none.
	MinWorkers int
	// MaxWorkers caps live workers: 0 means runtime.NumCPU().
	MaxWorkers int
	// CheckoutTimeout bounds waiting for a free worker: 0 waits forever.
	CheckoutTimeout time.Duration
	// RequestTimeout is the hard per-turn deadline: 0 means none for local
	// workers and 10s for remote ones; NoRequestTimeout disables it.
	RequestTimeout time.Duration
	// DurationLimitGrace pads the max-duration backstop: 0 means 1s; NoDurationLimitGrace disables it.
	DurationLimitGrace time.Duration
	// MaxCheckoutsPerWorker recycles a local worker after that many sessions: 0 means unlimited.
	MaxCheckoutsPerWorker int
	// MaxPendingBytes bounds worker output buffered by the parent per local
	// worker: 0 means 64 MiB, UnlimitedPendingBytes disables the bound.
	MaxPendingBytes int64
	// WorkerStderr receives worker diagnostics: nil means os.Stderr.
	WorkerStderr io.Writer
	// Telemetry receives this pool's spans, metrics and logs; nil records nothing.
	Telemetry *telemetry.Components
	// Stop is the stop policy sessions inherit; zero fields mean Timeout 3s and Join 3s.
	Stop StopPolicy
}

// PoolStats counts a pool's workers by state.
type PoolStats struct {
	Starting, Active, Idle, Retiring int
}

// Pool is an elastic set of sandbox workers.
type Pool struct {
	inner   *pool.Pool
	kind    WorkerKind
	binary  string
	closed  atomic.Bool
	rec     *itel.Recorder
	metered bool

	sessionsMu sync.Mutex
	sessions   map[*Session]struct{}
	stop       StopPolicy
	// recovery dials remote workers under the recovery policy; nil for local workers.
	recovery *recoverer
	// static is set for a fixed server URL: its endpoint is resolved once per
	// checkout and dialed once, so a headers error reaches the caller unchanged.
	static ServerSupervisor
	// rotation moves a session to a fresh connection before the server's deadline.
	rotation *rotationPolicy
}

// NewPool creates a pool.
func NewPool(ctx context.Context, opts PoolOptions) (*Pool, error) {
	src := opts.Workers
	if src == nil {
		src = Auto()
	}
	stop, err := effectivePolicy(builtinStopPolicy, []StopPolicy{opts.Stop})
	if err != nil {
		return nil, err
	}
	rec := resolveRecorder(opts.Telemetry)
	spawner, binary, remote, err := src.spawner(ctx, opts, rec)
	if err != nil {
		return nil, err
	}
	kind := src.kind()
	if binary == "" && kind == WorkerNative {
		kind = WorkerWasm
	}
	minWorkers := opts.MinWorkers
	switch {
	case minWorkers == 0:
		minWorkers = 1
	case minWorkers < 0:
		minWorkers = 0
	}
	maxWorkers := opts.MaxWorkers
	if maxWorkers == 0 {
		maxWorkers = runtime.NumCPU()
	}
	if maxWorkers < 1 {
		return nil, &monterr.OptionError{Message: "maxWorkers must be at least 1"}
	}
	if remote == nil && minWorkers > maxWorkers {
		return nil, &monterr.OptionError{Message: "minWorkers cannot exceed maxWorkers"}
	}
	grace := opts.DurationLimitGrace
	if grace == 0 {
		grace = time.Second
	}
	requestTimeout := opts.RequestTimeout
	switch {
	case requestTimeout == 0 && remote != nil:
		requestTimeout = defaultRemoteRequestTimeout
	case requestTimeout < 0:
		requestTimeout = 0
	}
	metrics := poolMetrics(rec)
	p := &Pool{kind: kind, binary: binary, rec: rec, metered: metrics != nil, sessions: map[*Session]struct{}{}, stop: stop}
	if remote != nil {
		p.recovery = newRecoverer(remote.sup, remote.opts.Recovery, rec)
		if _, fixed := remote.sup.(staticServer); fixed {
			p.static = remote.sup
		}
		if remote.opts.RotateSessions {
			info, err := FetchServerInfo(ctx, remote.sup, remote.opts)
			switch {
			case errors.Is(err, monterr.ErrNoServerInfo):
			case err != nil:
				return nil, err
			default:
				p.rotation = newRotationPolicy(info, remote.opts.RotationMargin)
			}
		}
	}
	cfg := pool.Config{
		Spawner:               spawner,
		MinProcesses:          minWorkers,
		MaxProcesses:          maxWorkers,
		CheckoutTimeout:       opts.CheckoutTimeout,
		RequestTimeout:        requestTimeout,
		DurationLimitGrace:    grace,
		GraceDisabled:         opts.DurationLimitGrace == NoDurationLimitGrace,
		MaxCheckoutsPerWorker: opts.MaxCheckoutsPerWorker,
		SingleUse:             remote != nil,
		MontyVersion:          MontyVersion,
		ProtocolVersion:       ProtocolVersion,
		Metrics:               metrics,
	}
	inner, err := pool.New(ctx, cfg)
	if err != nil {
		return nil, spawnError(err)
	}
	p.inner = inner
	return p, nil
}

// observe builds this pool's per-checkout telemetry observer.
func (p *Pool) observe(parent context.Context) func(pid int, hasPID bool) pool.Observer {
	rec, metered := p.rec, p.metered
	return func(pid int, hasPID bool) pool.Observer {
		if o := itel.NewCheckout(rec, parent, pid, hasPID, metered); o != nil {
			return o
		}
		return nil
	}
}

// Workers reports how the pool reaches its workers.
func (p *Pool) Workers() WorkerKind { return p.kind }

// BinaryPath is the native worker binary, or "" for other workers.
func (p *Pool) BinaryPath() string { return p.binary }

// Close shuts down idle workers; it is idempotent. Open sessions keep their
// workers until they close.
func (p *Pool) Close(ctx context.Context) error {
	if p.closed.Swap(true) {
		return nil
	}
	return p.inner.Close(ctx)
}

// Shutdown ends admission, closes every open session with the stop policy
// and waits for the workers to exit. ctx bounds only the caller's wait.
func (p *Pool) Shutdown(ctx context.Context, policy ...StopPolicy) error {
	if len(policy) > 1 {
		return &monterr.OptionError{Message: "at most one stop policy"}
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
		return err
	}
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return ctx.Err()
}

// Run checks out a session of rt, feeds code once and closes the session.
func (p *Pool) Run(ctx context.Context, rt *Runtime, code string, opts *RunOptions) (any, error) {
	if opts == nil {
		opts = &RunOptions{}
	}
	s, err := p.Checkout(ctx, rt, opts.CheckoutOptions)
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
		return monterr.ErrPoolClosed
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
	p.sessionsMu.Unlock()
}

// Checkout dedicates a worker to a new session of rt.
func (p *Pool) Checkout(ctx context.Context, rt *Runtime, opts CheckoutOptions) (*Session, error) {
	if rt == nil {
		return nil, &monterr.OptionError{Message: "runtime is required"}
	}
	if p.closed.Load() {
		return nil, monterr.ErrPoolClosed
	}
	cfg, err := rt.configure(opts.ScriptName, opts.Limits)
	if err != nil {
		return nil, err
	}
	limits, err := rt.sessionLimits(p.stop, opts.Stop)
	if err != nil {
		return nil, err
	}
	s := newSession(p, rt, cfg, limits)
	co, dialStart, err := p.dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.attach(co, dialStart)
	if h := rt.opts.Host; h != nil {
		if err := h.Register(s.store); err != nil {
			_ = s.Close(ctx, KillNow)
			return nil, err
		}
	}
	if err := p.track(s); err != nil {
		_ = s.Close(ctx, KillNow)
		return nil, err
	}
	return s, nil
}

// dial opens one session's connection. A supervised pool resolves its endpoint
// per attempt and retries under the recovery policy; a local pool, or one with
// a fixed URL, dials once. The returned time is taken before the dial, so a
// session deadline derived from it is never later than the server's.
func (p *Pool) dial(ctx context.Context, cfg wire.Configure) (*pool.Checkout, time.Time, error) {
	if p.recovery == nil || p.static != nil {
		if p.static != nil {
			ep, err := p.static.Endpoint(ctx)
			if err != nil {
				return nil, time.Time{}, err
			}
			ctx = p.recovery.withEndpoint(ctx, ep)
		} else if headers := traceContextHeaders(p.rec, ctx); len(headers) > 0 {
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
		return nil, time.Time{}, &monterr.SpawnError{Message: "monty-server unreachable " + err.Error()}
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

func spawnError(err error) error {
	var perr *pool.Error
	if errors.As(err, &perr) && perr.Kind == pool.KindSpawn {
		return &monterr.SpawnError{Message: perr.Error()}
	}
	return err
}

func checkoutError(err error) error {
	var perr *pool.Error
	if !errors.As(err, &perr) {
		return err
	}
	switch perr.Kind {
	case pool.KindExhausted:
		return monterr.ErrCheckoutTimeout
	case pool.KindClosed:
		return monterr.ErrPoolClosed
	case pool.KindSpawn:
		return &monterr.SpawnError{Message: perr.Error()}
	case pool.KindCrashed:
		return &monterr.CrashedError{Message: perr.Error(), ExitStatus: perr.Status.String()}
	case pool.KindTimeout:
		return &monterr.CrashedError{Message: perr.Error(), TimedOut: true}
	case pool.KindDisconnected:
		return &monterr.DisconnectError{Message: perr.Error()}
	case pool.KindShutdown:
		return &monterr.ShutdownError{Message: perr.Error(), Dump: perr.Dump}
	case pool.KindRuntime:
		return monterr.ErrorFromException(perr.Exception)
	case pool.KindCancelled:
		if perr.Cause != nil {
			return perr.Cause
		}
	}
	return &monterr.ProtocolError{Message: perr.Error()}
}

var _ worker.Spawner = (*poolWasmSpawner)(nil)
