package montygo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

func wsmInt32(v int32) *int32 { return &v }

func wsmString(v string) *string { return &v }

func TestWasmValueCodec(t *testing.T) {
	t.Run("WASM filesystem results preserve relative paths", func(t *testing.T) {
		ospCheckRelativePathResults(t, backendWasm)
	})

	t.Run("WASM rejects NUL paths before callbacks and reports clean no-handler paths", func(t *testing.T) {
		ospCheckOsPathValidation(t, backendWasm)
	})

	t.Run("OS callback paths are normalized over the wasm transport", func(t *testing.T) {
		p := newPool(t, backendWasm, montygo.PoolOptions{})
		var calls []any
		s := wsmCheckout(t, p, mustRuntime(montygo.RuntimeOptions{
			OS: func(_ context.Context, name string, args []any, _ host.Kwargs) (any, error) {
				calls = append(calls, []any{name, args})
				if name == "Path.iterdir" {
					return []any{}, nil
				}
				return true, nil
			},
		}), montygo.CheckoutOptions{})
		_, err := s.FeedRun(testCtx(t), `import os
from pathlib import Path
Path('sub/../file.txt').exists()
Path('/other//sub/../file.txt').exists()
os.listdir()
os.rename('./sub/../src', '../dst')`, &montygo.FeedOptions{Cwd: "/data"})
		require.NoError(t, err)
		require.Equal(t, []any{
			[]any{"Path.exists", []any{sandbox.Path("/data/file.txt")}},
			[]any{"Path.exists", []any{sandbox.Path("/other/file.txt")}},
			[]any{"Path.iterdir", []any{sandbox.Path("/data")}},
			[]any{"Path.rename", []any{sandbox.Path("/data/src"), sandbox.Path("/dst")}},
		}, calls)
	})

	t.Run("a time decodes over the wasm transport", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, backendWasm, montygo.PoolOptions{})
		s := wsmCheckout(t, p, defaultRuntime, montygo.CheckoutOptions{})

		v, err := s.FeedRun(ctx, "import datetime\ndatetime.time(0, 0)", nil)
		require.NoError(t, err)
		require.Equal(t, sandbox.Time{}, v)

		v, err = s.FeedRun(ctx, "import datetime\ndatetime.time(23, 59, 59, 999999, datetime.timezone(datetime.timedelta(hours=-5)))", nil)
		require.NoError(t, err)
		require.Equal(t, sandbox.Time{Hour: 23, Minute: 59, Second: 59, Microsecond: 999999, OffsetSeconds: wsmInt32(-18000)}, v)
	})

	t.Run("a time round-trips through the wasm transport", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, backendWasm, montygo.PoolOptions{})
		s := wsmCheckout(t, p, defaultRuntime, montygo.CheckoutOptions{})

		aware := sandbox.Time{Hour: 1, Minute: 2, Second: 3, Microsecond: 4, OffsetSeconds: wsmInt32(7200), TimezoneName: wsmString("P2"), Fold: 1}
		v, err := s.FeedRun(ctx, "x", &montygo.FeedOptions{Inputs: map[string]any{"x": aware}})
		require.NoError(t, err)
		require.Equal(t, aware, v)
		v, err = s.FeedRun(ctx, "x.isoformat()", &montygo.FeedOptions{Inputs: map[string]any{"x": aware}})
		require.NoError(t, err)
		require.Equal(t, "01:02:03.000004+02:00", v)

		utc := sandbox.Time{Hour: 12, OffsetSeconds: wsmInt32(0)}
		v, err = s.FeedRun(ctx, "x", &montygo.FeedOptions{Inputs: map[string]any{"x": utc}})
		require.NoError(t, err)
		require.Equal(t, utc, v)
		v, err = s.FeedRun(ctx, "x.isoformat()", &montygo.FeedOptions{Inputs: map[string]any{"x": utc}})
		require.NoError(t, err)
		require.Equal(t, "12:00:00+00:00", v)
	})
}
