package pool

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
)

// Reservation is capacity held without a worker. A single-use pool reserves
// before dialing, so a failed attempt can be retried, and a rotation keeps its
// place while it replaces the worker.
type Reservation struct {
	pool   *Pool
	budget sessionBudget
	cwdSet bool
	mu     sync.Mutex
	held   bool
}

// Reserve waits for capacity without spawning a worker. Only a single-use pool
// reserves: it never reuses an idle worker, so capacity alone is the slot.
func (p *Pool) Reserve(ctx context.Context) (*Reservation, error) {
	if !p.cfg.SingleUse {
		return nil, protocolError("reserve requires a single-use pool")
	}
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
		if p.live() < p.cfg.MaxProcesses {
			p.starting++
			p.mu.Unlock()
			outcome = acquireOutcome(waited, "spawned")
			return &Reservation{pool: p, held: true}, nil
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

// Release returns the reservation's capacity; it is idempotent and a no-op once
// a Bind has taken it over.
func (r *Reservation) Release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseLocked()
}

func (r *Reservation) releaseLocked() {
	if !r.held {
		return
	}
	r.held = false
	r.pool.mu.Lock()
	r.pool.starting--
	r.pool.wakeLocked()
	r.pool.mu.Unlock()
}

// Bind spawns a worker on r, configures its session and, with a non-nil state,
// restores that dump carrying r's budget. A failed attempt keeps r usable.
func (p *Pool) Bind(ctx context.Context, r *Reservation, cfg wire.Configure, state []byte, opts CheckoutOptions) (*Checkout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.held {
		return nil, protocolError("reservation is not held")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w, err := p.cfg.Spawner.Spawn(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		var perr *Error
		if errors.As(err, &perr) {
			return nil, perr
		}
		return nil, &Error{Kind: KindSpawn, Message: err.Error(), Cause: err}
	}
	p.mu.Lock()
	closed := p.closed
	p.starting--
	if closed {
		p.mu.Unlock()
		p.cfg.Metrics.WorkersLive(1)
		p.cfg.Metrics.WorkerTerminated("closed")
		p.mu.Lock()
		p.retireLocked(retiredWorker{w: w})
		p.wakeLocked()
		p.mu.Unlock()
		r.held = false
		return nil, &Error{Kind: KindClosed}
	}
	p.active++
	p.mu.Unlock()
	p.cfg.Metrics.WorkersLive(1)
	// The worker now owns the capacity: every failure below retires it through
	// the lease, which decrements active exactly once.
	r.held = false

	s := &slot{w: w}
	c := p.newCheckout(s, opts)
	cfg.MontyVersion = p.cfg.MontyVersion
	cfg.ProtocolVersion = p.cfg.ProtocolVersion
	c.applyLimits(cfg)
	ev, err := c.turn(ctx, cfg, true, nil)
	if err == nil && ev.Kind != wire.EventOk {
		err = protocolError("unexpected reply to Configure: %s", ev.Kind)
	}
	if err == nil && state != nil {
		err = c.restoreCarried(ctx, state, r.budget, r.cwdSet)
	}
	if err != nil {
		c.drop("discarded")
		r.reReserveLocked()
		return nil, err
	}
	return c, nil
}

// reReserveLocked takes capacity back after a failed bind, so the caller can
// attempt again. The freed slot is briefly visible to other waiters, so live
// workers MAY exceed MaxProcesses by one until the retiring worker exits.
func (r *Reservation) reReserveLocked() {
	r.pool.mu.Lock()
	r.pool.starting++
	r.pool.mu.Unlock()
	r.held = true
}

// Handoff closes a remote worker's connection and keeps its capacity, budget
// and working-directory state for a following Bind.
func (c *Checkout) Handoff() (*Reservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if c.pending.kind != pendingNone {
		return nil, protocolError("handoff with a pending suspension")
	}
	if c.worker.Kind() != worker.KindWebSocket {
		return nil, protocolError("handoff requires a remote worker")
	}
	s, won := c.lease.finish()
	if !won {
		return nil, &Error{Kind: KindFinished}
	}
	obs := s.obs
	s.obs = nil
	r := &Reservation{pool: c.pool, budget: c.budget, cwdSet: c.cwdSet, held: true}
	c.pending = pending{}
	c.feedMounts = nil
	p := c.pool
	p.mu.Lock()
	p.active--
	p.starting++
	p.cfg.Metrics.WorkerTerminated("single_use")
	p.retireLocked(retiredWorker{w: s.w})
	p.wakeLocked()
	p.mu.Unlock()
	// Nothing is sent to retire a remote worker, so its spans end here.
	obs.close()
	c.finishMetrics("ok")
	return r, nil
}

// restoreCarried loads a dump taken from this session's previous worker. Unlike
// Restore it keeps the suspension count and the working directory, because the
// session continues rather than starting over.
func (c *Checkout) restoreCarried(ctx context.Context, state []byte, budget sessionBudget, cwdSet bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	limit := c.budget.suspensionLimit
	c.budget = budget
	if limit < c.budget.suspensionLimit {
		c.budget.suspensionLimit = limit
	}
	ev, err := c.turn(ctx, wire.Load{State: state}, true, nil)
	if err != nil {
		return err
	}
	if ev.Kind != wire.EventOk {
		c.discard("discarded")
		return protocolError("unexpected reply to Load: %s", ev.Kind)
	}
	c.cwdSet = cwdSet
	return nil
}
