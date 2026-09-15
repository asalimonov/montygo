package monty_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

type smokePerson struct {
	Name string
	Age  int
}

func (p *smokePerson) Greeting() string { return "hi " + p.Name }

func TestSmoke(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		ctx := testCtx(t)
		t.Run("expression", func(t *testing.T) {
			require.Equal(t, int64(3), mustRun(t, b, "1 + 2", runOptions{}))
		})
		t.Run("state persists", func(t *testing.T) {
			s := newSession(t, b, monty.CheckoutOptions{})
			_, err := s.FeedRun(ctx, "x = 21", nil)
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "x * 2", nil)
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
		})
		t.Run("inputs and containers", func(t *testing.T) {
			v := mustRun(t, b, "{'a': items[0] + len(cfg), 't': (1, 2), 's': {3}}", runOptions{FeedOptions: monty.FeedOptions{
				Inputs: map[string]any{"items": []int{10, 20}, "cfg": map[string]any{"k": "v"}},
			}})
			d := v.(*monty.Dict)
			a, _ := d.Get("a")
			require.Equal(t, int64(11), a)
			tup, _ := d.Get("t")
			require.Equal(t, monty.Tuple{int64(1), int64(2)}, tup)
			require.Equal(t, "{'a': 11, 't': (1, 2), 's': {3}}", monty.Repr(v))
		})
		t.Run("external function", func(t *testing.T) {
			v := mustRun(t, b, "add(2, 3) + scale(4, factor=10)", runOptions{FeedOptions: monty.FeedOptions{ExternalLookup: map[string]any{
				"add":   func(a, b int) int { return a + b },
				"scale": func(x int, kw monty.Kwargs) int { return x * int(kw["factor"].(int64)) },
			}}})
			require.Equal(t, int64(45), v)
		})
		t.Run("async futures", func(t *testing.T) {
			v := mustRun(t, b, "import asyncio\nawait asyncio.gather(fetch(1), fetch(2))", runOptions{FeedOptions: monty.FeedOptions{ExternalLookup: map[string]any{
				"fetch": func(n int) *monty.Future {
					return monty.Async(func() (any, error) { time.Sleep(10 * time.Millisecond); return n * 10, nil })
				},
			}}})
			require.Equal(t, []any{int64(10), int64(20)}, v)
		})
		t.Run("runtime error", func(t *testing.T) {
			_, err := run(t, b, "1 / 0", runOptions{})
			var rt *monty.RuntimeError
			require.ErrorAs(t, err, &rt)
			require.Equal(t, "ZeroDivisionError", rt.TypeName)
			require.Contains(t, rt.Display(monty.DisplayTraceback), "Traceback (most recent call last):")
		})
		t.Run("host error type", func(t *testing.T) {
			v := mustRun(t, b, "try:\n    boom()\nexcept ValueError as e:\n    r = str(e)\nr", runOptions{FeedOptions: monty.FeedOptions{ExternalLookup: map[string]any{
				"boom": func() error { return monty.Raise("ValueError", "bad") },
			}}})
			require.Equal(t, "bad", v)
		})
		t.Run("syntax error", func(t *testing.T) {
			_, err := run(t, b, "def", runOptions{})
			var se *monty.SyntaxError
			require.ErrorAs(t, err, &se)
		})
		t.Run("type check", func(t *testing.T) {
			_, err := run(t, b, "x: int = 'a'", runOptions{CheckoutOptions: monty.CheckoutOptions{TypeCheck: true}})
			var te *monty.TypingError
			require.ErrorAs(t, err, &te)
			require.NotEmpty(t, te.Diagnostics)
		})
		t.Run("print collector", func(t *testing.T) {
			c, err := monty.NewCollectStreams(monty.DefaultMaxPrintCollectBytes)
			require.NoError(t, err)
			mustRun(t, b, "import sys\nprint('hello')\nprint('err', file=sys.stderr)", runOptions{FeedOptions: monty.FeedOptions{Print: c}})
			out := c.Output()
			joined := map[monty.Stream]string{}
			for _, e := range out {
				joined[e.Stream] += e.Text
			}
			require.Equal(t, "hello\n", joined[monty.Stdout])
			require.Equal(t, "err\n", joined[monty.Stderr])
		})
		t.Run("print callback error", func(t *testing.T) {
			sentinel := errors.New("stop")
			_, err := run(t, b, "print('x')", runOptions{FeedOptions: monty.FeedOptions{Print: monty.PrintFunc(func(monty.Stream, string) error { return sentinel })}})
			require.ErrorIs(t, err, sentinel)
		})
		t.Run("class instance", func(t *testing.T) {
			p := &smokePerson{Name: "Samuel", Age: 4}
			ci, err := monty.NewClassInstance(p, monty.ClassInstanceOptions{EagerAttrs: monty.All, AllowedMethods: monty.Names("greeting")})
			require.NoError(t, err)
			v := mustRun(t, b, "assert user.name == 'Samuel'\nassert user.greeting() == 'hi Samuel'\nuser", runOptions{FeedOptions: monty.FeedOptions{Inputs: map[string]any{"user": ci}}})
			require.Same(t, p, v)
		})
		t.Run("class type init", func(t *testing.T) {
			ct, err := monty.NewClassType[smokePerson](monty.ClassTypeOptions{Init: true, InstanceEagerAttrs: monty.All, InstanceAllowedMethods: monty.All})
			require.NoError(t, err)
			v := mustRun(t, b, "p = Person('Samuel', 4)\np.greeting()", runOptions{FeedOptions: monty.FeedOptions{Inputs: map[string]any{"Person": ct}}})
			require.Equal(t, "hi Samuel", v)
		})
		t.Run("sandbox class proxy", func(t *testing.T) {
			s := newSession(t, b, monty.CheckoutOptions{})
			v, err := s.FeedRun(ctx, "from dataclasses import dataclass\n@dataclass\nclass P:\n    x: int\np = P(3)\np", nil)
			require.NoError(t, err)
			proxy := v.(*monty.ClassProxy)
			require.Equal(t, "P", proxy.Name)
			require.True(t, proxy.IsDataclass)
			back, err := s.FeedRun(ctx, "back is p", &monty.FeedOptions{Inputs: map[string]any{"back": proxy}})
			require.NoError(t, err)
			require.Equal(t, true, back)
		})
		t.Run("feed start and dump", func(t *testing.T) {
			s := newSession(t, b, monty.CheckoutOptions{})
			snap, err := s.FeedStart(ctx, "greet(name) + '!'", &monty.FeedOptions{Inputs: map[string]any{"name": "Ada"}})
			require.NoError(t, err)
			fs := snap.(*monty.FunctionSnapshot)
			require.Equal(t, "greet", fs.FunctionName)
			require.Equal(t, []any{"Ada"}, fs.Args)
			state, err := fs.Dump(ctx)
			require.NoError(t, err)
			fresh := newSession(t, b, monty.CheckoutOptions{})
			restored, err := fresh.LoadSnapshot(ctx, state, nil)
			require.NoError(t, err)
			done, err := restored.(*monty.FunctionSnapshot).Resume(ctx, "hello Ada")
			require.NoError(t, err)
			require.Equal(t, "hello Ada!", done.(*monty.Complete).Output)
		})
		t.Run("name lookup value and os", func(t *testing.T) {
			v := mustRun(t, b, "import os\ngreeting + os.getenv('HOME')", runOptions{FeedOptions: monty.FeedOptions{
				ExternalLookup: map[string]any{"greeting": "hello "},
				OS: func(_ context.Context, name string, args []any, _ monty.Kwargs) (any, error) {
					if name == "os.getenv" && args[0] == "HOME" {
						return "/home/user", nil
					}
					return monty.NotHandled, nil
				},
			}})
			require.Equal(t, "hello /home/user", v)
		})
		t.Run("recursion limit", func(t *testing.T) {
			_, err := run(t, b, "def f(n):\n    return f(n + 1)\nf(0)", runOptions{CheckoutOptions: monty.CheckoutOptions{Limits: &monty.ResourceLimits{MaxRecursionDepth: 5}}})
			var rt *monty.RuntimeError
			require.ErrorAs(t, err, &rt)
			require.Equal(t, "RecursionError", rt.TypeName)
		})
	})
}
