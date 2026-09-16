package montygo_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/asalimonov/montygo"
)

func TestPoolTelemetry(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("Options.Telemetry scopes tracing to one pool", func(t *testing.T) {
			ctx := testCtx(t)
			tp, spans := telTracing()
			traced := newPool(t, b, montygo.Options{MaxProcesses: 1, Telemetry: &montygo.TelemetryComponents{Tracer: tp.Tracer("pool")}})
			plain := newPool(t, b, montygo.Options{MaxProcesses: 1})

			s, err := traced.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "1", nil)
			require.NoError(t, err)
			require.NoError(t, s.Close(ctx))
			require.NotNil(t, telFind(spans.Ended(), telNamed("run code")), "%v", telNames(spans.Ended()))
			before := len(spans.Ended())

			s, err = plain.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "1", nil)
			require.NoError(t, err)
			require.NoError(t, s.Close(ctx))
			require.Len(t, spans.Ended(), before)
		})

		t.Run("a pool with empty telemetry components records nothing", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1, Telemetry: &montygo.TelemetryComponents{}})
			s, err := p.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "2", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
			require.NoError(t, s.Close(ctx))
		})
	})
}

var _ sdktrace.ReadOnlySpan
