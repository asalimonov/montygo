package montygo_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
	"github.com/stretchr/testify/require"
)

func TestImmediateStop(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		ctx := testCtx(t)
		p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
		s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close(ctx, montygo.KillNow) })
		for i := range 1000 {
			r := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: map[string]any{
				"wait": func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			}})
			waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			result, err := r.Stop(waitCtx)
			require.NoError(t, err, "iteration %d", i)
			require.Equal(t, montygo.StopAborted, result.How, "iteration %d", i)
			require.True(t, result.SessionKept())
			_, err = r.WaitContext(waitCtx)
			cancel()
			var raised *monterr.RuntimeError
			require.ErrorAs(t, err, &raised)
			require.Equal(t, "KeyboardInterrupt", raised.TypeName)
			v, err := s.FeedRun(ctx, "1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		}
	})
}

func TestExecutionOwnership(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("busy requests do not queue or change the current run", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			started := make(chan struct{})
			r := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			_, err := s.FeedRun(ctx, "1", nil)
			require.ErrorIs(t, err, monterr.ErrSessionBusy)
			_, err = s.FeedStart(ctx, "1", nil)
			require.ErrorIs(t, err, monterr.ErrSessionBusy)
			_, err = s.Dump(ctx)
			require.ErrorIs(t, err, monterr.ErrSessionBusy)
			busy := s.Go(ctx, "1", nil)
			select {
			case <-busy.Done():
			default:
				t.Fatal("admission error did not return a completed Run")
			}
			_, err = busy.Wait()
			require.ErrorIs(t, err, monterr.ErrSessionBusy)
			result, err := busy.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopFinished, result.How)
			require.ErrorIs(t, result.Err, monterr.ErrSessionBusy)
			waitCtx, cancel := context.WithCancel(ctx)
			cancel()
			_, err = r.WaitContext(waitCtx)
			require.ErrorIs(t, err, context.Canceled)
			require.NoError(t, s.Err())
			result, err = r.Stop(ctx)
			require.NoError(t, err)
			require.Equal(t, montygo.StopAborted, result.How)
		})
		t.Run("old Run cannot stop the next execution", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			old := s.Go(ctx, "1", nil)
			_, err := old.Wait()
			require.NoError(t, err)
			started := make(chan struct{})
			r := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			result, err := old.Stop(ctx, montygo.KillNow)
			require.NoError(t, err)
			require.Equal(t, montygo.StopFinished, result.How)
			require.NoError(t, s.Err())
			_, err = r.Stop(ctx)
			require.NoError(t, err)
		})
		t.Run("aborted snapshot cannot answer a later paused call", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			snap, err := s.FeedStart(ctx, "old()", nil)
			require.NoError(t, err)
			old := snap.(*montygo.FunctionSnapshot)
			_, err = s.FeedRun(ctx, "1", nil)
			require.ErrorIs(t, err, monterr.ErrSessionBusy)
			_, err = s.Stop(ctx)
			require.NoError(t, err)
			snap, err = s.FeedStart(ctx, "new()", nil)
			require.NoError(t, err)
			_, err = old.Resume(ctx, 99)
			var raised *monterr.RuntimeError
			require.ErrorAs(t, err, &raised)
			require.Equal(t, "KeyboardInterrupt", raised.TypeName)
			require.NoError(t, s.Err())
			snap, err = snap.(*montygo.FunctionSnapshot).Resume(ctx, 42)
			require.NoError(t, err)
			require.Equal(t, int64(42), snap.(*montygo.Complete).Output)
		})
		t.Run("cancelled snapshot claim leaves token usable", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			snap, err := s.FeedStart(ctx, "f()", nil)
			require.NoError(t, err)
			call := snap.(*montygo.FunctionSnapshot)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, err = call.Resume(cancelled, 1)
			require.ErrorIs(t, err, context.Canceled)
			snap, err = call.Resume(ctx, 2)
			require.NoError(t, err)
			require.Equal(t, int64(2), snap.(*montygo.Complete).Output)
		})
	})
}

func TestStopUncooperativeCallback(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		ctx := testCtx(t)
		p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
		s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
		require.NoError(t, err)
		defer s.Close(ctx, montygo.KillNow)
		started, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		r := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: map[string]any{"wait": func() int {
			close(started)
			<-release
			return 1
		}}})
		<-started
		firstReason := errors.New("first reason wins")
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		result, err := r.Stop(short, montygo.StopPolicy{Reason: firstReason, Timeout: time.Hour})
		cancel()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, montygo.StopPending, result.How)
		require.NoError(t, s.Err())
		result, err = r.Stop(ctx, montygo.StopPolicy{Reason: errors.New("ignored reason"), Timeout: -1, Join: 50 * time.Millisecond})
		require.ErrorIs(t, err, monterr.ErrCallbackDetached)
		require.Equal(t, montygo.StopKilled, result.How)
		require.ErrorIs(t, result.SessionErr, firstReason)
		require.ErrorIs(t, result.SessionErr, monterr.ErrSessionLost)
		require.Same(t, result.SessionErr, s.Err())
		require.NoError(t, s.Close(ctx, montygo.KillNow))
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		require.NoError(t, s.Close(cancelled))
		_, err = r.WaitContext(cancelled)
		require.ErrorIs(t, err, context.Canceled)
		fresh, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
		require.NoError(t, err)
		defer fresh.Close(ctx)
		v, err := fresh.FeedRun(ctx, "42", nil)
		require.NoError(t, err)
		require.Equal(t, int64(42), v)
		// The separate release below also verifies that the Go driver eventually joins.
		unblock()
		_, err = r.WaitContext(ctx)
		require.Same(t, result.SessionErr, err)
	})
}

func TestForceDuringPrint(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		ctx := testCtx(t)
		p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
		s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
		require.NoError(t, err)
		defer s.Close(ctx, montygo.KillNow)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		r := s.Go(ctx, "print('hello')\n42", &montygo.FeedOptions{Print: sandbox.PrintFunc(func(sandbox.Stream, string) error {
			close(entered)
			<-release
			return nil
		})})
		<-entered
		result, err := r.Stop(ctx, montygo.StopPolicy{Timeout: -1, Join: 50 * time.Millisecond})
		require.ErrorIs(t, err, monterr.ErrCallbackDetached)
		require.Equal(t, montygo.StopKilled, result.How)
		fresh, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
		require.NoError(t, err)
		defer fresh.Close(ctx)
		unblock()
		_, err = r.WaitContext(ctx)
		require.Same(t, result.SessionErr, err)
	})
}

func TestTerminalPathsReleaseCapacity(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		for _, failure := range []string{"idle close", "failed host registration", "failed restore"} {
			t.Run(failure, func(t *testing.T) {
				ctx := testCtx(t)
				p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
				if failure == "failed host registration" {
					h, err := recordHost()
					require.NoError(t, err)
					require.NoError(t, h.Object("other", &recordTable{}, host.ClassInstanceOptions{}))
					_, err = p.Checkout(ctx, mustRuntime(montygo.RuntimeOptions{Host: h, MaxHostObjects: 1}), montygo.CheckoutOptions{})
					require.ErrorContains(t, err, "host object limit")
				} else {
					s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
					require.NoError(t, err)
					if failure == "failed restore" {
						err = s.LoadSession(ctx, []byte("invalid dump"))
						require.Error(t, err)
						require.Error(t, s.Err())
					} else {
						require.NoError(t, s.Close(ctx, montygo.KillNow))
					}
				}
				fresh, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
				require.NoError(t, err)
				defer fresh.Close(ctx)
				v, err := fresh.FeedRun(ctx, "42", nil)
				require.NoError(t, err)
				require.Equal(t, int64(42), v)
			})
		}
	})
}

func TestPausedCancellationBoundaries(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		for _, code := range []string{"f()", "name", "from pathlib import Path\nPath('/missing').exists()"} {
			t.Run(code, func(t *testing.T) {
				ctx := testCtx(t)
				s := newSession(t, b, montygo.CheckoutOptions{})
				step, cancel := context.WithCancel(ctx)
				snap, err := s.FeedStart(step, code, nil)
				require.NoError(t, err)
				cancel()
				result, err := s.Stop(ctx)
				require.NoError(t, err)
				require.Contains(t, []montygo.StopKind{montygo.StopAborted, montygo.StopNotRunning}, result.How)
				var resumeErr error
				switch call := snap.(type) {
				case *montygo.FunctionSnapshot:
					_, resumeErr = call.Resume(ctx, 1)
				case *montygo.NameLookupSnapshot:
					_, resumeErr = call.ResumeValue(ctx, 1)
				default:
					t.Fatalf("unexpected snapshot %T", snap)
				}
				var raised *monterr.RuntimeError
				require.ErrorAs(t, resumeErr, &raised)
				require.Equal(t, "KeyboardInterrupt", raised.TypeName)
				require.NoError(t, s.Err())
			})
		}
	})
}

type conversionProbe struct{ Value int }

func TestStopDuringConversion(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		for _, point := range []string{"preparation", "function resume", "name resume"} {
			t.Run(point, func(t *testing.T) {
				ctx := testCtx(t)
				s := newSession(t, b, montygo.CheckoutOptions{})
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				probe := host.MustClassInstance(&conversionProbe{Value: 42}, host.ClassInstanceOptions{
					EagerAttrs: host.All(), ConvertValue: func(_ string, value any) (any, error) {
						close(entered)
						<-release
						return value, nil
					},
				})
				finished := make(chan error, 1)
				if point == "preparation" {
					r := s.Go(ctx, "probe.value", &montygo.FeedOptions{Inputs: map[string]any{"probe": probe}})
					go func() { _, err := r.WaitContext(ctx); finished <- err }()
				} else {
					code := "f()"
					if point == "name resume" {
						code = "missing_name"
					}
					snap, err := s.FeedStart(ctx, code, nil)
					require.NoError(t, err)
					go func() {
						var err error
						switch call := snap.(type) {
						case *montygo.FunctionSnapshot:
							_, err = call.Resume(ctx, probe)
						case *montygo.NameLookupSnapshot:
							_, err = call.ResumeValue(ctx, probe)
						}
						finished <- err
					}()
				}
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				wait, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
				result, err := s.Stop(wait, montygo.StopPolicy{Timeout: time.Hour})
				cancel()
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Equal(t, montygo.StopPending, result.How)
				unblock()
				err = <-finished
				var raised *monterr.RuntimeError
				require.ErrorAs(t, err, &raised)
				require.Equal(t, "KeyboardInterrupt", raised.TypeName)
				require.NoError(t, s.Err())
				v, err := s.FeedRun(ctx, "42", nil)
				require.NoError(t, err)
				require.Equal(t, int64(42), v)
			})
		}
	})
}
