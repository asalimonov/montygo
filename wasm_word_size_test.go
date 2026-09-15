package monty_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func TestWasmWordSize(t *testing.T) {
	cases := []struct{ name, code, message string }{
		{"deque maxlen", "from collections import deque\ndeque([], 2**40)", "Python int too large to convert to C ssize_t"},
		{"bytes count", "bytes(2**40)", "cannot fit 'int' into an index-sized integer"},
	}
	for _, tc := range cases {
		t.Run("an over-32-bit "+tc.name+" raises rather than trapping", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, monty.BackendWasm, monty.Options{})
			s := wsmCheckout(t, p, monty.CheckoutOptions{})
			_, err := s.FeedRun(ctx, tc.code, nil)
			var rt *monty.RuntimeError
			require.ErrorAs(t, err, &rt)
			require.Equal(t, "OverflowError", rt.TypeName)
			require.Equal(t, tc.message, rt.Message)
			v, err := s.FeedRun(ctx, "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})
	}
}
