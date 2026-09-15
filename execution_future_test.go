package montygo_test

import (
	"context"
	"testing"
	"time"

	"github.com/asalimonov/montygo"
	"github.com/stretchr/testify/require"
)

func TestExecutionFutures(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("failed gather clears subscriptions but does not settle shared futures", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{MaxPendingFutures: 1})
			shared, settle := montygo.NewFuture()
			opts := &montygo.FeedOptions{ExternalLookup: map[string]any{"pending": func() *montygo.Future { return shared }}}
			for range 3 {
				_, err := s.FeedRun(ctx, "import asyncio\nawait asyncio.gather(pending(), pending())", opts)
				require.ErrorContains(t, err, "pending future limit 1 exceeded")
				require.Zero(t, s.Stats().PendingFutures)
				require.NoError(t, s.Err())
				select {
				case <-shared.Done():
					t.Fatal("execution settled a caller-owned future")
				default:
				}
			}
			settle(42, nil)
			v, err := s.FeedRun(ctx, "await pending()", opts)
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
		})
		t.Run("pending work survives snapshot steps and manual settlement removes its subscription", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			callbacks := make(chan context.Context, 2)
			opts := &montygo.FeedOptions{ExternalLookup: map[string]any{"pending": func(cb context.Context) *montygo.Future {
				callbacks <- cb
				return montygo.AsyncContext(cb, func(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() })
			}}}
			snap, err := s.FeedStart(ctx, "import asyncio\nawait asyncio.gather(pending(), pending())", opts)
			require.NoError(t, err)
			snap, err = snap.(*montygo.FunctionSnapshot).ResumeAuto(ctx)
			require.NoError(t, err)
			first := <-callbacks
			require.NoError(t, first.Err())
			require.Equal(t, 1, s.Stats().PendingFutures)
			snap, err = snap.(*montygo.FunctionSnapshot).ResumeAuto(ctx)
			require.NoError(t, err)
			second := <-callbacks
			require.NoError(t, first.Err())
			require.NoError(t, second.Err())
			require.Equal(t, 2, s.Stats().PendingFutures)
			pending := snap.(*montygo.FutureSnapshot)
			snap, err = pending.Resume(ctx, []montygo.FutureResolution{{CallID: pending.PendingCallIDs[0], Value: 1}})
			require.NoError(t, err)
			require.Equal(t, 1, s.Stats().PendingFutures)
			pending = snap.(*montygo.FutureSnapshot)
			_, err = pending.Resume(ctx, []montygo.FutureResolution{{CallID: pending.PendingCallIDs[0], Value: 2}})
			require.NoError(t, err)
			require.Zero(t, s.Stats().PendingFutures)
			require.ErrorIs(t, first.Err(), context.Canceled)
			require.ErrorIs(t, second.Err(), context.Canceled)
		})
		t.Run("closing one execution does not settle another session's shared future", func(t *testing.T) {
			ctx := testCtx(t)
			first := newSession(t, b, montygo.CheckoutOptions{})
			second := newSession(t, b, montygo.CheckoutOptions{})
			shared, settle := montygo.NewFuture()
			entered := make(chan struct{}, 4)
			opts := &montygo.FeedOptions{ExternalLookup: map[string]any{"pending": func() *montygo.Future { entered <- struct{}{}; return shared }}}
			a := first.Go(ctx, "import asyncio\nawait asyncio.gather(pending(), pending())", opts)
			<-entered
			<-entered
			other := second.Go(ctx, "await pending()", opts)
			<-entered
			require.NoError(t, first.Close(ctx, montygo.KillNow))
			_, err := a.WaitContext(ctx)
			require.ErrorIs(t, err, montygo.ErrSessionClosed)
			require.Zero(t, first.Stats().PendingFutures)
			settle(42, nil)
			v, err := other.WaitContext(ctx)
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
		})
		t.Run("shared future pointer counts each wire call separately", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{MaxPendingFutures: 2})
			shared, settle := montygo.NewFuture()
			opts := &montygo.FeedOptions{ExternalLookup: map[string]any{"pending": func() *montygo.Future { return shared }}}
			r := s.Go(ctx, "import asyncio\nawait asyncio.gather(pending(), pending())", opts)
			require.Eventually(t, func() bool { return s.Stats().PendingFutures == 2 }, time.Second, time.Millisecond)
			settle(42, nil)
			v, err := r.WaitContext(ctx)
			require.NoError(t, err)
			require.Equal(t, []any{int64(42), int64(42)}, v)
			require.Zero(t, s.Stats().PendingFutures)
		})
		t.Run("cooperative abort drops the subscription without settling the future", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			pending, _ := montygo.NewFuture()
			opts := &montygo.FeedOptions{ExternalLookup: map[string]any{"pending": func() *montygo.Future { return pending }}}
			snap, err := s.FeedStart(ctx, "import asyncio\nawait asyncio.gather(pending())", opts)
			require.NoError(t, err)
			snap, err = snap.(*montygo.FunctionSnapshot).ResumeAuto(ctx)
			require.NoError(t, err)
			require.IsType(t, &montygo.FutureSnapshot{}, snap)
			require.Equal(t, 1, s.Stats().PendingFutures)
			result, err := s.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopAborted, result.How)
			require.True(t, result.SessionKept())
			require.Zero(t, s.Stats().PendingFutures)
			select {
			case <-pending.Done():
				t.Fatal("abort settled a caller-owned future")
			default:
			}
		})
	})
}
