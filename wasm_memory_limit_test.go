package montygo_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
)

var wsmMemoryErrorPattern = regexp.MustCompile(`^MemoryError: memory limit exceeded: (\d+) bytes > (\d+) bytes$`)

func wsmAssertMemoryError(t *testing.T, err error, expected, maxMemory, tolerance int64) {
	t.Helper()
	m := wsmMemoryErrorPattern.FindStringSubmatch(err.Error())
	require.NotNil(t, m, "unexpected MemoryError message: %s", err.Error())
	used, perr := strconv.ParseInt(m[1], 10, 64)
	require.NoError(t, perr)
	limit, perr := strconv.ParseInt(m[2], 10, 64)
	require.NoError(t, perr)
	require.Equal(t, maxMemory, limit)
	diff := used - expected
	if diff < 0 {
		diff = -diff
	}
	require.LessOrEqual(t, diff, tolerance, "reported %d bytes, expected within %d of %d", used, tolerance, expected)
}

func TestWasmMemoryLimit(t *testing.T) {
	t.Run("a session limit leaves normal wasm work alone", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, backendWasm, montygo.PoolOptions{})
		s := wsmCheckout(t, p, defaultRuntime, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxMemory: 1024 * 1024}})
		v, err := s.FeedRun(ctx, "sum(range(1000))", nil)
		require.NoError(t, err)
		require.Equal(t, int64(499500), v)
	})

	t.Run("an overrun the interpreter catches leaves the instance alive", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, backendWasm, montygo.PoolOptions{})
		const maxMemory = 1024 * 1024
		s := wsmCheckout(t, p, defaultRuntime, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxMemory: maxMemory}})
		_, err := s.FeedRun(ctx, "[str(i) for i in range(131_072)]", nil)
		var rt *monterr.RuntimeError
		require.ErrorAs(t, err, &rt)
		wsmAssertMemoryError(t, err, 1_060_648, maxMemory, 1024)
		require.Equal(t, "MemoryError", rt.TypeName)
		v, err := s.FeedRun(ctx, "1 + 1", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), v)
	})

	t.Run("exceeding the limit kills the instance and the pool recovers", func(t *testing.T) {
		ctx := testCtx(t)
		p := newPool(t, backendWasm, montygo.PoolOptions{})
		s := wsmCheckout(t, p, defaultRuntime, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxMemory: 1024}})
		_, err := s.FeedRun(ctx, "# "+strings.Repeat("a", 16*1024*1024), nil)
		var rt *monterr.RuntimeError
		require.ErrorAs(t, err, &rt)
		require.Equal(t, "MemoryError", rt.TypeName)
		require.Equal(t, "the worker exceeded its memory limit and was terminated", rt.Message)
		next := wsmCheckout(t, p, defaultRuntime, montygo.CheckoutOptions{})
		v, err := next.FeedRun(ctx, "3 + 3", nil)
		require.NoError(t, err)
		require.Equal(t, int64(6), v)
	})
}
