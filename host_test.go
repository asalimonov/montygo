package montygo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox/host"
)

type hostCounter struct {
	Count int
}

func (c *hostCounter) Add(n int) int { c.Count += n; return c.Count }

func (c *hostCounter) Reset() { c.Count = 0 }

// hostAdder is the surface Expose exports from hostCounter.
type hostAdder interface {
	Add(n int) int
}

const hostStubsGolden = `from typing import Any, Awaitable

def fetch(arg0: str, /) -> Awaitable[Any]: ...

def scale(arg0: float, /, *args: int) -> list[float]: ...

class hostCounter:
    def add(self, arg0: int, /) -> int: ...

counter: hostCounter
`

func TestHost(t *testing.T) {
	t.Run("Func rejects invalid names and signatures", func(t *testing.T) {
		h := host.NewHost()
		require.Error(t, h.Func("", func() {}))
		require.Error(t, h.Func("not a name", func() {}))
		require.Error(t, h.Func("2fast", func() {}))
		require.Error(t, h.Func("f", 42))
		require.Error(t, h.Func("f", func() (int, int, int) { return 0, 0, 0 }))
		require.NoError(t, h.Func("f", func() int { return 1 }))
		require.Error(t, h.Func("f", func() int { return 2 }))
		require.Error(t, h.Object("f", &hostCounter{}, host.ClassInstanceOptions{}))
		require.Equal(t, []string{"f"}, h.Names())
	})

	t.Run("Stubs renders registered functions and objects", func(t *testing.T) {
		h := host.NewHost()
		require.NoError(t, h.Func("scale", func(x float64, factors ...int) []float64 { return nil }))
		require.NoError(t, h.Func("fetch", func(ctx context.Context, url string) *host.Future { return nil }))
		require.NoError(t, h.Object("counter", &hostCounter{}, host.ClassInstanceOptions{AllowedMethods: host.Expose[hostAdder]()}))
		require.Equal(t, hostStubsGolden, h.Stubs())
	})

	t.Run("Restorable requires pinned IDs", func(t *testing.T) {
		h := host.NewHost()
		require.NoError(t, h.Restorable())
		require.NoError(t, h.Object("a", &hostCounter{}, host.ClassInstanceOptions{ID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8"}))
		require.NoError(t, h.Restorable())
		require.NoError(t, h.Object("b", &hostCounter{}, host.ClassInstanceOptions{}))
		require.ErrorIs(t, h.Restorable(), host.ErrHostObjectNotRestorable)
	})

	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("a session exposes the host's functions and objects", func(t *testing.T) {
			ctx := testCtx(t)
			h := host.NewHost()
			require.NoError(t, h.Func("double", func(n int) int { return n * 2 }))
			counter := &hostCounter{}
			require.NoError(t, h.Object("counter", counter, host.ClassInstanceOptions{AllowedMethods: host.Expose[hostAdder]()}))
			s := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{Host: h}), montygo.CheckoutOptions{})
			v, err := s.FeedRun(ctx, "counter.add(double(4))", nil)
			require.NoError(t, err)
			require.Equal(t, int64(8), v)
			require.Equal(t, 8, counter.Count)
			_, err = s.FeedRun(ctx, "counter.reset()", nil)
			var re *monterr.RuntimeError
			require.ErrorAs(t, err, &re)
			require.Equal(t, "AttributeError", re.TypeName)
		})

		t.Run("ExternalLookup entries override host names", func(t *testing.T) {
			ctx := testCtx(t)
			h := host.NewHost()
			require.NoError(t, h.Func("answer", func() int { return 1 }))
			s := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{Host: h}), montygo.CheckoutOptions{})
			v, err := s.FeedRun(ctx, "answer()", &montygo.FeedOptions{ExternalLookup: map[string]any{"answer": func() int { return 42 }}})
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
		})

		t.Run("host stubs type check sandbox code", func(t *testing.T) {
			ctx := testCtx(t)
			h := host.NewHost()
			require.NoError(t, h.Func("double", func(n int) int { return n * 2 }))
			s := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{Host: h, TypeCheck: true, TypeCheckStubs: h.Stubs()}), montygo.CheckoutOptions{})
			_, err := s.FeedRun(ctx, "double('x')", nil)
			var te *monterr.TypingError
			require.ErrorAs(t, err, &te)
		})

		t.Run("pinned objects survive a dump and restore", func(t *testing.T) {
			ctx := testCtx(t)
			h := host.NewHost()
			counter := &hostCounter{}
			require.NoError(t, h.Object("counter", counter, host.ClassInstanceOptions{
				AllowedMethods: host.Expose[hostAdder](),
				ID:             "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			}))
			require.NoError(t, h.Restorable())
			first := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{Host: h}), montygo.CheckoutOptions{})
			_, err := first.FeedRun(ctx, "c = counter\nc.add(1)", nil)
			require.NoError(t, err)
			blob, err := first.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			s := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{Host: h}), montygo.CheckoutOptions{})
			require.NoError(t, s.LoadSession(ctx, blob))
			v, err := s.FeedRun(ctx, "[c is counter, c.add(2)]", nil)
			require.NoError(t, err)
			require.Equal(t, []any{true, int64(3)}, v)
			require.Equal(t, 3, counter.Count)
		})

		t.Run("LoadSession refuses a host with unpinned objects", func(t *testing.T) {
			ctx := testCtx(t)
			h := host.NewHost()
			require.NoError(t, h.Object("counter", &hostCounter{}, host.ClassInstanceOptions{}))
			s := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{Host: h}), montygo.CheckoutOptions{})
			require.ErrorIs(t, s.LoadSession(ctx, []byte("x")), host.ErrHostObjectNotRestorable)
			v, err := s.FeedRun(ctx, "1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})
	})
}

func TestExpose(t *testing.T) {
	t.Run("Expose lists the interface's methods by sandbox name", func(t *testing.T) {
		p := host.Expose[hostAdder]()
		require.True(t, p.Allows("add"))
		require.False(t, p.Allows("reset"))
	})
	t.Run("Expose panics for a non-interface type", func(t *testing.T) {
		require.Panics(t, func() { host.Expose[hostCounter]() })
	})
}
