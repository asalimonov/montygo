package montygo

import (
	"context"
	"errors"
	"time"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wire"
	monterr "github.com/asalimonov/montygo/monterr"
)

// StopKind classifies how a stop ended.
type StopKind uint8

const (
	// StopPending: the stop is still in progress; returned only with a context error.
	StopPending StopKind = iota
	// StopNotRunning: nothing was running.
	StopNotRunning
	// StopAborted: the reason ended the run; the session is kept.
	StopAborted
	// StopKilled: the worker was killed when Timeout expired.
	StopKilled
	// StopFinished: the run ended on its own; Err is its result.
	StopFinished
)

var stopKindNames = [...]string{"pending", "not running", "aborted", "killed", "finished"}

func (k StopKind) String() string {
	if int(k) < len(stopKindNames) {
		return stopKindNames[k]
	}
	return "unknown"
}

// Stopped is the result of Stop.
type Stopped struct {
	How StopKind
	// Err is what Run.Wait returns for the run.
	Err error
	// SessionErr is the session's terminal error, nil while it is usable.
	SessionErr error
}

// SessionKept reports whether the session is still usable.
func (s Stopped) SessionKept() bool { return s.SessionErr == nil }

var (
	errExecutionInterrupted = errors.New("execution interruption requested")
	errDeliverStop          = errors.New("stop reason to deliver")
)

// stopRequest is the one stop of an execution; later callers can only shorten it.
type stopRequest struct {
	policy       StopPolicy
	requestAt    time.Time
	killAt       time.Time
	requested    bool
	wake         chan struct{}
	resolved     chan struct{}
	kind         StopKind
	resolvedOnce bool
	forceClaimed bool
}

func newStopRequest(p StopPolicy, now time.Time) *stopRequest {
	req := &stopRequest{policy: p, wake: make(chan struct{}, 1), resolved: make(chan struct{})}
	req.requestAt = now.Add(p.Drain)
	req.killAt = req.requestAt.Add(max(p.Timeout, 0))
	return req
}

// shorten applies a later policy: only earlier deadlines take effect.
func (req *stopRequest) shorten(p StopPolicy, now time.Time) {
	requestAt := now.Add(p.Drain)
	if req.requested || !requestAt.Before(req.requestAt) {
		requestAt = req.requestAt
	}
	killAt := requestAt.Add(max(p.Timeout, 0))
	changed := false
	if requestAt.Before(req.requestAt) {
		req.requestAt, changed = requestAt, true
	}
	if killAt.Before(req.killAt) {
		req.killAt, changed = killAt, true
	}
	if changed {
		select {
		case req.wake <- struct{}{}:
		default:
		}
	}
}

func interruptException(reason error) error {
	typ, msg := monterr.ExceptionParts(reason)
	return monterr.ErrorFromException(wire.NewException(typ, msg))
}

// Stop ends the run: Reason is delivered at the next host call or
// suspension, the worker is killed when Timeout expires, and Stop returns
// after the run has ended. ctx bounds only the caller's wait; an accepted
// stop always completes.
func (r *Run) Stop(ctx context.Context, policy ...StopPolicy) (Stopped, error) {
	s, e := r.s, r.exec
	p, err := effectivePolicy(s.limits.stop, policy)
	if err != nil {
		return Stopped{}, err
	}
	if err := ctx.Err(); err != nil {
		return Stopped{How: StopPending, SessionErr: s.Err()}, err
	}
	req, immediate, done := s.requestStop(e, p)
	if done {
		return immediate, nil
	}
	select {
	case <-req.resolved:
	case <-ctx.Done():
		if !channelClosed(req.resolved) {
			return Stopped{How: StopPending, SessionErr: s.Err()}, ctx.Err()
		}
	}
	st := s.stoppedFrom(e, req)
	if st.How == StopKilled {
		timer := time.NewTimer(p.Join)
		defer timer.Stop()
		select {
		case <-e.done:
			st.Err = e.err
			return st, nil
		case <-ctx.Done():
			return st, ctx.Err()
		case <-timer.C:
			return st, monterr.ErrCallbackDetached
		}
	}
	select {
	case <-e.done:
		st.Err = e.err
		return st, nil
	case <-ctx.Done():
		return Stopped{How: StopPending, SessionErr: st.SessionErr}, ctx.Err()
	}
}

// Stop ends the current execution or paused snapshot; see Run.Stop.
func (s *Session) Stop(ctx context.Context, policy ...StopPolicy) (Stopped, error) {
	if _, err := effectivePolicy(s.limits.stop, policy); err != nil {
		return Stopped{}, err
	}
	s.life.mu.Lock()
	e := s.life.current
	terminal := s.life.terminal
	s.life.mu.Unlock()
	if e == nil {
		return Stopped{How: StopNotRunning, SessionErr: terminal}, nil
	}
	return (&Run{s: s, exec: e}).Stop(ctx, policy...)
}

func (s *Session) stoppedFrom(e *execution, req *stopRequest) Stopped {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	st := Stopped{How: req.kind, SessionErr: s.life.terminal}
	if e.phase == executionFinished {
		st.Err = e.err
	} else if e.terminalCause != nil {
		st.Err = e.terminalCause
	}
	return st
}

// requestStop registers or shortens the stop of e. done reports a result
// that needs no waiting.
func (s *Session) requestStop(e *execution, p StopPolicy) (req *stopRequest, immediate Stopped, done bool) {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if e.stop != nil && e.stop.resolvedOnce {
		st := Stopped{How: e.stop.kind, Err: e.terminalCause, SessionErr: s.life.terminal}
		if e.phase == executionFinished {
			st.Err = e.err
		}
		return nil, st, true
	}
	if e.phase == executionFinished || (s.life.current != e && e.stop == nil) {
		return nil, Stopped{How: StopFinished, Err: e.err, SessionErr: s.life.terminal}, true
	}
	if e.terminalCause != nil && e.stop == nil {
		return nil, Stopped{How: StopFinished, Err: e.terminalCause, SessionErr: s.life.terminal}, true
	}
	p = p.over(s.limits.stop)
	now := time.Now()
	if e.stop == nil {
		e.stop = newStopRequest(p, now)
		go s.watchStop(e, e.stop)
	} else {
		e.stop.shorten(p, now)
	}
	if !e.stop.requestAt.After(now) {
		s.enterRequestLocked(e)
	}
	return e.stop, Stopped{}, false
}

// requestStopForStep is the feed-context hook; a stale step is ignored.
func (s *Session) requestStopForStep(e *execution, step uint64) {
	s.life.mu.Lock()
	stale := e.step != step || e.phase == executionFinished
	s.life.mu.Unlock()
	if stale {
		return
	}
	s.requestStop(e, s.limits.stop)
}

// enterRequestLocked starts the request phase once: callbacks are cancelled
// and a paused snapshot is aborted.
func (s *Session) enterRequestLocked(e *execution) {
	req := e.stop
	if req.requested {
		return
	}
	req.requested = true
	cancel := e.cancelCallbacks
	paused := e.phase == executionPaused
	if paused {
		e.phase = executionAborting
		e.operationDone = make(chan struct{})
	}
	go func() {
		cancel()
		if paused {
			s.abortPaused(e)
		}
	}()
}

func (s *Session) watchStop(e *execution, req *stopRequest) {
	for {
		s.life.mu.Lock()
		if req.resolvedOnce {
			s.life.mu.Unlock()
			return
		}
		next := req.killAt
		if !req.requested {
			next = req.requestAt
		}
		s.life.mu.Unlock()
		timer := time.NewTimer(time.Until(next))
		select {
		case <-req.resolved:
			timer.Stop()
			return
		case <-req.wake:
			timer.Stop()
		case <-timer.C:
			s.life.mu.Lock()
			if !req.requested {
				if e.phase != executionFinished {
					s.enterRequestLocked(e)
				}
				s.life.mu.Unlock()
				continue
			}
			s.life.mu.Unlock()
			if s.forceExecution(e, req) {
				return
			}
		}
	}
}

func (s *Session) resolveStopLocked(e *execution, kind StopKind) {
	if e.stop == nil || e.stop.resolvedOnce {
		return
	}
	e.stop.kind, e.stop.resolvedOnce = kind, true
	close(e.stop.resolved)
}

// forceExecution kills the worker at the kill deadline; true when the request is settled.
func (s *Session) forceExecution(e *execution, req *stopRequest) bool {
	s.life.mu.Lock()
	if req.resolvedOnce || req.forceClaimed {
		s.life.mu.Unlock()
		return true
	}
	if s.life.current != e || e.phase == executionFinished || s.life.terminal != nil {
		s.resolveStopLocked(e, StopFinished)
		s.life.mu.Unlock()
		return true
	}
	if time.Now().Before(req.killAt) {
		s.life.mu.Unlock()
		return false
	}
	cause := &monterr.SessionKilledError{Reason: req.policy.Reason}
	req.forceClaimed, req.kind = true, StopKilled
	s.life.terminal, e.terminalCause = cause, cause
	clear(e.pending)
	close(s.life.done)
	cancel := e.cancelCallbacks
	s.life.mu.Unlock()
	cancel()
	s.co.Terminate(cause, "interrupted")
	s.pool.untrack(s)
	s.life.mu.Lock()
	s.resolveStopLocked(e, StopKilled)
	s.life.mu.Unlock()
	return true
}

func (s *Session) abortPaused(e *execution) {
	err := s.abortExecution(e.stepCtx, e, e.print)
	if e.print != nil {
		if ferr := e.print.finish(); ferr != nil {
			err = ferr
		}
	}
	s.finishExecution(e, nil, err)
}

// abortExecution sends AbortFeed with the stop reason; the turn cannot outlive the kill deadline.
func (s *Session) abortExecution(ctx context.Context, e *execution, pt *printTarget) error {
	s.life.mu.Lock()
	if err := s.executionErrorLocked(e); err != nil {
		s.life.mu.Unlock()
		return err
	}
	req := e.stop
	if req == nil {
		s.life.mu.Unlock()
		return &monterr.ProtocolError{Message: "abort without a stop request"}
	}
	deadline := req.killAt
	e.phase, e.wireInFlight = executionAborting, true
	s.releaseTurnLocked(e)
	e.turnDone = make(chan struct{})
	s.life.mu.Unlock()
	actx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	typ, msg := monterr.ExceptionParts(req.policy.Reason)
	var onPrint pool.OnPrint
	if pt != nil {
		onPrint = pt.onPrint
	}
	err := s.co.Abort(actx, wire.NewException(typ, msg), onPrint)
	s.receivedTurn(e)
	if actx.Err() != nil {
		s.forceExecution(e, req)
	}
	if terminal := s.Err(); terminal != nil {
		return terminal
	}
	var perr *pool.Error
	if errors.As(err, &perr) && perr.Kind == pool.KindRuntime && !perr.PreSend && !perr.WorkerLost {
		s.life.mu.Lock()
		e.aborted = true
		s.life.mu.Unlock()
		return s.mapError(err)
	}
	if err == nil {
		err = &monterr.ProtocolError{Message: "abort ended without an Error event"}
	}
	err = s.mapError(err)
	if s.Err() == nil {
		err = monterr.NewProtocolError("failed to abort suspended feed: "+err.Error(), err)
	}
	err = s.terminateSession(err)
	s.life.mu.Lock()
	s.resolveStopLocked(e, StopFinished)
	s.life.mu.Unlock()
	return err
}

func (s *Session) abortOrTerminal(ctx context.Context, e *execution, pt *printTarget, err error) error {
	if terminal := s.Err(); terminal != nil {
		return terminal
	}
	if errors.Is(err, errExecutionInterrupted) {
		return s.abortExecution(ctx, e, pt)
	}
	return err
}

// deliverStop marks the catchable reason delivered and gives later host
// calls a live context.
func (s *Session) deliverStop(e *execution) *wire.Exception {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	e.delivered = true
	e.callbackCtx, e.cancelCallbacks = context.WithCancel(context.WithoutCancel(e.stepCtx))
	typ, msg := monterr.ExceptionParts(e.stop.policy.Reason)
	return wire.NewException(typ, msg)
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
