package montygo_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

const wsmTypeCheckAttempts = 15

func TestWasmTypeCheck(t *testing.T) {
	t.Run("a feed after a failed type check leaves the worker alive", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, montygo.BackendWasm, montygo.Options{})
		for attempt := 0; attempt < wsmTypeCheckAttempts; attempt++ {
			s := wsmCheckout(t, p, montygo.CheckoutOptions{TypeCheck: true})
			_, err := s.FeedRun(ctx, "x = 1", nil)
			require.NoError(t, err)

			_, err = s.FeedRun(ctx, "x = 2\n\"hello\" + 1", nil)
			var te *montygo.TypingError
			require.ErrorAs(t, err, &te)
			require.Equal(t, "TypeError: error[invalid-assignment]: Object of type `Literal[2]` is not assignable to `Literal[1]`", err.Error())

			v, err := s.FeedRun(ctx, "x", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
			require.NoError(t, s.Close(ctx))
		}
	})

	t.Run("a repeated failing feed reports the same diagnostic every time", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, montygo.BackendWasm, montygo.Options{})
		s := wsmCheckout(t, p, montygo.CheckoutOptions{TypeCheck: true})
		_, err := s.FeedRun(ctx, "x = 1", nil)
		require.NoError(t, err)
		expected := strings.Join([]string{
			"error[unsupported-operator]: Unsupported `+` operation",
			" --> main.py:1:1",
			"  |",
			"1 | \"hello\" + 1",
			"  | -------^^^-",
			"  | |         |",
			"  | |         Has type `Literal[1]`",
			"  | Has type `Literal[\"hello\"]`",
			"",
			"",
		}, "\n")
		for attempt := 0; attempt < wsmTypeCheckAttempts; attempt++ {
			_, err := s.FeedRun(ctx, "\"hello\" + 1", nil)
			var te *montygo.TypingError
			require.ErrorAs(t, err, &te)
			require.Equal(t, expected, te.Display(montygo.DisplayTraceback))
		}
		require.NoError(t, s.Close(ctx))
	})
}
