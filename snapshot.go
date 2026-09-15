package montygo

import (
	"context"
	"errors"
	"sync/atomic"

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
	s   *Session
	pt  *printTarget
	ans *answerer
}

func (d *snapshotDriver) advance(ev *wire.Event, err error) (Snapshot, error) {
	s := d.s
	if err != nil {
		if s.life.isClosing() {
			return nil, ErrSessionClosed
		}
		var perr *pool.Error
		if errors.As(err, &perr) && (perr.Kind == pool.KindRuntime || perr.Kind == pool.KindTyping) {
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
		return &Complete{Output: restoreValue(ev.Value, s.store)}, nil
	}
	if d.pt.failure != nil {
		return nil, s.poison(d.pt.failure)
	}
	switch ev.Kind {
	case wire.EventOsCall:
		args, kw := ev.OsCall.Args()
		return &FunctionSnapshot{d: d, ev: ev, FunctionName: ev.OsCall.Name(), Args: d.restoreArgs(args), Kwargs: kwargsRecord(kw, s.store), CallID: ev.OsCall.CallID, IsOSFunction: true}, nil
	case wire.EventFunctionCall:
		fc := ev.FunctionCall
		return &FunctionSnapshot{d: d, ev: ev, FunctionName: fc.FunctionName, Args: d.restoreArgs(fc.Args), Kwargs: kwargsRecord(fc.Kwargs, s.store), CallID: fc.CallID, AllowEagerAwait: fc.AllowEagerAwait, ObjectID: fc.ObjectID}, nil
	case wire.EventNameLookup:
		return &NameLookupSnapshot{d: d, ev: ev, VariableName: ev.NameLookup.Name, ObjectID: ev.NameLookup.ObjectID}, nil
	case wire.EventResolveFutures:
		return &FutureSnapshot{d: d, ev: ev, PendingCallIDs: append([]uint32(nil), ev.PendingCallIDs...)}, nil
	}
	return nil, s.poison(&ProtocolError{Message: "unexpected turn kind: " + ev.Kind.String()})
}

func (d *snapshotDriver) restoreArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = restoreValue(a, d.s.store)
	}
	return out
}

func (d *snapshotDriver) lock() error {
	d.s.mu.Lock()
	if d.s.closed || d.s.life.isClosing() {
		d.s.mu.Unlock()
		return ErrSessionClosed
	}
	return nil
}

func (d *snapshotDriver) run(ctx context.Context, fn func() (*wire.Event, error)) (Snapshot, error) {
	if err := d.lock(); err != nil {
		return nil, err
	}
	defer d.s.mu.Unlock()
	if err := d.s.ensureUsable(); err != nil {
		return nil, err
	}
	d.s.life.mu.Lock()
	aborted := d.s.life.aborted
	d.s.life.aborted = nil
	d.s.life.mu.Unlock()
	if aborted != nil {
		return nil, aborted
	}
	d.s.life.beginFeed(ctx)
	defer d.s.life.endFeed()
	return d.advance(fn())
}

func (d *snapshotDriver) resume(ctx context.Context, fn func(ctx context.Context) (*wire.Event, error)) (Snapshot, error) {
	return d.run(ctx, func() (*wire.Event, error) {
		d.pt.ctx = ctx
		return fn(ctx)
	})
}

func (d *snapshotDriver) resumeValue(ctx context.Context, v any) (*wire.Event, error) {
	return d.ans.resumeReturn(ctx, v)
}

func (d *snapshotDriver) resumeError(ctx context.Context, err error) (*wire.Event, error) {
	excType, msg := exceptionParts(err)
	return d.ans.resumeError(ctx, excType, msg)
}

func (d *snapshotDriver) dump(ctx context.Context) ([]byte, error) {
	if err := d.lock(); err != nil {
		return nil, err
	}
	defer d.s.mu.Unlock()
	state, err := d.s.co.Dump(ctx)
	if err != nil {
		return nil, d.s.mapError(err)
	}
	return state, nil
}

func (d *snapshotDriver) resumeAuto(ctx context.Context, ev *wire.Event) (Snapshot, error) {
	return d.run(ctx, func() (*wire.Event, error) {
		d.pt.ctx = ctx
		return d.ans.answer(ctx, ev)
	})
}

type singleUse struct{ used atomic.Bool }

func (u *singleUse) claim() error {
	if !u.used.CompareAndSwap(false, true) {
		return ErrSnapshotResumed
	}
	return nil
}

// FunctionSnapshot is paused at an external function, host method or OS call.
type FunctionSnapshot struct {
	singleUse
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
	if err := f.claim(); err != nil {
		return nil, err
	}
	return f.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) { return f.d.resumeValue(ctx, v) })
}

// ResumeAuto answers the call from the captured ExternalLookup, OS handler and mounts.
func (f *FunctionSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error) {
	if err := f.claim(); err != nil {
		return nil, err
	}
	return f.d.resumeAuto(ctx, f.ev)
}

// ResumeError raises err inside the sandbox.
func (f *FunctionSnapshot) ResumeError(ctx context.Context, err error) (Snapshot, error) {
	if e := f.claim(); e != nil {
		return nil, e
	}
	return f.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) { return f.d.resumeError(ctx, err) })
}

// ResumeNotFound makes the sandbox raise NameError.
func (f *FunctionSnapshot) ResumeNotFound(ctx context.Context) (Snapshot, error) {
	if err := f.claim(); err != nil {
		return nil, err
	}
	return f.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) {
		return f.d.s.co.Resume(ctx, wire.ExtResult{Kind: wire.ExtNotFound}, f.d.pt.onPrint)
	})
}

// ResumeFuture registers the call as a pending future.
func (f *FunctionSnapshot) ResumeFuture(ctx context.Context) (Snapshot, error) {
	if err := f.claim(); err != nil {
		return nil, err
	}
	return f.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) {
		return f.d.s.co.Resume(ctx, wire.ExtResult{Kind: wire.ExtFuture}, f.d.pt.onPrint)
	})
}

// ResumeNotHandled applies the OS call's default unhandled behaviour.
func (f *FunctionSnapshot) ResumeNotHandled(ctx context.Context) (Snapshot, error) {
	if !f.IsOSFunction {
		return nil, ErrNotOSCall
	}
	if err := f.claim(); err != nil {
		return nil, err
	}
	return f.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) {
		return f.d.s.co.Resume(ctx, wire.ExtResult{Kind: wire.ExtNotHandled}, f.d.pt.onPrint)
	})
}

// Dump serializes the paused worker.
func (f *FunctionSnapshot) Dump(ctx context.Context) ([]byte, error) { return f.d.dump(ctx) }

// NameLookupSnapshot is paused at an undefined name or lazy attribute.
type NameLookupSnapshot struct {
	singleUse
	d            *snapshotDriver
	ev           *wire.Event
	VariableName string
	ObjectID     string
}

func (*NameLookupSnapshot) isSnapshot() {}

// ResumeUnresolved leaves the name undefined (NameError, or AttributeError for an attribute).
func (n *NameLookupSnapshot) ResumeUnresolved(ctx context.Context) (Snapshot, error) {
	if err := n.claim(); err != nil {
		return nil, err
	}
	return n.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) {
		return n.d.s.co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupUndefined}, n.d.pt.onPrint)
	})
}

// ResumeFunction resolves the name to an external function.
func (n *NameLookupSnapshot) ResumeFunction(ctx context.Context, functionName string) (Snapshot, error) {
	if err := n.claim(); err != nil {
		return nil, err
	}
	return n.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) {
		return n.d.s.co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: value.Function{Name: functionName}}, n.d.pt.onPrint)
	})
}

// ResumeValue resolves the name to a value.
func (n *NameLookupSnapshot) ResumeValue(ctx context.Context, v any) (Snapshot, error) {
	if err := n.claim(); err != nil {
		return nil, err
	}
	return n.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) {
		prepared, err := prepareValue(v, n.d.s.store)
		if err != nil {
			return nil, &hostFailure{err: err}
		}
		return n.d.s.co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: prepared}, n.d.pt.onPrint)
	})
}

// ResumeAuto answers from the captured ExternalLookup.
func (n *NameLookupSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error) {
	if err := n.claim(); err != nil {
		return nil, err
	}
	return n.d.resumeAuto(ctx, n.ev)
}

// Dump serializes the paused worker.
func (n *NameLookupSnapshot) Dump(ctx context.Context) ([]byte, error) { return n.d.dump(ctx) }

// FutureResolution is a settled outcome for a pending future.
type FutureResolution struct {
	CallID uint32
	Value  any
	Err    error
}

// FutureSnapshot is paused with every sandbox task blocked on host futures.
type FutureSnapshot struct {
	singleUse
	d              *snapshotDriver
	ev             *wire.Event
	PendingCallIDs []uint32
}

func (*FutureSnapshot) isSnapshot() {}

// Resume delivers outcomes for one or more pending futures.
func (f *FutureSnapshot) Resume(ctx context.Context, results []FutureResolution) (Snapshot, error) {
	if err := f.claim(); err != nil {
		return nil, err
	}
	return f.d.resume(ctx, func(ctx context.Context) (*wire.Event, error) {
		out := make([]wire.FutureResult, 0, len(results))
		for _, r := range results {
			out = append(out, f.d.ans.settledResult(r.CallID, r.Value, r.Err))
		}
		return f.d.s.co.ResumeFutures(ctx, out, f.d.pt.onPrint)
	})
}

// ResumeAuto waits for the futures registered by earlier ResumeAuto calls.
func (f *FutureSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error) {
	if err := f.claim(); err != nil {
		return nil, err
	}
	return f.d.resumeAuto(ctx, f.ev)
}

// Dump serializes the paused worker.
func (f *FutureSnapshot) Dump(ctx context.Context) ([]byte, error) { return f.d.dump(ctx) }
