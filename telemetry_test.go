package montygo_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/telemetry"
)

type telCase struct {
	title string
	run   func(t *testing.T, b backend)
}

// telRun runs each case on every backend. Telemetry is a pool parameter, so
// cases share the process.
func telRun(t *testing.T, cases []telCase) {
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			eachBackend(t, c.run)
		})
	}
}

func telPool(t *testing.T, b backend, opts montygo.PoolOptions) *montygo.Pool {
	t.Helper()
	p, err := openPool(testCtx(t), b, opts)
	if err != nil {
		poolUnavailable(t, b, err)
	}
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// telTraced builds a pool that records into tracer only.
func telTraced(t *testing.T, b backend, tracer trace.Tracer) *montygo.Pool {
	t.Helper()
	return telPool(t, b, montygo.PoolOptions{Telemetry: &telemetry.Components{Tracer: tracer}})
}

func telTracing(opts ...sdktrace.TracerProviderOption) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	return sdktrace.NewTracerProvider(append(opts, sdktrace.WithSpanProcessor(rec))...), rec
}

func telNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name()
	}
	return names
}

func telFind(spans []sdktrace.ReadOnlySpan, match func(sdktrace.ReadOnlySpan) bool) sdktrace.ReadOnlySpan {
	for _, s := range spans {
		if match(s) {
			return s
		}
	}
	return nil
}

func telNamed(name string) func(sdktrace.ReadOnlySpan) bool {
	return func(s sdktrace.ReadOnlySpan) bool { return s.Name() == name }
}

func telSpanAttr(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func telSameSpan(a, b trace.Span) bool {
	return a != nil && b != nil && a.SpanContext().Equal(b.SpanContext())
}

type telLogs struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (l *telLogs) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }

func (l *telLogs) OnEmit(_ context.Context, r *sdklog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r.Clone())
	return nil
}

func (l *telLogs) Shutdown(context.Context) error   { return nil }
func (l *telLogs) ForceFlush(context.Context) error { return nil }

func (l *telLogs) all() []sdklog.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]sdklog.Record(nil), l.records...)
}

func telLogAttr(r sdklog.Record, key string) (attribute.Value, bool) {
	var out attribute.Value
	found := false
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == key {
			out, found = kv.Value, true
			return false
		}
		return true
	})
	return out, found
}

func telBool(v bool) *bool { return &v }

func telRunCode(t *testing.T, ctx context.Context, s *montygo.Session, code string, opts *montygo.FeedOptions) any {
	t.Helper()
	v, err := s.FeedRun(ctx, code, opts)
	require.NoError(t, err)
	return v
}

type telPanicTracer struct{ tracenoop.Tracer }

func (telPanicTracer) Start(context.Context, string, ...trace.SpanStartOption) (context.Context, trace.Span) {
	panic("tracing failed")
}

type telPanicLogger struct{ lognoop.Logger }

func (telPanicLogger) Emit(context.Context, log.Record) { panic("logging failed") }

func (telPanicLogger) Enabled(context.Context, log.EnabledParameters) bool { return true }

type telPanicMeter struct{ metricnoop.Meter }

func (telPanicMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return telPanicCounter{}, nil
}

func (telPanicMeter) Int64UpDownCounter(string, ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	return telPanicUpDownCounter{}, nil
}

func (telPanicMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return telPanicHistogram{}, nil
}

type telPanicCounter struct{ metricnoop.Int64Counter }

func (telPanicCounter) Add(context.Context, int64, ...metric.AddOption) { panic("metrics failed") }

type telPanicUpDownCounter struct{ metricnoop.Int64UpDownCounter }

func (telPanicUpDownCounter) Add(context.Context, int64, ...metric.AddOption) {
	panic("metrics failed")
}

type telPanicHistogram struct{ metricnoop.Float64Histogram }

func (telPanicHistogram) Record(context.Context, float64, ...metric.RecordOption) {
	panic("metrics failed")
}

type telRejectFirstRun struct{ rejected atomic.Bool }

func (s *telRejectFirstRun) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	state := trace.SpanContextFromContext(p.ParentContext).TraceState()
	if p.Name == "run code" && s.rejected.CompareAndSwap(false, true) {
		return sdktrace.SamplingResult{Decision: sdktrace.Drop, Tracestate: state}
	}
	return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample, Tracestate: state}
}

func (*telRejectFirstRun) Description() string { return "RejectFirstRun" }

func TestTelemetry(t *testing.T) {
	telRun(t, []telCase{
		{"MontyInstrumentation accepts SDK providers", telAcceptsProviders},
		{"instrumentations are independent", telIndependent},
		{"a disabled instrumentation has no components", telDisabledComponents},
		{"concurrent checkouts deliver complete span trees", telConcurrentCheckouts},
		{"host sampling can reject one child without disabling its root", telSamplingRejectsChild},
		{"logging failure does not disable tracing", telLoggingFailure},
		{"metric failure does not disable tracing", telMetricFailure},
		{"logger-only installation preserves native trace context", telLoggerOnly},
		{"tracing failure does not disable logging", telTracingFailure},
		{"standard OpenTelemetry components receive Monty telemetry", telStandardComponents},
		{"function call spans carry their answer and runs their execution budget", telAnswerAttributes},
	})
}

func telAcceptsProviders(t *testing.T, b backend) {
	ctx := testCtx(t)
	tp, spans := telTracing()
	inst, err := telemetry.NewInstrumentation(telemetry.InstrumentationConfig{Logs: telBool(false), Metrics: telBool(false)})
	require.NoError(t, err)
	inst.SetTracerProvider(tp)
	c := inst.Components()
	require.NotNil(t, c)
	require.NotNil(t, c.Tracer)
	require.Nil(t, c.Meter)
	require.Nil(t, c.Logger)
	p := telPool(t, b, montygo.PoolOptions{Telemetry: c})
	s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(3), telRunCode(t, ctx, s, "1 + 2", nil))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))
	require.NoError(t, tp.ForceFlush(ctx))
	require.Equal(t, []string{"run code", "session {script_name}"}, telNames(spans.Ended()))
	require.NoError(t, tp.Shutdown(ctx))
}

func telIndependent(t *testing.T, b backend) {
	ctx := testCtx(t)
	tp1, spans1 := telTracing()
	tp2, spans2 := telTracing()
	off := telemetry.InstrumentationConfig{Logs: telBool(false), Metrics: telBool(false)}
	first, err := telemetry.NewInstrumentation(off)
	require.NoError(t, err)
	first.SetTracerProvider(tp1)
	second, err := telemetry.NewInstrumentation(off)
	require.NoError(t, err)
	second.SetTracerProvider(tp2)
	p1 := telPool(t, b, montygo.PoolOptions{Telemetry: first.Components()})
	p2 := telPool(t, b, montygo.PoolOptions{Telemetry: second.Components()})
	for _, p := range []*montygo.Pool{p1, p2} {
		s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
		require.NoError(t, err)
		require.Equal(t, int64(3), telRunCode(t, ctx, s, "1 + 2", nil))
		require.NoError(t, s.Close(ctx))
	}
	require.Equal(t, []string{"run code", "session {script_name}"}, telNames(spans1.Ended()))
	require.Equal(t, []string{"run code", "session {script_name}"}, telNames(spans2.Ended()))
	require.NoError(t, tp1.Shutdown(ctx))
	require.NoError(t, tp2.Shutdown(ctx))
}

func telDisabledComponents(t *testing.T, b backend) {
	ctx := testCtx(t)
	tp, spans := telTracing()
	inst, err := telemetry.NewInstrumentation(telemetry.InstrumentationConfig{Logs: telBool(false), Metrics: telBool(false)})
	require.NoError(t, err)
	inst.SetTracerProvider(tp)
	traced := telPool(t, b, montygo.PoolOptions{Telemetry: inst.Components()})
	inst.SetConfig(telemetry.InstrumentationConfig{Enabled: telBool(false)})
	require.Nil(t, inst.Components())
	silent := telPool(t, b, montygo.PoolOptions{Telemetry: inst.Components()})
	for _, p := range []*montygo.Pool{traced, silent} {
		s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
		require.NoError(t, err)
		require.Equal(t, int64(3), telRunCode(t, ctx, s, "1 + 2", nil))
		require.NoError(t, s.Close(ctx))
	}
	require.Equal(t, []string{"run code", "session {script_name}"}, telNames(spans.Ended()))
	// Re-enabled without a tracer provider and with the other signals off, nothing is left to record into.
	inst.SetTracerProvider(nil)
	inst.SetConfig(telemetry.InstrumentationConfig{Logs: telBool(false), Metrics: telBool(false)})
	require.Nil(t, inst.Components())
	require.NoError(t, tp.Shutdown(ctx))
}

func telConcurrentCheckouts(t *testing.T, b backend) {
	ctx := testCtx(t)
	tp, spans := telTracing()
	p := telPool(t, b, montygo.PoolOptions{MinWorkers: 2, MaxWorkers: 2, Telemetry: &telemetry.Components{Tracer: tp.Tracer("test")}})
	var wg sync.WaitGroup
	for _, v := range []int64{1, 2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
			if !assert.NoError(t, err) {
				return
			}
			got, err := s.FeedRun(ctx, "value + 1", &montygo.FeedOptions{Inputs: map[string]any{"value": v}})
			assert.NoError(t, err)
			assert.Equal(t, v+1, got)
			assert.NoError(t, s.Close(ctx))
		}()
	}
	wg.Wait()
	require.NoError(t, p.Close(ctx))
	ended := spans.Ended()
	require.Len(t, ended, 4, telNames(ended))
	traces := map[trace.TraceID]struct{}{}
	for _, s := range ended {
		traces[s.SpanContext().TraceID()] = struct{}{}
	}
	require.Len(t, traces, 2)
	require.NoError(t, tp.Shutdown(ctx))
}

func telSamplingRejectsChild(t *testing.T, b backend) {
	ctx := testCtx(t)
	tp, spans := telTracing(sdktrace.WithSampler(&telRejectFirstRun{}))
	p := telTraced(t, b, tp.Tracer("test"))
	s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(3), telRunCode(t, ctx, s, "1 + 2", nil))
	require.Equal(t, int64(9), telRunCode(t, ctx, s, "4 + 5", nil))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))
	require.Equal(t, []string{"run code", "session {script_name}"}, telNames(spans.Ended()))
	require.NoError(t, tp.Shutdown(ctx))
}

func telLoggingFailure(t *testing.T, b backend) {
	ctx := testCtx(t)
	tp, spans := telTracing()
	p := telPool(t, b, montygo.PoolOptions{Telemetry: &telemetry.Components{Tracer: tp.Tracer("test"), Logger: telPanicLogger{}}})
	s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(3), telRunCode(t, ctx, s, "print('hello')\n1 + 2", nil))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))
	require.Equal(t, []string{"run code", "session {script_name}"}, telNames(spans.Ended()))
	require.NoError(t, tp.Shutdown(ctx))
}

func telMetricFailure(t *testing.T, b backend) {
	ctx := testCtx(t)
	tp, spans := telTracing()
	p := telPool(t, b, montygo.PoolOptions{Telemetry: &telemetry.Components{Tracer: tp.Tracer("test"), Meter: telPanicMeter{}}})
	s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(3), telRunCode(t, ctx, s, "1 + 2", nil))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))
	require.Equal(t, []string{"run code", "session {script_name}"}, telNames(spans.Ended()))
	require.NoError(t, tp.Shutdown(ctx))
}

func telLoggerOnly(t *testing.T, b backend) {
	ctx := testCtx(t)
	logs := &telLogs{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(logs))
	p := telPool(t, b, montygo.PoolOptions{Telemetry: &telemetry.Components{Logger: lp.Logger("test")}})
	s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(3), telRunCode(t, ctx, s, "print('hello')\n1 + 2", nil))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))
	records := logs.all()
	require.NotEmpty(t, records)
	require.True(t, records[0].TraceID().IsValid())
	require.True(t, records[0].SpanID().IsValid())
	require.NoError(t, lp.Shutdown(ctx))
}

func telTracingFailure(t *testing.T, b backend) {
	ctx := testCtx(t)
	logs := &telLogs{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(logs))
	p := telPool(t, b, montygo.PoolOptions{Telemetry: &telemetry.Components{Tracer: telPanicTracer{}, Logger: lp.Logger("test")}})
	s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(3), telRunCode(t, ctx, s, "print('still logged')\n1 + 2", nil))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))
	records := logs.all()
	require.NotEmpty(t, records)
	require.Equal(t, "print stdout", records[0].Body().AsString())
	text, ok := telLogAttr(records[0], "text")
	require.True(t, ok)
	require.Equal(t, "still logged\n", text.AsString())
	require.NoError(t, lp.Shutdown(ctx))
}

func telStandardComponents(t *testing.T, b backend) {
	ctx := testCtx(t)
	inst, err := telemetry.NewInstrumentation(telemetry.InstrumentationConfig{Traces: telBool(false), Metrics: telBool(false), Logs: telBool(false)})
	require.NoError(t, err)
	require.Equal(t, "github.com/asalimonov/montygo", inst.Name())
	require.Equal(t, montygo.BindingVersion(), inst.Version())
	cfg := inst.Config()
	require.NotNil(t, cfg.Enabled)
	require.True(t, *cfg.Enabled)
	require.Nil(t, inst.Components())

	tp, spans := telTracing()
	tracer := tp.Tracer("test")
	logs := &telLogs{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(logs))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(sdkmetric.NewView(sdkmetric.Instrument{Name: "monty.run.duration"}, sdkmetric.Stream{Name: "monty.custom.run.duration"})),
	)

	// Empty components record nothing and are not an error.
	empty := telPool(t, b, montygo.PoolOptions{Telemetry: &telemetry.Components{}})
	require.NoError(t, empty.Close(ctx))

	parentCtx, parent := tracer.Start(ctx, "parent")
	func() {
		p := telPool(t, b, montygo.PoolOptions{Telemetry: &telemetry.Components{Tracer: tracer, Meter: mp.Meter("test"), Logger: lp.Logger("test")}})
		defer func() { require.NoError(t, p.Close(ctx)) }()
		s, err := p.Checkout(parentCtx, defaultRuntime, montygo.CheckoutOptions{ScriptName: "calculation.py"})
		require.NoError(t, err)
		defer func() { require.NoError(t, s.Close(ctx)) }()
		v := telRunCode(t, parentCtx, s, "print('hello')\n'\\x00' * 70000", &montygo.FeedOptions{Print: &sandbox.CollectString{}})
		require.Len(t, v, 70000)
	}()
	parent.End()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	ended := spans.Ended()
	require.Equal(t, []string{"run code", "session {script_name}", "parent"}, telNames(ended))
	run, session := ended[0], ended[1]
	require.Equal(t, parent.SpanContext().SpanID(), session.Parent().SpanID())
	require.False(t, session.Parent().IsRemote())
	require.Equal(t, session.SpanContext().SpanID(), run.Parent().SpanID())
	scriptName, _ := telSpanAttr(session, "script_name")
	require.Equal(t, "calculation.py", scriptName.AsString())
	output, ok := telSpanAttr(run, "output")
	require.True(t, ok)
	require.Equal(t, attribute.STRING, output.Type())
	require.Less(t, len(output.AsString()), 70000)
	cut, ok := telSpanAttr(run, "length_limit_exceeded")
	require.True(t, ok)
	require.True(t, cut.AsBool())

	records := logs.all()
	require.NotEmpty(t, records)
	printed := records[0]
	require.Equal(t, "print stdout", printed.Body().AsString())
	text, _ := telLogAttr(printed, "text")
	require.Equal(t, "hello\n", text.AsString())
	require.Equal(t, run.SpanContext().SpanID(), printed.SpanID())

	names := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			names[m.Name] = true
		}
	}
	require.True(t, names["monty.custom.run.duration"], names)
	require.False(t, names["monty.run.duration"], names)

	require.NoError(t, tp.Shutdown(ctx))
	require.NoError(t, lp.Shutdown(ctx))
	require.NoError(t, mp.Shutdown(ctx))
}

func telAnswerAttributes(t *testing.T, b backend) {
	ctx := testCtx(t)
	tp, spans := telTracing()
	p := telTraced(t, b, tp.Tracer("test"))
	s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	double := func(x int) int { return x * 2 }
	require.Equal(t, int64(42), telRunCode(t, ctx, s, "double(21)", &montygo.FeedOptions{ExternalLookup: map[string]any{"double": double}}))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))

	ended := spans.Ended()
	call := telFind(ended, telNamed("call {function_name}"))
	require.NotNil(t, call, telNames(ended))
	returned, ok := telSpanAttr(call, "return_value")
	require.True(t, ok)
	require.Equal(t, attribute.INT64, returned.Type())
	require.Equal(t, int64(42), returned.AsInt64())
	run := telFind(ended, telNamed("run code"))
	require.NotNil(t, run, telNames(ended))
	micros, ok := telSpanAttr(run, "total_execution_micros")
	require.True(t, ok)
	require.Equal(t, attribute.INT64, micros.Type())
	session := telFind(ended, telNamed("session {script_name}"))
	require.NotNil(t, session, telNames(ended))
	_, hasPID := telSpanAttr(session, "worker_pid")
	require.Equal(t, b == backendNative, hasPID)
	require.NoError(t, tp.Shutdown(ctx))
}
