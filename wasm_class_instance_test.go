package montygo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

type clsPoint struct {
	X int
	Y int
}

func (p *clsPoint) Sum() int { return p.X + p.Y }

func (p *clsPoint) SumAsync() *host.Future {
	return host.Async(func() (any, error) { return p.Sum(), nil })
}

func clsPointStatics() map[string]any {
	return map[string]any{"DIMS": 2, "describe": func() string { return "a point" }}
}

func TestWasmClassInstance(t *testing.T) {
	b := backendWasm
	sharedPool(t, b)

	t.Run("a ClassInstance round-trips over the wasm transport", func(t *testing.T) {
		ctx := testCtx(t)
		s := newSession(t, b, montygo.CheckoutOptions{})
		point := &clsPoint{X: 1, Y: 2}
		opts := &montygo.FeedOptions{Inputs: map[string]any{"p": clsInstance(t, point, host.ClassInstanceOptions{EagerAttrs: host.All(), AllowedMethods: host.All()})}}
		v, err := s.FeedRun(ctx, "[p.x, p.y, p.sum(), type(p).__name__]", opts)
		require.NoError(t, err)
		require.Equal(t, []any{int64(1), int64(2), int64(3), "clsPoint"}, v)
		v, err = s.FeedRun(ctx, "p", opts)
		require.NoError(t, err)
		require.Same(t, point, v)
		v, err = s.FeedRun(ctx, "[p, [p]]", opts)
		require.NoError(t, err)
		items, ok := v.([]any)
		require.True(t, ok, "%T", v)
		require.Len(t, items, 2)
		require.Same(t, point, items[0])
		nested, ok := items[1].([]any)
		require.True(t, ok, "%T", items[1])
		require.Len(t, nested, 1)
		require.Same(t, point, nested[0])
	})

	t.Run("async host methods use one suspension per call over wasm", func(t *testing.T) {
		ctx := testCtx(t)
		s := newSession(t, b, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxSuspensions: 2}})
		point := clsInstance(t, &clsPoint{X: 1, Y: 2}, host.ClassInstanceOptions{AllowedMethods: host.All()})
		v, err := s.FeedRun(ctx, "a = await p.sum_async()\nb = await p.sum_async()\na + b", &montygo.FeedOptions{Inputs: map[string]any{"p": point}})
		require.NoError(t, err)
		require.Equal(t, int64(6), v)
	})

	t.Run("a ClassType round-trips over the wasm transport", func(t *testing.T) {
		ctx := testCtx(t)
		s := newSession(t, b, montygo.CheckoutOptions{})
		wrapper := clsType[clsPoint](t, host.ClassTypeOptions{
			Init:               true,
			EagerAttrs:         host.All(),
			AllowedMethods:     host.All(),
			InstanceEagerAttrs: host.All(),
			Statics:            clsPointStatics(),
		})
		opts := &montygo.FeedOptions{Inputs: map[string]any{"Point": wrapper}}
		v, err := s.FeedRun(ctx, "[Point.DIMS, Point.describe()]", opts)
		require.NoError(t, err)
		require.Equal(t, []any{int64(2), "a point"}, v)
		v, err = s.FeedRun(ctx, "Point", opts)
		require.NoError(t, err)
		require.Same(t, wrapper, v)
		v, err = s.FeedRun(ctx, "Point(3, 4)", opts)
		require.NoError(t, err)
		constructed, ok := v.(*clsPoint)
		require.True(t, ok, "%T", v)
		require.Equal(t, 7, constructed.Sum())
		v, err = s.FeedRun(ctx, "type(Point(3, 4)) is Point", opts)
		require.NoError(t, err)
		require.Equal(t, true, v)
	})

	t.Run("a lazy attribute host error is raised in the sandbox over the wasm transport", func(t *testing.T) {
		ctx := testCtx(t)
		s := newSession(t, b, montygo.CheckoutOptions{})
		opts := &montygo.FeedOptions{Inputs: map[string]any{"f": clsInstance(t, &clsFlaky{}, host.ClassInstanceOptions{LazyAttrs: host.All()})}}
		v, err := s.FeedRun(ctx, clsCatchBoom, opts)
		require.NoError(t, err)
		require.Equal(t, "'boom'", v)
		_, err = s.FeedRun(ctx, "hasattr(f, 'boom')", opts)
		require.Equal(t, "KeyError: boom", clsRuntimeMessage(t, err))
		v, err = s.FeedRun(ctx, "f.value + 1", opts)
		require.NoError(t, err)
		require.Equal(t, int64(2), v)
		unencodable := "try:\n    f.sym\n    r = 'unexpected'\nexcept TypeError as e:\n    r = str(e)\nr"
		v, err = s.FeedRun(ctx, unencodable, opts)
		require.NoError(t, err)
		require.Equal(t, "Cannot convert Go chan int to Monty value", v)
	})

	t.Run("a MontyClassProxy round-trips over the wasm transport", func(t *testing.T) {
		ctx := testCtx(t)
		s := newSession(t, b, montygo.CheckoutOptions{})
		_, err := s.FeedRun(ctx, "class Foo:\n    def __init__(self):\n        self.x = 1\nfoo = Foo()", nil)
		require.NoError(t, err)
		v, err := s.FeedRun(ctx, "foo", nil)
		require.NoError(t, err)
		proxy, ok := v.(*host.ClassProxy)
		require.True(t, ok, "%T", v)
		require.Equal(t, "Foo", proxy.Name)
		require.True(t, sandbox.Equal(clsDict("x", int64(1)), proxy.Attributes), sandbox.Repr(proxy.Attributes))
		v, err = s.FeedRun(ctx, "back is foo and back.x == 1", &montygo.FeedOptions{Inputs: map[string]any{"back": proxy}})
		require.NoError(t, err)
		require.Equal(t, true, v)
	})
}
