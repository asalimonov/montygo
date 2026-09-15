package monty

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

// hostFailure is a host-side error that leaves the worker suspended; it poisons the session.
type hostFailure struct{ err error }

func (h *hostFailure) Error() string { return h.err.Error() }
func (h *hostFailure) Unwrap() error { return h.err }

func panicError(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	return fmt.Errorf("%v", r)
}

type answerer struct {
	s       *Session
	lookup  map[string]any
	os      OSHandler
	pt      *printTarget
	futures map[uint32]*Future
}

func (a *answerer) answer(ctx context.Context, ev *wire.Event) (*wire.Event, error) {
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
	return nil, &hostFailure{err: &ProtocolError{Message: "unexpected turn kind: " + ev.Kind.String()}}
}

func asFunction(entry any) (Function, bool) {
	if f, ok := entry.(Function); ok {
		return f, true
	}
	if entry != nil && reflect.TypeOf(entry).Kind() == reflect.Func {
		f, err := Func(entry)
		return f, err == nil
	}
	return nil, false
}

func safeCall(ctx context.Context, fn Function, args []any, kwargs Kwargs) (result any, err error) {
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
		out[i] = restoreValue(arg, a.s.store)
	}
	return out
}

func (a *answerer) resumeError(ctx context.Context, excType, message string) (*wire.Event, error) {
	return a.s.co.Resume(ctx, wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException(excType, message)}, a.pt.onPrint)
}

// sendable prepares a host value for a resume, mapping failures to in-sandbox exceptions.
func (a *answerer) sendable(v any) wire.ExtResult {
	prepared, err := prepareValue(v, a.s.store)
	if err != nil {
		excType, msg := exceptionParts(err)
		return wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException(excType, msg)}
	}
	if value.ExceedsMaxDepth(prepared) {
		return wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException("RuntimeError", "Max input depth exceeded")}
	}
	return wire.ExtResult{Kind: wire.ExtReturn, Value: prepared}
}

func (a *answerer) resumeReturn(ctx context.Context, v any) (*wire.Event, error) {
	return a.s.co.Resume(ctx, a.sendable(v), a.pt.onPrint)
}

func (a *answerer) settledResult(callID uint32, v any, err error) wire.FutureResult {
	if err != nil {
		excType, msg := exceptionParts(err)
		return wire.FutureResult{CallID: callID, Result: wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException(excType, msg)}}
	}
	return wire.FutureResult{CallID: callID, Result: a.sendable(v)}
}

func (a *answerer) resumeOutcome(ctx context.Context, callID uint32, eager bool, result any) (*wire.Event, error) {
	if fut, ok := result.(*Future); ok {
		if eager {
			v, err := fut.Wait(ctx)
			if ctx.Err() != nil {
				return nil, &hostFailure{err: ctx.Err()}
			}
			return a.s.co.ResumeFutures(ctx, []wire.FutureResult{a.settledResult(callID, v, err)}, a.pt.onPrint)
		}
		a.futures[callID] = fut
		return a.s.co.Resume(ctx, wire.ExtResult{Kind: wire.ExtFuture}, a.pt.onPrint)
	}
	return a.resumeReturn(ctx, result)
}

func (a *answerer) answerFunctionCall(ctx, cbCtx context.Context, fc *wire.FunctionCall) (*wire.Event, error) {
	entry, ok := a.lookup[fc.FunctionName]
	if !ok {
		return a.s.co.Resume(ctx, wire.ExtResult{Kind: wire.ExtNotFound}, a.pt.onPrint)
	}
	fn, callable := asFunction(entry)
	if !callable {
		return a.resumeError(ctx, "TypeError", fmt.Sprintf("'%s' object is not callable", hostTypeName(entry)))
	}
	result, err := safeCall(cbCtx, fn, a.restoreArgs(fc.Args), kwargsRecord(fc.Kwargs, a.s.store))
	if err != nil {
		excType, msg := exceptionParts(err)
		return a.resumeError(ctx, excType, msg)
	}
	return a.resumeOutcome(ctx, fc.CallID, fc.AllowEagerAwait, result)
}

func (a *answerer) answerMethodCall(ctx, cbCtx context.Context, fc *wire.FunctionCall) (*wire.Event, error) {
	w, found := a.s.store.get(fc.ObjectID)
	if strings.HasPrefix(fc.FunctionName, "_") && fc.FunctionName != "__call__" {
		name := "object"
		if found {
			name = w.wrapperName()
		}
		return a.resumeError(ctx, "AttributeError", fmt.Sprintf("'%s' object has no attribute '%s'", name, fc.FunctionName))
	}
	if !found {
		return a.resumeError(ctx, "RuntimeError", fmt.Sprintf("no host object registered for method call '%s' (id %s) — the instance store is empty after loading a dump into a fresh session", fc.FunctionName, fc.ObjectID))
	}
	result, err := safeMethod(cbCtx, w, fc.FunctionName, a.restoreArgs(fc.Args), kwargsRecord(fc.Kwargs, a.s.store))
	if err != nil {
		var ae *attrError
		if errors.As(err, &ae) {
			return a.resumeError(ctx, "AttributeError", ae.msg)
		}
		excType, msg := exceptionParts(err)
		return a.resumeError(ctx, excType, msg)
	}
	return a.resumeOutcome(ctx, fc.CallID, fc.AllowEagerAwait, result)
}

func safeMethod(ctx context.Context, w wrapper, name string, args []any, kwargs Kwargs) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, panicError(r)
		}
	}()
	return w.callMethod(ctx, name, args, kwargs)
}

func (a *answerer) answerObjectLookup(ctx context.Context, nl *wire.NameLookup) (*wire.Event, error) {
	undefined := wire.ResumeNameLookup{Kind: wire.LookupUndefined}
	w, found := a.s.store.get(nl.ObjectID)
	if !found || strings.HasPrefix(nl.Name, "_") {
		return a.s.co.ResumeNameLookup(ctx, undefined, a.pt.onPrint)
	}
	v, err := safeLazy(w, nl.Name)
	if err == nil {
		var prepared any
		prepared, err = prepareValue(v, a.s.store)
		if err == nil && value.ExceedsMaxDepth(prepared) {
			err = &RaisedError{ExcType: "RuntimeError", Message: "Max input depth exceeded"}
		}
		if err == nil {
			return a.s.co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: prepared}, a.pt.onPrint)
		}
	}
	if errors.Is(err, ErrAttrNotExposed) {
		return a.s.co.ResumeNameLookup(ctx, undefined, a.pt.onPrint)
	}
	excType, msg := exceptionParts(err)
	return a.s.co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupError, Error: wire.NewException(excType, msg)}, a.pt.onPrint)
}

func safeLazy(w wrapper, name string) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, panicError(r)
		}
	}()
	return w.lazyAttr(name)
}

func (a *answerer) answerNameLookup(ctx context.Context, nl *wire.NameLookup) (*wire.Event, error) {
	entry, ok := a.lookup[nl.Name]
	if !ok {
		return a.s.co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupUndefined}, a.pt.onPrint)
	}
	if _, callable := asFunction(entry); callable {
		return a.s.co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: value.Function{Name: nl.Name}}, a.pt.onPrint)
	}
	prepared, err := prepareValue(entry, a.s.store)
	if err != nil {
		return nil, &hostFailure{err: err}
	}
	return a.s.co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupValue, Value: prepared}, a.pt.onPrint)
}

func (a *answerer) answerOsCall(ctx, cbCtx context.Context, call *wire.OsCall) (*wire.Event, error) {
	ev, handled, err := a.s.co.ResumeFromMounts(ctx, a.pt.onPrint)
	if handled || err != nil {
		return ev, err
	}
	notHandled := wire.ExtResult{Kind: wire.ExtNotHandled}
	if a.os == nil {
		return a.s.co.Resume(ctx, notHandled, a.pt.onPrint)
	}
	args, kw := call.Args()
	result, err := safeOS(cbCtx, a.os, call.Name(), a.restoreArgs(args), kwargsRecord(kw, a.s.store))
	if err != nil {
		excType, msg := exceptionParts(err)
		return a.resumeError(ctx, excType, msg)
	}
	if fut, ok := result.(*Future); ok {
		result, err = fut.Wait(ctx)
		if ctx.Err() != nil {
			return nil, &hostFailure{err: ctx.Err()}
		}
		if err != nil {
			excType, msg := exceptionParts(err)
			return a.resumeError(ctx, excType, msg)
		}
	}
	if result == NotHandled {
		return a.s.co.Resume(ctx, notHandled, a.pt.onPrint)
	}
	return a.resumeReturn(ctx, result)
}

func safeOS(ctx context.Context, handler OSHandler, name string, args []any, kwargs Kwargs) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, panicError(r)
		}
	}()
	return handler(ctx, name, args, kwargs)
}

func (a *answerer) answerResolveFutures(ctx context.Context, ids []uint32) (*wire.Event, error) {
	if len(ids) == 0 {
		return nil, &hostFailure{err: &ProtocolError{Message: "worker reported ResolveFutures with no pending call ids"}}
	}
	cases := make([]reflect.SelectCase, 0, len(ids)+1)
	for _, id := range ids {
		f, ok := a.futures[id]
		if !ok {
			return nil, &hostFailure{err: &ProtocolError{Message: fmt.Sprintf("worker reported unknown pending call id %d", id)}}
		}
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(f.Done())})
	}
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})
	if chosen, _, _ := reflect.Select(cases); chosen == len(ids) {
		return nil, &hostFailure{err: ctx.Err()}
	}
	var results []wire.FutureResult
	for _, id := range ids {
		f := a.futures[id]
		if f.settled() {
			delete(a.futures, id)
			results = append(results, a.settledResult(id, f.value, f.err))
		}
	}
	return a.s.co.ResumeFutures(ctx, results, a.pt.onPrint)
}

func hostTypeName(v any) string {
	switch x := v.(type) {
	case *ClassInstance:
		return x.Name()
	case *ClassType:
		return "type"
	case *ClassProxy:
		return x.Name
	}
	return value.PyTypeName(v)
}
