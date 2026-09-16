package montygo_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
)

func TestRepl(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("feed preserves state without replay", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			_, err := session.FeedRun(ctx, "counter = 0", nil)
			require.NoError(t, err)
			v, err := session.FeedRun(ctx, "counter = counter + 1", nil)
			require.NoError(t, err)
			require.Nil(t, v)
			v, err = session.FeedRun(ctx, "counter", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
			v, err = session.FeedRun(ctx, "counter = counter + 1", nil)
			require.NoError(t, err)
			require.Nil(t, v)
			v, err = session.FeedRun(ctx, "counter", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})

		t.Run("runtime error does not kill the session", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			_, err := session.FeedRun(ctx, "x = 1", nil)
			require.NoError(t, err)
			_, err = session.FeedRun(ctx, "1 / 0", nil)
			var runtimeErr *monterr.RuntimeError
			require.ErrorAs(t, err, &runtimeErr)
			require.EqualError(t, err, "ZeroDivisionError: division by zero")
			require.Equal(t, strings.Join([]string{
				"Traceback (most recent call last):",
				`  File "<python-input-1>", line 1, in <module>`,
				"    1 / 0",
				"    ~~~~~",
				"ZeroDivisionError: division by zero",
			}, "\n"), runtimeErr.Display(monterr.DisplayTraceback))
			v, err := session.FeedRun(ctx, "x", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})

		t.Run("session dump returns opaque state", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			_, err := session.FeedRun(ctx, "x = 40", nil)
			require.NoError(t, err)
			v, err := session.FeedRun(ctx, "x = x + 1", nil)
			require.NoError(t, err)
			require.Nil(t, v)
			state, err := session.Dump(ctx)
			require.NoError(t, err)
			require.IsType(t, []byte{}, state)
			require.NotEmpty(t, state)
			v, err = session.FeedRun(ctx, "x + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
		})
	})
}
