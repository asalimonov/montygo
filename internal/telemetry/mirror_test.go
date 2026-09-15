package telemetry

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/asalimonov/montygo/internal/wire"
)

type mirrorRig struct {
	spans *tracetest.SpanRecorder
	logs  *logSink
}

func newMirror(t *testing.T, pid int, hasPID bool) (*Spans, *mirrorRig) {
	t.Helper()
	t.Cleanup(Reset)
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	logs := &logSink{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(logs))
	require.True(t, Install(new(byte), Components{Tracer: tp.Tracer("test"), Logger: lp.Logger("test")}, false))
	return NewSpans(Global(), context.Background(), pid, hasPID), &mirrorRig{spans: spans, logs: logs}
}

func (r *mirrorRig) span(t *testing.T, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range r.spans.Ended() {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("span %q was not ended", name)
	return nil
}

func (r *mirrorRig) record(t *testing.T, event string) sdklog.Record {
	t.Helper()
	for _, rec := range r.logs.all() {
		if rec.EventName() == event {
			return rec
		}
	}
	t.Fatalf("no %q record", event)
	return sdklog.Record{}
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name()
	}
	return names
}

func spanAttr(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func recordAttr(rec sdklog.Record, key string) (attribute.Value, bool) {
	var out attribute.Value
	found := false
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == key {
			out, found = kv.Value, true
			return false
		}
		return true
	})
	return out, found
}

func strPtr(s string) *string { return &s }

func micros(kind wire.EventKind) *wire.Event {
	return &wire.Event{Kind: kind, TotalExecutionMicros: 42}
}

func configure() wire.Configure {
	return wire.Configure{ScriptName: "main.py", MontyVersion: "0.0.1"}
}

func TestMirrorOneFeedProducesANestedSpanTree(t *testing.T) {
	m, rig := newMirror(t, 4321, true)
	m.Sent(configure())
	m.Sent(wire.Feed{Code: "double(2)", Cwd: "/"})
	call := micros(wire.EventFunctionCall)
	call.FunctionCall = &wire.FunctionCall{FunctionName: "double", Args: []any{int64(2)}, CallID: 1}
	m.Received(call)
	m.Sent(wire.ResumeCall{CallID: 1, Result: wire.ExtResult{Kind: wire.ExtReturn, Value: int64(4)}})
	complete := micros(wire.EventComplete)
	complete.Value, complete.HasValue = int64(4), true
	m.Received(complete)
	m.Sent(wire.Reset{})

	ended := rig.spans.Ended()
	require.Equal(t, []string{"call {function_name}", "run code", "session {script_name}"}, spanNames(ended))
	callSpan, feed, session := ended[0], ended[1], ended[2]
	require.False(t, session.Parent().IsValid())
	require.Equal(t, session.SpanContext().SpanID(), feed.Parent().SpanID())
	require.Equal(t, feed.SpanContext().SpanID(), callSpan.Parent().SpanID())
	pid, _ := spanAttr(session, "worker_pid")
	require.Equal(t, int64(4321), pid.AsInt64())
	output, _ := spanAttr(feed, "output")
	require.Equal(t, int64(4), output.AsInt64())
	total, _ := spanAttr(feed, "total_execution_micros")
	require.Equal(t, int64(42), total.AsInt64())
	returned, _ := spanAttr(callSpan, "return_value")
	require.Equal(t, attribute.INT64, returned.Type())
	require.Equal(t, int64(4), returned.AsInt64())
	require.Empty(t, rig.logs.all())
}

func TestMirrorConfigureAttributes(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	limit := uint64(5)
	m.Sent(wire.Configure{ScriptName: "calc.py", MontyVersion: "0.0.1", TypeCheck: true, TypeCheckStubs: strPtr("x: int"), AssertMessageAnnotations: new(uint32), Limits: &wire.Limits{MaxSuspensions: &limit}})
	m.Close()
	session := rig.span(t, "session {script_name}")
	for key, want := range map[string]string{"script_name": "calc.py", "monty_version": "0.0.1", "type_check": "true", "type_check_stubs": "x: int", "assert_message_annotations": "0", "max_suspensions": "5"} {
		v, ok := spanAttr(session, key)
		require.True(t, ok, key)
		require.Equal(t, want, v.String(), key)
	}
	_, ok := spanAttr(session, "worker_pid")
	require.False(t, ok)
	_, ok = spanAttr(session, "max_memory_bytes")
	require.False(t, ok)
}

func TestMirrorSuspensionAnswersLandOnTheirSpan(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	long := strings.Repeat("x", AttrSizeLimit*2)
	lookup := micros(wire.EventNameLookup)
	lookup.NameLookup = &wire.NameLookup{Name: "fetch"}
	m.Received(lookup)
	m.Sent(wire.ResumeNameLookup{Kind: wire.LookupValue, Value: "<function>"})
	osCall := micros(wire.EventOsCall)
	osCall.OsCall = &wire.OsCall{CallID: 1, Op: wire.OpWriteText, Path: "/mnt/data/f.txt", Text: long}
	m.Received(osCall)
	m.Sent(wire.ResumeCall{CallID: 1, Result: wire.ExtResult{Kind: wire.ExtReturn, Value: long}})

	value, _ := spanAttr(rig.span(t, "name lookup {name}"), "value")
	require.Equal(t, "<function>", value.AsString())
	span := rig.span(t, "os call {function}")
	flags := 0
	for _, kv := range span.Attributes() {
		if kv.Key == cutKey {
			flags++
			require.True(t, kv.Value.AsBool())
		}
	}
	require.Equal(t, 1, flags)
	returned, _ := spanAttr(span, "return_value")
	require.Len(t, returned.AsString(), AttrSizeLimit)
	function, _ := spanAttr(span, "function")
	require.Equal(t, "write_text", function.AsString())
}

func TestMirrorHousekeepingTurnsReportOnTheirSpan(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(wire.Load{State: make([]byte, 8)})
	ok := micros(wire.EventOk)
	ok.RestoredScriptName = strPtr("dumped.py")
	m.Received(ok)
	m.Sent(wire.Dump{})
	dump := micros(wire.EventDumpResult)
	dump.State = make([]byte, 16)
	m.Received(dump)
	m.Sent(wire.InstallDependencies{Requirements: []string{"pydantic"}})
	m.Received(micros(wire.EventOk))

	load := rig.span(t, "load")
	script, _ := spanAttr(load, "script_name")
	require.Equal(t, "dumped.py", script.AsString())
	loadBytes, _ := spanAttr(load, "state_bytes")
	require.Equal(t, int64(8), loadBytes.AsInt64())
	dumpSpan := rig.span(t, "dump")
	state, _ := spanAttr(dumpSpan, "state_bytes")
	require.Equal(t, int64(16), state.AsInt64())
	total, _ := spanAttr(dumpSpan, "total_execution_micros")
	require.Equal(t, int64(42), total.AsInt64())
	requirements, _ := spanAttr(rig.span(t, "install dependencies"), "requirements")
	require.Equal(t, `["pydantic"]`, requirements.AsString())
	require.Empty(t, rig.logs.all())
}

func TestMirrorIdleRestoreKeepsItsSessionParent(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(wire.Configure{ScriptName: "new.py", MontyVersion: "0.0.1"})
	m.Received(micros(wire.EventOk))
	m.Sent(wire.Load{})
	ok := micros(wire.EventOk)
	ok.RestoredScriptName = strPtr("restored.py")
	m.Received(ok)
	m.Sent(wire.Feed{Code: "1", Cwd: "/"})
	complete := micros(wire.EventComplete)
	complete.Value, complete.HasValue = int64(1), true
	m.Received(complete)
	m.Sent(wire.Reset{})

	session := rig.span(t, "session {script_name}")
	require.Equal(t, session.SpanContext().SpanID(), rig.span(t, "run code").Parent().SpanID())
}

func TestMirrorRestoredFeedKeepsItsLoadSpanAcrossResume(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(wire.Load{})
	lookup := micros(wire.EventNameLookup)
	lookup.NameLookup = &wire.NameLookup{Name: "value"}
	m.Received(lookup)
	m.Sent(wire.ResumeNameLookup{Kind: wire.LookupValue, Value: int64(1)})
	complete := micros(wire.EventComplete)
	complete.Value, complete.HasValue = int64(1), true
	m.Received(complete)

	load := rig.span(t, "load")
	require.Equal(t, load.SpanContext().SpanID(), rig.span(t, "name lookup {name}").Parent().SpanID())
	record := rig.record(t, "complete")
	require.Equal(t, load.SpanContext().SpanID(), record.SpanID())
	output, _ := recordAttr(record, "output")
	require.Equal(t, int64(1), output.AsInt64())
}

func TestMirrorCompletionWithoutAFeedSpanIsRecorded(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	complete := micros(wire.EventComplete)
	complete.Value, complete.HasValue = int64(7), true
	m.Received(complete)
	record := rig.record(t, "complete")
	require.Equal(t, "complete", record.Body().AsString())
	output, _ := recordAttr(record, "output")
	require.Equal(t, int64(7), output.AsInt64())
}

func TestMirrorDumpErrorKeepsTheFeedOpen(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(configure())
	m.Sent(wire.Feed{Code: "fetch()", Cwd: "/"})
	call := micros(wire.EventFunctionCall)
	call.FunctionCall = &wire.FunctionCall{FunctionName: "fetch", CallID: 1}
	m.Received(call)
	m.Sent(wire.Dump{})
	failed := micros(wire.EventError)
	failed.Exception = wire.NewException("RuntimeError", "cannot dump")
	m.Received(failed)

	require.Equal(t, []string{"dump"}, spanNames(rig.spans.Ended()))
	record := rig.record(t, "error {exc_type}")
	require.Equal(t, rig.span(t, "dump").SpanContext().SpanID(), record.SpanID())

	m.Sent(wire.ResumeCall{CallID: 1, Result: wire.ExtResult{Kind: wire.ExtReturn, Value: int64(1)}})
	complete := micros(wire.EventComplete)
	complete.Value, complete.HasValue = int64(1), true
	m.Received(complete)
	require.Equal(t, []string{"dump", "call {function_name}", "run code"}, spanNames(rig.spans.Ended()))
}

func TestMirrorEagerFutureClosesTheCallSpan(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(wire.Feed{Code: "await fetch()", Cwd: "/"})
	call := micros(wire.EventFunctionCall)
	call.FunctionCall = &wire.FunctionCall{FunctionName: "fetch", CallID: 3, AllowEagerAwait: true}
	m.Received(call)
	m.Sent(wire.ResumeFutures{Results: []wire.FutureResult{{CallID: 3, Result: wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException("ValueError", "failed")}}}})

	returned, _ := spanAttr(rig.span(t, "call {function_name}"), "return_value")
	require.Equal(t, "raise ValueError: failed", returned.AsString())
	require.Empty(t, rig.logs.all())
}

func TestMirrorDeferredFutureKeepsFutureResolutionTelemetry(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(wire.Feed{Code: "pending = fetch()\nawait pending", Cwd: "/"})
	call := micros(wire.EventFunctionCall)
	call.FunctionCall = &wire.FunctionCall{FunctionName: "fetch", CallID: 1}
	m.Received(call)
	m.Sent(wire.ResumeCall{CallID: 1, Result: wire.ExtResult{Kind: wire.ExtFuture, FutureCallID: 1}})
	waiting := micros(wire.EventResolveFutures)
	waiting.PendingCallIDs = []uint32{1}
	m.Received(waiting)
	m.Sent(wire.ResumeFutures{Results: []wire.FutureResult{{CallID: 1, Result: wire.ExtResult{Kind: wire.ExtReturn, Value: int64(42)}}}})

	returned, _ := spanAttr(rig.span(t, "call {function_name}"), "return_value")
	require.Equal(t, "future 1", returned.AsString())
	resolve := rig.span(t, "resolve futures")
	ids, _ := spanAttr(resolve, "pending_call_ids")
	require.Equal(t, "[1]", ids.AsString())
	record := rig.record(t, "future results")
	require.Equal(t, resolve.SpanContext().SpanID(), record.SpanID())
	results, _ := recordAttr(record, "results")
	require.Equal(t, "1: 42", results.AsString())
}

func TestMirrorAbortAnswersTheSuspension(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(wire.Feed{Code: "fetch()", Cwd: "/"})
	call := micros(wire.EventFunctionCall)
	call.FunctionCall = &wire.FunctionCall{FunctionName: "fetch", CallID: 1}
	m.Received(call)
	m.Sent(wire.AbortFeed{Exception: wire.NewException("RuntimeError", "suspension limit 1 exceeded")})
	aborted, _ := spanAttr(rig.span(t, "call {function_name}"), "aborted_with")
	require.Equal(t, "raise RuntimeError: suspension limit 1 exceeded", aborted.AsString())
}

func TestMirrorErrorsCarryTracebackAndExcData(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(wire.Feed{Code: "json.loads('x')", Cwd: "/"})
	failed := micros(wire.EventError)
	failed.Exception = &wire.Exception{
		ExcType:   "json.JSONDecodeError",
		Message:   strPtr("Expecting value"),
		Traceback: []wire.Frame{{Filename: "main.py", Start: wire.CodeLoc{Line: 1}}},
		Data:      &wire.ExcData{JSON: &wire.JSONErrorData{Msg: "Expecting value", Doc: strPtr("x"), Pos: 0, Lineno: 1, Colno: 1}},
	}
	m.Received(failed)

	require.Equal(t, []string{"run code"}, spanNames(rig.spans.Ended()))
	record := rig.record(t, "error {exc_type}")
	require.Equal(t, "error json.JSONDecodeError", record.Body().AsString())
	require.Equal(t, rig.span(t, "run code").SpanContext().SpanID(), record.SpanID())
	for key, want := range map[string]string{
		"exc_type": "json.JSONDecodeError", "exc_message": "Expecting value", "traceback": "main.py:1 in <module>",
		"exc_data.msg": "Expecting value", "exc_data.doc": "x", "exc_data.pos": "0", "exc_data.lineno": "1", "exc_data.colno": "1",
		"total_execution_micros": "42",
	} {
		v, ok := recordAttr(record, key)
		require.True(t, ok, key)
		require.Equal(t, want, v.String(), key)
	}
}

func TestMirrorOversizeErrorPayloadIsCapped(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	failed := micros(wire.EventError)
	failed.Exception = &wire.Exception{
		ExcType: "UnicodeDecodeError",
		Data:    &wire.ExcData{Unicode: &wire.UnicodeErrorData{Encoding: strings.Repeat("u", AttrSizeLimit*2), Start: 0, End: 1, Reason: "bad"}},
	}
	m.Received(failed)
	record := rig.record(t, "error {exc_type}")
	encoding, _ := recordAttr(record, "exc_data.encoding")
	require.Len(t, encoding.AsString(), AttrSizeLimit)
	object, _ := recordAttr(record, "exc_data.object")
	require.Equal(t, missing, object.AsString())
	cut, _ := recordAttr(record, cutKey)
	require.True(t, cut.AsBool())
}

func TestMirrorOversizeAttributesAreCappedAndFlagged(t *testing.T) {
	long := strings.Repeat("x", AttrSizeLimit*2)
	v, cut := renderExtResult(wire.ExtResult{Kind: wire.ExtError, Error: wire.NewException("ValueError", long)})
	require.True(t, cut)
	require.Len(t, v.AsString(), AttrSizeLimit)

	v, cut = renderExtResult(wire.ExtResult{Kind: wire.ExtNotFound, NotFoundName: long})
	require.True(t, cut)
	require.Len(t, v.AsString(), AttrSizeLimit)

	text, cut := BytesText([]byte(strings.Repeat("a", AttrSizeLimit+1)))
	require.True(t, cut)
	require.Len(t, text, AttrSizeLimit)
	text, cut = BytesText([]byte{'h', 'i', 0xff})
	require.False(t, cut)
	require.Equal(t, `hi\xff`, text)
}

func TestMirrorPrintsOneRecordPerSegment(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(wire.Feed{Code: "print()", Cwd: "/"})
	m.Received(&wire.Event{Kind: wire.EventPrint, Print: []wire.PrintSegment{{Stream: 1, Text: "out"}, {Stream: 2, Text: "err"}}})
	records := rig.logs.all()
	require.Len(t, records, 2)
	require.Equal(t, "print stdout", records[0].Body().AsString())
	require.Equal(t, "print stderr", records[1].Body().AsString())
	text, _ := recordAttr(records[1], "text")
	require.Equal(t, "err", text.AsString())
}

type storageKey struct{}

func TestMirrorCallbackContextIsTheInnermostOpenSpan(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	caller := context.WithValue(context.Background(), storageKey{}, "caller")
	require.False(t, trace.SpanContextFromContext(m.CallbackContext(caller)).IsValid())
	m.Sent(configure())
	m.Sent(wire.Feed{Code: "fetch()", Cwd: "/"})
	call := micros(wire.EventFunctionCall)
	call.FunctionCall = &wire.FunctionCall{FunctionName: "fetch", CallID: 1}
	m.Received(call)
	inCall := m.CallbackContext(caller)
	require.Equal(t, "caller", inCall.Value(storageKey{}))
	m.Sent(wire.ResumeCall{CallID: 1, Result: wire.ExtResult{Kind: wire.ExtReturn}})
	inFeed := m.CallbackContext(caller)
	m.Close()

	require.Equal(t, []string{"call {function_name}", "run code", "session {script_name}"}, spanNames(rig.spans.Ended()))
	require.Equal(t, rig.span(t, "call {function_name}").SpanContext(), trace.SpanContextFromContext(inCall))
	require.Equal(t, rig.span(t, "run code").SpanContext(), trace.SpanContextFromContext(inFeed))
	require.False(t, trace.SpanContextFromContext(m.CallbackContext(caller)).IsValid())
}

func TestMirrorDropsOpenSpansOnceUninstalled(t *testing.T) {
	m, rig := newMirror(t, 0, false)
	m.Sent(configure())
	m.Sent(wire.Feed{Code: "1", Cwd: "/"})
	Reset()
	caller := context.Background()
	require.False(t, trace.SpanContextFromContext(m.CallbackContext(caller)).IsValid())
	complete := micros(wire.EventComplete)
	complete.Value, complete.HasValue = int64(1), true
	m.Received(complete)
	m.Sent(wire.Reset{})
	require.Empty(t, rig.spans.Ended())
	require.Empty(t, rig.logs.all())
}
