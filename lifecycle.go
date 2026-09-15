package montygo

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/worker"
)

type executionPhase uint8

const (
	executionStarting executionPhase = iota
	executionPreparing
	executionExecuting
	executionCallback
	executionPaused
	executionControl
	executionAborting
	executionFinished
)

type execution struct {
	id              uint64
	phase           executionPhase
	sent            bool
	wireInFlight    bool
	turnDone        chan struct{}
	sequence        uint64
	step            uint64
	stepCtx         context.Context
	callbackCtx     context.Context
	cancelCallbacks context.CancelFunc
	stopContext     func() bool
	stop            *interruptRequest
	pending         map[uint32]*Future
	done            chan struct{}
	operationDone   chan struct{}
	value           any
	err             error
	terminalCause   error
	beforeStart     bool
	aborted         bool
	print           *printTarget
	releaseObserver func()
}

type closeAttempt struct {
	done chan struct{}
	err  error
}

type lifecycle struct {
	mu           sync.Mutex
	nextID       uint64
	current      *execution
	controlDone  chan struct{}
	closeAttempt *closeAttempt
	terminal     error
	done         chan struct{}
}

type snapshotToken struct {
	exec     *execution
	sequence uint64
	used     bool
}

func newSession(p *Pool, scriptName string, limits sessionLimits) *Session {
	return &Session{pool: p, store: newInstanceStore(limits.hostObjects), scriptName: scriptName, limits: limits, life: lifecycle{done: make(chan struct{})}}
}

func (s *Session) attach(co *pool.Checkout) {
	s.co = co
	go func() {
		select {
		case <-co.Done():
		case <-s.Done():
			return
		}
		for {
			s.life.mu.Lock()
			ended := s.life.terminal != nil
			wait := s.life.controlDone
			if e := s.life.current; e != nil {
				wait = e.turnDone
			}
			s.life.mu.Unlock()
			if ended {
				return
			}
			if wait == nil {
				break
			}
			// Let the protocol owner classify queued Error/ShutdownDump frames.
			select {
			case <-wait:
			case <-s.Done():
				return
			}
		}
		var err error
		if s.pool.backend == BackendWebSocket {
			de := &DisconnectError{Message: "monty worker connection closed while idle"}
			var closed *worker.ClosedError
			if errors.As(co.WorkerErr(), &closed) {
				de.Code, de.Reason = closed.Code, closed.Reason
				de.Message += ": " + closed.Error()
			}
			err = de
		} else {
			err = &CrashedError{Message: "monty worker crashed while idle"}
		}
		_ = s.terminateSession(err)
	}()
}

func (s *Session) reserveExecution(ctx context.Context) (*execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.admissionErrorLocked(); err != nil {
		return nil, err
	}
	if s.life.current != nil || s.life.controlDone != nil {
		return nil, ErrSessionBusy
	}
	if s.life.nextID == math.MaxUint64 {
		return nil, &ValueError{Message: "execution ID exhausted"}
	}
	s.life.nextID++
	cb, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e := &execution{id: s.life.nextID, phase: executionStarting, callbackCtx: cb, cancelCallbacks: cancel,
		pending: map[uint32]*Future{}, done: make(chan struct{}), operationDone: make(chan struct{}), releaseObserver: s.co.HoldObserver()}
	s.life.current = e
	s.installStepLocked(e, ctx)
	return e, nil
}

func (s *Session) installStepLocked(e *execution, ctx context.Context) {
	if e.stopContext != nil {
		e.stopContext()
	}
	e.step++
	e.stepCtx = ctx
	step := e.step
	e.stopContext = context.AfterFunc(ctx, func() {
		_, _, _ = s.requestInterruptForStep(e, InterruptOptions{}, step)
	})
}

func (s *Session) admissionErrorLocked() error {
	if s.life.terminal != nil {
		return s.life.terminal
	}
	if s.life.closeAttempt != nil {
		return ErrSessionClosed
	}
	return nil
}

func (s *Session) executionErrorLocked(e *execution) error {
	if s.life.terminal != nil {
		return s.life.terminal
	}
	if e.terminalCause != nil {
		return e.terminalCause
	}
	if s.life.current != e || e.phase == executionFinished {
		return ErrSnapshotStale
	}
	return nil
}

func (s *Session) beginExecution(e *execution) error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if err := e.stepCtx.Err(); err != nil {
		return err
	}
	if e.stop != nil {
		e.beforeStart = true
		return interruptException(e.stop.reason)
	}
	e.phase = executionPreparing
	return nil
}

func (s *Session) beginSend(e *execution) error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if err := e.stepCtx.Err(); err != nil {
		return err
	}
	if e.stop != nil {
		e.beforeStart = true
		return interruptException(e.stop.reason)
	}
	e.sent, e.wireInFlight, e.phase = true, true, executionExecuting
	e.turnDone = make(chan struct{})
	s.driven = true
	return nil
}

func (s *Session) receivedTurn(e *execution) {
	s.life.mu.Lock()
	e.wireInFlight = false
	s.life.mu.Unlock()
}

func (s *Session) stopAtBoundary(e *execution) error {
	s.life.mu.Lock()
	if err := s.executionErrorLocked(e); err != nil {
		s.life.mu.Unlock()
		return err
	}
	cancelled := e.stepCtx.Err() != nil
	s.life.mu.Unlock()
	if cancelled {
		_, _, _ = s.requestInterrupt(e, InterruptOptions{})
	}
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if e.stop != nil {
		return errExecutionInterrupted
	}
	return nil
}

func (s *Session) beginCallback(e *execution, cbCtx context.Context) (context.Context, error) {
	if err := s.stopAtBoundary(e); err != nil {
		return nil, err
	}
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return nil, err
	}
	if e.stop != nil {
		return nil, errExecutionInterrupted
	}
	e.phase = executionCallback
	s.releaseTurnLocked(e)
	return callbackContext{Context: e.callbackCtx, values: cbCtx}, nil
}

func (s *Session) endCallback(e *execution) error {
	s.life.mu.Lock()
	e.phase = executionExecuting
	s.life.mu.Unlock()
	return s.stopAtBoundary(e)
}

func (s *Session) beforeResume(e *execution) error {
	if err := s.stopAtBoundary(e); err != nil {
		return err
	}
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if e.stop != nil {
		return errExecutionInterrupted
	}
	e.phase, e.wireInFlight = executionExecuting, true
	s.releaseTurnLocked(e)
	e.turnDone = make(chan struct{})
	return nil
}

func (s *Session) releaseTurnLocked(e *execution) {
	if e.turnDone != nil {
		close(e.turnDone)
		e.turnDone = nil
	}
}

func (s *Session) releaseOperationLocked(e *execution) {
	if e.operationDone != nil {
		close(e.operationDone)
		e.operationDone = nil
	}
}

func (s *Session) finishExecution(e *execution, value any, err error) {
	s.life.mu.Lock()
	cancel, stopWatch := e.cancelCallbacks, e.stopContext
	s.life.mu.Unlock()
	if stopWatch != nil {
		stopWatch()
	}
	cancel()
	s.life.mu.Lock()
	if e.phase == executionFinished {
		s.life.mu.Unlock()
		return
	}
	if e.terminalCause != nil {
		value, err = nil, e.terminalCause
	}
	e.value, e.err = value, err
	e.phase, e.wireInFlight = executionFinished, false
	s.releaseTurnLocked(e)
	clear(e.pending)
	if s.life.current == e {
		s.life.current = nil
	}
	s.releaseOperationLocked(e)
	if e.stop != nil && !e.stop.resolvedOnce && !e.stop.forceClaimed {
		outcome := InterruptFinished
		if e.beforeStart {
			outcome = InterruptBeforeStart
		} else if e.aborted {
			outcome = InterruptAborted
		}
		s.resolveInterruptLocked(e, outcome, nil)
	}
	release := e.releaseObserver
	e.releaseObserver = nil
	close(e.done)
	s.life.mu.Unlock()
	if release != nil {
		release()
	}
}

func (s *Session) terminateSession(err error) error {
	s.life.mu.Lock()
	if s.life.terminal == nil {
		s.life.terminal = err
		close(s.life.done)
	}
	err = s.life.terminal
	e := s.life.current
	paused := false
	if e != nil {
		e.terminalCause = err
		clear(e.pending)
		paused = e.phase == executionPaused
		if paused {
			e.phase = executionAborting
			e.operationDone = make(chan struct{})
		}
	}
	s.life.mu.Unlock()
	if e != nil {
		e.cancelCallbacks()
	}
	s.co.Terminate(err, "session_ended")
	s.pool.untrack(s)
	if paused {
		s.finishExecution(e, nil, err)
	}
	return err
}

func (s *Session) addFuture(e *execution, callID uint32, f *Future) error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if f == nil {
		return &ValueError{Message: "host returned a nil Future"}
	}
	if _, exists := e.pending[callID]; exists {
		return &ProtocolError{Message: "duplicate pending future call ID"}
	}
	if s.limits.pendingFutures != Unlimited && uint64(len(e.pending)) >= s.limits.pendingFutures {
		return &ResourceError{Resource: "pending future", Limit: s.limits.pendingFutures}
	}
	e.pending[callID] = f
	return nil
}

func (s *Session) pendingCount() int {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if s.life.current == nil {
		return 0
	}
	return len(s.life.current.pending)
}

type callbackContext struct {
	context.Context
	values context.Context
}

func (c callbackContext) Value(key any) any {
	if v := c.values.Value(key); v != nil {
		return v
	}
	return c.Context.Value(key)
}
