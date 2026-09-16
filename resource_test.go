package montygo_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

func TestResourceBounds(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("Unlimited disables the wire limits", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{
				MaxDuration:    montygo.UnlimitedDuration,
				MaxMemory:      montygo.Unlimited,
				MaxSuspensions: montygo.Unlimited,
			}})
			v, err := s.FeedRun(ctx, "sum(range(1000))", nil)
			require.NoError(t, err)
			require.Equal(t, int64(499500), v)
		})

		t.Run("MaxRecursionDepth cannot be unlimited", func(t *testing.T) {
			ctx := testCtx(t)
			_, err := sharedPool(t, b).Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxRecursionDepth: montygo.Unlimited}})
			var oe *monterr.OptionError
			require.ErrorAs(t, err, &oe)
			require.Contains(t, err.Error(), "maxRecursionDepth cannot be unlimited")
		})

		t.Run("MaxHostObjects bounds retained host objects", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{MaxHostObjects: 2}), montygo.CheckoutOptions{})
			inputs := map[string]any{}
			for i := 0; i < 3; i++ {
				inputs[fmt.Sprintf("o%d", i)] = clsInstance(t, &clsGreeter{Greeting: "hi"}, host.ClassInstanceOptions{EagerAttrs: host.All()})
			}
			_, err := s.FeedRun(ctx, "1", &montygo.FeedOptions{Inputs: inputs})
			var re *monterr.ResourceError
			require.ErrorAs(t, err, &re, "%v", err)
			require.Equal(t, "host object", re.Resource)
			require.Equal(t, uint64(2), re.Limit)
			require.NoError(t, s.Err())
		})

		t.Run("MaxPendingFutures bounds unresolved futures", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{MaxPendingFutures: 1}), montygo.CheckoutOptions{})
			never, _ := host.NewFuture()
			settled, settle := host.NewFuture()
			settle(1, nil)
			lookup := map[string]any{
				"never": func() *host.Future { return never },
				"ready": func() *host.Future { return settled },
			}
			_, err := s.FeedRun(ctx, "import asyncio\nawait asyncio.gather(never(), never())", &montygo.FeedOptions{ExternalLookup: lookup})
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re, "%v", err)
			require.Contains(t, re.Message, "pending future limit 1 exceeded")
			require.NoError(t, s.Err())
			v, err := s.FeedRun(ctx, "await ready()", &montygo.FeedOptions{ExternalLookup: lookup})
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})

		t.Run("ResourceError maps to a RuntimeError inside the sandbox", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			lookup := map[string]any{"fail": func() error { return &monterr.ResourceError{Resource: "widget", Limit: 3} }}
			v, err := s.FeedRun(ctx, "try:\n    fail()\nexcept RuntimeError as e:\n    r = str(e)\nr", &montygo.FeedOptions{ExternalLookup: lookup})
			require.NoError(t, err)
			require.Equal(t, "widget limit 3 exceeded", v)
		})
	})
}

func TestPendingBytes(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		if remoteBackend(b) {
			t.Skip("the byte bound applies to local workers only")
		}
		t.Run("a small bound throttles a flood of print frames", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1, MaxPendingBytes: 16 << 10})
			s, err := p.Checkout(ctx, mustRuntime(montygo.RuntimeOptions{PrintFlushInterval: montygo.DurationPtr(0)}), montygo.CheckoutOptions{})
			require.NoError(t, err)
			defer s.Close(ctx)
			var total int
			pt := sandbox.PrintFunc(func(_ sandbox.Stream, text string) error {
				total += len(text)
				time.Sleep(50 * time.Microsecond)
				return nil
			})
			line := strings.Repeat("x", 1000)
			_, err = s.FeedRun(ctx, "for _ in range(300):\n    print(line)", &montygo.FeedOptions{Print: pt, Inputs: map[string]any{"line": line}})
			require.NoError(t, err)
			require.Equal(t, 300*1001, total)
		})

		t.Run("UnlimitedPendingBytes disables the bound", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 1, MaxPendingBytes: montygo.UnlimitedPendingBytes})
			s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			defer s.Close(ctx)
			v, err := s.FeedRun(ctx, "'ok'", nil)
			require.NoError(t, err)
			require.Equal(t, "ok", v)
		})
	})
}

func TestPoolLifecycle(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("Stats follows workers through their states", func(t *testing.T) {
			ctx := testCtx(t)
			p := newPool(t, b, montygo.PoolOptions{MaxWorkers: 2, MinWorkers: -1})
			require.Equal(t, montygo.PoolStats{}, p.Stats())
			s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			require.Equal(t, 1, p.Stats().Active)
			require.NoError(t, s.Close(ctx))
			require.Eventually(t, func() bool {
				st := p.Stats()
				return st.Active == 0 && st.Retiring == 0 && st.Starting == 0
			}, 10*time.Second, 10*time.Millisecond)
		})

		t.Run("Shutdown closes open sessions and waits for workers", func(t *testing.T) {
			ctx := testCtx(t)
			p, err := openPool(ctx, b, montygo.PoolOptions{MaxWorkers: 2})
			require.NoError(t, err)
			s, err := p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
			require.NoError(t, err)
			started := make(chan struct{})
			run := s.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: blockingLookup(started)})
			<-started
			shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			require.NoError(t, p.Shutdown(shutdownCtx))
			_, err = run.Wait()
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re, "%v", err)
			require.Equal(t, "KeyboardInterrupt", re.TypeName)
			<-s.Done()
			require.ErrorIs(t, s.Err(), monterr.ErrSessionClosed)
			require.Equal(t, montygo.PoolStats{}, p.Stats())
			_, err = p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
			require.ErrorIs(t, err, monterr.ErrPoolClosed)
		})
	})
}

func TestLines(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("Lines delivers whole lines and flushes the tail", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			var got []string
			pt := sandbox.Lines(func(stream sandbox.Stream, line string) error {
				got = append(got, fmt.Sprintf("%s:%s", stream, line))
				return nil
			})
			_, err := s.FeedRun(ctx, "import sys\nprint('a\\nb')\nprint('c', end='')\nprint('err', file=sys.stderr)\nprint('d', end='')", &montygo.FeedOptions{Print: pt})
			require.NoError(t, err)
			require.Equal(t, []string{"stdout:a", "stdout:b", "stderr:err", "stdout:cd"}, got)
		})
	})
}
