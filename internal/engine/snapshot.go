package engine

import (
	"context"
	"errors"
	"math"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

// Snapshot is a paused execution or its completion.
type Snapshot interface{ isSnapshot() }

// Complete is a finished FeedStart execution.
type Complete struct {
	Output any
}

func (*Complete) isSnapshot() {}

type snapshotDriver struct {
	exec *execution
	s    *Session
	pt   *printTarget
	ans  *answerer
}

func (d *snapshotDriver) advance(ev *wire.Event, err error) (snap Snapshot, resultErr error) {
	s, e := d.s, d.exec
	s.receivedTurn(e)
	defer func() {
		if resultErr != nil {
			s.finishExecution(e, nil, resultErr)
		}
	}()
	if terminal := s.Err(); terminal != nil {
		return nil, terminal
	}
	if err != nil {
		var perr *pool.Error
		if errors.As(err, &perr) && (perr.Kind == pool.KindRuntime || perr.Kind == pool.KindTyping) || e.aborted {
			if ferr := d.pt.finish(); ferr != nil {
				return nil, ferr
			}
			if d.pt.failure != nil {
				return nil, d.pt.failure
			}
		}
		var hf *hostFailure
		if errors.As(err, &hf) {
			return nil, s.poison(hf.err)
		}
		return nil, s.mapError(err)
	}
	if ev.Kind == wire.EventComplete {
		if ferr := d.pt.finish(); ferr != nil {
			return nil, ferr
		}
		if d.pt.failure != nil {
			return nil, d.pt.failure
		}
		value := restoreValue(ev.Value, s.store)
		s.finishExecution(e, value, nil)
		return &Complete{Output: e.value}, e.err
	}
	if d.pt.failure != nil {
		return nil, s.poison(d.pt.failure)
	}
	if err := s.stopAtBoundary(e); err != nil {
		return nil, s.abortOrTerminal(d.pt.ctx, e, d.pt, err)
	}
	var token *snapshotToken
	switch ev.Kind {
	case wire.EventOsCall:
		args, kw := ev.OsCall.Args()
		f := &FunctionSnapshot{d: d, ev: ev, FunctionName: ev.OsCall.Name(), Args: d.restoreArgs(args), Kwargs: kwargsRecord(kw, s.store), CallID: ev.OsCall.CallID, IsOSFunction: true}
		snap, token = f, &f.token
	case wire.EventFunctionCall:
		fc := ev.FunctionCall
		f := &FunctionSnapshot{d: d, ev: ev, FunctionName: fc.FunctionName, Args: d.restoreArgs(fc.Args), Kwargs: kwargsRecord(fc.Kwargs, s.store), CallID: fc.CallID, AllowEagerAwait: fc.AllowEagerAwait, ObjectID: fc.ObjectID}
		snap, token = f, &f.token
	case wire.EventNameLookup:
		n := &NameLookupSnapshot{d: d, ev: ev, VariableName: ev.NameLookup.Name, ObjectID: ev.NameLookup.ObjectID}
		snap, token = n, &n.token
	case wire.EventResolveFutures:
		f := &FutureSnapshot{d: d, ev: ev, PendingCallIDs: append([]uint32(nil), ev.PendingCallIDs...)}
		snap, token = f, &f.token
	default:
		return nil, s.poison(&ProtocolError{Message: "unexpected turn kind: " + ev.Kind.String()})
	}
	if err := s.publishPause(e, token); err != nil {
		return nil, s.abortOrTerminal(d.pt.ctx, e, d.pt, err)
	}
	return snap, nil
}

func (s *Session) publishPause(e *execution, token *snapshotToken) error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.executionErrorLocked(e); err != nil {
		return err
	}
	if e.stop != nil && e.stop.requested {
		// A catchable stop cannot be delivered through an application-driven
		// snapshot chain; it falls back to AbortFeed.
		return errExecutionInterrupted
	}
	if e.sequence == math.MaxUint64 {
		return &ValueError{Message: "snapshot sequence exhausted"}
	}
	e.sequence++
	*token = snapshotToken{exec: e, sequence: e.sequence}
	e.phase = executionPaused
	s.releaseTurnLocked(e)
	s.releaseOperationLocked(e)
	return nil
}

func (s *Session) snapshotErrorLocked(t *snapshotToken) error {
	if t.used {
		return ErrSnapshotResumed
	}
	if t.exec.terminalCause != nil {
		return t.exec.terminalCause
	}
	if t.exec.phase == executionFinished {
		if t.exec.err != nil {
			return t.exec.err
		}
		return ErrSnapshotStale
	}
	if s.life.terminal != nil {
		return s.life.terminal
	}
	if s.life.current != t.exec || t.sequence != t.exec.sequence {
		return ErrSnapshotStale
	}
	if t.exec.phase != executionPaused {
		return ErrSessionBusy
	}
	return s.admissionErrorLocked()
}

func (s *Session) claimSnapshot(ctx context.Context, t *snapshotToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if err := s.snapshotErrorLocked(t); err != nil {
		return err
	}
	t.used = true
	t.exec.phase = executionExecuting
	t.exec.operationDone = make(chan struct{})
	s.installStepLocked(t.exec, ctx)
	return nil
}

func (d *snapshotDriver) restoreArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = restoreValue(a, d.s.store)
	}
	return out
}

func (d *snapshotDriver) run(ctx context.Context, token *snapshotToken, fn func() (*wire.Event, error)) (Snapshot, error) {
	if err := d.s.claimSnapshot(ctx, token); err != nil {
		return nil, err
	}
	d.pt.ctx = context.WithoutCancel(ctx)
	return d.advance(fn())
}

func (d *snapshotDriver) resume(ctx context.Context, token *snapshotToken, fn func(context.Context) (*wire.Event, error)) (Snapshot, error) {
	wctx := context.WithoutCancel(ctx)
	return d.run(ctx, token, func() (*wire.Event, error) { return fn(wctx) })
}

func (d *snapshotDriver) resumeValue(ctx context.Context, v any) (*wire.Event, error) {
	return d.ans.resumeReturn(ctx, v)
}

func (d *snapshotDriver) resumeError(ctx context.Context, err error) (*wire.Event, error) {
	excType, msg := exceptionParts(err)
	return d.ans.resumeError(ctx, excType, msg)
}

func (d *snapshotDriver) dump(ctx context.Context, token *snapshotToken) ([]byte, error) {
	release, err := d.s.reserveControlFor(ctx, true, token)
	if err != nil {
		return nil, err
	}
	defer release()
	state, err := d.s.co.Dump(ctx)
	if err != nil {
		return nil, d.s.mapError(err)
	}
	return state, nil
}

func (d *snapshotDriver) resumeAuto(ctx context.Context, token *snapshotToken, ev *wire.Event) (Snapshot, error) {
	wctx := context.WithoutCancel(ctx)
	return d.run(ctx, token, func() (*wire.Event, error) { return d.ans.answer(wctx, ev) })
}

// FunctionSnapshot is paused at an external function, host method or OS call.
type FunctionSnapshot struct {
	token           snapshotToken
	d               *snapshotDriver
	ev              *wire.Event
	FunctionName    string
	Args            []any
	Kwargs          Kwargs
	CallID          uint32
	IsOSFunction    bool
	AllowEagerAwait bool
	// ObjectID is the receiver's id for host-routed calls, "" otherwise.
	ObjectID string
}

func (*FunctionSnapshot) isSnapshot() {}

// Resume answers the call with its return value.
func (f *FunctionSnapshot) Resume(ctx context.Context, v any) (Snapshot, error) {
	return f.d.resume(ctx, &f.token, func(ctx context.Context) (*wire.Event, error) { return f.d.resumeValue(ctx, v) })
}

// ResumeAuto answers the call from the captured ExternalLookup, OS handler and mounts.
func (f *FunctionSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error) {
	return f.d.resumeAuto(ctx, &f.token, f.ev)
}

// ResumeError raises err inside the sandbox.
func (f *FunctionSnapshot) ResumeError(ctx context.Context, err error) (Snapshot, error) {
	return f.d.resume(ctx, &f.token, func(ctx context.Context) (*wire.Event, error) { return f.d.resumeError(ctx, err) })
}

// ResumeNotFound makes the sandbox raise NameError.
func (f *FunctionSnapshot) ResumeNotFound(ctx context.Context) (Snapshot, error) {
	return f.d.resume(ctx, &f.token, func(ctx context.Context) (*wire.Event, error) {
		return f.d.ans.resumeWire(ctx, wire.ExtResult{Kind: wire.ExtNotFound})
	})
}

// ResumeFuture registers the call as a pending future.
func (f *FunctionSnapshot) ResumeFuture(ctx context.Context) (Snapshot, error) {
	return f.d.resume(ctx, &f.token, func(ctx context.Context) (*wire.Event, error) {
		return f.d.ans.resumeWire(ctx, wire.ExtResult{Kind: wire.ExtFuture})
	})
}

// ResumeNotHandled applies the OS call's default unhandled behaviour.
func (f *FunctionSnapshot) ResumeNotHandled(ctx context.Context) (Snapshot, error) {
	if !f.IsOSFunction {
		return nil, ErrNotOSCall
	}
	return f.d.resume(ctx, &f.token, func(ctx context.Context) (*wire.Event, error) {
		return f.d.ans.resumeWire(ctx, wire.ExtResult{Kind: wire.ExtNotHandled})
	})
}

// Dump serializes the paused worker.
func (f *FunctionSnapshot) Dump(ctx context.Context) ([]byte, error) { return f.d.dump(ctx, &f.token) }

// NameLookupSnapshot is paused at an undefined name or lazy attribute.
type NameLookupSnapshot struct {
	token        snapshotToken
	d            *snapshotDriver
	ev           *wire.Event
	VariableName string
	ObjectID     string
}

func (*NameLookupSnapshot) isSnapshot() {}

// ResumeUnresolved leaves the name undefined (NameError, or AttributeError for an attribute).
func (n *NameLookupSnapshot) ResumeUnresolved(ctx context.Context) (Snapshot, error) {
	return n.d.resume(ctx, &n.token, func(ctx context.Context) (*wire.Event, error) {
		return n.d.ans.resumeLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupUndefined})
	})
}

// ResumeFunction resolves the name to an external function.
func (n *NameLookupSnapshot) ResumeFunction(ctx context.Context, functionName string) (Snapshot, error) {
	return n.d.resume(ctx, &n.token, func(ctx context.Context) (*wire.Event, error) {
		return n.d.ans.resumeLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: value.Function{Name: functionName}})
	})
}

// ResumeValue resolves the name to a value.
func (n *NameLookupSnapshot) ResumeValue(ctx context.Context, v any) (Snapshot, error) {
	return n.d.resume(ctx, &n.token, func(ctx context.Context) (*wire.Event, error) {
		prepared, err := prepareValue(v, n.d.s.store)
		if err != nil {
			return nil, &hostFailure{err: err}
		}
		return n.d.ans.resumeLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: prepared})
	})
}

// ResumeAuto answers from the captured ExternalLookup.
func (n *NameLookupSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error) {
	return n.d.resumeAuto(ctx, &n.token, n.ev)
}

// Dump serializes the paused worker.
func (n *NameLookupSnapshot) Dump(ctx context.Context) ([]byte, error) {
	return n.d.dump(ctx, &n.token)
}

// FutureResolution is a settled outcome for a pending future.
type FutureResolution struct {
	CallID uint32
	Value  any
	Err    error
}

// FutureSnapshot is paused with every sandbox task blocked on host futures.
type FutureSnapshot struct {
	token          snapshotToken
	d              *snapshotDriver
	ev             *wire.Event
	PendingCallIDs []uint32
}

func (*FutureSnapshot) isSnapshot() {}

// Resume delivers outcomes for one or more pending futures.
func (f *FutureSnapshot) Resume(ctx context.Context, results []FutureResolution) (Snapshot, error) {
	return f.d.resume(ctx, &f.token, func(ctx context.Context) (*wire.Event, error) {
		out := make([]wire.FutureResult, 0, len(results))
		for _, r := range results {
			out = append(out, f.d.ans.settledResult(r.CallID, r.Value, r.Err))
		}
		return f.d.ans.resumeFutures(ctx, f.PendingCallIDs, out)
	})
}

// ResumeAuto waits for the futures registered by earlier ResumeAuto calls.
func (f *FutureSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error) {
	return f.d.resumeAuto(ctx, &f.token, f.ev)
}

// Dump serializes the paused worker.
func (f *FutureSnapshot) Dump(ctx context.Context) ([]byte, error) { return f.d.dump(ctx, &f.token) }
