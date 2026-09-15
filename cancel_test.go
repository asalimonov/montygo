package monty_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func TestCancellation(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("a context deadline mid-turn loses the session and the pool recovers", func(t *testing.T) {
			p := newPool(t, b, monty.Options{MaxProcesses: 1})
			s, err := p.Checkout(testCtx(t), monty.CheckoutOptions{})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			_, err = s.FeedRun(ctx, "while True:\n    pass", nil)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			_, err = s.FeedRun(testCtx(t), "1", nil)
			var perr *monty.ProtocolError
			require.ErrorAs(t, err, &perr)
			require.Equal(t, "a previous protocol turn was cancelled mid-flight; the worker was discarded", perr.Message)
			require.ErrorIs(t, err, monty.ErrTurnCancelled)
			require.NoError(t, s.Close(testCtx(t)))
			fresh, err := p.Checkout(testCtx(t), monty.CheckoutOptions{})
			require.NoError(t, err)
			defer fresh.Close(testCtx(t))
			v, err := fresh.FeedRun(testCtx(t), "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})
		t.Run("cancelling while awaiting a host future poisons the session", func(t *testing.T) {
			p := newPool(t, b, monty.Options{MaxProcesses: 1})
			s, err := p.Checkout(testCtx(t), monty.CheckoutOptions{})
			require.NoError(t, err)
			never, _ := monty.NewFuture()
			ctx, cancel := context.WithCancel(context.Background())
			lookup := map[string]any{"wait": func() *monty.Future {
				cancel()
				return never
			}}
			_, err = s.FeedRun(ctx, "await wait()", &monty.FeedOptions{ExternalLookup: lookup})
			require.True(t, errors.Is(err, context.Canceled), "%v", err)
			_, err = s.FeedRun(testCtx(t), "1", nil)
			require.ErrorIs(t, err, context.Canceled)
			require.NoError(t, s.Close(testCtx(t)))
			fresh, err := p.Checkout(testCtx(t), monty.CheckoutOptions{})
			require.NoError(t, err)
			defer fresh.Close(testCtx(t))
			v, err := fresh.FeedRun(testCtx(t), "2 + 2", nil)
			require.NoError(t, err)
			require.Equal(t, int64(4), v)
		})
		t.Run("a gathered future wait honours cancellation", func(t *testing.T) {
			s := newSession(t, b, monty.CheckoutOptions{})
			never, _ := monty.NewFuture()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			lookup := map[string]any{"wait": func() *monty.Future { return never }}
			_, err := s.FeedRun(ctx, "import asyncio\nawait asyncio.gather(wait(), wait())", &monty.FeedOptions{ExternalLookup: lookup})
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
		t.Run("a cancelled checkout wait does not leak capacity", func(t *testing.T) {
			p := newPool(t, b, monty.Options{MaxProcesses: 1})
			held, err := p.Checkout(testCtx(t), monty.CheckoutOptions{})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err = p.Checkout(ctx, monty.CheckoutOptions{})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NoError(t, held.Close(testCtx(t)))
			next, err := p.Checkout(testCtx(t), monty.CheckoutOptions{})
			require.NoError(t, err)
			require.NoError(t, next.Close(testCtx(t)))
		})
	})
}
