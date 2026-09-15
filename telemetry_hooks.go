package monty

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/telemetry"
)

func currentMetrics() pool.Metrics {
	if telemetry.Current().Metering() {
		return telemetry.PoolMetrics{}
	}
	return nil
}

func traceContextHeaders(ctx context.Context) [][2]string {
	if !telemetry.Current().Tracing() || !trace.SpanContextFromContext(ctx).IsValid() {
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
	metered := p.metered
	return func(pid int, hasPID bool) pool.Observer {
		if o := telemetry.NewCheckout(parent, pid, hasPID, metered); o != nil {
			return o
		}
		return nil
	}
}
