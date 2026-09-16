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

// blockingLookup returns a host function that blocks until its context ends
// and reports the wait on started.
func blockingLookup(started chan<- struct{}) map[string]any {
	return map[string]any{"wait": func(ctx context.Context) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}}
}

func TestStop(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("stopping a host call raises KeyboardInterrupt and keeps the session", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			started := make(chan struct{})
			run := s.Go(ctx, "x = 'before'\ntry:\n    wait()\nexcept KeyboardInterrupt:\n    x = 'caught'", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			stopped, err := s.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopAborted, stopped.How)
			require.True(t, stopped.SessionKept())
			_, err = run.Wait()
			require.Same(t, stopped.Err, err)
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re, "%v", err)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			require.NoError(t, s.Err())
			v, err := s.FeedRun(ctx, "x", nil)
			require.NoError(t, err)
			require.Equal(t, "before", v, "an aborted feed cannot catch the stop")
		})

		t.Run("the stop reason becomes the raised exception", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			stopped, err := run.Stop(ctx, montygo.StopPolicy{Reason: monterr.Raise("TimeoutError", "budget spent")})
			require.NoError(t, err)
			require.Equal(t, montygo.StopAborted, stopped.How)
			_, err = run.Wait()
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re)
			require.Equal(t, "TimeoutError", re.TypeName)
			require.Contains(t, re.Message, "budget spent")
			require.NoError(t, s.Err())
		})

		t.Run("an AsyncContext future observes the stop", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			started := make(chan struct{})
			lookup := map[string]any{"wait": func(ctx context.Context) *host.Future {
				return host.AsyncContext(ctx, func(ctx context.Context) (any, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				})
			}}
			run := s.Go(ctx, "await wait()", &montygo.FeedOptions{ExternalLookup: lookup})
			<-started
			stopped, err := s.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopAborted, stopped.How)
			_, err = run.Wait()
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			require.NoError(t, s.Err())
		})

		t.Run("stopping running Python kills the worker when Timeout expires", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{Stop: montygo.StopPolicy{Timeout: 50 * time.Millisecond}})
			require.NoError(t, err)
			run := s.Go(ctx, "while True:\n    pass", nil)
			time.Sleep(100 * time.Millisecond)
			stopped, err := s.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopKilled, stopped.How)
			require.False(t, stopped.SessionKept())
			_, err = run.Wait()
			require.ErrorIs(t, err, monterr.ErrSessionLost)
			var killed *monterr.SessionKilledError
			require.ErrorAs(t, err, &killed)
			require.Same(t, stopped.Err, err)
			<-s.Done()
			require.ErrorIs(t, s.Err(), monterr.ErrSessionLost)
		})

		t.Run("stopping a suspended snapshot aborts it", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			snap, err := s.FeedStart(ctx, "try:\n    pending()\nexcept KeyboardInterrupt:\n    x = 'aborted'\nx", nil)
			require.NoError(t, err)
			fn, ok := snap.(*montygo.FunctionSnapshot)
			require.True(t, ok, "%T", snap)
			require.Equal(t, "pending", fn.FunctionName)
			stopped, err := s.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopAborted, stopped.How)
			_, err = fn.Resume(ctx, 1)
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			require.NoError(t, s.Err())
			v, err := s.FeedRun(ctx, "2 + 2", nil)
			require.NoError(t, err)
			require.Equal(t, int64(4), v)
		})

		t.Run("Stop with nothing running reports StopNotRunning", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			stopped, err := s.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopNotRunning, stopped.How)
			require.Equal(t, "not running", stopped.How.String())
			v, err := s.FeedRun(ctx, "1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})
	})
}

func TestSessionLifecycle(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("Go returns the feed result", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			run := s.Go(ctx, "6 * 7", nil)
			<-run.Done()
			v, err := run.Wait()
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
		})

		t.Run("Close with KillNow ends a running feed with ErrSessionClosed", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			require.NoError(t, s.Close(ctx, montygo.KillNow))
			_, err = run.Wait()
			require.ErrorIs(t, err, monterr.ErrSessionClosed)
			require.NotErrorIs(t, err, monterr.ErrSessionLost)
			<-s.Done()
			require.ErrorIs(t, s.Err(), monterr.ErrSessionClosed)
			require.NoError(t, s.Close(ctx))
			fresh, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			defer fresh.Close(ctx)
			v, err := fresh.FeedRun(ctx, "1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})

		t.Run("Done closes and Err reports ErrSessionClosed after Close", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			select {
			case <-s.Done():
				t.Fatal("session done before close")
			default:
			}
			require.NoError(t, s.Err())
			require.NoError(t, s.Close(ctx))
			<-s.Done()
			require.ErrorIs(t, s.Err(), monterr.ErrSessionClosed)
			_, err := s.FeedRun(ctx, "1", nil)
			require.ErrorIs(t, err, monterr.ErrSessionClosed)
		})

		t.Run("a crashed worker is reported through Done and Err", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxDuration: 200 * time.Millisecond}})
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "while True:\n    pass", nil)
			var re *monterr.RuntimeError
			if errors.As(err, &re) {
				t.Skip("duration limit is enforced inside the sandbox on this backend")
			}
			require.ErrorIs(t, err, monterr.ErrSessionLost)
			<-s.Done()
			require.ErrorIs(t, s.Err(), monterr.ErrSessionLost)
		})

		t.Run("Stats counts host objects and pending futures", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			require.Equal(t, montygo.SessionStats{}, s.Stats())
			obj := clsInstance(t, &clsGreeter{Greeting: "hi"}, host.ClassInstanceOptions{EagerAttrs: host.All()})
			_, err := s.FeedRun(ctx, "x = obj", &montygo.FeedOptions{Inputs: map[string]any{"obj": obj}})
			require.NoError(t, err)
			st := s.Stats()
			require.Equal(t, 2, st.HostObjects, "the instance and its class")
			require.Equal(t, 2, st.PeakHostObjects)
			require.Equal(t, 0, st.PendingFutures)
		})
	})
}
