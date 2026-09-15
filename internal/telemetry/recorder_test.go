package telemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
)

type panicTracer struct{ tracenoop.Tracer }

func (panicTracer) Start(context.Context, string, ...trace.SpanStartOption) (context.Context, trace.Span) {
	panic("tracing failed")
}

type panicLogger struct{ lognoop.Logger }

func (panicLogger) Emit(context.Context, log.Record) { panic("logging failed") }

type errMeter struct{ metricnoop.Meter }

func (errMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return nil, errors.New("metrics failed")
}

type logSink struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (s *logSink) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }

func (s *logSink) OnEmit(_ context.Context, r *sdklog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r.Clone())
	return nil
}

func (s *logSink) Shutdown(context.Context) error   { return nil }
func (s *logSink) ForceFlush(context.Context) error { return nil }

func (s *logSink) all() []sdklog.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sdklog.Record(nil), s.records...)
}

func tracing(t *testing.T) (trace.Tracer, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp.Tracer("test"), rec
}

func TestInstall(t *testing.T) {
	t.Cleanup(Reset)
	a, b := new(byte), new(byte)
	require.True(t, Install(a, Components{}, false))
	first := Global()
	require.True(t, Installed())
	require.False(t, Install(a, Components{}, false))
	require.False(t, Install(b, Components{}, true))
	require.True(t, Install(a, Components{}, true))
	require.NotSame(t, first, Global())
	require.True(t, first.closed.Load())
	Uninstall(b)
	require.NotNil(t, Global())
	Uninstall(a)
	require.Nil(t, Global())
	require.False(t, Installed())
}

func TestTracingFailureKeepsLogs(t *testing.T) {
	t.Cleanup(Reset)
	sink := &logSink{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sink))
	require.True(t, Install(new(byte), Components{Tracer: panicTracer{}, Logger: lp.Logger("test")}, false))
	r := Global()
	sp := r.StartSpan(context.Background(), "run code", nil, nil)
	require.NotNil(t, sp)
	require.False(t, r.Tracing())
	require.True(t, r.Logging())
	child := r.StartSpan(sp.Context(context.Background()), "call {function_name}", nil, nil)
	r.Emit(child.Context(context.Background()), log.SeverityInfo, "print {stream}", "print stdout", []attribute.KeyValue{attribute.String("text", "hi")})
	records := sink.all()
	require.Len(t, records, 1)
	require.Equal(t, trace.SpanContextFromContext(sp.Context(context.Background())).TraceID(), records[0].TraceID())
	require.True(t, records[0].SpanID().IsValid())
	require.Equal(t, "print stdout", records[0].Body().AsString())
	require.Equal(t, "print {stream}", records[0].EventName())
}

func TestLoggingFailureKeepsTraces(t *testing.T) {
	t.Cleanup(Reset)
	tracer, rec := tracing(t)
	require.True(t, Install(new(byte), Components{Tracer: tracer, Logger: panicLogger{}}, false))
	r := Global()
	r.Emit(context.Background(), log.SeverityInfo, "print {stream}", "print stdout", nil)
	require.False(t, r.Logging())
	require.True(t, r.Tracing())
	r.StartSpan(context.Background(), "run code", nil, nil).End()
	require.Len(t, rec.Ended(), 1)
}

func TestMetricFailureKeepsTraces(t *testing.T) {
	t.Cleanup(Reset)
	tracer, _ := tracing(t)
	require.True(t, Install(new(byte), Components{Tracer: tracer, Meter: errMeter{}}, false))
	r := Global()
	r.Record(RunDuration, 1)
	require.False(t, r.Metering())
	require.True(t, r.Tracing())
}

func TestSpanEndSkippedAfterUninstall(t *testing.T) {
	t.Cleanup(Reset)
	tracer, rec := tracing(t)
	o := new(byte)
	require.True(t, Install(o, Components{Tracer: tracer}, false))
	sp := Global().StartSpan(context.Background(), "session {script_name}", nil, o)
	require.True(t, sp.OwnedBy(o))
	Uninstall(o)
	sp.End()
	require.Empty(t, rec.Ended())
}

func TestSyntheticSpanJoinsParentTrace(t *testing.T) {
	t.Cleanup(Reset)
	require.True(t, Install(new(byte), Components{Logger: lognoop.Logger{}}, false))
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled})
	sp := Global().StartSpan(trace.ContextWithSpanContext(context.Background(), parent), "run code", nil, nil)
	sc := trace.SpanContextFromContext(sp.Context(context.Background()))
	require.Equal(t, parent.TraceID(), sc.TraceID())
	require.NotEqual(t, parent.SpanID(), sc.SpanID())
	require.True(t, sc.IsValid())
}

func TestPoolMetrics(t *testing.T) {
	t.Cleanup(Reset)
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	require.True(t, Install(new(byte), Components{Meter: mp.Meter("test")}, false))
	m := NewPoolMetrics(Global())
	m.CheckoutWait(time.Millisecond, "waited")
	m.WorkersLive(1)
	m.WorkerTerminated("closed")
	m.SessionDuration(time.Millisecond, "abandoned")
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	byName := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			byName[metric.Name] = metric
		}
	}
	waits := byName["monty.pool.checkout.wait"].Data.(metricdata.Histogram[float64]).DataPoints
	require.Len(t, waits, 1)
	outcome, _ := waits[0].Attributes.Value(attribute.Key("outcome"))
	require.Equal(t, "waited", outcome.AsString())
	require.Equal(t, "s", byName["monty.pool.checkout.wait"].Unit)
	live := byName["monty.pool.workers.live"].Data.(metricdata.Sum[int64]).DataPoints
	require.Equal(t, int64(1), live[0].Value)
	require.Contains(t, byName, "monty.pool.worker.terminated")
	require.Contains(t, byName, "monty.pool.session.duration")
}
