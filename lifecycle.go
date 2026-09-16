package montygo

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
	monterr "github.com/asalimonov/montygo/monterr"
	host "github.com/asalimonov/montygo/sandbox/host"
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
	stop            *stopRequest
	// delivered is set once a catchable stop reason was raised in the sandbox.
	delivered       bool
	pending         map[uint32]*host.Future
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

func newSession(p *Pool, rt *Runtime, cfg wire.Configure, limits sessionLimits) *Session {
	return &Session{pool: p, rt: rt, store: host.NewInstanceStore(limits.hostObjects), scriptName: cfg.ScriptName, cfg: cfg,
		limits: limits, life: lifecycle{done: make(chan struct{})}}
}

// attach binds the session to a connection and watches it. A rotation stops the
// watcher before closing the old connection, so a planned close is not a loss.
func (s *Session) attach(co *pool.Checkout, dialStart time.Time) {
	s.co = co
	stop := make(chan struct{})
	var once sync.Once
	s.armRotation(dialStart, func() { once.Do(func() { close(stop) }) })
	go func() {
		select {
		case <-co.Done():
		case <-stop:
			return
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
			case <-stop:
				return
			case <-s.Done():
				return
			}
		}
		var err error
		if co.Kind() == worker.KindWebSocket {
			de := &monterr.DisconnectError{Message: "monty worker connection closed while idle"}
			var closed *worker.ClosedError
			if errors.As(co.WorkerErr(), &closed) {
				de.Code, de.Reason = closed.Code, closed.Reason
				de.Message += ": " + closed.Error()
			}
			err = de
		} else {
			err = &monterr.CrashedError{Message: "monty worker crashed while idle"}
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
		return nil, monterr.ErrSessionBusy
	}
	if s.life.nextID == math.MaxUint64 {
		return nil, &monterr.ValueError{Message: "execution ID exhausted"}
	}
	s.life.nextID++
	cb, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e := &execution{id: s.life.nextID, phase: executionStarting, callbackCtx: cb, cancelCallbacks: cancel,
		pending: map[uint32]*host.Future{}, done: make(chan struct{}), operationDone: make(chan struct{}), releaseObserver: s.co.HoldObserver()}
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
	e.stopContext = context.AfterFunc(ctx, func() { s.requestStopForStep(e, step) })
}

func (s *Session) admissionErrorLocked() error {
	if s.life.terminal != nil {
		return s.life.terminal
	}
	if s.life.closeAttempt != nil {
		return monterr.ErrSessionClosed
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
		return monterr.ErrSnapshotStale
	}
	return nil
}

func (s *Session) beginExecution(e *execution) error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if err := s.stopBeforeStartLocked(e); err != nil {
		return err
	}
	e.phase = executionPreparing
	return nil
}

// stopBeforeStartLocked ends an execution whose stop was requested, or whose
// context ended, before anything was sent: nothing runs, so nothing drains.
func (s *Session) stopBeforeStartLocked(e *execution) error {
	if e.stop == nil && e.stepCtx.Err() != nil {
		e.stop = newStopRequest(s.limits.stop, time.Now())
		e.stop.requestAt = time.Now()
		s.enterRequestLocked(e)
		go s.watchStop(e, e.stop)
	}
	if e.stop == nil || !e.stop.requested {
		return nil
	}
	e.beforeStart = true
	return interruptException(e.stop.policy.Reason)
}

func (s *Session) beginSend(e *execution) error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if err := s.stopBeforeStartLocked(e); err != nil {
		return err
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

// stopAtBoundary reports a requested stop at a host boundary: an uncatchable
// stop aborts the feed; a catchable one is delivered by the next resume.
func (s *Session) stopAtBoundary(e *execution) error {
	s.life.mu.Lock()
	if err := s.executionErrorLocked(e); err != nil {
		s.life.mu.Unlock()
		return err
	}
	cancelled := e.stepCtx.Err() != nil && e.stop == nil
	s.life.mu.Unlock()
	if cancelled {
		s.requestStop(e, s.limits.stop)
	}
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	return s.boundaryStopLocked(e)
}

func (s *Session) boundaryStopLocked(e *execution) error {
	if e.stop == nil || !e.stop.requested {
		return nil
	}
	if e.stop.policy.Catchable {
		return nil
	}
	return errExecutionInterrupted
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
	if err := s.boundaryStopLocked(e); err != nil {
		return nil, err
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
	if err := s.boundaryStopLocked(e); err != nil {
		return err
	}
	deliver := e.stop != nil && e.stop.requested && !e.delivered
	e.phase, e.wireInFlight = executionExecuting, true
	s.releaseTurnLocked(e)
	e.turnDone = make(chan struct{})
	if deliver {
		return errDeliverStop
	}
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
		kind := StopFinished
		if e.beforeStart || e.aborted || (e.delivered && err != nil) {
			kind = StopAborted
		}
		s.resolveStopLocked(e, kind)
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
	var cancel context.CancelFunc
	if e != nil {
		e.terminalCause = err
		clear(e.pending)
		paused = e.phase == executionPaused
		if paused {
			e.phase = executionAborting
			e.operationDone = make(chan struct{})
		}
		cancel = e.cancelCallbacks
	}
	s.life.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.conn.disarm()
	s.co.Terminate(err, "session_ended")
	s.pool.untrack(s)
	if paused {
		s.finishExecution(e, nil, err)
	}
	return err
}

func (s *Session) addFuture(e *execution, callID uint32, f *host.Future) error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if f == nil {
		return &monterr.ValueError{Message: "host returned a nil Future"}
	}
	if _, exists := e.pending[callID]; exists {
		return &monterr.ProtocolError{Message: "duplicate pending future call ID"}
	}
	if s.limits.pendingFutures != Unlimited && uint64(len(e.pending)) >= s.limits.pendingFutures {
		return &monterr.ResourceError{Resource: "pending future", Limit: s.limits.pendingFutures}
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
