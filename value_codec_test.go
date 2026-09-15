package monty_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func TestValueCodec(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("the component codec preserves time timezone presence", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, monty.CheckoutOptions{})
			timeProbe := "(x.tzinfo is not None, x.isoformat(), x.fold)"
			datetimeProbe := "(x.tzinfo is not None, x.isoformat())"
			cases := []struct {
				value any
				probe string
				want  monty.Tuple
			}{
				{monty.Time{}, timeProbe, monty.Tuple{false, "00:00:00", int64(0)}},
				{monty.Time{Fold: 1}, timeProbe, monty.Tuple{false, "00:00:00", int64(1)}},
				{monty.Time{OffsetSeconds: coreAInt32(0)}, timeProbe, monty.Tuple{true, "00:00:00+00:00", int64(0)}},
				{monty.Time{OffsetSeconds: coreAInt32(0), TimezoneName: coreAString("UTC")}, timeProbe, monty.Tuple{true, "00:00:00+00:00", int64(0)}},
				{monty.DateTime{Year: 2026, Month: 1, Day: 1}, datetimeProbe, monty.Tuple{false, "2026-01-01T00:00:00"}},
				{monty.DateTime{Year: 2026, Month: 1, Day: 1, OffsetSeconds: coreAInt32(0)}, datetimeProbe, monty.Tuple{true, "2026-01-01T00:00:00+00:00"}},
				{monty.DateTime{Year: 2026, Month: 1, Day: 1, OffsetSeconds: coreAInt32(0), TimezoneName: coreAString("UTC")}, datetimeProbe, monty.Tuple{true, "2026-01-01T00:00:00+00:00"}},
			}
			for _, tc := range cases {
				inputs := &monty.FeedOptions{Inputs: map[string]any{"x": tc.value}}
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
			session := newSession(t, b, monty.CheckoutOptions{})
			var conversionErr *monty.ConversionError

			_, err := session.FeedRun(ctx, "x", &monty.FeedOptions{Inputs: map[string]any{
				"x": monty.Time{TimezoneName: coreAString("UTC")},
			}})
			assert.ErrorAs(t, err, &conversionErr)
			assert.EqualError(t, err, "MontyTime timezoneName requires offsetSeconds")

			datetime := monty.DateTime{Year: 2026, Month: 1, Day: 1}
			datetime.TimezoneName = coreAString("UTC")
			_, err = session.FeedRun(ctx, "x", &monty.FeedOptions{Inputs: map[string]any{"x": datetime}})
			assert.ErrorAs(t, err, &conversionErr)
			assert.EqualError(t, err, "MontyDateTime timezoneName requires offsetSeconds")

			v, err := session.FeedRun(ctx, "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})
	})
}
