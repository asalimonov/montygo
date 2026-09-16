package montygo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
	monterr "github.com/asalimonov/montygo/monterr"
	host "github.com/asalimonov/montygo/sandbox/host"
)

// hostFailure is a host-side error that leaves the worker suspended; it poisons the session.
type hostFailure struct{ err error }

func (h *hostFailure) Error() string { return h.err.Error() }
func (h *hostFailure) Unwrap() error { return h.err }

type answerer struct {
	s      *Session
	exec   *execution
	lookup map[string]any
	host   *host.Host
	os     host.OSHandler
	pt     *printTarget
}

// errAborted marks a turn ended by AbortFeed; the pool error is returned beside it.
var errAborted = errors.New("feed aborted")

type abortedTurn struct{ cause error }

func (e *abortedTurn) Error() string        { return e.cause.Error() }
func (e *abortedTurn) Unwrap() error        { return e.cause }
func (e *abortedTurn) Is(target error) bool { return target == errAborted }

// lookupEntry resolves a sandbox name: ExternalLookup first, then the session host.
func (a *answerer) lookupEntry(name string) (any, bool) {
	if entry, ok := a.lookup[name]; ok {
		return entry, true
	}
	if a.host == nil {
		return nil, false
	}
	return a.host.LookupEntry(name)
}

// callHost runs one host call under a cancellable context registered with the
// session lifecycle. When the call's context was cancelled by the time it
// returns, the feed is aborted with a KeyboardInterrupt (or the interrupt
// reason) instead of resumed, and the session stays usable.
func (a *answerer) callHost(ctx, cbCtx context.Context, run func(context.Context) (any, error)) (any, *wire.Event, error) {
	cb, err := a.s.beginCallback(a.exec, cbCtx)
	if err != nil {
		return nil, nil, a.stop(ctx, err)
	}
	result, callErr := run(cb)
	if err := a.s.endCallback(a.exec); err != nil {
		return nil, nil, a.stop(ctx, err)
	}
	return result, nil, callErr
}

// beforeResume prepares a resume; a catchable stop replaces the payload.
func (a *answerer) beforeResume(ctx context.Context) (*wire.Exception, error) {
	err := a.s.beforeResume(a.exec)
	if errors.Is(err, errDeliverStop) {
		return a.s.deliverStop(a.exec), nil
	}
	if err != nil {
		return nil, a.stop(ctx, err)
	}
	return nil, nil
}

func (a *answerer) stop(ctx context.Context, err error) error {
	return &abortedTurn{cause: a.s.abortOrTerminal(ctx, a.exec, a.pt, err)}
}

func (a *answerer) registerFuture(callID uint32, fut *host.Future) error {
	return a.s.addFuture(a.exec, callID, fut)
}

func (a *answerer) resumeWire(ctx context.Context, result wire.ExtResult) (*wire.Event, error) {
	exc, err := a.beforeResume(ctx)
	if err != nil {
		return nil, err
	}
	if exc != nil {
		result = wire.ExtResult{Kind: wire.ExtError, Error: exc}
	}
	return a.s.co.Resume(ctx, result, a.pt.onPrint)
}

func (a *answerer) resumeLookup(ctx context.Context, result wire.ResumeNameLookup) (*wire.Event, error) {
	exc, err := a.beforeResume(ctx)
	if err != nil {
		return nil, err
	}
	if exc != nil {
		result = wire.ResumeNameLookup{Kind: wire.LookupError, Error: exc}
	}
	return a.s.co.ResumeNameLookup(ctx, result, a.pt.onPrint)
}

// resumeFutures answers the ids the worker waits on; a delivered stop
// settles every one of them with the reason.
func (a *answerer) resumeFutures(ctx context.Context, ids []uint32, results []wire.FutureResult) (*wire.Event, error) {
	exc, err := a.beforeResume(ctx)
	if err != nil {
		return nil, err
	}
	if exc != nil {
		results = results[:0]
		for _, id := range ids {
			results = append(results, wire.FutureResult{CallID: id, Result: wire.ExtResult{Kind: wire.ExtError, Error: exc}})
		}
	}
	a.s.life.mu.Lock()
	for _, result := range results {
		delete(a.exec.pending, result.CallID)
	}
	a.s.life.mu.Unlock()
	return a.s.co.ResumeFutures(ctx, results, a.pt.onPrint)
}

func (a *answerer) answer(ctx context.Context, ev *wire.Event) (*wire.Event, error) {
	if err := a.s.stopAtBoundary(a.exec); err != nil {
		return nil, a.stop(ctx, err)
	}
	cbCtx := a.s.co.CallbackContext(ctx)
	switch ev.Kind {
	case wire.EventFunctionCall:
		if ev.FunctionCall.ObjectID != "" {
			return a.answerMethodCall(ctx, cbCtx, ev.FunctionCall)
		}
		return a.answerFunctionCall(ctx, cbCtx, ev.FunctionCall)
	case wire.EventOsCall:
		return a.answerOsCall(ctx, cbCtx, ev.OsCall)
	case wire.EventNameLookup:
		if ev.NameLookup.ObjectID != "" {
			return a.answerObjectLookup(ctx, ev.NameLookup)
		}
		return a.answerNameLookup(ctx, ev.NameLookup)
	case wire.EventResolveFutures:
		return a.answerResolveFutures(ctx, ev.PendingCallIDs)
	}
	return nil, &hostFailure{err: &monterr.ProtocolError{Message: "unexpected turn kind: " + ev.Kind.String()}}
}

// asFunction adapts a lookup entry to a Function; a non-function entry is
// (nil, nil) and an invalid function signature reports its error.
func asFunction(entry any) (host.Function, error) {
	if f, ok := entry.(host.Function); ok {
		return f, nil
	}
	if entry != nil && reflect.TypeOf(entry).Kind() == reflect.Func {
		return host.Func(entry)
	}
	return nil, nil
}

func safeCall(ctx context.Context, fn host.Function, args []any, kwargs host.Kwargs) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, panicError(r)
		}
	}()
	return fn.Call(ctx, args, kwargs)
}

func (a *answerer) restoreArgs(args []any) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = host.RestoreValue(arg, a.s.store)
	}
	return out
}

func (a *answerer) resumeError(ctx context.Context, excType, message string) (*wire.Event, error) {
	return a.resumeWire(ctx, wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException(excType, message)})
}

// sendable prepares a host value for a resume, mapping failures to in-sandbox exceptions.
func (a *answerer) sendable(v any) wire.ExtResult {
	prepared, err := host.PrepareValue(v, a.s.store)
	if err != nil {
		excType, msg := monterr.ExceptionParts(err)
		return wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException(excType, msg)}
	}
	if value.ExceedsMaxDepth(prepared) {
		return wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException("RuntimeError", "Max input depth exceeded")}
	}
	return wire.ExtResult{Kind: wire.ExtReturn, Value: prepared}
}

func (a *answerer) resumeReturn(ctx context.Context, v any) (*wire.Event, error) {
	return a.resumeWire(ctx, a.sendable(v))
}

func (a *answerer) settledResult(callID uint32, v any, err error) wire.FutureResult {
	if err != nil {
		excType, msg := monterr.ExceptionParts(err)
		return wire.FutureResult{CallID: callID, Result: wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException(excType, msg)}}
	}
	return wire.FutureResult{CallID: callID, Result: a.sendable(v)}
}

func (a *answerer) resumeOutcome(ctx, cbCtx context.Context, callID uint32, eager bool, result any) (*wire.Event, error) {
	if fut, ok := result.(*host.Future); ok {
		if fut == nil {
			return a.resumeError(ctx, "TypeError", "host returned a nil Future")
		}
		if eager {
			v, aborted, err := a.callHost(ctx, cbCtx, func(cb context.Context) (any, error) { return fut.Wait(cb) })
			if aborted != nil || errors.Is(err, errAborted) {
				return aborted, err
			}
			return a.resumeFutures(ctx, []uint32{callID}, []wire.FutureResult{a.settledResult(callID, v, err)})
		}
		if err := a.registerFuture(callID, fut); err != nil {
			var protocol *monterr.ProtocolError
			if errors.As(err, &protocol) {
				return nil, &hostFailure{err: err}
			}
			excType, msg := monterr.ExceptionParts(err)
			return a.resumeError(ctx, excType, msg)
		}
		return a.resumeWire(ctx, wire.ExtResult{Kind: wire.ExtFuture})
	}
	return a.resumeReturn(ctx, result)
}

func (a *answerer) answerFunctionCall(ctx, cbCtx context.Context, fc *wire.FunctionCall) (*wire.Event, error) {
	entry, ok := a.lookupEntry(fc.FunctionName)
	if !ok {
		return a.resumeWire(ctx, wire.ExtResult{Kind: wire.ExtNotFound})
	}
	fn, err := asFunction(entry)
	if err != nil {
		return a.resumeError(ctx, "TypeError", fc.FunctionName+": "+err.Error())
	}
	if fn == nil {
		return a.resumeError(ctx, "TypeError", fmt.Sprintf("'%s' object is not callable", host.TypeName(entry)))
	}
	args, kwargs := a.restoreArgs(fc.Args), host.KwargsRecord(fc.Kwargs, a.s.store)
	result, aborted, err := a.callHost(ctx, cbCtx, func(cb context.Context) (any, error) { return safeCall(cb, fn, args, kwargs) })
	if aborted != nil || errors.Is(err, errAborted) {
		return aborted, err
	}
	if err != nil {
		excType, msg := monterr.ExceptionParts(err)
		return a.resumeError(ctx, excType, msg)
	}
	return a.resumeOutcome(ctx, cbCtx, fc.CallID, fc.AllowEagerAwait, result)
}

func (a *answerer) answerMethodCall(ctx, cbCtx context.Context, fc *wire.FunctionCall) (*wire.Event, error) {
	w, found := a.s.store.Get(fc.ObjectID)
	if strings.HasPrefix(fc.FunctionName, "_") && fc.FunctionName != "__call__" {
		name := "object"
		if found {
			name = host.WrapperName(w)
		}
		return a.resumeError(ctx, "AttributeError", fmt.Sprintf("'%s' object has no attribute '%s'", name, fc.FunctionName))
	}
	if !found {
		return a.resumeError(ctx, "RuntimeError", fmt.Sprintf("no host object registered for method call '%s' (id %s) — the instance store is empty after loading a dump into a fresh session", fc.FunctionName, fc.ObjectID))
	}
	args, kwargs := a.restoreArgs(fc.Args), host.KwargsRecord(fc.Kwargs, a.s.store)
	result, aborted, err := a.callHost(ctx, cbCtx, func(cb context.Context) (any, error) {
		return safeMethod(cb, w, fc.FunctionName, args, kwargs)
	})
	if aborted != nil || errors.Is(err, errAborted) {
		return aborted, err
	}
	if err != nil {
		var ae *host.AttrError
		if errors.As(err, &ae) {
			return a.resumeError(ctx, "AttributeError", ae.Error())
		}
		excType, msg := monterr.ExceptionParts(err)
		return a.resumeError(ctx, excType, msg)
	}
	return a.resumeOutcome(ctx, cbCtx, fc.CallID, fc.AllowEagerAwait, result)
}

func safeMethod(ctx context.Context, w host.Wrapper, name string, args []any, kwargs host.Kwargs) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, panicError(r)
		}
	}()
	return host.CallWrapperMethod(ctx, w, name, args, kwargs)
}

func (a *answerer) answerObjectLookup(ctx context.Context, nl *wire.NameLookup) (*wire.Event, error) {
	undefined := wire.ResumeNameLookup{Kind: wire.LookupUndefined}
	w, found := a.s.store.Get(nl.ObjectID)
	if !found || strings.HasPrefix(nl.Name, "_") {
		return a.resumeLookup(ctx, undefined)
	}
	v, _, err := a.callHost(ctx, a.s.co.CallbackContext(ctx), func(context.Context) (any, error) { return safeLazy(w, nl.Name) })
	if errors.Is(err, errAborted) {
		return nil, err
	}
	if err == nil {
		var prepared any
		prepared, err = host.PrepareValue(v, a.s.store)
		if err == nil && value.ExceedsMaxDepth(prepared) {
			err = &monterr.RaisedError{ExcType: "RuntimeError", Message: "Max input depth exceeded"}
		}
		if err == nil {
			return a.resumeLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: prepared})
		}
	}
	if errors.Is(err, host.ErrAttrNotExposed) {
		return a.resumeLookup(ctx, undefined)
	}
	excType, msg := monterr.ExceptionParts(err)
	return a.resumeLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupError, Error: wire.NewException(excType, msg)})
}

func safeLazy(w host.Wrapper, name string) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, panicError(r)
		}
	}()
	return host.WrapperLazyAttr(w, name)
}

func (a *answerer) answerNameLookup(ctx context.Context, nl *wire.NameLookup) (*wire.Event, error) {
	entry, ok := a.lookupEntry(nl.Name)
	if !ok {
		return a.resumeLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupUndefined})
	}
	if fn, _ := asFunction(entry); fn != nil {
		return a.resumeLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: value.Function{Name: nl.Name}})
	}
	prepared, err := host.PrepareValue(entry, a.s.store)
	if err != nil {
		return nil, &hostFailure{err: err}
	}
	return a.resumeLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: prepared})
}

func (a *answerer) answerOsCall(ctx, cbCtx context.Context, call *wire.OsCall) (*wire.Event, error) {
	type mountResult struct {
		handled bool
		result  wire.ExtResult
	}
	result, _, err := a.callHost(ctx, cbCtx, func(cb context.Context) (any, error) {
		handled, result, err := a.s.co.HandlePendingMount(cb)
		return mountResult{handled, result}, err
	})
	if err != nil {
		return nil, err
	}
	mounted := result.(mountResult)
	if mounted.handled {
		ev, err := a.resumeWire(ctx, mounted.result)
		var perr *pool.Error
		if errors.As(err, &perr) && perr.Kind == pool.KindRuntime && perr.PreSend {
			a.s.receivedTurn(a.exec)
			return a.resumeWire(ctx, wire.ExtResult{Kind: wire.ExtError, Error: perr.Exception})
		}
		return ev, err
	}
	notHandled := wire.ExtResult{Kind: wire.ExtNotHandled}
	if a.os == nil {
		return a.resumeWire(ctx, notHandled)
	}
	args, kw := call.Args()
	restored, kwargs := a.restoreArgs(args), host.KwargsRecord(kw, a.s.store)
	result, aborted, err := a.callHost(ctx, cbCtx, func(cb context.Context) (any, error) {
		r, err := safeOS(cb, a.os, call.Name(), restored, kwargs)
		if fut, ok := r.(*host.Future); ok && err == nil {
			return fut.Wait(cb)
		}
		return r, err
	})
	if aborted != nil || errors.Is(err, errAborted) {
		return aborted, err
	}
	if err != nil {
		excType, msg := monterr.ExceptionParts(err)
		return a.resumeError(ctx, excType, msg)
	}
	if result == host.NotHandled {
		return a.resumeWire(ctx, notHandled)
	}
	return a.resumeReturn(ctx, result)
}

func safeOS(ctx context.Context, handler host.OSHandler, name string, args []any, kwargs host.Kwargs) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, panicError(r)
		}
	}()
	return handler(ctx, name, args, kwargs)
}

func (a *answerer) answerResolveFutures(ctx context.Context, ids []uint32) (*wire.Event, error) {
	if len(ids) == 0 {
		return nil, &hostFailure{err: &monterr.ProtocolError{Message: "worker reported ResolveFutures with no pending call ids"}}
	}
	cases := make([]reflect.SelectCase, 0, len(ids)+1)
	futures := make(map[uint32]*host.Future, len(ids))
	a.s.life.mu.Lock()
	for _, id := range ids {
		f, ok := a.exec.pending[id]
		if !ok || futures[id] != nil {
			a.s.life.mu.Unlock()
			return nil, &hostFailure{err: &monterr.ProtocolError{Message: fmt.Sprintf("worker reported unknown or duplicate pending call id %d", id)}}
		}
		futures[id] = f
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(f.Done())})
	}
	a.s.life.mu.Unlock()
	_, aborted, err := a.callHost(ctx, ctx, func(cb context.Context) (any, error) {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(cb.Done())})
		reflect.Select(cases)
		return nil, nil
	})
	if aborted != nil || errors.Is(err, errAborted) {
		return aborted, err
	}
	var results []wire.FutureResult
	for _, id := range ids {
		f := futures[id]
		if f.IsSettled() {
			a.s.life.mu.Lock()
			delete(a.exec.pending, id)
			a.s.life.mu.Unlock()
			value, ferr := f.Result()
			results = append(results, a.settledResult(id, value, ferr))
		}
	}
	return a.resumeFutures(ctx, ids, results)
}

// panicError turns a recovered panic value into the error a host call reports.
func panicError(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	return fmt.Errorf("%v", r)
}
