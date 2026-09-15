package monty_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func wsmInt32(v int32) *int32 { return &v }

func wsmString(v string) *string { return &v }

func TestWasmValueCodec(t *testing.T) {
	t.Run("WASM filesystem results preserve relative paths", func(t *testing.T) {
		ospCheckRelativePathResults(t, monty.BackendWasm)
	})

	t.Run("WASM rejects NUL paths before callbacks and reports clean no-handler paths", func(t *testing.T) {
		ospCheckOsPathValidation(t, monty.BackendWasm)
	})

	t.Run("OS callback paths are normalized over the wasm transport", func(t *testing.T) {
		p := newPool(t, monty.BackendWasm, monty.Options{})
		s := wsmCheckout(t, p, monty.CheckoutOptions{})
		var calls []any
		_, err := s.FeedRun(testCtx(t), `import os
from pathlib import Path
Path('sub/../file.txt').exists()
Path('/other//sub/../file.txt').exists()
os.listdir()
os.rename('./sub/../src', '../dst')`, &monty.FeedOptions{
			Cwd: "/data",
			OS: func(_ context.Context, name string, args []any, _ monty.Kwargs) (any, error) {
				calls = append(calls, []any{name, args})
				if name == "Path.iterdir" {
					return []any{}, nil
				}
				return true, nil
			},
		})
		require.NoError(t, err)
		require.Equal(t, []any{
			[]any{"Path.exists", []any{monty.Path("/data/file.txt")}},
			[]any{"Path.exists", []any{monty.Path("/other/file.txt")}},
			[]any{"Path.iterdir", []any{monty.Path("/data")}},
			[]any{"Path.rename", []any{monty.Path("/data/src"), monty.Path("/dst")}},
		}, calls)
	})

	t.Run("a time decodes over the wasm transport", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, monty.BackendWasm, monty.Options{})
		s := wsmCheckout(t, p, monty.CheckoutOptions{})

		v, err := s.FeedRun(ctx, "import datetime\ndatetime.time(0, 0)", nil)
		require.NoError(t, err)
		require.Equal(t, monty.Time{}, v)

		v, err = s.FeedRun(ctx, "import datetime\ndatetime.time(23, 59, 59, 999999, datetime.timezone(datetime.timedelta(hours=-5)))", nil)
		require.NoError(t, err)
		require.Equal(t, monty.Time{Hour: 23, Minute: 59, Second: 59, Microsecond: 999999, OffsetSeconds: wsmInt32(-18000)}, v)
	})

	t.Run("a time round-trips through the wasm transport", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, monty.BackendWasm, monty.Options{})
		s := wsmCheckout(t, p, monty.CheckoutOptions{})

		aware := monty.Time{Hour: 1, Minute: 2, Second: 3, Microsecond: 4, OffsetSeconds: wsmInt32(7200), TimezoneName: wsmString("P2"), Fold: 1}
		v, err := s.FeedRun(ctx, "x", &monty.FeedOptions{Inputs: map[string]any{"x": aware}})
		require.NoError(t, err)
		require.Equal(t, aware, v)
		v, err = s.FeedRun(ctx, "x.isoformat()", &monty.FeedOptions{Inputs: map[string]any{"x": aware}})
		require.NoError(t, err)
		require.Equal(t, "01:02:03.000004+02:00", v)

		utc := monty.Time{Hour: 12, OffsetSeconds: wsmInt32(0)}
		v, err = s.FeedRun(ctx, "x", &monty.FeedOptions{Inputs: map[string]any{"x": utc}})
		require.NoError(t, err)
		require.Equal(t, utc, v)
		v, err = s.FeedRun(ctx, "x.isoformat()", &monty.FeedOptions{Inputs: map[string]any{"x": utc}})
		require.NoError(t, err)
		require.Equal(t, "12:00:00+00:00", v)
	})
}
