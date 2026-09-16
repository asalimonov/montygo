package montygo_test

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox/host"
)

func plNativeOnly(t *testing.T, b backend, reason string) {
	t.Helper()
	if b != backendNative {
		t.Skipf("%s backend: %s", b, reason)
	}
}

func plPID(t *testing.T, s *montygo.Session) int {
	t.Helper()
	pid, ok := s.WorkerPID()
	require.True(t, ok, "session has no worker pid")
	require.Positive(t, pid)
	return pid
}

func plKill(t *testing.T, pid int) {
	t.Helper()
	proc, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, proc.Kill())
}

func plCheckout(t *testing.T, p *montygo.Pool, opts montygo.CheckoutOptions) *montygo.Session {
	t.Helper()
	s, err := p.Checkout(testCtx(t), defaultRuntime, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func plFeed(t *testing.T, s *montygo.Session, code string, opts *montygo.FeedOptions) any {
	t.Helper()
	v, err := s.FeedRun(testCtx(t), code, opts)
	require.NoError(t, err)
	return v
}

func plClose(t *testing.T, s *montygo.Session) {
	t.Helper()
	require.NoError(t, s.Close(testCtx(t)))
}

// plRequireMemoryError accepts the interpreter's own MemoryError text on the
// websocket backend, because the server's memory ceiling fires before the allocator abort.
func plRequireMemoryError(t *testing.T, b backend, err error) {
	t.Helper()
	var rt *monterr.RuntimeError
	require.ErrorAs(t, err, &rt)
	require.Equal(t, "MemoryError", rt.Exception().TypeName)
	if !remoteBackend(b) {
		require.Equal(t, "MemoryError: the worker exceeded its memory limit and was terminated", rt.Error())
	}
}

func plRequireCrashed(t *testing.T, err error) *monterr.CrashedError {
	t.Helper()
	var crashed *monterr.CrashedError
	require.ErrorAs(t, err, &crashed)
	return crashed
}

func TestPool(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("checkout after close rejects", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{})
			require.NoError(t, p.Close(testCtx(t)))
			_, err := p.Checkout(testCtx(t), defaultRuntime, montygo.CheckoutOptions{})
			require.ErrorIs(t, err, monterr.ErrPoolClosed)
			require.EqualError(t, err, "the pool is closed — create a new Monty pool")
		})

		t.Run("close is idempotent", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{})
			require.NoError(t, p.Close(testCtx(t)))
			require.NoError(t, p.Close(testCtx(t)))
		})

		t.Run("feed after session close rejects", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{})
			s := plCheckout(t, p, montygo.CheckoutOptions{})
			plClose(t, s)
			_, err := s.FeedRun(testCtx(t), "1", nil)
			require.ErrorIs(t, err, monterr.ErrSessionClosed)
			require.EqualError(t, err, "the session is closed — check out a new one")
		})

		t.Run("workers are reused across checkouts", func(t *testing.T) {
			plNativeOnly(t, b, "worker identity is observed through the worker pid")
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			first := plCheckout(t, p, montygo.CheckoutOptions{})
			pid := plPID(t, first)
			plClose(t, first)
			second := plCheckout(t, p, montygo.CheckoutOptions{})
			require.Equal(t, pid, plPID(t, second))
			plClose(t, second)
		})

		t.Run("maxCheckoutsPerWorker recycles the worker", func(t *testing.T) {
			plNativeOnly(t, b, "worker identity is observed through the worker pid")
			p := newPool(t, b, montygo.PoolOptions{MaxCheckoutsPerWorker: 1})
			first := plCheckout(t, p, montygo.CheckoutOptions{})
			pid := plPID(t, first)
			plClose(t, first)
			second := plCheckout(t, p, montygo.CheckoutOptions{})
			require.NotEqual(t, pid, plPID(t, second))
			plClose(t, second)
		})

		t.Run("maxMemory leaves normal work alone", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{})
			s := plCheckout(t, p, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxMemory: 1024 * 1024}})
			require.Equal(t, int64(2), plFeed(t, s, "1 + 1", nil))
			plClose(t, s)
		})

		t.Run("a refused allocation raises MemoryError and the pool recovers", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{})
			s := plCheckout(t, p, montygo.CheckoutOptions{})
			code := "x = ' ' * (1 << 60)"
			if b == backendWasm {
				code = "x = ' ' * ((1 << 31) - 1)"
			}
			_, err := s.FeedRun(testCtx(t), code, nil)
			plRequireMemoryError(t, b, err)
			next := plCheckout(t, p, montygo.CheckoutOptions{})
			require.Equal(t, int64(2), plFeed(t, next, "1 + 1", nil))
			plClose(t, next)
		})

		t.Run("exceeding maxMemory in the allocator raises MemoryError and the pool recovers", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{})
			s := plCheckout(t, p, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxMemory: 1024}})
			_, err := s.FeedRun(testCtx(t), "# "+strings.Repeat("a", 16*1024*1024), nil)
			plRequireMemoryError(t, b, err)
			next := plCheckout(t, p, montygo.CheckoutOptions{})
			require.Equal(t, int64(2), plFeed(t, next, "1 + 1", nil))
			plClose(t, next)
		})

		t.Run("concurrent sessions run in distinct workers", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 2})
			a := plCheckout(t, p, montygo.CheckoutOptions{})
			c := plCheckout(t, p, montygo.CheckoutOptions{})
			if b == backendNative {
				require.NotEqual(t, plPID(t, a), plPID(t, c))
			}
			var wg sync.WaitGroup
			var ra, rc any
			var ea, ec error
			wg.Add(2)
			go func() { defer wg.Done(); ra, ea = a.FeedRun(testCtx(t), "1 + 1", nil) }()
			go func() { defer wg.Done(); rc, ec = c.FeedRun(testCtx(t), "2 + 2", nil) }()
			wg.Wait()
			require.NoError(t, ea)
			require.NoError(t, ec)
			require.Equal(t, int64(2), ra)
			require.Equal(t, int64(4), rc)
			plClose(t, a)
			plClose(t, c)
		})

		t.Run("exhausted pool times out the checkout", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1, CheckoutTimeout: 200 * time.Millisecond})
			held := plCheckout(t, p, montygo.CheckoutOptions{})
			_, err := p.Checkout(testCtx(t), defaultRuntime, montygo.CheckoutOptions{})
			require.ErrorIs(t, err, monterr.ErrCheckoutTimeout)
			require.EqualError(t, err, "no monty worker became available within the checkout timeout")
			plClose(t, held)
		})

		t.Run("released worker is handed to a waiting checkout", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			held := plCheckout(t, p, montygo.CheckoutOptions{})
			type result struct {
				s   *montygo.Session
				err error
			}
			waiting := make(chan result, 1)
			go func() {
				s, err := p.Checkout(testCtx(t), defaultRuntime, montygo.CheckoutOptions{})
				waiting <- result{s, err}
			}()
			time.Sleep(50 * time.Millisecond)
			plClose(t, held)
			r := <-waiting
			require.NoError(t, r.err)
			t.Cleanup(func() { _ = r.s.Close(context.Background()) })
			require.Equal(t, int64(42), plFeed(t, r.s, "40 + 2", nil))
			plClose(t, r.s)
		})

		t.Run("killed worker surfaces as MontyCrashedError", func(t *testing.T) {
			plNativeOnly(t, b, "wasm workers have no pid to signal")
			p := newPool(t, b, montygo.PoolOptions{})
			s := plCheckout(t, p, montygo.CheckoutOptions{})
			plKill(t, plPID(t, s))
			_, err := s.FeedRun(testCtx(t), "1 + 1", nil)
			crashed := plRequireCrashed(t, err)
			require.False(t, crashed.TimedOut)
			require.Equal(t, "signal: 9 (SIGKILL)", crashed.ExitStatus)
		})

		t.Run("session is unusable after a crash but the pool recovers", func(t *testing.T) {
			opts := montygo.PoolOptions{}
			if b != backendNative {
				opts.RequestTimeout = 500 * time.Millisecond
			}
			p := newPool(t, b, opts)
			s := plCheckout(t, p, montygo.CheckoutOptions{})
			if b == backendNative {
				plKill(t, plPID(t, s))
				_, err := s.FeedRun(testCtx(t), "1", nil)
				plRequireCrashed(t, err)
			} else {
				_, err := s.FeedRun(testCtx(t), "while True:\n    pass", nil)
				require.True(t, plRequireCrashed(t, err).TimedOut)
			}
			start := time.Now()
			_, err := s.FeedRun(testCtx(t), "1", nil)
			plRequireCrashed(t, err)
			require.Less(t, time.Since(start), 100*time.Millisecond)
			plClose(t, s)
			fresh := plCheckout(t, p, montygo.CheckoutOptions{})
			require.Equal(t, int64(2), plFeed(t, fresh, "1 + 1", nil))
			plClose(t, fresh)
		})

		t.Run("worker crashing while idle is replaced transparently", func(t *testing.T) {
			plNativeOnly(t, b, "wasm workers have no pid to signal")
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1})
			first := plCheckout(t, p, montygo.CheckoutOptions{})
			pid := plPID(t, first)
			plClose(t, first)
			plKill(t, pid)
			time.Sleep(100 * time.Millisecond)
			second := plCheckout(t, p, montygo.CheckoutOptions{})
			require.NotEqual(t, pid, plPID(t, second))
			require.Equal(t, int64(2), plFeed(t, second, "1 + 1", nil))
			plClose(t, second)
		})

		t.Run("requestTimeout kills a wedged worker", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{RequestTimeout: 500 * time.Millisecond})
			s := plCheckout(t, p, montygo.CheckoutOptions{})
			_, err := s.FeedRun(testCtx(t), "while True:\n    pass", nil)
			crashed := plRequireCrashed(t, err)
			require.True(t, crashed.TimedOut)
			require.EqualError(t, err, "RuntimeError: monty worker killed after exceeding request timeout of 500ms")
			plClose(t, s)
		})

		t.Run("suspension time does not consume the duration budget", func(t *testing.T) {
			p := newPool(t, b, montygo.PoolOptions{})
			s := plCheckout(t, p, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxDuration: 300 * time.Millisecond}})
			v := plFeed(t, s, "await fetch_data('u') + '!'", &montygo.FeedOptions{ExternalLookup: map[string]any{
				"fetch_data": func(string) *host.Future {
					return host.Async(func() (any, error) {
						time.Sleep(600 * time.Millisecond)
						return "body", nil
					})
				},
			}})
			require.Equal(t, "body!", v)
		})

		t.Run("worker environment is empty", func(t *testing.T) {
			plNativeOnly(t, b, "wasm workers are not processes")
			require.NotEmpty(t, os.Getenv("PATH"), "test process should have PATH set")
			p := newPool(t, b, montygo.PoolOptions{})
			s := plCheckout(t, p, montygo.CheckoutOptions{})
			pid := plPID(t, s)
			switch runtime.GOOS {
			case "linux":
				environ, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
				require.NoError(t, err)
				require.Empty(t, environ, "worker environment should be empty, got: %s", strings.ReplaceAll(string(environ), "\x00", " "))
			case "darwin":
				out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
				require.NoError(t, err)
				require.Equal(t, p.BinaryPath()+" subprocess", strings.TrimSpace(string(out)), "worker environment should be empty")
			default:
				t.Skipf("observing a child environment is not supported on %s", runtime.GOOS)
			}
			require.Equal(t, int64(2), plFeed(t, s, "1 + 1", nil))
			plClose(t, s)
		})
	})
}
