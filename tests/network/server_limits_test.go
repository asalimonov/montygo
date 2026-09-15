package network

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func requireRuntimeError(t *testing.T, err error, typeName string) *monty.RuntimeError {
	t.Helper()
	var re *monty.RuntimeError
	require.ErrorAs(t, err, &re)
	require.Equal(t, typeName, re.Exception().TypeName, re.Error())
	return re
}

func TestLimits_MemoryClampedToCeiling(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-memory-mib", "16"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	session := s.Checkout(ctx, p, monty.CheckoutOptions{Limits: &monty.ResourceLimits{MaxMemory: 1 << 30}})

	_, err := session.FeedRun(ctx, "x = 'a' * (64 * 1024 * 1024)\nlen(x)", nil)
	requireRuntimeError(t, err, "MemoryError")
}

func TestLimits_DurationClampedToCeiling(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-duration", "1"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	session := s.Checkout(ctx, p, monty.CheckoutOptions{Limits: &monty.ResourceLimits{MaxDuration: time.Hour}})

	_, err := session.FeedRun(ctx, "while True:\n    pass", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "time limit exceeded")
	require.Contains(t, err.Error(), "> 1s")
}

func TestLimits_RecursionClampedToCeiling(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-recursion-depth", "50"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	session := s.Checkout(ctx, p, monty.CheckoutOptions{Limits: &monty.ResourceLimits{MaxRecursionDepth: 100000}})

	_, err := session.FeedRun(ctx, "def f(n):\n    return f(n + 1) if n < 200 else n\nf(0)", nil)
	requireRuntimeError(t, err, "RecursionError")

	ok := s.Checkout(ctx, p, monty.CheckoutOptions{})
	v, err := ok.FeedRun(ctx, "def g(n):\n    return g(n + 1) if n < 20 else n\ng(0)", nil)
	require.NoError(t, err)
	require.Equal(t, int64(20), v)
}

func TestLimits_LowerClientLimitWins(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	const allocate = "x = 'a' * (8 * 1024 * 1024)\nlen(x)"

	unlimited := s.Checkout(ctx, p, monty.CheckoutOptions{})
	v, err := unlimited.FeedRun(ctx, allocate, nil)
	require.NoError(t, err)
	require.Equal(t, int64(8*1024*1024), v)

	tight := s.Checkout(ctx, p, monty.CheckoutOptions{Limits: &monty.ResourceLimits{MaxMemory: 1024 * 1024}})
	_, err = tight.FeedRun(ctx, allocate, nil)
	requireRuntimeError(t, err, "MemoryError")
}

func TestLimits_DisabledCeilingPassesClientValue(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-memory-mib", "0"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	session := s.Checkout(ctx, p, monty.CheckoutOptions{})

	v, err := session.FeedRun(ctx, "x = 'a' * (128 * 1024 * 1024)\nlen(x)", nil)
	require.NoError(t, err)
	require.Equal(t, int64(128*1024*1024), v)
}
