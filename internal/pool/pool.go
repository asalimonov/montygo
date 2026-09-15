package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
)

const (
	shutdownExitGrace = 500 * time.Millisecond
	fatalExitGrace    = 100 * time.Millisecond
)

// Metrics receives pool-level measurements; every method must be cheap.
type Metrics interface {
	WorkersLive(delta int64)
	WorkersIdle(delta int64)
	WorkersRetiring(delta int64)
	CheckoutWait(d time.Duration, outcome string)
	WorkerTerminated(reason string)
	SessionDuration(d time.Duration, outcome string)
}

// Observer sees one checkout's protocol conversation: every request that
// reached the wire and every decoded event, then Closed exactly once when the
// worker is released or discarded.
type Observer interface {
	Sent(req wire.Request, frameLen int)
	Received(ev *wire.Event, frameLen int)
	Closed()
}

// ContextObserver is an Observer that also supplies host callback contexts.
type ContextObserver interface {
	Observer
	CallbackContext(ctx context.Context) context.Context
}

type observer struct {
	o    Observer
	life observerLifetime
}

func (o *observer) sent(req wire.Request, frameLen int) {
	if o != nil {
		o.o.Sent(req, frameLen)
	}
}

func (o *observer) received(ev *wire.Event, frameLen int) {
	if o != nil {
		o.o.Received(ev, frameLen)
	}
}

// Config configures a pool.
type Config struct {
	Spawner               worker.Spawner
	MinProcesses          int
	MaxProcesses          int
	CheckoutTimeout       time.Duration
	RequestTimeout        time.Duration
	DurationLimitGrace    time.Duration
	GraceDisabled         bool
	MaxCheckoutsPerWorker int
	SingleUse             bool
	MontyVersion          string
	ProtocolVersion       uint32
	Metrics               Metrics
}

type slot struct {
	w      worker.Worker
	served int
	obs    *observer
}

type retiredWorker struct {
	w      worker.Worker
	obs    *observer
	killed bool
	done   chan struct{}
}

// Stats counts the workers a pool holds by state.
type Stats struct {
	Starting, Active, Idle, Retiring int
}

// Pool is an elastic set of workers. Starting, active, idle and retiring
// workers all count toward MaxProcesses.
type Pool struct {
	cfg      Config
	mu       sync.Mutex
	idle     []*slot
	starting int
	active   int
	retiring int
	notify   chan struct{}
	closed   bool
	reaper   chan retiredWorker
	reapers  sync.WaitGroup
	reaped   bool
}

// WithConnectHeaders attaches per-checkout WebSocket upgrade headers.
func WithConnectHeaders(ctx context.Context, headers [][2]string) context.Context {
	return worker.WithConnectHeaders(ctx, headers)
}

// ConnectHeaders returns headers attached by WithConnectHeaders.
func ConnectHeaders(ctx context.Context) [][2]string {
	return worker.ConnectHeaders(ctx)
}

const maxReapers = 8

// New creates a pool and prewarms MinProcesses workers.
func New(ctx context.Context, cfg Config) (*Pool, error) {
	if cfg.SingleUse {
		cfg.MinProcesses = 0
	}
	if cfg.MaxProcesses == 0 || cfg.MinProcesses > cfg.MaxProcesses {
		return nil, &Error{Kind: KindSpawn, Message: fmt.Sprintf("invalid pool size: min_processes=%d max_processes=%d", cfg.MinProcesses, cfg.MaxProcesses)}
	}
	if cfg.Metrics == nil {
		cfg.Metrics = noopMetrics{}
	}
	p := &Pool{cfg: cfg, notify: make(chan struct{}), reaper: make(chan retiredWorker, cfg.MaxProcesses)}
	for i := 0; i < min(cfg.MaxProcesses, maxReapers); i++ {
		p.reapers.Add(1)
		go p.reap(p.reaper)
	}
	for i := 0; i < cfg.MinProcesses; i++ {
		w, err := cfg.Spawner.Spawn(ctx)
		if err != nil {
			_ = p.Close(ctx)
			return nil, &Error{Kind: KindSpawn, Message: err.Error(), Cause: err}
		}
		p.mu.Lock()
		p.idle = append(p.idle, &slot{w: w})
		p.mu.Unlock()
		cfg.Metrics.WorkersLive(1)
		cfg.Metrics.WorkersIdle(1)
	}
	return p, nil
}

func (p *Pool) live() int { return p.starting + p.active + len(p.idle) + p.retiring }

func (p *Pool) wakeLocked() {
	close(p.notify)
	p.notify = make(chan struct{})
}

func (p *Pool) acquire(ctx context.Context) (*slot, error) {
	start := time.Now()
	outcome := "error"
	defer func() { p.cfg.Metrics.CheckoutWait(time.Since(start), outcome) }()
	var timer <-chan time.Time
	if p.cfg.CheckoutTimeout > 0 {
		t := time.NewTimer(p.cfg.CheckoutTimeout)
		defer t.Stop()
		timer = t.C
	}
	waited := false
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, &Error{Kind: KindClosed}
		}
		for !p.cfg.SingleUse && len(p.idle) > 0 {
			s := p.idle[len(p.idle)-1]
			p.idle = p.idle[:len(p.idle)-1]
			p.cfg.Metrics.WorkersIdle(-1)
			if s.w.Alive() {
				p.active++
				p.mu.Unlock()
				outcome = acquireOutcome(waited, "idle")
				return s, nil
			}
			p.cfg.Metrics.WorkerTerminated("died_idle")
			s.w.Kill()
			p.retireLocked(retiredWorker{w: s.w, killed: true})
		}
		if p.live() < p.cfg.MaxProcesses {
			p.starting++
			p.mu.Unlock()
			w, err := p.cfg.Spawner.Spawn(ctx)
			p.mu.Lock()
			p.starting--
			if err != nil {
				p.wakeLocked()
				p.mu.Unlock()
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				var perr *Error
				if errors.As(err, &perr) {
					return nil, perr
				}
				return nil, &Error{Kind: KindSpawn, Message: err.Error(), Cause: err}
			}
			if p.closed {
				p.cfg.Metrics.WorkersLive(1)
				p.cfg.Metrics.WorkerTerminated("closed")
				p.retireLocked(retiredWorker{w: w})
				p.wakeLocked()
				p.mu.Unlock()
				return nil, &Error{Kind: KindClosed}
			}
			p.active++
			p.mu.Unlock()
			p.cfg.Metrics.WorkersLive(1)
			outcome = acquireOutcome(waited, "spawned")
			return &slot{w: w}, nil
		}
		wait := p.notify
		p.mu.Unlock()
		waited = true
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer:
			outcome = "exhausted"
			return nil, &Error{Kind: KindExhausted}
		}
	}
}

func acquireOutcome(waited bool, immediate string) string {
	if waited {
		return "waited"
	}
	return immediate
}

func (p *Pool) release(s *slot) {
	obs := s.obs
	s.obs = nil
	p.mu.Lock()
	p.active--
	recycle := p.cfg.SingleUse || p.closed || !s.w.Alive() ||
		(p.cfg.MaxCheckoutsPerWorker > 0 && s.served >= p.cfg.MaxCheckoutsPerWorker)
	if recycle {
		reason := "recycled"
		if p.cfg.SingleUse {
			reason = "single_use"
		} else if p.closed {
			reason = "closed"
		}
		p.cfg.Metrics.WorkerTerminated(reason)
		if s.w.Kind() == worker.KindWebSocket {
			// Nothing is sent to retire a remote worker, so its spans end before release returns.
			obs.close()
			obs = nil
		}
		s.obs = obs
		p.retireLocked(retiredWorker{w: s.w, obs: obs})
	} else {
		p.idle = append(p.idle, s)
		p.cfg.Metrics.WorkersIdle(1)
	}
	p.wakeLocked()
	p.mu.Unlock()
	if !recycle {
		obs.close()
	}
}

// retireLocked hands a worker to the reaper; it counts toward capacity until it has exited.
func (p *Pool) retireLocked(s retiredWorker) {
	p.retiring++
	p.cfg.Metrics.WorkersRetiring(1)
	p.reaper <- s
}

func (p *Pool) reap(queue <-chan retiredWorker) {
	defer p.reapers.Done()
	for s := range queue {
		if !s.killed {
			shutdownWorker(s.w, s.obs)
		}
		for {
			wctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, exited := s.w.Wait(wctx)
			cancel()
			if exited {
				break
			}
			s.w.Kill()
		}
		s.obs.close()
		p.mu.Lock()
		p.retiring--
		p.cfg.Metrics.WorkersRetiring(-1)
		p.cfg.Metrics.WorkersLive(-1)
		p.wakeLocked()
		p.mu.Unlock()
		if s.done != nil {
			close(s.done)
		}
	}
}

// retireLease consumes an active accounting claim already transferred by a
// checkout lease. Its job contains no mutable checkout-owned slot state.
func (p *Pool) retireLease(w worker.Worker, reason string, killed bool) {
	p.mu.Lock()
	p.active--
	p.cfg.Metrics.WorkerTerminated(reason)
	p.retireLocked(retiredWorker{w: w, killed: killed})
	p.wakeLocked()
	p.mu.Unlock()
}

func shutdownWorker(w worker.Worker, obs *observer) {
	if w.Kind() == worker.KindWebSocket {
		w.Close()
		return
	}
	req := wire.Shutdown{}
	payload, _ := wire.EncodeRequest(req, "")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownExitGrace)
	defer cancel()
	sendCtx, sendCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		if w.Send(sendCtx, payload) == nil {
			obs.sent(req, len(payload))
		}
		close(done)
	}()
	if _, ok := w.Wait(sctx); !ok {
		w.Kill()
		wctx, wcancel := context.WithTimeout(context.Background(), time.Second)
		w.Wait(wctx)
		wcancel()
	}
	sendCancel()
	<-done
}

// Close shuts down idle workers; checked-out workers finish with their sessions.
func (p *Pool) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	waits := make([]chan struct{}, 0, len(idle))
	for _, s := range idle {
		p.cfg.Metrics.WorkersIdle(-1)
		p.cfg.Metrics.WorkerTerminated("closed")
		done := make(chan struct{})
		waits = append(waits, done)
		p.retireLocked(retiredWorker{w: s.w, done: done})
	}
	p.wakeLocked()
	p.mu.Unlock()
	for _, done := range waits {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Shutdown closes the pool and waits for every worker to exit within ctx.
func (p *Pool) Shutdown(ctx context.Context) error {
	_ = p.Close(ctx)
	if err := p.waitDrained(ctx); err != nil {
		return err
	}
	p.stopReapers()
	return nil
}

func (p *Pool) waitDrained(ctx context.Context) error {
	for {
		p.mu.Lock()
		drained := p.live() == 0
		wait := p.notify
		p.mu.Unlock()
		if drained {
			return nil
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *Pool) stopReapers() {
	p.mu.Lock()
	if !p.reaped {
		p.reaped = true
		close(p.reaper)
	}
	p.mu.Unlock()
	p.reapers.Wait()
}

// Closed reports whether Close was called.
func (p *Pool) Closed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Stats counts the workers by state.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Starting: p.starting, Active: p.active, Idle: len(p.idle), Retiring: p.retiring}
}

// Size returns the live and idle worker counts.
func (p *Pool) Size() (live, idle int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live(), len(p.idle)
}

type noopMetrics struct{}

func (noopMetrics) WorkersLive(int64)                     {}
func (noopMetrics) WorkersIdle(int64)                     {}
func (noopMetrics) WorkersRetiring(int64)                 {}
func (noopMetrics) CheckoutWait(time.Duration, string)    {}
func (noopMetrics) WorkerTerminated(string)               {}
func (noopMetrics) SessionDuration(time.Duration, string) {}
