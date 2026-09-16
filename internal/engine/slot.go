package engine

import (
	"context"
	"sync"
)

// Slot owns at most one session checked out with fixed options. It checks
// out lazily and again after the session is lost; sandbox state is not
// restored after a loss. Overlapping executions return ErrSessionBusy.
type Slot struct {
	p      *Pool
	opts   CheckoutOptions
	mu     sync.Mutex
	s      *Session
	closed bool
}

// Slot returns a holder that always yields a usable session.
func (p *Pool) Slot(opts CheckoutOptions) *Slot {
	return &Slot{p: p, opts: opts}
}

// Session is the current session, or nil before the first use or after a loss.
func (k *Slot) Session() *Session {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.s
}

// State is the current session's state; SessionIdle without one, SessionClosed after Close.
func (k *Slot) State() SessionState {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch {
	case k.closed:
		return SessionClosed
	case k.s == nil || k.s.State() == SessionClosed:
		return SessionIdle
	}
	return k.s.State()
}

func (k *Slot) acquireLocked(ctx context.Context) (*Session, error) {
	if k.closed {
		return nil, ErrSessionClosed
	}
	if k.s != nil && k.s.State() != SessionClosed {
		return k.s, nil
	}
	s, err := k.p.Checkout(ctx, k.opts)
	if err != nil {
		return nil, err
	}
	k.s = s
	return s, nil
}

func (k *Slot) idle(ctx context.Context) (*Session, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	s, err := k.acquireLocked(ctx)
	if err != nil {
		return nil, err
	}
	switch s.State() {
	case SessionIdle:
		return s, nil
	case SessionClosed:
		return nil, s.Err()
	}
	return nil, ErrSessionBusy
}

// Go starts a background feed; see Session.Go.
func (k *Slot) Go(ctx context.Context, code string, opts *FeedOptions) (*Run, error) {
	s, err := k.idle(ctx)
	if err != nil {
		return nil, err
	}
	return s.Go(ctx, code, opts), nil
}

// FeedRun executes one snippet; see Session.FeedRun.
func (k *Slot) FeedRun(ctx context.Context, code string, opts *FeedOptions) (any, error) {
	s, err := k.idle(ctx)
	if err != nil {
		return nil, err
	}
	return s.FeedRun(ctx, code, opts)
}

// FeedStart starts a snippet and returns its first snapshot; see Session.FeedStart.
func (k *Slot) FeedStart(ctx context.Context, code string, opts *FeedOptions) (Snapshot, error) {
	s, err := k.idle(ctx)
	if err != nil {
		return nil, err
	}
	return s.FeedStart(ctx, code, opts)
}

// Stop ends the current execution; see Session.Stop.
func (k *Slot) Stop(ctx context.Context, policy ...StopPolicy) (Stopped, error) {
	s := k.Session()
	if s == nil {
		return Stopped{How: StopNotRunning}, nil
	}
	return s.Stop(ctx, policy...)
}

// Close closes the current session and refuses further use; it is idempotent.
func (k *Slot) Close(ctx context.Context, policy ...StopPolicy) error {
	k.mu.Lock()
	s := k.s
	k.s, k.closed = nil, true
	k.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close(ctx, policy...)
}
