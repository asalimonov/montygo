package montygo_test

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
)

type prtCollector struct {
	t      *testing.T
	mu     sync.Mutex
	output []string
}

func prtNewCollector(t *testing.T) *prtCollector { return &prtCollector{t: t} }

func (c *prtCollector) Print(stream sandbox.Stream, text string) error {
	assert.Equal(c.t, sandbox.Stdout, stream)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.output = append(c.output, text)
	return nil
}

func (c *prtCollector) Output() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.output...)
}

func prtErrorCallback(t *testing.T, err error) sandbox.PrintFunc {
	return func(stream sandbox.Stream, _ string) error {
		assert.Equal(t, sandbox.Stdout, stream)
		return err
	}
}

func prtLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(i) + "\n"
	}
	return out
}

func prtFeed(p sandbox.PrintTarget) runOptions {
	return runOptions{FeedOptions: montygo.FeedOptions{Print: p}}
}

func prtLineBuffered(p sandbox.PrintTarget) runOptions {
	return runOptions{
		Runtime:     mustRuntime(montygo.RuntimeOptions{PrintFlushInterval: montygo.DurationPtr(0)}),
		FeedOptions: montygo.FeedOptions{Print: p},
	}
}

func prtRequireMemoryError(t *testing.T, err error, message string) {
	t.Helper()
	var rt *monterr.RuntimeError
	require.ErrorAs(t, err, &rt)
	require.Equal(t, "MemoryError", rt.TypeName)
	require.Equal(t, message, rt.Message)
}

func TestPrint(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("basic", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, `print("hello")`, prtFeed(c))
			require.Equal(t, []string{"hello\n"}, c.Output())
		})

		t.Run("multiple", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, "print(\"hello\")\nprint(\"world\")", prtFeed(c))
			require.Equal(t, "hello\nworld\n", strings.Join(c.Output(), ""))
		})

		t.Run("batched into fewer callbacks than prints", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, "for i in range(500):\n    print(i)", prtFeed(c))
			out := c.Output()
			require.Equal(t, strings.Join(prtLines(500), ""), strings.Join(out, ""))
			require.Less(t, len(out), 100, "expected far fewer callbacks than prints, got %d", len(out))
		})

		t.Run("stderr is labelled, and keeps its place in the output", func(t *testing.T) {
			var mu sync.Mutex
			var received [][2]string
			mustRun(t, b, "import sys\nprint('a')\nprint('b', file=sys.stderr)\nprint('c')", prtFeed(sandbox.PrintFunc(func(stream sandbox.Stream, text string) error {
				mu.Lock()
				defer mu.Unlock()
				received = append(received, [2]string{string(stream), text})
				return nil
			})))
			require.Equal(t, [][2]string{{"stdout", "a\n"}, {"stderr", "b\n"}, {"stdout", "c\n"}}, received)
		})

		t.Run("a zero flush interval delivers one callback per line", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, "for i in range(20):\n    print(i)", prtLineBuffered(c))
			require.Equal(t, prtLines(20), c.Output())
		})

		t.Run("a negative or non-finite flush interval is rejected", func(t *testing.T) {
			for _, bad := range []time.Duration{-time.Nanosecond, -time.Second, time.Duration(math.MinInt64)} {
				_, err := montygo.NewRuntime(montygo.RuntimeOptions{PrintFlushInterval: montygo.DurationPtr(bad)})
				var oe *monterr.OptionError
				require.ErrorAs(t, err, &oe, "expected a named rejection for %s", bad)
				require.True(t, strings.HasPrefix(oe.Message, "invalid printFlushInterval"), "expected a named rejection for %s, got %s", bad, oe.Message)
			}
		})

		t.Run("a sub-millisecond flush interval does not become line buffering", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, "for i in range(100):\n    print(i)", runOptions{Runtime: mustRuntime(montygo.RuntimeOptions{PrintFlushInterval: montygo.DurationPtr(400 * time.Microsecond)}),
				CheckoutOptions: montygo.CheckoutOptions{},
				FeedOptions:     montygo.FeedOptions{Print: c},
			})
			out := c.Output()
			require.Equal(t, strings.Join(prtLines(100), ""), strings.Join(out, ""))
			require.Less(t, len(out), 100, "expected batching, got one callback per line (%d)", len(out))
		})

		t.Run("with values", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, `print("The answer is", 42)`, prtFeed(c))
			require.Equal(t, []string{"The answer is 42\n"}, c.Output())
		})

		t.Run("with step", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, `print(1, 2, 3, sep="-")`, prtFeed(c))
			require.Equal(t, []string{"1-2-3\n"}, c.Output())
		})

		t.Run("with end", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, `print("hello", end="!")`, prtFeed(c))
			require.Equal(t, []string{"hello!"}, c.Output())
		})

		t.Run(`a print with end="" joins the next print`, func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, "print(\"a\", end=\"\")\nprint(\"b\")", prtFeed(c))
			require.Equal(t, []string{"ab\n"}, c.Output())
		})

		t.Run("returns none", func(t *testing.T) {
			c := prtNewCollector(t)
			require.Nil(t, mustRun(t, b, `result = print("hello")`, prtFeed(c)))
		})

		t.Run("empty", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, "print()", prtFeed(c))
			require.Equal(t, []string{"\n"}, c.Output())
		})

		t.Run("with limits", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, `print("with limits")`, runOptions{
				CheckoutOptions: montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxDuration: 5 * time.Second}},
				FeedOptions:     montygo.FeedOptions{Print: c},
			})
			require.Equal(t, []string{"with limits\n"}, c.Output())
		})

		t.Run("with inputs", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, `print("Input value is", x)`, runOptions{FeedOptions: montygo.FeedOptions{Inputs: map[string]any{"x": 99}, Print: c}})
			require.Equal(t, []string{"Input value is 99\n"}, c.Output())
		})

		t.Run("print in loop", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, "\nfor i in range(3):\n\tprint(\"Count\", i)\n", prtFeed(c))
			require.Equal(t, "Count 0\nCount 1\nCount 2\n", strings.Join(c.Output(), ""))
		})

		t.Run("print mixed types", func(t *testing.T) {
			c := prtNewCollector(t)
			mustRun(t, b, `print("Value:", 3.14, True, None, [1, 2, 3])`, prtFeed(c))
			require.Equal(t, "Value: 3.14 True None [1, 2, 3]\n", strings.Join(c.Output(), ""))
		})

		t.Run("raises error", func(t *testing.T) {
			sentinel := errors.New("Custom print error")
			_, err := run(t, b, `print("This will error")`, prtFeed(prtErrorCallback(t, sentinel)))
			require.Same(t, sentinel, err)
			require.Equal(t, "Custom print error", err.Error())
		})

		t.Run("raises in function", func(t *testing.T) {
			sentinel := errors.New("Print error in function")
			_, err := run(t, b, "\ndef greet(name):\n\tprint(f\"Hello, {name}!\")\n\ngreet(\"Alice\")\n", prtFeed(prtErrorCallback(t, sentinel)))
			require.Same(t, sentinel, err)
		})

		t.Run("raises in nested function", func(t *testing.T) {
			sentinel := errors.New("Print error in nested function")
			_, err := run(t, b, "\ndef outer():\n\tdef inner():\n\t\tprint(\"Inside inner function\")\n\tinner()\n\nouter()\n", prtFeed(prtErrorCallback(t, sentinel)))
			require.Same(t, sentinel, err)
		})

		t.Run("raises in loop", func(t *testing.T) {
			sentinel := errors.New("Print error in loop")
			_, err := run(t, b, "\nfor i in range(3):\n\tprint(f\"Count: {i}\")\n", prtFeed(prtErrorCallback(t, sentinel)))
			require.Same(t, sentinel, err)
		})

		t.Run("print with external function result", func(t *testing.T) {
			c := prtNewCollector(t)
			v := mustRun(t, b, "\nprint(\"hello\")\nprint(func())\n", runOptions{FeedOptions: montygo.FeedOptions{
				Print:          c,
				ExternalLookup: map[string]any{"func": func() string { return "world" }},
			}})
			require.Nil(t, v)
			require.Equal(t, []string{"hello\n", "world\n"}, c.Output())
		})

		t.Run("CollectString accumulates", func(t *testing.T) {
			c := &sandbox.CollectString{}
			v := mustRun(t, b, `print("a"); print("b", 1); 123`, prtFeed(c))
			require.Equal(t, int64(123), v)
			require.Equal(t, "a\nb 1\n", c.Output())
		})

		t.Run("CollectStreams accumulates with labels", func(t *testing.T) {
			c := &sandbox.CollectStreams{}
			v := mustRun(t, b, `print("a"); print("b", 1); 123`, prtLineBuffered(c))
			require.Equal(t, int64(123), v)
			require.Equal(t, []sandbox.CollectedStreamEntry{{Stream: sandbox.Stdout, Text: "a\n"}, {Stream: sandbox.Stdout, Text: "b 1\n"}}, c.Output())
		})

		t.Run("CollectStreams preserves stderr stream label", func(t *testing.T) {
			c := &sandbox.CollectStreams{}
			require.NoError(t, c.Print(sandbox.Stderr, "err\n"))
			require.Equal(t, []sandbox.CollectedStreamEntry{{Stream: sandbox.Stderr, Text: "err\n"}}, c.Output())
		})

		t.Run("CollectString maxBytes first write fails", func(t *testing.T) {
			c, err := sandbox.NewCollectString(100)
			require.NoError(t, err)
			_, err = run(t, b, "print('x' * 200)", prtFeed(c))
			prtRequireMemoryError(t, err, "memory limit exceeded: 201 bytes > 100 bytes")
			require.Equal(t, "", c.Output())
		})

		t.Run("CollectStreams maxBytes first write fails with overhead", func(t *testing.T) {
			c, err := sandbox.NewCollectStreams(100)
			require.NoError(t, err)
			_, err = run(t, b, "print('x' * 200)", prtFeed(c))
			prtRequireMemoryError(t, err, "memory limit exceeded: 265 bytes > 100 bytes")
			require.Empty(t, c.Output())
		})

		t.Run("CollectString partial success keeps prior buffer", func(t *testing.T) {
			c, err := sandbox.NewCollectString(10)
			require.NoError(t, err)
			_, err = run(t, b, "print('a'); print('x' * 20)", prtLineBuffered(c))
			prtRequireMemoryError(t, err, "memory limit exceeded: 23 bytes > 10 bytes")
			require.Equal(t, "a\n", c.Output())
		})

		t.Run("CollectStreams partial success keeps prior entries", func(t *testing.T) {
			c, err := sandbox.NewCollectStreams(100)
			require.NoError(t, err)
			_, err = run(t, b, "print('a'); print('x' * 20)", prtLineBuffered(c))
			prtRequireMemoryError(t, err, "memory limit exceeded: 151 bytes > 100 bytes")
			require.Equal(t, []sandbox.CollectedStreamEntry{{Stream: sandbox.Stdout, Text: "a\n"}}, c.Output())
		})

		t.Run("CollectString charges UTF-8 multi-byte characters", func(t *testing.T) {
			c, err := sandbox.NewCollectString(1)
			require.NoError(t, err)
			prtRequireMemoryError(t, c.Print(sandbox.Stdout, "é"), "memory limit exceeded: 2 bytes > 1 bytes")
			require.Equal(t, "", c.Output())
		})

		t.Run("CollectStreams charges UTF-8 multi-byte characters", func(t *testing.T) {
			c, err := sandbox.NewCollectStreams(5)
			require.NoError(t, err)
			prtRequireMemoryError(t, c.Print(sandbox.Stdout, "😀"), "memory limit exceeded: 68 bytes > 5 bytes")
			require.Empty(t, c.Output())
		})

		t.Run("CollectString reuses across feeds", func(t *testing.T) {
			ctx := testCtx(t)
			c := &sandbox.CollectString{}
			s := newSession(t, b, montygo.CheckoutOptions{})
			_, err := s.FeedRun(ctx, `print("first")`, &montygo.FeedOptions{Print: c})
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, `print("second")`, &montygo.FeedOptions{Print: c})
			require.NoError(t, err)
			require.NoError(t, s.Close(ctx))
			require.Equal(t, "first\nsecond\n", c.Output())
		})

		t.Run("CollectString/CollectStreams reject invalid maxBytes", func(t *testing.T) {
			const msg = "maxBytes must be a finite non-negative number or null"
			for _, bad := range []int64{-2, math.MinInt64} {
				_, err := sandbox.NewCollectString(bad)
				var oe *monterr.OptionError
				require.ErrorAs(t, err, &oe)
				require.Equal(t, msg, oe.Message)
				_, err = sandbox.NewCollectStreams(bad)
				require.ErrorAs(t, err, &oe)
				require.Equal(t, msg, oe.Message)
			}
			unlimited, err := sandbox.NewCollectString(sandbox.UnlimitedPrintCollect)
			require.NoError(t, err)
			require.NoError(t, unlimited.Print(sandbox.Stdout, strings.Repeat("x", 200)))
			require.Len(t, unlimited.Output(), 200)
		})

		t.Run("print collect cap fails before feedStart returns a snapshot", func(t *testing.T) {
			ctx := testCtx(t)
			c, err := sandbox.NewCollectString(10)
			require.NoError(t, err)
			s := newSession(t, b, montygo.CheckoutOptions{})
			_, err = s.FeedStart(ctx, "print('x' * 100)\nfetch()", &montygo.FeedOptions{
				Print:          c,
				ExternalLookup: map[string]any{"fetch": func() int { return 1 }},
			})
			var rt *monterr.RuntimeError
			require.ErrorAs(t, err, &rt)
			require.Equal(t, "MemoryError", rt.TypeName)
			require.True(t, strings.HasPrefix(rt.Message, "memory limit exceeded:"), rt.Message)
			_, err = s.FeedRun(ctx, "1 + 1", nil)
			var next *monterr.RuntimeError
			require.ErrorAs(t, err, &next)
			require.Equal(t, "MemoryError", next.TypeName)
		})

		t.Run("print collect cap fails before feedRun answers a suspension", func(t *testing.T) {
			ctx := testCtx(t)
			c, err := sandbox.NewCollectString(10)
			require.NoError(t, err)
			s := newSession(t, b, montygo.CheckoutOptions{})
			_, err = s.FeedRun(ctx, "print('x' * 100)\nfetch()", &montygo.FeedOptions{
				Print:          c,
				ExternalLookup: map[string]any{"fetch": func() int { return 1 }},
			})
			var rt *monterr.RuntimeError
			require.ErrorAs(t, err, &rt)
			require.Equal(t, "MemoryError", rt.TypeName)
			_, err = s.FeedRun(ctx, "1 + 1", nil)
			var next *monterr.RuntimeError
			require.ErrorAs(t, err, &next)
		})
	})
}
