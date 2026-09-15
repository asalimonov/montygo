package monty_test

import (
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

var limMemoryErrorRe = regexp.MustCompile(`^MemoryError: memory limit exceeded: (\d+) bytes > (\d+) bytes$`)

type limFigures struct{ native, wasm int64 }

func (f limFigures) at(b monty.Backend) int64 {
	if b == monty.BackendWasm {
		return f.wasm
	}
	return f.native
}

func limRuntimeError(t *testing.T, err error) *monty.RuntimeError {
	t.Helper()
	require.Error(t, err)
	var rerr *monty.RuntimeError
	require.ErrorAsf(t, err, &rerr, "expected *monty.RuntimeError, got %T: %v", err, err)
	return rerr
}

func limAssertMemoryError(t *testing.T, err error, expected int64, maxMemory uint64) {
	t.Helper()
	rerr := limRuntimeError(t, err)
	match := limMemoryErrorRe.FindStringSubmatch(rerr.Error())
	require.NotNilf(t, match, "unexpected MemoryError message: %s", rerr.Error())
	used, perr := strconv.ParseInt(match[1], 10, 64)
	require.NoError(t, perr)
	require.Equal(t, strconv.FormatUint(maxMemory, 10), match[2])
	const tolerance = 1024
	require.LessOrEqualf(t, max(used-expected, expected-used), int64(tolerance),
		"reported %d bytes, expected within %d of %d", used, tolerance, expected)
}

func limLimits(l monty.ResourceLimits) monty.CheckoutOptions {
	return monty.CheckoutOptions{Limits: &l}
}

func TestLimits(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("resource limits custom", func(t *testing.T) {
			limits := limLimits(monty.ResourceLimits{
				MaxDuration:       5 * time.Second,
				MaxMemory:         64 * 1024,
				GCInterval:        10,
				MaxRecursionDepth: 500,
				MaxSuspensions:    20,
			})
			require.Equal(t, int64(2), mustRun(t, b, "1 + 1", runOptions{CheckoutOptions: limits}))
		})

		t.Run("run with limits", func(t *testing.T) {
			limits := limLimits(monty.ResourceLimits{MaxDuration: 5 * time.Second})
			require.Equal(t, int64(2), mustRun(t, b, "1 + 1", runOptions{CheckoutOptions: limits}))
		})

		t.Run("recursion limit", func(t *testing.T) {
			code := "\ndef recurse(n):\n    if n <= 0:\n        return 0\n    return 1 + recurse(n - 1)\n\nrecurse(10)\n"
			_, err := run(t, b, code, runOptions{CheckoutOptions: limLimits(monty.ResourceLimits{MaxRecursionDepth: 5})})
			rerr := limRuntimeError(t, err)
			require.Equal(t, "RecursionError: maximum recursion depth exceeded", rerr.Error())
		})

		t.Run("recursion limit ok", func(t *testing.T) {
			code := "\ndef recurse(n):\n    if n <= 0:\n        return 0\n    return 1 + recurse(n - 1)\n\nrecurse(5)\n"
			v := mustRun(t, b, code, runOptions{CheckoutOptions: limLimits(monty.ResourceLimits{MaxRecursionDepth: 100})})
			require.Equal(t, int64(5), v)
		})

		t.Run("memory limit", func(t *testing.T) {
			code := "\nresult = []\nfor i in range(1000):\n    result.append('x' * 100)\nlen(result)\n"
			const maxMemory = 64 * 1024
			_, err := run(t, b, code, runOptions{CheckoutOptions: limLimits(monty.ResourceLimits{MaxMemory: maxMemory})})
			limAssertMemoryError(t, err, limFigures{native: 89_113, wasm: 75_047}.at(b), maxMemory)
		})

		t.Run("memory limit accepts values above u32 max", func(t *testing.T) {
			limits := limLimits(monty.ResourceLimits{MaxMemory: 1 << 33})
			require.Equal(t, int64(2), mustRun(t, b, "1 + 1", runOptions{CheckoutOptions: limits}))
		})

		t.Run("limits with inputs", func(t *testing.T) {
			v := mustRun(t, b, "x * 2", runOptions{
				CheckoutOptions: limLimits(monty.ResourceLimits{MaxDuration: 5 * time.Second}),
				FeedOptions:     monty.FeedOptions{Inputs: map[string]any{"x": 21}},
			})
			require.Equal(t, int64(42), v)
		})

		t.Run("pow memory limit", func(t *testing.T) {
			_, err := run(t, b, "2 ** 10000000", runOptions{CheckoutOptions: limLimits(monty.ResourceLimits{MaxMemory: 1_000_000})})
			limAssertMemoryError(t, err, limFigures{native: 10_031_312, wasm: 10_023_470}.at(b), 1_000_000)
		})

		t.Run("lshift memory limit", func(t *testing.T) {
			_, err := run(t, b, "1 << 10000000", runOptions{CheckoutOptions: limLimits(monty.ResourceLimits{MaxMemory: 1_000_000})})
			limAssertMemoryError(t, err, limFigures{native: 1_281_313, wasm: 1_273_471}.at(b), 1_000_000)
		})

		t.Run("mult memory limit", func(t *testing.T) {
			code := "\nbig = 2 ** 4000000\nresult = big * big\n"
			_, err := run(t, b, code, runOptions{CheckoutOptions: limLimits(monty.ResourceLimits{MaxMemory: 1_000_000})})
			limAssertMemoryError(t, err, limFigures{native: 4_031_972, wasm: 4_024_130}.at(b), 1_000_000)
		})

		t.Run("small operations within limit", func(t *testing.T) {
			v := mustRun(t, b, "2 ** 1000", runOptions{CheckoutOptions: limLimits(monty.ResourceLimits{MaxMemory: 1_000_000})})
			got, ok := v.(*big.Int)
			require.Truef(t, ok, "expected *big.Int, got %T", v)
			want := new(big.Int).Exp(big.NewInt(2), big.NewInt(1000), nil)
			require.Zerof(t, want.Cmp(got), "got %s", got)
		})

		t.Run("time limit", func(t *testing.T) {
			_, err := run(t, b, "while True:\n    pass\n", runOptions{CheckoutOptions: limLimits(monty.ResourceLimits{MaxDuration: 100 * time.Millisecond})})
			rerr := limRuntimeError(t, err)
			require.Equal(t, "TimeoutError", rerr.Exception().TypeName)
			require.Regexp(t, `^time limit exceeded: \d+(\.\d+)?ms > 100ms$`, rerr.Display(monty.DisplayMsg))
		})

		t.Run("suspension limit", func(t *testing.T) {
			code := "\nn = 0\nwhile True:\n    try:\n        fetch('x')\n    except Exception:\n        n += 1\n"
			fetch := func(string) (any, error) { return nil, errors.New("refused") }
			_, err := run(t, b, code, runOptions{
				CheckoutOptions: limLimits(monty.ResourceLimits{MaxSuspensions: 3}),
				FeedOptions:     monty.FeedOptions{ExternalLookup: map[string]any{"fetch": fetch}},
			})
			rerr := limRuntimeError(t, err)
			require.Equal(t, "RuntimeError", rerr.Exception().TypeName)
			require.Equal(t, "suspension limit 3 exceeded", rerr.Display(monty.DisplayMsg))
		})

		t.Run("suspension limit defaults to 1000", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, monty.CheckoutOptions{})
			_, err := session.FeedRun(ctx, "n = 0\nwhile True:\n    fetch()\n    n += 1", &monty.FeedOptions{
				ExternalLookup: map[string]any{"fetch": func() any { return nil }},
			})
			rerr := limRuntimeError(t, err)
			require.Equal(t, "suspension limit 1000 exceeded", rerr.Display(monty.DisplayMsg))
			v, err := session.FeedRun(ctx, "n", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1000), v)
		})

		t.Run("suspension limit leaves the session usable", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, limLimits(monty.ResourceLimits{MaxSuspensions: 1}))
			feed := &monty.FeedOptions{ExternalLookup: map[string]any{"fetch": func(string) string { return "ok" }}}
			v, err := session.FeedRun(ctx, "fetch('x')", feed)
			require.NoError(t, err)
			require.Equal(t, "ok", v)
			_, err = session.FeedRun(ctx, "fetch('y')", feed)
			rerr := limRuntimeError(t, err)
			require.Equal(t, "suspension limit 1 exceeded", rerr.Display(monty.DisplayMsg))
			v, err = session.FeedRun(ctx, "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})

		t.Run("restored session keeps its suspension limit with a fresh count", func(t *testing.T) {
			ctx := testCtx(t)
			feed := &monty.FeedOptions{ExternalLookup: map[string]any{"fetch": func(string) string { return "ok" }}}
			first := newSession(t, b, limLimits(monty.ResourceLimits{MaxSuspensions: 1}))
			v, err := first.FeedRun(ctx, "fetch('x')", feed)
			require.NoError(t, err)
			require.Equal(t, "ok", v)
			state, err := first.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			restored := newSession(t, b, monty.CheckoutOptions{})
			require.NoError(t, restored.LoadSession(ctx, state))
			v, err = restored.FeedRun(ctx, "fetch('y')", feed)
			require.NoError(t, err)
			require.Equal(t, "ok", v)
			_, err = restored.FeedRun(ctx, "fetch('z')", feed)
			rerr := limRuntimeError(t, err)
			require.Equal(t, "suspension limit 1 exceeded", rerr.Display(monty.DisplayMsg))
		})
	})
}
