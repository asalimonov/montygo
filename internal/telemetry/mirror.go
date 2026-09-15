package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/asalimonov/montygo/internal/wire"
)

type openSpan struct {
	span    *Span
	cut     bool
	eager   bool
	eagerID uint32
}

func (o *openSpan) get() *Span {
	if o == nil {
		return nil
	}
	return o.span
}

// record sets key and flags a cut the span does not carry yet, so the flag is recorded once.
func (o *openSpan) record(key string, v attribute.Value, cut bool) {
	o.span.SetAttributes(attribute.KeyValue{Key: attribute.Key(key), Value: v})
	if cut && !o.cut {
		o.span.SetAttributes(attribute.Bool(cutKey, true))
		o.cut = true
	}
}

// Spans mirrors one checkout's protocol conversation into spans and log
// records: Configure opens the session span, Feed a run span held across
// suspensions, and each suspension a child span its answer closes.
type Spans struct {
	rec      *Recorder
	parent   context.Context
	pid      int
	hasPID   bool
	session  *Span
	feed     *openSpan
	pending  *openSpan
	turn     *Span
	dumpTurn bool
	detached bool
}

// NewSpans creates the mirror of a checkout whose session span is a child of the span in parent.
func NewSpans(rec *Recorder, parent context.Context, pid int, hasPID bool) *Spans {
	return &Spans{rec: rec, parent: parent, pid: pid, hasPID: hasPID}
}

func (m *Spans) innermost() *Span {
	for _, s := range [...]*Span{m.turn, m.pending.get(), m.feed.get(), m.session} {
		if s != nil {
			return s
		}
	}
	return nil
}

func (m *Spans) context() context.Context { return m.innermost().Context(m.parent) }

func (m *Spans) start(name string, attrs []attribute.KeyValue) *Span {
	return m.rec.StartSpan(m.context(), name, attrs, nil)
}

func (m *Spans) emit(severity log.Severity, event, body string, attrs []attribute.KeyValue) {
	m.rec.Emit(m.context(), severity, event, body, attrs)
}

func (m *Spans) endTurn() {
	m.turn.End()
	m.turn = nil
}

func (m *Spans) endPending() {
	m.pending.get().End()
	m.pending = nil
}

func (m *Spans) endFeedSpan() {
	m.feed.get().End()
	m.feed = nil
}

func (m *Spans) endFeed() {
	m.endTurn()
	m.endPending()
	m.endFeedSpan()
}

func (m *Spans) replacePending(o *openSpan) {
	m.endPending()
	m.pending = o
}

func (m *Spans) closePending(key string, v attribute.Value, cut bool) {
	pending := m.pending
	if pending == nil {
		return
	}
	m.pending = nil
	pending.record(key, v, cut)
	pending.span.End()
}

// CallbackContext returns ctx carrying the innermost open span.
func (m *Spans) CallbackContext(ctx context.Context) context.Context {
	if !m.rec.Tracing() {
		return ctx
	}
	if s := m.innermost(); s != nil && s.span != nil {
		return trace.ContextWithSpan(ctx, s.span)
	}
	return ctx
}

// Close ends every open span innermost-first and stops recording.
func (m *Spans) Close() {
	m.endFeed()
	m.session.End()
	m.session = nil
	m.detached = true
}

// Sent records a request that reached the wire.
func (m *Spans) Sent(req wire.Request) {
	if m.detached {
		return
	}
	switch req.(type) {
	case wire.ResumeCall, wire.ResumeNameLookup, wire.ResumeFutures, wire.AbortFeed:
	default:
		m.endTurn()
	}
	m.dumpTurn = false
	switch r := req.(type) {
	case wire.Configure:
		m.endPending()
		m.endFeedSpan()
		session := m.rec.StartSpan(m.parent, "session {script_name}", configureAttributes(r, m.pid, m.hasPID), nil)
		m.session.End()
		m.session = session
	case wire.Load:
		m.turn = m.start("load", []attribute.KeyValue{attribute.Int("state_bytes", len(r.State))})
	case wire.Reset, wire.Shutdown:
		m.Close()
	case wire.InstallDependencies:
		var attrs []attribute.KeyValue
		cut := false
		if len(r.Requirements) > 0 {
			var requirements string
			requirements, cut = JSONStrings(r.Requirements)
			attrs = append(attrs, attribute.String("requirements", requirements))
		}
		m.turn = m.start("install dependencies", flagCut(attrs, cut))
	case wire.Feed:
		code, cut := Truncate(r.Code)
		attrs := []attribute.KeyValue{attribute.String("code", code), attribute.String("code.language", "python")}
		if len(r.Inputs) > 0 {
			inputs, inputsCut := JSONNamed(r.Inputs)
			attrs = append(attrs, attribute.String("inputs", inputs))
			cut = cut || inputsCut
		}
		attrs = flagCut(append(attrs, attribute.Bool("skip_type_check", r.SkipTypeCheck)), cut)
		m.endPending()
		m.endFeedSpan()
		m.feed = &openSpan{span: m.start("run code", attrs), cut: cut}
	case wire.ResumeCall:
		v, cut := renderExtResult(r.Result)
		m.closePending("return_value", v, cut)
	case wire.ResumeNameLookup:
		v, cut := renderNameLookup(r)
		m.closePending("value", v, cut)
	case wire.AbortFeed:
		v, cut := renderRaised(r.Exception)
		m.closePending("aborted_with", v, cut)
	case wire.ResumeFutures:
		if len(r.Results) == 1 && m.pending != nil && m.pending.eager && m.pending.eagerID == r.Results[0].CallID {
			v, cut := renderExtResult(r.Results[0].Result)
			m.closePending("return_value", v, cut)
			return
		}
		pending := m.pending.get()
		m.pending = nil
		var attrs []attribute.KeyValue
		cut := false
		if len(r.Results) > 0 {
			var results string
			results, cut = renderFutureResults(r.Results)
			attrs = append(attrs, attribute.String("results", results))
		}
		m.rec.Emit(pending.Context(m.parent), log.SeverityInfo, "future results", "future results", flagCut(attrs, cut))
		pending.End()
	case wire.Dump:
		m.dumpTurn = true
		m.turn = m.start("dump", nil)
	}
}

// Received records a decoded event.
func (m *Spans) Received(ev *wire.Event) {
	if m.detached {
		return
	}
	budget := eventBudget(ev)
	if ev.RestoredScriptName != nil && m.turn != nil {
		name, cut := Truncate(*ev.RestoredScriptName)
		m.turn.SetAttributes(flagCut([]attribute.KeyValue{attribute.String("script_name", name)}, cut)...)
	}
	switch ev.Kind {
	case wire.EventPrint:
		for _, seg := range ev.Print {
			text, cut := Truncate(seg.Text)
			stream := printStream(seg.Stream)
			attrs := []attribute.KeyValue{attribute.String("stream", stream), attribute.String("text", text)}
			m.emit(log.SeverityInfo, "print {stream}", "print "+stream, flagCut(attrs, cut))
		}
	case wire.EventFunctionCall:
		m.replacePending(m.callSpan(ev.FunctionCall, budget))
	case wire.EventOsCall:
		m.replacePending(m.osCallSpan(ev.OsCall, budget))
	case wire.EventNameLookup:
		name, cut := missing, false
		if ev.NameLookup != nil {
			name, cut = Truncate(ev.NameLookup.Name)
		}
		attrs := flagCut(append([]attribute.KeyValue{attribute.String("name", name)}, budget...), cut)
		m.replacePending(&openSpan{span: m.start("name lookup {name}", attrs), cut: cut})
	case wire.EventResolveFutures:
		var attrs []attribute.KeyValue
		cut := false
		if len(ev.PendingCallIDs) > 0 {
			var ids string
			ids, cut = JSONUint32s(ev.PendingCallIDs)
			attrs = append(attrs, attribute.String("pending_call_ids", ids))
		}
		attrs = append(flagCut(attrs, cut), budget...)
		m.replacePending(&openSpan{span: m.start("resolve futures", attrs), cut: cut})
	case wire.EventComplete:
		m.recordComplete(ev, budget)
		m.endFeed()
	case wire.EventError:
		m.recordError(ev.Exception, budget)
		if m.dumpTurn {
			m.endTurn()
		} else {
			m.endFeed()
		}
	case wire.EventTypingError:
		diagnostics, cut := Truncate(ev.Diagnostics)
		attrs := append(flagCut([]attribute.KeyValue{attribute.String("diagnostics", diagnostics)}, cut), budget...)
		m.emit(log.SeverityError, "typing error", "typing error", attrs)
		m.endFeed()
	case wire.EventDumpResult:
		m.turn.SetAttributes(append([]attribute.KeyValue{attribute.Int("state_bytes", len(ev.State))}, budget...)...)
		m.endTurn()
	case wire.EventShutdown:
		var attrs []attribute.KeyValue
		if ev.HasShutdownDump {
			attrs = append(attrs, attribute.Int("state_bytes", len(ev.ShutdownDump)))
		}
		m.emit(log.SeverityInfo, "shutdown", "shutdown", append(attrs, budget...))
	case wire.EventOk:
		m.endTurn()
	case wire.EventFatalError:
		message, cut := Truncate(ev.FatalMessage)
		m.emit(log.SeverityError, "fatal error", "fatal error", flagCut([]attribute.KeyValue{attribute.String("message", message)}, cut))
	default:
		m.emit(log.SeverityError, "event with no kind", "event with no kind", nil)
	}
}

func (m *Spans) callSpan(call *wire.FunctionCall, budget []attribute.KeyValue) *openSpan {
	if call == nil {
		return &openSpan{span: m.start("call {function_name}", budget)}
	}
	name, cut := Truncate(call.FunctionName)
	attrs := []attribute.KeyValue{attribute.String("function_name", name)}
	if len(call.Args) > 0 {
		args, argsCut := JSONSeq(call.Args)
		attrs = append(attrs, attribute.String("args", args))
		cut = cut || argsCut
	}
	if len(call.Kwargs) > 0 {
		kwargs, kwargsCut := JSONPairs(call.Kwargs)
		attrs = append(attrs, attribute.String("kwargs", kwargs))
		cut = cut || kwargsCut
	}
	attrs = append(attrs, attribute.Int64("call_id", int64(call.CallID)))
	if call.ObjectID != "" {
		attrs = append(attrs, attribute.String("object_id", call.ObjectID))
	}
	attrs = append(flagCut(attrs, cut), budget...)
	return &openSpan{span: m.start("call {function_name}", attrs), cut: cut, eager: call.AllowEagerAwait, eagerID: call.CallID}
}

func (m *Spans) osCallSpan(call *wire.OsCall, budget []attribute.KeyValue) *openSpan {
	function := missing
	var args []attribute.KeyValue
	cut := false
	if call != nil {
		if name, ok := osFunctions[call.Op]; ok {
			function = name
		}
		args, cut = osArguments(call)
	}
	attrs := append([]attribute.KeyValue{attribute.String("function", function)}, args...)
	if call != nil {
		attrs = append(attrs, attribute.Int64("call_id", int64(call.CallID)))
	}
	attrs = flagCut(append(attrs, budget...), cut)
	return &openSpan{span: m.start("os call {function}", attrs), cut: cut}
}

func (m *Spans) recordComplete(ev *wire.Event, budget []attribute.KeyValue) {
	var output attribute.Value
	cut := false
	if ev.HasValue {
		output, cut = AttrValue(ev.Value)
	}
	if feed := m.feed; feed != nil {
		m.feed = nil
		if ev.HasValue {
			feed.record("output", output, cut)
		}
		feed.span.SetAttributes(budget...)
		feed.span.End()
		return
	}
	var attrs []attribute.KeyValue
	if ev.HasValue {
		attrs = append(attrs, attribute.KeyValue{Key: "output", Value: output})
	}
	m.emit(log.SeverityInfo, "complete", "complete", append(flagCut(attrs, cut), budget...))
}

func (m *Spans) recordError(exc *wire.Exception, budget []attribute.KeyValue) {
	excType, cut := missing, false
	if exc != nil {
		excType, cut = Truncate(exc.ExcType)
	}
	attrs := []attribute.KeyValue{attribute.String("exc_type", excType)}
	if exc != nil {
		if exc.Message != nil {
			message, messageCut := Truncate(*exc.Message)
			attrs = append(attrs, attribute.String("exc_message", message))
			cut = cut || messageCut
		}
		if len(exc.Traceback) > 0 {
			traceback, tracebackCut := renderTraceback(exc.Traceback)
			attrs = append(attrs, attribute.String("traceback", traceback))
			cut = cut || tracebackCut
		}
		if exc.Data != nil {
			data, dataCut := excDataAttributes(exc.Data)
			attrs = append(attrs, data...)
			cut = cut || dataCut
		}
	}
	m.emit(log.SeverityError, "error {exc_type}", "error "+excType, append(flagCut(attrs, cut), budget...))
}
