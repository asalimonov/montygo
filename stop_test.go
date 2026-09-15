package montygo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

const catchableScript = `x = 'before'
try:
    wait()
except KeyboardInterrupt:
    x = 'caught'
    x = x + suffix()
x`

func TestStopPolicy(t *testing.T) {
	t.Run("zero fields inherit and KillNow is a negative timeout", func(t *testing.T) {
		require.Equal(t, "pending", montygo.StopPending.String())
		require.Equal(t, "killed", montygo.StopKilled.String())
		require.Equal(t, "unknown", montygo.StopKind(99).String())
		require.Equal(t, "closed", montygo.SessionClosed.String())
		require.Negative(t, montygo.KillNow.Timeout)
		require.True(t, montygo.Stopped{}.SessionKept())
		require.False(t, montygo.Stopped{SessionErr: montygo.ErrSessionClosed}.SessionKept())
	})
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("a catchable stop lets Python clean up with host calls", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			started := make(chan struct{})
			lookup := blockingLookup(started)
			lookup["suffix"] = func(ctx context.Context) (string, error) {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				return " cleanly", nil
			}
			run := s.Go(ctx, catchableScript, &montygo.FeedOptions{ExternalLookup: lookup})
			<-started
			stopped, err := run.Stop(ctx, montygo.StopPolicy{Catchable: true})
			require.NoError(t, err)
			require.Equal(t, montygo.StopFinished, stopped.How)
			require.NoError(t, stopped.Err)
			require.True(t, stopped.SessionKept())
			v, err := run.Wait()
			require.NoError(t, err)
			require.Equal(t, "caught cleanly", v)
		})

		t.Run("an uncaught catchable stop reports StopAborted", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{Stop: montygo.StopPolicy{Catchable: true}})
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			stopped, err := run.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopAborted, stopped.How)
			var re *montygo.RuntimeError
			require.ErrorAs(t, stopped.Err, &re)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			require.True(t, stopped.SessionKept())
			v, err := s.FeedRun(ctx, "1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})

		t.Run("a catchable stop reaches an awaited future", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			never, _ := montygo.NewFuture()
			started := make(chan struct{})
			lookup := map[string]any{"pending": func() *montygo.Future { close(started); return never }}
			run := s.Go(ctx, "try:\n    await pending()\nexcept KeyboardInterrupt:\n    r = 'caught'\nr", &montygo.FeedOptions{ExternalLookup: lookup})
			<-started
			stopped, err := run.Stop(ctx, montygo.StopPolicy{Catchable: true})
			require.NoError(t, err)
			require.Equal(t, montygo.StopFinished, stopped.How)
			v, err := run.Wait()
			require.NoError(t, err)
			require.Equal(t, "caught", v)
		})

		t.Run("a catchable stop that ignores the exception is killed at Timeout", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1})
			s, err := p.Checkout(ctx, montygo.CheckoutOptions{Stop: montygo.StopPolicy{Catchable: true, Timeout: 150 * time.Millisecond}})
			require.NoError(t, err)
			started := make(chan struct{})
			run := s.Go(ctx, "try:\n    wait()\nexcept KeyboardInterrupt:\n    pass\nwhile True:\n    pass", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			stopped, err := run.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopKilled, stopped.How)
			require.False(t, stopped.SessionKept())
			var killed *montygo.SessionKilledError
			require.ErrorAs(t, stopped.Err, &killed)
		})

		t.Run("Drain lets the run end on its own", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			run := s.Go(ctx, "sum(range(1000))", nil)
			stopped, err := run.Stop(ctx, montygo.StopPolicy{Drain: 5 * time.Second})
			require.NoError(t, err)
			require.Equal(t, montygo.StopFinished, stopped.How)
			require.NoError(t, stopped.Err)
			v, err := run.Wait()
			require.NoError(t, err)
			require.Equal(t, int64(499500), v)
		})

		t.Run("Drain expiry requests the stop", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			begin := time.Now()
			stopped, err := run.Stop(ctx, montygo.StopPolicy{Drain: 100 * time.Millisecond})
			require.NoError(t, err)
			require.Equal(t, montygo.StopAborted, stopped.How)
			require.GreaterOrEqual(t, time.Since(begin), 100*time.Millisecond)
		})

		t.Run("KillNow ends the run without a request", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1})
			s, err := p.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			stopped, err := run.Stop(ctx, montygo.KillNow)
			require.NoError(t, err)
			require.Equal(t, montygo.StopKilled, stopped.How)
			require.ErrorIs(t, stopped.SessionErr, montygo.ErrSessionLost)
			require.Equal(t, montygo.SessionClosed, s.State())
		})

		t.Run("a second Stop joins the first and can only shorten it", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1})
			s, err := p.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: map[string]any{"wait": func() int { close(started); time.Sleep(400 * time.Millisecond); return 1 }}})
			<-started
			first := make(chan montygo.Stopped, 1)
			go func() {
				st, _ := run.Stop(ctx, montygo.StopPolicy{Timeout: time.Hour})
				first <- st
			}()
			time.Sleep(20 * time.Millisecond)
			stopped, err := run.Stop(ctx, montygo.StopPolicy{Timeout: 50 * time.Millisecond, Join: time.Second})
			require.NoError(t, err)
			require.Equal(t, montygo.StopKilled, stopped.How)
			require.Equal(t, montygo.StopKilled, (<-first).How)
		})

		t.Run("policy levels resolve pool, checkout and call", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1, Stop: montygo.StopPolicy{Reason: montygo.Raise("TimeoutError", "pool")}})
			started := make(chan struct{})
			s, err := p.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			stopped, err := run.Stop(ctx)
			require.NoError(t, err)
			var re *montygo.RuntimeError
			require.ErrorAs(t, stopped.Err, &re)
			require.Equal(t, "TimeoutError", re.TypeName)
			require.Equal(t, "pool", re.Message)

			started = make(chan struct{})
			run = s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			stopped, err = run.Stop(ctx, montygo.StopPolicy{Reason: montygo.Raise("ValueError", "call")})
			require.NoError(t, err)
			require.ErrorAs(t, stopped.Err, &re)
			require.Equal(t, "ValueError", re.TypeName)
			require.NoError(t, s.Close(ctx))

			s, err = p.Checkout(ctx, montygo.CheckoutOptions{Stop: montygo.StopPolicy{Reason: montygo.Raise("RuntimeError", "checkout")}})
			require.NoError(t, err)
			defer s.Close(ctx)
			started = make(chan struct{})
			run = s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			stopped, err = run.Stop(ctx)
			require.NoError(t, err)
			require.ErrorAs(t, stopped.Err, &re)
			require.Equal(t, "checkout", re.Message)
		})

		t.Run("an invalid policy is rejected before anything happens", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			run := s.Go(ctx, "1", nil)
			_, err := run.Stop(ctx, montygo.StopPolicy{Drain: -time.Second})
			var oe *montygo.OptionError
			require.ErrorAs(t, err, &oe)
			_, err = run.Stop(ctx, montygo.StopPolicy{}, montygo.StopPolicy{})
			require.ErrorAs(t, err, &oe)
			err = s.Close(ctx, montygo.StopPolicy{Join: -1})
			require.ErrorAs(t, err, &oe)
			_, err = p_checkout_invalid(ctx, b)
			require.ErrorAs(t, err, &oe)
			v, err := run.Wait()
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})

		t.Run("the feed context ends the run through the policy", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			feed, cancel := context.WithCancel(ctx)
			started := make(chan struct{})
			lookup := blockingLookup(started)
			go func() { <-started; cancel() }()
			_, err := s.FeedRun(feed, "wait()", &montygo.FeedOptions{ExternalLookup: lookup})
			var re *montygo.RuntimeError
			require.ErrorAs(t, err, &re, "%v", err)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			require.Equal(t, montygo.SessionIdle, s.State())
			v, err := s.FeedRun(ctx, "1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})
	})
}

// p_checkout_invalid checks out with an invalid stop policy.
func p_checkout_invalid(ctx context.Context, b montygo.Backend) (*montygo.Session, error) {
	p, err := openPool(ctx, b, montygo.Options{MaxProcesses: 1, MinProcesses: -1})
	if err != nil {
		return nil, err
	}
	defer p.Close(ctx)
	return p.Checkout(ctx, montygo.CheckoutOptions{Stop: montygo.StopPolicy{Join: -time.Second}})
}

func TestSessionClose(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("Close stops a running feed first", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1})
			s, err := p.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			require.NoError(t, s.Close(ctx))
			_, err = run.Wait()
			var re *montygo.RuntimeError
			require.ErrorAs(t, err, &re)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			require.ErrorIs(t, s.Err(), montygo.ErrSessionClosed)
			require.Equal(t, montygo.SessionClosed, s.State())
			fresh, err := p.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			defer fresh.Close(ctx)
			v, err := fresh.FeedRun(ctx, "2", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})

		t.Run("Close continues after the caller stops waiting", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1})
			s, err := p.Checkout(ctx, montygo.CheckoutOptions{Stop: montygo.StopPolicy{Timeout: 200 * time.Millisecond, Join: 200 * time.Millisecond}})
			require.NoError(t, err)
			run := s.Go(ctx, "while True:\n    pass", nil)
			time.Sleep(50 * time.Millisecond)
			short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			err = s.Close(short)
			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded)
			_, err = run.WaitContext(ctx)
			require.ErrorIs(t, err, montygo.ErrSessionLost)
			<-s.Done()
			require.NoError(t, s.Close(ctx))
		})

		t.Run("State follows the session", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			require.Equal(t, montygo.SessionIdle, s.State())
			snap, err := s.FeedStart(ctx, "f()", nil)
			require.NoError(t, err)
			require.Equal(t, montygo.SessionPaused, s.State())
			_, err = snap.(*montygo.FunctionSnapshot).Resume(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, montygo.SessionIdle, s.State())
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			require.Equal(t, montygo.SessionRunning, s.State())
			_, err = run.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.SessionIdle, s.State())
			require.NoError(t, s.Close(ctx))
			require.Equal(t, montygo.SessionClosed, s.State())
		})
	})
}

func TestPoolRunAndSlot(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("Run checks out, feeds once and closes", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1})
			v, err := p.Run(ctx, "x * 2", &montygo.RunOptions{FeedOptions: montygo.FeedOptions{Inputs: map[string]any{"x": 21}}})
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
			_, err = p.Run(ctx, "1 / 0", nil)
			var re *montygo.RuntimeError
			require.ErrorAs(t, err, &re)
			require.Equal(t, "ZeroDivisionError", re.TypeName)
			require.Eventually(t, func() bool { return p.Stats().Active == 0 }, 5*time.Second, 10*time.Millisecond)
		})

		t.Run("Shutdown drains before stopping", func(t *testing.T) {
			ctx := testCtx(t)
			p, err := openPool(ctx, b, montygo.Options{MaxProcesses: 1})
			require.NoError(t, err)
			s, err := p.Checkout(ctx, montygo.CheckoutOptions{})
			require.NoError(t, err)
			run := s.Go(ctx, "sum(range(100000))", nil)
			require.NoError(t, p.Shutdown(ctx, montygo.StopPolicy{Drain: 10 * time.Second}))
			v, err := run.Wait()
			require.NoError(t, err)
			require.Equal(t, int64(4999950000), v)
			require.ErrorIs(t, s.Err(), montygo.ErrSessionClosed)
		})

		t.Run("Slot re-checks out after a loss and rejects overlap", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.Options{MaxProcesses: 1})
			slot := p.Slot(montygo.CheckoutOptions{})
			require.Nil(t, slot.Session())
			require.Equal(t, montygo.SessionIdle, slot.State())
			v, err := slot.FeedRun(ctx, "x = 1\nx", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
			first := slot.Session()
			require.NotNil(t, first)

			started := make(chan struct{})
			run, err := slot.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			require.NoError(t, err)
			<-started
			require.Equal(t, montygo.SessionRunning, slot.State())
			_, err = slot.Go(ctx, "1", nil)
			require.ErrorIs(t, err, montygo.ErrSessionBusy)
			stopped, err := slot.Stop(ctx, montygo.KillNow)
			require.NoError(t, err)
			require.Equal(t, montygo.StopKilled, stopped.How)
			_, err = run.Wait()
			require.ErrorIs(t, err, montygo.ErrSessionLost)
			require.Equal(t, montygo.SessionIdle, slot.State())

			_, err = slot.FeedRun(ctx, "x", nil)
			require.ErrorAs(t, err, new(*montygo.RuntimeError), "sandbox state is not restored")
			require.NotSame(t, first, slot.Session())
			require.NoError(t, slot.Close(ctx))
			require.NoError(t, slot.Close(ctx))
			require.Equal(t, montygo.SessionClosed, slot.State())
			_, err = slot.Go(ctx, "1", nil)
			require.ErrorIs(t, err, montygo.ErrSessionClosed)
			require.True(t, errors.Is(err, montygo.ErrSessionClosed))
		})
	})
}
