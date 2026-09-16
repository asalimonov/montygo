package montygo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
)

func TestValueCodec(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("the component codec preserves time timezone presence", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			timeProbe := "(x.tzinfo is not None, x.isoformat(), x.fold)"
			datetimeProbe := "(x.tzinfo is not None, x.isoformat())"
			cases := []struct {
				value any
				probe string
				want  sandbox.Tuple
			}{
				{sandbox.Time{}, timeProbe, sandbox.Tuple{false, "00:00:00", int64(0)}},
				{sandbox.Time{Fold: 1}, timeProbe, sandbox.Tuple{false, "00:00:00", int64(1)}},
				{sandbox.Time{OffsetSeconds: coreAInt32(0)}, timeProbe, sandbox.Tuple{true, "00:00:00+00:00", int64(0)}},
				{sandbox.Time{OffsetSeconds: coreAInt32(0), TimezoneName: coreAString("UTC")}, timeProbe, sandbox.Tuple{true, "00:00:00+00:00", int64(0)}},
				{sandbox.DateTime{Year: 2026, Month: 1, Day: 1}, datetimeProbe, sandbox.Tuple{false, "2026-01-01T00:00:00"}},
				{sandbox.DateTime{Year: 2026, Month: 1, Day: 1, OffsetSeconds: coreAInt32(0)}, datetimeProbe, sandbox.Tuple{true, "2026-01-01T00:00:00+00:00"}},
				{sandbox.DateTime{Year: 2026, Month: 1, Day: 1, OffsetSeconds: coreAInt32(0), TimezoneName: coreAString("UTC")}, datetimeProbe, sandbox.Tuple{true, "2026-01-01T00:00:00+00:00"}},
			}
			for _, tc := range cases {
				inputs := &montygo.FeedOptions{Inputs: map[string]any{"x": tc.value}}
				v, err := session.FeedRun(ctx, "x", inputs)
				require.NoError(t, err)
				require.Equal(t, tc.value, v)
				v, err = session.FeedRun(ctx, tc.probe, inputs)
				require.NoError(t, err)
				require.Equal(t, tc.want, v)
			}
		})

		t.Run("the component codec rejects a timezone name with no offset", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			var conversionErr *monterr.ConversionError

			_, err := session.FeedRun(ctx, "x", &montygo.FeedOptions{Inputs: map[string]any{
				"x": sandbox.Time{TimezoneName: coreAString("UTC")},
			}})
			assert.ErrorAs(t, err, &conversionErr)
			assert.EqualError(t, err, "MontyTime timezoneName requires offsetSeconds")

			datetime := sandbox.DateTime{Year: 2026, Month: 1, Day: 1}
			datetime.TimezoneName = coreAString("UTC")
			_, err = session.FeedRun(ctx, "x", &montygo.FeedOptions{Inputs: map[string]any{"x": datetime}})
			assert.ErrorAs(t, err, &conversionErr)
			assert.EqualError(t, err, "MontyDateTime timezoneName requires offsetSeconds")

			v, err := session.FeedRun(ctx, "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})
	})
}
