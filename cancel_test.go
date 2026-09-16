package montygo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox/host"
)

func TestCancellation(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		// Deviation from @pydantic/monty: the feed context ends the run through the
		// stop policy; Python that never yields is killed when Timeout expires.
		t.Run("a context deadline mid-turn loses the session and the pool recovers", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			s, err := p.Checkout(testCtx(t), defaultRuntime, montygo.CheckoutOptions{Stop: montygo.StopPolicy{Timeout: 100 * time.Millisecond}})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			_, err = s.FeedRun(ctx, "while True:\n    pass", nil)
			var killed *monterr.SessionKilledError
			require.ErrorAs(t, err, &killed, "%v", err)
			require.ErrorIs(t, err, monterr.ErrSessionLost)
			require.Equal(t, montygo.SessionClosed, s.State())
			_, err = s.FeedRun(testCtx(t), "1", nil)
			require.ErrorIs(t, err, monterr.ErrSessionLost)
			require.NoError(t, s.Close(testCtx(t)))
			fresh, err := p.Checkout(testCtx(t), defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			defer fresh.Close(testCtx(t))
			v, err := fresh.FeedRun(testCtx(t), "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})
		// Deviation from @pydantic/monty: a cancelled context while the worker waits on a host
		// call ends the feed with AbortFeed instead of killing the worker (docs/parity/tests.md).
		t.Run("cancelling while awaiting a host future interrupts the feed and keeps the session", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			s, err := p.Checkout(testCtx(t), defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			never, _ := host.NewFuture()
			ctx, cancel := context.WithCancel(context.Background())
			lookup := map[string]any{"wait": func() *host.Future {
				cancel()
				return never
			}}
			_, err = s.FeedRun(ctx, "await wait()", &montygo.FeedOptions{ExternalLookup: lookup})
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re, "%v", err)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			require.False(t, errors.Is(err, monterr.ErrSessionLost))
			v, err := s.FeedRun(testCtx(t), "2 + 2", nil)
			require.NoError(t, err)
			require.Equal(t, int64(4), v)
			require.NoError(t, s.Close(testCtx(t)))
		})
		t.Run("a gathered future wait honours cancellation", func(t *testing.T) {
			s := newSession(t, b, montygo.CheckoutOptions{})
			never, _ := host.NewFuture()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			lookup := map[string]any{"wait": func() *host.Future { return never }}
			_, err := s.FeedRun(ctx, "import asyncio\nawait asyncio.gather(wait(), wait())", &montygo.FeedOptions{ExternalLookup: lookup})
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re, "%v", err)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			v, err := s.FeedRun(testCtx(t), "1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})
		t.Run("a cancelled checkout wait does not leak capacity", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			held, err := p.Checkout(testCtx(t), defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err = p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NoError(t, held.Close(testCtx(t)))
			next, err := p.Checkout(testCtx(t), defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			require.NoError(t, next.Close(testCtx(t)))
		})
	})
}
