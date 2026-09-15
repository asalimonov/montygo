package montygo

import (
	"context"
	"errors"
	"time"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wire"
)

// InterruptOptions controls one request to stop an execution.
type InterruptOptions struct {
	Reason error
	// Grace overrides the checkout default. Zero forces immediately; nil inherits.
	Grace *time.Duration
}

// InterruptOutcome describes what happened to the targeted execution.
type InterruptOutcome uint8

const (
	InterruptUnknown InterruptOutcome = iota
	InterruptPending
	InterruptNotRunning
	InterruptAlreadyFinished
	InterruptBeforeStart
	InterruptAborted
	InterruptKilled
	InterruptFinished
)

// InterruptResult separates worker termination from completion of Go callbacks.
type InterruptResult struct {
	Outcome    InterruptOutcome
	RunDone    bool
	SessionErr error
}

type interruptRequest struct {
	reason       error
	deadline     time.Time
	wake         chan struct{}
	resolved     chan struct{}
	outcome      InterruptOutcome
	err          error
	resolvedOnce bool
	forceClaimed bool
}

var (
	keyboardInterrupt       = Raise("KeyboardInterrupt", "")
	errExecutionInterrupted = errors.New("execution interruption requested")
)

const abortDeadline = 5 * time.Second

func interruptException(reason error) error {
	typ, msg := exceptionParts(reason)
	return errorFromException(wire.NewException(typ, msg))
}

func normalizeInterruptOptions(opts InterruptOptions, fallback time.Duration) (error, time.Duration, error) {
	grace := fallback
	if opts.Grace != nil {
		grace = *opts.Grace
	}
	if grace < 0 {
		return nil, 0, &OptionError{Message: "interrupt grace must be non-negative"}
	}
	reason := opts.Reason
	if reason == nil {
		reason = keyboardInterrupt
	}
	return reason, grace, nil
}

// Interrupt captures the current execution once and stops only that execution.
// The caller context bounds waiting; an accepted request continues after timeout.
func (s *Session) Interrupt(ctx context.Context, opts InterruptOptions) (InterruptResult, error) {
	if err := ctx.Err(); err != nil {
		return InterruptResult{}, err
	}
	if _, _, err := normalizeInterruptOptions(opts, s.limits.interruptGrace); err != nil {
		return InterruptResult{}, err
	}
	s.life.mu.Lock()
	e := s.life.current
	if e == nil {
		result := InterruptResult{Outcome: InterruptNotRunning, RunDone: true, SessionErr: s.life.terminal}
		s.life.mu.Unlock()
		return result, nil
	}
	s.life.mu.Unlock()
	return (&Run{s: s, exec: e}).Interrupt(ctx, opts)
}

func (s *Session) requestInterrupt(e *execution, opts InterruptOptions) (*interruptRequest, InterruptResult, error) {
	return s.requestInterruptForStep(e, opts, 0)
}

func (s *Session) requestInterruptForStep(e *execution, opts InterruptOptions, step uint64) (*interruptRequest, InterruptResult, error) {
	reason, grace, err := normalizeInterruptOptions(opts, s.limits.interruptGrace)
	if err != nil {
		return nil, InterruptResult{}, err
	}
	s.life.mu.Lock()
	if step != 0 {
		if s.life.current != e || e.step != step || e.phase == executionFinished {
			s.life.mu.Unlock()
			return nil, InterruptResult{}, nil
		}
		if !e.sent || e.wireInFlight {
			// The protocol owner classifies cancellation of an in-flight turn.
			cancel := e.cancelCallbacks
			s.life.mu.Unlock()
			cancel()
			return nil, InterruptResult{}, nil
		}
	}
	if e.phase == executionFinished {
		result := InterruptResult{Outcome: InterruptAlreadyFinished, RunDone: true, SessionErr: s.life.terminal}
		s.life.mu.Unlock()
		return nil, result, nil
	}
	if e.terminalCause != nil {
		if e.stop != nil {
			req := e.stop
			s.life.mu.Unlock()
			return req, InterruptResult{}, nil
		}
		result := InterruptResult{Outcome: InterruptFinished, RunDone: channelClosed(e.done), SessionErr: s.life.terminal}
		s.life.mu.Unlock()
		return nil, result, nil
	}
	if s.life.current != e {
		s.life.mu.Unlock()
		return nil, InterruptResult{}, ErrSnapshotStale
	}
	created := e.stop == nil
	deadline := time.Now().Add(grace)
	if created {
		e.stop = &interruptRequest{reason: reason, deadline: deadline, wake: make(chan struct{}, 1), resolved: make(chan struct{})}
	} else if deadline.Before(e.stop.deadline) {
		e.stop.deadline = deadline
	}
	req := e.stop
	paused := e.phase == executionPaused
	if paused {
		e.phase = executionAborting
		e.operationDone = make(chan struct{})
	}
	cancel := e.cancelCallbacks
	s.life.mu.Unlock()
	cancel()
	if created {
		go s.watchInterrupt(e, req)
	} else {
		select {
		case req.wake <- struct{}{}:
		default:
		}
	}
	if paused {
		go s.abortPaused(e)
	}
	return req, InterruptResult{}, nil
}

func (s *Session) waitInterrupt(ctx context.Context, e *execution, req *interruptRequest) (InterruptResult, error) {
	select {
	case <-req.resolved:
		return s.interruptResult(e, req)
	default:
	}
	select {
	case <-req.resolved:
		return s.interruptResult(e, req)
	case <-ctx.Done():
		if channelClosed(req.resolved) {
			return s.interruptResult(e, req)
		}
		return InterruptResult{Outcome: InterruptPending, RunDone: channelClosed(e.done), SessionErr: s.Err()}, ctx.Err()
	}
}

func (s *Session) interruptResult(e *execution, req *interruptRequest) (InterruptResult, error) {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	return InterruptResult{Outcome: req.outcome, RunDone: channelClosed(e.done), SessionErr: s.life.terminal}, req.err
}

func (s *Session) resolveInterruptLocked(e *execution, outcome InterruptOutcome, err error) {
	if e.stop == nil || e.stop.resolvedOnce {
		return
	}
	e.stop.outcome, e.stop.err, e.stop.resolvedOnce = outcome, err, true
	close(e.stop.resolved)
}

func (s *Session) watchInterrupt(e *execution, req *interruptRequest) {
	for {
		s.life.mu.Lock()
		deadline, resolved := req.deadline, req.resolvedOnce
		s.life.mu.Unlock()
		if resolved {
			return
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-req.resolved:
			timer.Stop()
			return
		case <-req.wake:
			timer.Stop()
		case <-timer.C:
			if s.forceExecution(e, req) {
				return
			}
		}
	}
}

func (s *Session) forceExecution(e *execution, req *interruptRequest) bool {
	s.life.mu.Lock()
	if req.resolvedOnce {
		s.life.mu.Unlock()
		return true
	}
	if req.forceClaimed {
		s.life.mu.Unlock()
		return true
	}
	if s.life.current != e || e.phase == executionFinished || s.life.terminal != nil {
		s.resolveInterruptLocked(e, InterruptFinished, nil)
		s.life.mu.Unlock()
		return true
	}
	if time.Now().Before(req.deadline) {
		s.life.mu.Unlock()
		return false
	}
	cause := &SessionKilledError{Reason: req.reason}
	req.forceClaimed, req.outcome = true, InterruptKilled
	s.life.terminal, e.terminalCause = cause, cause
	clear(e.pending)
	close(s.life.done)
	s.life.mu.Unlock()
	e.cancelCallbacks()
	s.co.Terminate(cause, "interrupted")
	s.pool.untrack(s)
	s.life.mu.Lock()
	s.resolveInterruptLocked(e, InterruptKilled, nil)
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

func (s *Session) abortExecution(ctx context.Context, e *execution, pt *printTarget) error {
	s.life.mu.Lock()
	if err := s.executionErrorLocked(e); err != nil {
		s.life.mu.Unlock()
		return err
	}
	req := e.stop
	if req == nil {
		s.life.mu.Unlock()
		return &ProtocolError{Message: "abort without an interrupt request"}
	}
	deadline := req.deadline
	if ceiling := time.Now().Add(abortDeadline); ceiling.Before(deadline) {
		deadline = ceiling
	}
	e.phase, e.wireInFlight = executionAborting, true
	s.releaseTurnLocked(e)
	e.turnDone = make(chan struct{})
	s.life.mu.Unlock()
	actx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	typ, msg := exceptionParts(req.reason)
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
		err = &ProtocolError{Message: "abort ended without an Error event"}
	}
	err = s.mapError(err)
	if s.Err() == nil {
		err = &ProtocolError{Message: "failed to abort suspended feed: " + err.Error(), cause: err}
	}
	err = s.terminateSession(err)
	s.life.mu.Lock()
	s.resolveInterruptLocked(e, InterruptFinished, err)
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

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
