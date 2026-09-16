package montygo

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/asalimonov/montygo/internal/pool"
	itel "github.com/asalimonov/montygo/internal/telemetry"
	"github.com/asalimonov/montygo/telemetry"
)

// resolveRecorder returns the recorder a pool records into: its own components,
// or the process-wide installation at creation time.
func resolveRecorder(c *telemetry.Components) *itel.Recorder {
	if c == nil {
		return itel.Global()
	}
	if c.Tracer == nil && c.Meter == nil && c.Logger == nil {
		return nil
	}
	return itel.NewRecorder(itel.Components(*c))
}

func poolMetrics(rec *itel.Recorder) pool.Metrics {
	if rec.Metering() {
		return itel.NewPoolMetrics(rec)
	}
	return nil
}

func traceContextHeaders(rec *itel.Recorder, ctx context.Context) [][2]string {
	if !rec.Tracing() || !trace.SpanContextFromContext(ctx).IsValid() {
		return nil
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	headers := [][2]string{{"traceparent", carrier.Get("traceparent")}}
	if state := carrier.Get("tracestate"); state != "" {
		headers = append(headers, [2]string{"tracestate", state})
	}
	return headers
}
