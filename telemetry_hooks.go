package montygo

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/telemetry"
)

// resolveRecorder returns the recorder a pool records into: its own components,
// or the process-wide installation at creation time.
func resolveRecorder(c *TelemetryComponents) *telemetry.Recorder {
	if c == nil {
		return telemetry.Global()
	}
	if c.Tracer == nil && c.Meter == nil && c.Logger == nil {
		return nil
	}
	return telemetry.NewRecorder(telemetry.Components(*c))
}

func poolMetrics(rec *telemetry.Recorder) pool.Metrics {
	if rec.Metering() {
		return telemetry.NewPoolMetrics(rec)
	}
	return nil
}

func traceContextHeaders(rec *telemetry.Recorder, ctx context.Context) [][2]string {
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

func (p *Pool) observe(parent context.Context) func(pid int, hasPID bool) pool.Observer {
	rec, metered := p.rec, p.metered
	return func(pid int, hasPID bool) pool.Observer {
		if o := telemetry.NewCheckout(rec, parent, pid, hasPID, metered); o != nil {
			return o
		}
		return nil
	}
}
