package montygo

import (
	"context"
	monterr "github.com/asalimonov/montygo/monterr"
)

func (s *Session) reserveControl(ctx context.Context, allowPaused bool) (func(), error) {
	return s.reserveControlFor(ctx, allowPaused, nil)
}

func (s *Session) reserveControlFor(ctx context.Context, allowPaused bool, token *snapshotToken) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if token != nil {
		if err := s.snapshotErrorLocked(token); err != nil {
			return nil, err
		}
	}
	if err := s.admissionErrorLocked(); err != nil {
		return nil, err
	}
	if s.life.controlDone != nil {
		return nil, monterr.ErrSessionBusy
	}
	e := s.life.current
	if e != nil {
		if !allowPaused || e.phase != executionPaused {
			return nil, monterr.ErrSessionBusy
		}
		e.phase, e.wireInFlight = executionControl, true
		e.turnDone = make(chan struct{})
		e.operationDone = make(chan struct{})
	} else {
		s.life.controlDone = make(chan struct{})
	}
	releaseObserver := s.co.HoldObserver()
	return func() {
		s.life.mu.Lock()
		abort := false
		terminal := s.life.terminal
		if e == nil {
			close(s.life.controlDone)
			s.life.controlDone = nil
		} else {
			e.wireInFlight = false
			s.releaseTurnLocked(e)
			s.releaseOperationLocked(e)
			e.phase = executionPaused
			if terminal != nil || e.stop != nil {
				e.phase = executionAborting
				e.operationDone = make(chan struct{})
				abort = terminal == nil
			}
		}
		s.life.mu.Unlock()
		releaseObserver()
		if e != nil && terminal != nil {
			s.finishExecution(e, nil, terminal)
		} else if abort {
			go s.abortPaused(e)
		}
	}, nil
}

// Close ends the session. A running execution is stopped with the policy
// first; a paused snapshot is invalidated; KillNow retires the worker without
// joining a host callback. ctx bounds only the caller's wait.
func (s *Session) Close(ctx context.Context, policy ...StopPolicy) error {
	p, err := effectivePolicy(s.limits.stop, policy)
	if err != nil {
		return err
	}
	if p.Timeout < 0 {
		_ = s.terminateSession(monterr.ErrSessionClosed)
		return nil
	}
	s.life.mu.Lock()
	if s.life.terminal != nil {
		s.life.mu.Unlock()
		return nil
	}
	if a := s.life.closeAttempt; a != nil {
		s.life.mu.Unlock()
		select {
		case <-a.done:
			return a.err
		case <-ctx.Done():
			return ctx.Err()
		case <-s.Done():
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		s.life.mu.Unlock()
		return err
	}
	a := &closeAttempt{done: make(chan struct{})}
	s.life.closeAttempt = a
	e := s.life.current
	running := e != nil && e.phase != executionPaused
	s.life.mu.Unlock()
	if running {
		st, err := (&Run{s: s, exec: e}).Stop(ctx, p)
		if err != nil && st.How == StopPending {
			go s.finishClose(a, e, p)
			return err
		}
	}
	err = s.closeOwned(ctx)
	s.settleClose(a, err)
	return err
}

// finishClose completes a close whose caller stopped waiting.
func (s *Session) finishClose(a *closeAttempt, e *execution, p StopPolicy) {
	<-e.done
	ctx, cancel := context.WithTimeout(context.Background(), p.Timeout+p.Join)
	defer cancel()
	s.settleClose(a, s.closeOwned(ctx))
}

func (s *Session) settleClose(a *closeAttempt, err error) {
	s.life.mu.Lock()
	a.err = err
	if s.life.closeAttempt == a {
		s.life.closeAttempt = nil
	}
	close(a.done)
	s.life.mu.Unlock()
}

func (s *Session) closeOwned(ctx context.Context) error {
	for {
		s.life.mu.Lock()
		if s.life.terminal != nil {
			s.life.mu.Unlock()
			return nil
		}
		if err := ctx.Err(); err != nil {
			s.life.mu.Unlock()
			return err
		}
		e := s.life.current
		wait := s.life.controlDone
		if e != nil {
			wait = e.operationDone
		}
		if wait == nil {
			if e != nil {
				e.phase, e.wireInFlight = executionControl, true
				e.turnDone = make(chan struct{})
				e.operationDone = make(chan struct{})
			} else {
				s.life.controlDone = make(chan struct{})
			}
			s.life.mu.Unlock()
			release := s.co.HoldObserver()
			err := s.co.Finish(ctx)
			cause := error(monterr.ErrSessionClosed)
			if err != nil {
				err = s.mapError(err)
				cause = err
			}
			_ = s.terminateSession(cause)
			s.life.mu.Lock()
			if e == nil {
				close(s.life.controlDone)
				s.life.controlDone = nil
			}
			s.life.mu.Unlock()
			if e != nil {
				s.finishExecution(e, nil, cause)
			}
			release()
			return err
		}
		s.life.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.Done():
			return nil
		}
	}
}
