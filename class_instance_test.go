package monty_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/internal/value"
)

type clsGreeter struct {
	Greeting string
	Hidden   string `monty:"_hidden"`
}

func (g *clsGreeter) Greet(name string) string { return g.Greeting + " " + name }

type clsCalculator struct {
	Value      int
	Memo       string `monty:"-"`
	lastKwargs monty.Kwargs
}

func (c *clsCalculator) Add(n int) int { return c.Value + n }

func (c *clsCalculator) Scale(kw monty.Kwargs) int {
	factor := int64(2)
	if f, ok := kw["factor"].(int64); ok {
		factor = f
	}
	return c.Value * int(factor)
}

func (c *clsCalculator) Combine(a, b int, kw monty.Kwargs) string {
	c.lastKwargs = kw
	sep := ","
	if s, ok := kw["sep"].(string); ok {
		sep = s
	}
	return fmt.Sprintf("%d%s%d", a, sep, b)
}

func (c *clsCalculator) Boom() error { return monty.Raise("ValueError", "nope") }

func (c *clsCalculator) Fetch() *monty.Future {
	return monty.Async(func() (any, error) {
		time.Sleep(5 * time.Millisecond)
		return c.Value * 10, nil
	})
}

func (c *clsCalculator) reset() { c.Value = 0 }

var _ = (*clsCalculator).reset

type clsFlaky struct{}

func (f *clsFlaky) GetAttr(name string) (any, error) {
	switch name {
	case "value":
		return 1, nil
	case "boom":
		return nil, monty.Raise("KeyError", "boom")
	case "raw":
		return &clsGreeter{Greeting: "hi"}, nil
	case "sym":
		return make(chan int), nil
	}
	return nil, monty.ErrAttrNotExposed
}

type clsWallet struct{ Balance int }

func (w *clsWallet) Pay(amount int) *clsWallet { return &clsWallet{Balance: w.Balance - amount} }

type clsShape struct{ Size int }

func (s *clsShape) Area() int { return s.Size * s.Size }

func clsShapeStatics() map[string]any {
	return map[string]any{"SIDES": 4, "KIND": "polygon", "double": func(n int) int { return n * 2 }}
}

type clsConfig struct{ options monty.Kwargs }

func (c *clsConfig) Read() any { return c.options["mode"] }

type clsOther struct{ V int }

type clsFactory struct{}

type clsShadowing struct {
	clsOther
	N int
}

type clsTool func(x int) int

func (f clsTool) Double(x int) int { return x * 2 }

type clsBag struct{ Run func() string }

func (b *clsBag) Helper() string { return "helped" }

type clsBagInner struct{}

const clsProbe = "def probe(f):\n    try:\n        f()\n    except AttributeError as e:\n        return str(e)\n    return None\n"

const clsCatchBoom = "try:\n    f.boom\n    r = 'unexpected'\nexcept KeyError as e:\n    r = str(e)\nr"

func clsWrapDerivedWallet(_ string, v any) (any, error) {
	if w, ok := v.(*clsWallet); ok {
		return monty.NewClassInstance(w, monty.ClassInstanceOptions{EagerAttrs: monty.All, ConvertValue: clsWrapDerivedWallet})
	}
	return v, nil
}

func clsUpper(_ string, v any) (any, error) {
	if s, ok := v.(string); ok {
		return strings.ToUpper(s), nil
	}
	return v, nil
}

func clsInstance(t testing.TB, obj any, opts monty.ClassInstanceOptions) *monty.ClassInstance {
	t.Helper()
	ci, err := monty.NewClassInstance(obj, opts)
	require.NoError(t, err)
	return ci
}

func clsType[T any](t testing.TB, opts monty.ClassTypeOptions) *monty.ClassType {
	t.Helper()
	ct, err := monty.NewClassType[T](opts)
	require.NoError(t, err)
	return ct
}

func clsRun(t testing.TB, b monty.Backend, code string, inputs map[string]any) (any, error) {
	t.Helper()
	return run(t, b, code, runOptions{FeedOptions: monty.FeedOptions{Inputs: inputs}})
}

func clsMustRun(t testing.TB, b monty.Backend, code string, inputs map[string]any) any {
	t.Helper()
	v, err := clsRun(t, b, code, inputs)
	require.NoError(t, err)
	return v
}

func clsRuntimeMessage(t testing.TB, err error) string {
	t.Helper()
	var rt *monty.RuntimeError
	require.ErrorAs(t, err, &rt)
	return rt.Error()
}

func clsConversionMessage(t testing.TB, err error) string {
	t.Helper()
	var ce *monty.ConversionError
	require.ErrorAs(t, err, &ce)
	return ce.Message
}

func clsDict(pairs ...any) *monty.Dict {
	d := monty.NewDict()
	for i := 0; i < len(pairs); i += 2 {
		d.Set(pairs[i], pairs[i+1])
	}
	return d
}

func TestClassInstance(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("eagerAttrs all sends own non-underscore props", func(t *testing.T) {
			g := &clsGreeter{Greeting: "hello", Hidden: "secret"}
			x := clsInstance(t, g, monty.ClassInstanceOptions{EagerAttrs: monty.All})
			require.Equal(t, "hello", clsMustRun(t, b, "x.greeting", map[string]any{"x": x}))
			require.Equal(t, false, clsMustRun(t, b, "hasattr(x, '_hidden')", map[string]any{"x": x}))
		})

		t.Run("eagerAttrs explicit list sends exactly those props", func(t *testing.T) {
			c := &clsCalculator{Value: 5}
			x := clsInstance(t, c, monty.ClassInstanceOptions{EagerAttrs: monty.Names("value")})
			require.Equal(t, int64(6), clsMustRun(t, b, "x.value + 1", map[string]any{"x": x}))
		})

		t.Run("an attr outside the eager list raises AttributeError", func(t *testing.T) {
			g := &clsGreeter{Greeting: "hello"}
			x := clsInstance(t, g, monty.ClassInstanceOptions{EagerAttrs: monty.Names()})
			_, err := clsRun(t, b, "x.greeting", map[string]any{"x": x})
			require.Equal(t, "AttributeError: 'clsGreeter' object has no attribute 'greeting'", clsRuntimeMessage(t, err))
		})

		t.Run("returning a host-sent instance gives the original object back", func(t *testing.T) {
			g := &clsGreeter{Greeting: "hello"}
			x := clsInstance(t, g, monty.ClassInstanceOptions{EagerAttrs: monty.All})
			require.Same(t, g, clsMustRun(t, b, "x", map[string]any{"x": x}))
		})

		t.Run("the same instance round-trips across feeds in one session", func(t *testing.T) {
			ctx := testCtx(t)
			g := &clsGreeter{Greeting: "hello"}
			s := newSession(t, b, monty.CheckoutOptions{})
			v, err := s.FeedRun(ctx, "x", &monty.FeedOptions{Inputs: map[string]any{"x": clsInstance(t, g, monty.ClassInstanceOptions{EagerAttrs: monty.All})}})
			require.NoError(t, err)
			require.Same(t, g, v)
			v, err = s.FeedRun(ctx, "y", &monty.FeedOptions{Inputs: map[string]any{"y": clsInstance(t, g, monty.ClassInstanceOptions{EagerAttrs: monty.All})}})
			require.NoError(t, err)
			require.Same(t, g, v)
		})

		t.Run("sync method call with args", func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 5}, monty.ClassInstanceOptions{AllowedMethods: monty.Names("add")})
			require.Equal(t, int64(15), clsMustRun(t, b, "c.add(10)", map[string]any{"c": c}))
		})

		t.Run(`method call allowed by "all"`, func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 5}, monty.ClassInstanceOptions{AllowedMethods: monty.All})
			require.Equal(t, int64(15), clsMustRun(t, b, "c.add(10)", map[string]any{"c": c}))
		})

		t.Run(`instance callMethod rejects __call__ even under allowedMethods "all"`, func(t *testing.T) {
			invoked := false
			tool := clsTool(func(x int) int {
				invoked = true
				return x
			})
			x := clsInstance(t, tool, monty.ClassInstanceOptions{AllowedMethods: monty.All})
			_, err := clsRun(t, b, "x(21)", map[string]any{"x": x})
			require.Equal(t, "TypeError: 'clsTool' object is not callable", clsRuntimeMessage(t, err))
			require.False(t, invoked)
		})

		t.Run("kwargs are delivered as a trailing options bag", func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 5}, monty.ClassInstanceOptions{AllowedMethods: monty.All})
			require.Equal(t, int64(15), clsMustRun(t, b, "c.scale(factor=3)", map[string]any{"c": c}))
		})

		t.Run("method call with args and kwargs", func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 5}, monty.ClassInstanceOptions{AllowedMethods: monty.All})
			require.Equal(t, "1-2", clsMustRun(t, b, "c.combine(1, 2, sep='-')", map[string]any{"c": c}))
		})

		t.Run("promise-returning method resolves via the future machinery", func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 4}, monty.ClassInstanceOptions{AllowedMethods: monty.All})
			require.Equal(t, int64(40), clsMustRun(t, b, "await c.fetch()", map[string]any{"c": c}))
		})

		t.Run("denied method raises AttributeError", func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 5}, monty.ClassInstanceOptions{AllowedMethods: monty.Names("scale")})
			_, err := clsRun(t, b, "c.add(1)", map[string]any{"c": c})
			require.Equal(t, "AttributeError: 'clsCalculator' object has no attribute 'add'", clsRuntimeMessage(t, err))
		})

		t.Run("no allowedMethods policy denies every method", func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 5}, monty.ClassInstanceOptions{})
			_, err := clsRun(t, b, "c.add(1)", map[string]any{"c": c})
			require.Equal(t, "AttributeError: 'clsCalculator' object has no attribute 'add'", clsRuntimeMessage(t, err))
		})

		t.Run("a throwing method surfaces its error in the sandbox", func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 5}, monty.ClassInstanceOptions{AllowedMethods: monty.All})
			code := "try:\n    c.boom()\n    r = 'unexpected'\nexcept ValueError as e:\n    r = str(e)\nr"
			require.Equal(t, "nope", clsMustRun(t, b, code, map[string]any{"c": c}))
		})

		t.Run(`underscore methods are never dispatched, even with "all"`, func(t *testing.T) {
			c := clsInstance(t, &clsCalculator{Value: 5}, monty.ClassInstanceOptions{AllowedMethods: monty.All})
			_, err := clsRun(t, b, "c._secret()", map[string]any{"c": c})
			require.Equal(t, "AttributeError: 'clsCalculator' object has no attribute '_secret'", clsRuntimeMessage(t, err))
		})

		t.Run("lazy attr allowed by an explicit set", func(t *testing.T) {
			g := clsInstance(t, &clsGreeter{Greeting: "hello"}, monty.ClassInstanceOptions{LazyAttrs: monty.Names("greeting")})
			require.Equal(t, "hello", clsMustRun(t, b, "g.greeting", map[string]any{"g": g}))
		})

		t.Run(`lazy attr allowed by "all"`, func(t *testing.T) {
			g := clsInstance(t, &clsGreeter{Greeting: "hello"}, monty.ClassInstanceOptions{LazyAttrs: monty.All})
			require.Equal(t, "hello", clsMustRun(t, b, "g.greeting", map[string]any{"g": g}))
		})

		t.Run("lazy attr outside the policy raises AttributeError", func(t *testing.T) {
			g := clsInstance(t, &clsGreeter{Greeting: "hello"}, monty.ClassInstanceOptions{LazyAttrs: monty.Names("other")})
			_, err := clsRun(t, b, "g.greeting", map[string]any{"g": g})
			require.Equal(t, "AttributeError: 'clsGreeter' object has no attribute 'greeting'", clsRuntimeMessage(t, err))
		})

		t.Run("getattr/hasattr consult lazy attrs like g.attr", func(t *testing.T) {
			inputs := map[string]any{"g": clsInstance(t, &clsGreeter{Greeting: "hello"}, monty.ClassInstanceOptions{LazyAttrs: monty.Names("greeting")})}
			code := "[hasattr(g, 'greeting'), getattr(g, 'greeting'), hasattr(g, 'other'), getattr(g, 'other', 7)]"
			require.Equal(t, []any{true, "hello", false, int64(7)}, clsMustRun(t, b, code, inputs))
			_, err := clsRun(t, b, "getattr(g, 'other')", inputs)
			require.Equal(t, "AttributeError: 'clsGreeter' object has no attribute 'other'", clsRuntimeMessage(t, err))
		})

		t.Run("underscore attrs never reach the host", func(t *testing.T) {
			var convertCalls []string
			g := clsInstance(t, &clsGreeter{Greeting: "hello", Hidden: "secret"}, monty.ClassInstanceOptions{
				LazyAttrs: monty.All,
				ConvertValue: func(name string, v any) (any, error) {
					convertCalls = append(convertCalls, name)
					return v, nil
				},
			})
			_, err := clsRun(t, b, "g._hidden", map[string]any{"g": g})
			require.Equal(t, "AttributeError: 'clsGreeter' object has no attribute '_hidden'", clsRuntimeMessage(t, err))
			require.Empty(t, convertCalls)
		})

		t.Run("a throwing lazy getter raises its error in the sandbox", func(t *testing.T) {
			f := clsInstance(t, &clsFlaky{}, monty.ClassInstanceOptions{LazyAttrs: monty.All})
			require.Equal(t, "'boom'", clsMustRun(t, b, clsCatchBoom, map[string]any{"f": f}))
		})

		t.Run("hasattr and getattr defaults do not swallow a lazy attribute host error", func(t *testing.T) {
			inputs := map[string]any{"f": clsInstance(t, &clsFlaky{}, monty.ClassInstanceOptions{LazyAttrs: monty.All})}
			for _, code := range []string{"hasattr(f, 'boom')", "getattr(f, 'boom', 7)"} {
				_, err := clsRun(t, b, code, inputs)
				require.Equal(t, "KeyError: boom", clsRuntimeMessage(t, err), code)
			}
		})

		t.Run("a convertValue throw on a lazy attr raises in the sandbox", func(t *testing.T) {
			g := clsInstance(t, &clsGreeter{Greeting: "hi"}, monty.ClassInstanceOptions{
				LazyAttrs: monty.All,
				ConvertValue: func(name string, _ any) (any, error) {
					return nil, monty.Raise("ValueError", "cannot convert "+name)
				},
			})
			code := "try:\n    g.greeting\n    r = 'unexpected'\nexcept ValueError as e:\n    r = str(e)\nr"
			require.Equal(t, "cannot convert greeting", clsMustRun(t, b, code, map[string]any{"g": g}))
		})

		t.Run("an unconvertible lazy value raises TypeError in the sandbox", func(t *testing.T) {
			inputs := map[string]any{"f": clsInstance(t, &clsFlaky{}, monty.ClassInstanceOptions{LazyAttrs: monty.All})}
			catching := func(attr string) string {
				return "try:\n    f." + attr + "\n    r = 'unexpected'\nexcept TypeError as e:\n    r = str(e)\nr"
			}
			require.Equal(t, "Cannot convert clsGreeter instance to a Monty value — wrap it in ClassInstance(...)", clsMustRun(t, b, catching("raw"), inputs))
			require.Equal(t, "Cannot convert Go chan int to Monty value", clsMustRun(t, b, catching("sym"), inputs))
		})

		t.Run("the session stays usable after a lazy attribute host error", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			opts := &monty.FeedOptions{Inputs: map[string]any{"f": clsInstance(t, &clsFlaky{}, monty.ClassInstanceOptions{LazyAttrs: monty.All})}}
			_, err := s.FeedRun(ctx, "f.boom", opts)
			require.Equal(t, "KeyError: boom", clsRuntimeMessage(t, err))
			v, err := s.FeedRun(ctx, "f.value + 1", opts)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})

		t.Run("NameLookupSnapshot.resumeAuto raises a lazy attribute host error in the sandbox", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			snap, err := s.FeedStart(ctx, clsCatchBoom, &monty.FeedOptions{Inputs: map[string]any{"f": clsInstance(t, &clsFlaky{}, monty.ClassInstanceOptions{LazyAttrs: monty.All})}})
			require.NoError(t, err)
			lookup, ok := snap.(*monty.NameLookupSnapshot)
			require.True(t, ok, "%T", snap)
			require.Equal(t, "boom", lookup.VariableName)
			done, err := lookup.ResumeAuto(ctx)
			require.NoError(t, err)
			complete, ok := done.(*monty.Complete)
			require.True(t, ok, "%T", done)
			require.Equal(t, "'boom'", complete.Output)
		})

		t.Run("convertValue transforms eager attrs and method returns", func(t *testing.T) {
			g := &clsGreeter{Greeting: "hello"}
			eager := clsInstance(t, g, monty.ClassInstanceOptions{EagerAttrs: monty.All, ConvertValue: clsUpper})
			require.Equal(t, "HELLO", clsMustRun(t, b, "g.greeting", map[string]any{"g": eager}))
			methods := clsInstance(t, g, monty.ClassInstanceOptions{AllowedMethods: monty.All, ConvertValue: clsUpper})
			require.Equal(t, "HELLO SAM", clsMustRun(t, b, "g.greet('sam')", map[string]any{"g": methods}))
		})

		t.Run("a method returning a class instance is not auto-wrapped", func(t *testing.T) {
			w := clsInstance(t, &clsWallet{Balance: 100}, monty.ClassInstanceOptions{EagerAttrs: monty.All, AllowedMethods: monty.All})
			code := "try:\n    w.pay(30)\n    r = 'unexpected'\nexcept TypeError as e:\n    r = str(e)\nr"
			require.Equal(t, "Cannot convert clsWallet instance to a Monty value — wrap it in ClassInstance(...)", clsMustRun(t, b, code, map[string]any{"w": w}))
		})

		t.Run("a convertValue override chooses the derived instance policy", func(t *testing.T) {
			w := &clsWallet{Balance: 100}
			opts := monty.ClassInstanceOptions{EagerAttrs: monty.All, AllowedMethods: monty.All, ConvertValue: clsWrapDerivedWallet}
			require.Equal(t, int64(70), clsMustRun(t, b, "w.pay(30).balance", map[string]any{"w": clsInstance(t, w, opts)}))
			_, err := clsRun(t, b, "w.pay(30).pay(5)", map[string]any{"w": clsInstance(t, w, opts)})
			require.Contains(t, clsRuntimeMessage(t, err), "'clsWallet' object has no attribute 'pay'")
		})

		t.Run("a returned override-wrapped instance restores to the original object", func(t *testing.T) {
			w := clsInstance(t, &clsWallet{Balance: 100}, monty.ClassInstanceOptions{AllowedMethods: monty.All, ConvertValue: clsWrapDerivedWallet})
			v := clsMustRun(t, b, "w.pay(30)", map[string]any{"w": w})
			result, ok := v.(*clsWallet)
			require.True(t, ok, "%T", v)
			require.Equal(t, 70, result.Balance)
		})

		t.Run("a sandbox-defined class instance surfaces as a MontyClassProxy", func(t *testing.T) {
			v := clsMustRun(t, b, "class Foo:\n    def __init__(self, a: int):\n        self.a = a\nFoo(1)", nil)
			proxy, ok := v.(*monty.ClassProxy)
			require.True(t, ok, "%T", v)
			require.Equal(t, "Foo", proxy.Name)
			require.False(t, proxy.IsDataclass)
			require.True(t, monty.Equal(clsDict("a", int64(1)), proxy.Attributes), monty.Repr(proxy.Attributes))
		})

		t.Run("a MontyClassProxy passed back hands the sandbox its original object", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			_, err := s.FeedRun(ctx, "class Foo:\n    def __init__(self):\n        self.x = 1\nfoo = Foo()", nil)
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "foo", nil)
			require.NoError(t, err)
			proxy, ok := v.(*monty.ClassProxy)
			require.True(t, ok, "%T", v)
			require.NotEmpty(t, proxy.ID)
			v, err = s.FeedRun(ctx, "back is foo and isinstance(back, Foo) and back.x == 1", &monty.FeedOptions{Inputs: map[string]any{"back": proxy}})
			require.NoError(t, err)
			require.Equal(t, true, v)
			v, err = s.FeedRun(ctx, "echo(foo) is foo", &monty.FeedOptions{ExternalLookup: map[string]any{"echo": func(x any) any { return x }}})
			require.NoError(t, err)
			require.Equal(t, true, v)
		})

		t.Run("a MontyClassProxy of a freed sandbox object is rejected", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			v, err := s.FeedRun(ctx, "class Foo:\n    pass\nFoo()", nil)
			require.NoError(t, err)
			proxy, ok := v.(*monty.ClassProxy)
			require.True(t, ok, "%T", v)
			_, err = s.FeedRun(ctx, "x", &monty.FeedOptions{Inputs: map[string]any{"x": proxy}})
			require.Equal(t,
				"RuntimeError: invalid input type: sandbox instance of 'Foo' (id <id>) no longer exists",
				strings.ReplaceAll(clsRuntimeMessage(t, err), proxy.ID, "<id>"),
			)
		})

		t.Run("a MontyClassProxy round-trips nested in containers and its edited attributes are ignored", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			_, err := s.FeedRun(ctx, "class Foo:\n    def __init__(self):\n        self.x = 1\nfoo = Foo()", nil)
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "foo", nil)
			require.NoError(t, err)
			proxy, ok := v.(*monty.ClassProxy)
			require.True(t, ok, "%T", v)
			proxy.Attributes.Set("x", 99)
			inputs := map[string]any{"items": []any{proxy}, "mapping": map[string]any{"k": proxy}}
			v, err = s.FeedRun(ctx, "items[0] is foo and mapping['k'] is foo and foo.x == 1", &monty.FeedOptions{Inputs: inputs})
			require.NoError(t, err)
			require.Equal(t, true, v)
		})

		t.Run("a MontyClassProxy still resolves after dump and loadSession", func(t *testing.T) {
			ctx := testCtx(t)
			first := newSession(t, b, monty.CheckoutOptions{})
			_, err := first.FeedRun(ctx, "class Foo:\n    def __init__(self):\n        self.x = 1\nfoo = Foo()", nil)
			require.NoError(t, err)
			v, err := first.FeedRun(ctx, "foo", nil)
			require.NoError(t, err)
			proxy, ok := v.(*monty.ClassProxy)
			require.True(t, ok, "%T", v)
			blob, err := first.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			s := newSession(t, b, monty.CheckoutOptions{})
			require.NoError(t, s.LoadSession(ctx, blob))
			v, err = s.FeedRun(ctx, "back is foo and isinstance(back, Foo)", &monty.FeedOptions{Inputs: map[string]any{"back": proxy}})
			require.NoError(t, err)
			require.Equal(t, true, v)
		})

		t.Run("a host-origin proxy from a restored session re-enters as a host-backed copy", func(t *testing.T) {
			ctx := testCtx(t)
			first := newSession(t, b, monty.CheckoutOptions{})
			obj := clsInstance(t, &clsGreeter{Greeting: "hello"}, monty.ClassInstanceOptions{EagerAttrs: monty.All})
			_, err := first.FeedRun(ctx, "x = obj", &monty.FeedOptions{Inputs: map[string]any{"obj": obj}})
			require.NoError(t, err)
			blob, err := first.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			s := newSession(t, b, monty.CheckoutOptions{})
			require.NoError(t, s.LoadSession(ctx, blob))
			v, err := s.FeedRun(ctx, "x", nil)
			require.NoError(t, err)
			proxy, ok := v.(*monty.ClassProxy)
			require.True(t, ok, "%T", v)
			v, err = s.FeedRun(ctx, "[type(y).__name__, y.greeting, y is x, y == x]", &monty.FeedOptions{Inputs: map[string]any{"y": proxy}})
			require.NoError(t, err)
			require.Equal(t, []any{"clsGreeter", "hello", false, true}, v)
		})

		t.Run("a sandbox-defined dataclass instance reports isDataclass", func(t *testing.T) {
			v := clsMustRun(t, b, "from dataclasses import dataclass\n@dataclass\nclass P:\n    x: int\n    y: int\nP(1, 2)", nil)
			proxy, ok := v.(*monty.ClassProxy)
			require.True(t, ok, "%T", v)
			require.Equal(t, "P", proxy.Name)
			require.True(t, proxy.IsDataclass)
			require.True(t, monty.Equal(clsDict("x", int64(1), "y", int64(2)), proxy.Attributes), monty.Repr(proxy.Attributes))
		})

		t.Run("an unwrapped class instance input is rejected with a wrap hint", func(t *testing.T) {
			_, err := clsRun(t, b, "x", map[string]any{"x": &clsGreeter{Greeting: "hi"}})
			require.Equal(t, "Cannot convert clsGreeter instance to a Monty value — wrap it in ClassInstance(...)", clsConversionMessage(t, err))
		})

		t.Run("an unwrapped instance returned from an external function raises in the sandbox", func(t *testing.T) {
			code := "try:\n    bad()\n    r = 'unexpected'\nexcept TypeError as e:\n    r = str(e)\nr"
			v, err := run(t, b, code, runOptions{FeedOptions: monty.FeedOptions{ExternalLookup: map[string]any{
				"bad": func() *clsGreeter { return &clsGreeter{Greeting: "hi"} },
			}}})
			require.NoError(t, err)
			require.Equal(t, "Cannot convert clsGreeter instance to a Monty value — wrap it in ClassInstance(...)", v)
		})

		t.Run("a duplicate wrapper id wrapping a different object is rejected", func(t *testing.T) {
			id := "11111111-2222-4333-8444-555555555555"
			a := clsInstance(t, &clsGreeter{Greeting: "hi"}, monty.ClassInstanceOptions{ID: id})
			c := clsInstance(t, &clsGreeter{Greeting: "yo"}, monty.ClassInstanceOptions{ID: id})
			_, err := clsRun(t, b, "[a, b]", map[string]any{"a": a, "b": c})
			require.Equal(t, "wrapper id '"+id+"' already identifies a different object in this session", clsConversionMessage(t, err))
		})

		t.Run("a forged raw ClassInstance marker is rejected", func(t *testing.T) {
			forged := value.Instance{Type: value.Type{Name: "Point"}, ID: "x", Attrs: monty.NewDict()}
			_, err := clsRun(t, b, "x", map[string]any{"x": map[string]any{"data": []any{forged}}})
			require.Equal(t, "raw ClassInstance markers are not accepted — wrap the object in ClassInstance(...)", clsConversionMessage(t, err))
		})

		t.Run("a too-deep input fails with a conversion error, not a stack overflow", func(t *testing.T) {
			var nested any = 1
			for range 60 {
				nested = []any{nested}
			}
			_, err := clsRun(t, b, "x", map[string]any{"x": nested})
			require.Equal(t, "Max input depth exceeded", clsConversionMessage(t, err))
		})

		t.Run("a set-like policy from another realm works (duck-typed .has)", func(t *testing.T) {
			t.Skip("JS-only: AttrPolicy is a typed Go value; there is no cross-realm Set to duck-type")
		})

		t.Run("NameLookupSnapshot.resumeValue answers a lazy attribute lookup by hand", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			g := clsInstance(t, &clsGreeter{Greeting: "hi"}, monty.ClassInstanceOptions{LazyAttrs: monty.All})
			snap, err := s.FeedStart(ctx, `g.greeting + "!"`, &monty.FeedOptions{Inputs: map[string]any{"g": g}})
			require.NoError(t, err)
			lookup, ok := snap.(*monty.NameLookupSnapshot)
			require.True(t, ok, "%T", snap)
			require.Equal(t, "greeting", lookup.VariableName)
			require.NotEmpty(t, lookup.ObjectID)
			done, err := lookup.ResumeValue(ctx, "manual")
			require.NoError(t, err)
			complete, ok := done.(*monty.Complete)
			require.True(t, ok, "%T", done)
			require.Equal(t, "manual!", complete.Output)
		})

		t.Run("sandbox instantiation of a host class via ClassType", func(t *testing.T) {
			wrapper := clsType[clsCalculator](t, monty.ClassTypeOptions{Init: true, InstanceAllowedMethods: monty.All})
			require.Equal(t, int64(15), clsMustRun(t, b, "c = Calculator(10)\nc.add(5)", map[string]any{"Calculator": wrapper}))
		})

		t.Run("a constructed instance round-trips to a real host instance", func(t *testing.T) {
			wrapper := clsType[clsGreeter](t, monty.ClassTypeOptions{Init: true})
			v := clsMustRun(t, b, "Greeter('hi')", map[string]any{"Greeter": wrapper})
			g, ok := v.(*clsGreeter)
			require.True(t, ok, "%T", v)
			require.Equal(t, "hi Sam", g.Greet("Sam"))
		})

		t.Run("init false raises TypeError in the sandbox", func(t *testing.T) {
			wrapper := clsType[clsCalculator](t, monty.ClassTypeOptions{})
			_, err := clsRun(t, b, "Calculator(10)", map[string]any{"Calculator": wrapper})
			require.Equal(t, "TypeError: cannot instantiate host class 'clsCalculator'", clsRuntimeMessage(t, err))
		})

		t.Run("constructor kwargs arrive as a trailing options bag", func(t *testing.T) {
			wrapper := clsType[clsConfig](t, monty.ClassTypeOptions{
				Init:                   true,
				InstanceAllowedMethods: monty.All,
				Constructor:            func(kw monty.Kwargs) *clsConfig { return &clsConfig{options: kw} },
			})
			require.Equal(t, "fast", clsMustRun(t, b, "Config(mode='fast').read()", map[string]any{"Config": wrapper}))
		})

		t.Run("a denied method on a constructed instance raises AttributeError", func(t *testing.T) {
			wrapper := clsType[clsCalculator](t, monty.ClassTypeOptions{Init: true, InstanceAllowedMethods: monty.Names("add")})
			_, err := clsRun(t, b, "Calculator(1).boom()", map[string]any{"Calculator": wrapper})
			require.Equal(t, "AttributeError: 'clsCalculator' object has no attribute 'boom'", clsRuntimeMessage(t, err))
		})

		t.Run("eagerAttrs on a ClassType sends static class constants", func(t *testing.T) {
			wrapper := clsType[clsShape](t, monty.ClassTypeOptions{EagerAttrs: monty.All, Statics: clsShapeStatics()})
			require.Equal(t, int64(11), clsMustRun(t, b, "Shape.SIDES + len(Shape.KIND)", map[string]any{"Shape": wrapper}))
		})

		t.Run("a constructed instance keeps the ClassType id and name", func(t *testing.T) {
			wrapper := clsType[clsWallet](t, monty.ClassTypeOptions{
				ID:                 "12345678-1234-4123-8123-123456789abc",
				Name:               "Purse",
				Init:               true,
				InstanceEagerAttrs: monty.All,
			})
			code := "w = Wallet(1)\n[type(w) == Wallet, type(w) is Wallet, type(w).__name__, isinstance(w, Wallet)]"
			require.Equal(t, []any{true, true, "Purse", true}, clsMustRun(t, b, code, map[string]any{"Wallet": wrapper}))
		})

		t.Run("a constructed instance of another class gets a default ClassType", func(t *testing.T) {
			wrapper := clsType[clsFactory](t, monty.ClassTypeOptions{
				Init:        true,
				Constructor: func() *clsOther { return &clsOther{V: 1} },
			})
			wrapped, err := wrapper.Construct(testCtx(t), nil, nil)
			require.NoError(t, err)
			require.NotSame(t, wrapper, wrapped.ClassType())
			require.Equal(t, reflect.TypeFor[clsOther](), wrapped.ClassType().GoType())
		})

		t.Run("an own constructor property does not change class identity", func(t *testing.T) {
			instance := &clsShadowing{clsOther: clsOther{V: 2}, N: 1}
			wrapper := clsType[clsShadowing](t, monty.ClassTypeOptions{
				Init:               true,
				InstanceEagerAttrs: monty.All,
				Constructor:        func() *clsShadowing { return instance },
			})
			constructed, err := wrapper.Construct(testCtx(t), nil, nil)
			require.NoError(t, err)
			require.Same(t, wrapper, constructed.ClassType())
			require.Equal(t, "clsShadowing", clsInstance(t, instance, monty.ClassInstanceOptions{}).Name())
			require.Same(t, wrapper, clsInstance(t, instance, monty.ClassInstanceOptions{ClassType: wrapper}).ClassType())
			require.Same(t, wrapper, clsInstance(t, *instance, monty.ClassInstanceOptions{ClassType: wrapper}).ClassType())
			inputs := map[string]any{"x": constructed, "Shadowing": wrapper}
			require.Equal(t, []any{"clsShadowing", true, int64(1)}, clsMustRun(t, b, "[type(x).__name__, isinstance(x, Shadowing), x.n]", inputs))
		})

		t.Run("an instance's type branch carries the ClassType's eager attrs", func(t *testing.T) {
			classType := clsType[clsShape](t, monty.ClassTypeOptions{EagerAttrs: monty.Names("SIDES"), Statics: clsShapeStatics()})
			inputs := map[string]any{"x": clsInstance(t, &clsShape{Size: 1}, monty.ClassInstanceOptions{ClassType: classType})}
			require.Equal(t, []any{int64(4), int64(4)}, clsMustRun(t, b, "[type(x).SIDES, x.__class__.SIDES]", inputs))
		})

		t.Run("type objects of two instances are one object", func(t *testing.T) {
			inputs := map[string]any{
				"a": clsInstance(t, &clsShape{Size: 1}, monty.ClassInstanceOptions{}),
				"b": clsInstance(t, &clsShape{Size: 2}, monty.ClassInstanceOptions{}),
			}
			code := "[type(a) is type(b), {type(a): 1}[type(b)], isinstance(a, type(b))]"
			require.Equal(t, []any{true, int64(1), true}, clsMustRun(t, b, code, inputs))
		})

		t.Run("a ClassType name override reaches the instance and its error message", func(t *testing.T) {
			classType := clsType[clsShape](t, monty.ClassTypeOptions{Name: "Polygon"})
			inputs := map[string]any{"x": clsInstance(t, &clsShape{Size: 1}, monty.ClassInstanceOptions{ClassType: classType})}
			require.Equal(t, "Polygon", clsMustRun(t, b, "type(x).__name__", inputs))
			_, err := clsRun(t, b, "x.missing", inputs)
			require.Equal(t, "AttributeError: 'Polygon' object has no attribute 'missing'", clsRuntimeMessage(t, err))
		})

		t.Run("name on a ClassInstance names its default ClassType, and clashes with classType", func(t *testing.T) {
			x := clsInstance(t, &clsShape{Size: 1}, monty.ClassInstanceOptions{Name: "Poly"})
			require.Equal(t, "Poly", clsMustRun(t, b, "type(x).__name__", map[string]any{"x": x}))
			_, err := monty.NewClassInstance(&clsShape{Size: 1}, monty.ClassInstanceOptions{Name: "Poly", ClassType: clsType[clsShape](t, monty.ClassTypeOptions{})})
			var ve *monty.ValueError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, "pass name on the ClassType wrapper, not alongside classType", ve.Message)
		})

		t.Run("lazyAttrs on a ClassType serves class constants on demand", func(t *testing.T) {
			wrapper := clsType[clsShape](t, monty.ClassTypeOptions{LazyAttrs: monty.Names("SIDES"), Statics: clsShapeStatics()})
			require.Equal(t, int64(4), clsMustRun(t, b, "Shape.SIDES", map[string]any{"Shape": wrapper}))
			_, err := clsRun(t, b, "Shape.KIND", map[string]any{"Shape": wrapper})
			require.Equal(t, "AttributeError: type object 'clsShape' has no attribute 'KIND'", clsRuntimeMessage(t, err))
		})

		t.Run("allowedMethods on a ClassType exposes static methods", func(t *testing.T) {
			wrapper := clsType[clsShape](t, monty.ClassTypeOptions{AllowedMethods: monty.Names("double"), Statics: clsShapeStatics()})
			require.Equal(t, int64(42), clsMustRun(t, b, "Shape.double(21)", map[string]any{"Shape": wrapper}))
		})

		t.Run("a denied static method uses the type-object wording", func(t *testing.T) {
			wrapper := clsType[clsShape](t, monty.ClassTypeOptions{Statics: clsShapeStatics()})
			_, err := clsRun(t, b, "Shape.double(1)", map[string]any{"Shape": wrapper})
			require.Equal(t, "AttributeError: type object 'clsShape' has no attribute 'double'", clsRuntimeMessage(t, err))
		})

		t.Run("instantiate turns carry the class uuid as objectId", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			wrapper := clsType[clsCalculator](t, monty.ClassTypeOptions{Init: true})
			snap, err := s.FeedStart(ctx, "Calculator(1)", &monty.FeedOptions{Inputs: map[string]any{"Calculator": wrapper}})
			require.NoError(t, err)
			call, ok := snap.(*monty.FunctionSnapshot)
			require.True(t, ok, "%T", snap)
			require.Equal(t, "__call__", call.FunctionName)
			require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, call.ObjectID)
			done, err := call.ResumeAuto(ctx)
			require.NoError(t, err)
			require.IsType(t, &monty.Complete{}, done)
		})

		t.Run(`JS object machinery is denied on an instance under "all", and the host object is untouched`, func(t *testing.T) {
			c := &clsCalculator{Value: 5, Memo: "memo"}
			inputs := map[string]any{"c": clsInstance(t, c, monty.ClassInstanceOptions{AllowedMethods: monty.All, LazyAttrs: monty.All})}
			code := []string{
				"probe(lambda: c.reset())",
				"probe(lambda: c.last_kwargs)",
				"probe(lambda: c.lastKwargs)",
				"probe(lambda: c.memo)",
				"probe(lambda: c.Memo)",
				"hasattr(c, 'reset')",
				"c.add(1)",
			}
			require.Equal(t, []any{
				"'clsCalculator' object has no attribute 'reset'",
				"'clsCalculator' object has no attribute 'last_kwargs'",
				"'clsCalculator' object has no attribute 'lastKwargs'",
				"'clsCalculator' object has no attribute 'memo'",
				"'clsCalculator' object has no attribute 'Memo'",
				false,
				int64(6),
			}, clsMustRun(t, b, clsProbe+"["+strings.Join(code, ", ")+"]", inputs))
			require.Equal(t, 5, c.Value)
		})

		t.Run(`JS function machinery is denied on a class under "all"`, func(t *testing.T) {
			inputs := map[string]any{"Shape": clsType[clsShape](t, monty.ClassTypeOptions{AllowedMethods: monty.All, LazyAttrs: monty.All, Statics: clsShapeStatics()})}
			code := []string{
				"probe(lambda: Shape.size)",
				"probe(lambda: Shape.area())",
				"probe(lambda: Shape.construct(1))",
				"probe(lambda: Shape.go_type())",
				"probe(lambda: Shape.GoType())",
				"Shape.double(21)",
				"Shape.SIDES",
			}
			require.Equal(t, []any{
				"type object 'clsShape' has no attribute 'size'",
				"type object 'clsShape' has no attribute 'area'",
				"type object 'clsShape' has no attribute 'construct'",
				"type object 'clsShape' has no attribute 'go_type'",
				"type object 'clsShape' has no attribute 'GoType'",
				int64(42),
				int64(4),
			}, clsMustRun(t, b, clsProbe+"["+strings.Join(code, ", ")+"]", inputs))
		})

		t.Run("an explicit policy cannot name JS object machinery either", func(t *testing.T) {
			c := &clsCalculator{Value: 5, Memo: "memo"}
			denied := []string{"reset", "last_kwargs", "lastKwargs", "memo", "Memo"}
			wrapper := clsInstance(t, c, monty.ClassInstanceOptions{
				AllowedMethods: monty.Names(append([]string{"add"}, denied...)...),
				LazyAttrs:      monty.Names(append([]string{"value"}, denied...)...),
			})
			inputs := map[string]any{"c": wrapper}
			require.Equal(t, []any{int64(6), int64(5)}, clsMustRun(t, b, "[c.add(1), c.value]", inputs))
			for _, name := range denied {
				want := "'clsCalculator' object has no attribute '" + name + "'"
				v := clsMustRun(t, b, clsProbe+"[probe(lambda: c."+name+"()), probe(lambda: c."+name+")]", inputs)
				require.Equal(t, []any{want, want}, v, name)
			}
			require.Equal(t, 5, c.Value)
		})

		t.Run("a wrapped function cannot be invoked through Function.prototype", func(t *testing.T) {
			invoked := false
			tool := clsTool(func(x int) int {
				invoked = true
				return x * 2
			})
			inputs := map[string]any{"tool": clsInstance(t, tool, monty.ClassInstanceOptions{AllowedMethods: monty.All, LazyAttrs: monty.All})}
			code := []string{
				"probe(lambda: tool.call(None, 21))",
				"probe(lambda: tool.Call(None, 21))",
				"probe(lambda: tool.apply(None, 21))",
				"probe(lambda: tool.bind(None, 21))",
				"tool.double(21)",
			}
			require.Equal(t, []any{
				"'clsTool' object has no attribute 'call'",
				"'clsTool' object has no attribute 'Call'",
				"'clsTool' object has no attribute 'apply'",
				"'clsTool' object has no attribute 'bind'",
				int64(42),
			}, clsMustRun(t, b, clsProbe+"["+strings.Join(code, ", ")+"]", inputs))
			require.False(t, invoked)
		})

		t.Run(`"all" exposes methods the class defines, not callables stored on the instance`, func(t *testing.T) {
			bag := &clsBag{Run: func() string { return "ran" }}
			all := map[string]any{
				"b": clsInstance(t, bag, monty.ClassInstanceOptions{AllowedMethods: monty.All}),
				"Bag": clsType[clsBag](t, monty.ClassTypeOptions{AllowedMethods: monty.All, Statics: map[string]any{
					"make":  func() string { return "made" },
					"Inner": clsType[clsBagInner](t, monty.ClassTypeOptions{}),
				}}),
			}
			code := []string{"b.helper()", "probe(lambda: b.run())", "Bag.make()", "probe(lambda: Bag.Inner())"}
			require.Equal(t, []any{
				"helped",
				"'clsBag' object has no attribute 'run'",
				"made",
				"type object 'clsBag' has no attribute 'Inner'",
			}, clsMustRun(t, b, clsProbe+"["+strings.Join(code, ", ")+"]", all))
			explicit := clsInstance(t, bag, monty.ClassInstanceOptions{AllowedMethods: monty.Names("run")})
			require.Equal(t, "ran", clsMustRun(t, b, "b.run()", map[string]any{"b": explicit}))
		})

		t.Run(`a string policy other than "all" is rejected at construction`, func(t *testing.T) {
			t.Skip("JS-only: AttrPolicy is a typed Go value built by All or Names; an invalid policy string is unrepresentable")
		})

		t.Run("a __proto__ keyword argument never reaches the host", func(t *testing.T) {
			c := &clsCalculator{Value: 5}
			inputs := map[string]any{"c": clsInstance(t, c, monty.ClassInstanceOptions{AllowedMethods: monty.All})}
			require.Equal(t, "1,2", clsMustRun(t, b, "c.combine(1, 2, __proto__={'sep': '-'})", inputs))
			require.NotContains(t, c.lastKwargs, "__proto__")
			require.Empty(t, c.lastKwargs)
		})

		t.Run("an explicit id is validated and lowercased, and still round-trips to the original", func(t *testing.T) {
			g := &clsGreeter{Greeting: "hi"}
			wrapper := clsInstance(t, g, monty.ClassInstanceOptions{ID: "ABCDEF01-2345-4678-89AB-CDEF01234567"})
			require.Equal(t, "abcdef01-2345-4678-89ab-cdef01234567", wrapper.ID())
			require.Same(t, g, clsMustRun(t, b, "x", map[string]any{"x": wrapper}))
			classType := clsType[clsShape](t, monty.ClassTypeOptions{ID: "12345678-1234-4123-8123-123456789ABC"})
			require.Equal(t, "12345678-1234-4123-8123-123456789abc", classType.ID())
			require.Same(t, classType, clsMustRun(t, b, "Shape", map[string]any{"Shape": classType}))

			var ve *monty.ValueError
			_, err := monty.NewClassInstance(g, monty.ClassInstanceOptions{ID: "not-a-uuid"})
			require.ErrorAs(t, err, &ve)
			require.Equal(t, `ClassInstance id must be a canonical uuid string, got "not-a-uuid"`, ve.Message)
			_, err = monty.NewClassType[clsShape](monty.ClassTypeOptions{ID: "12345678123441238123123456789abc"})
			require.ErrorAs(t, err, &ve)
			require.Equal(t, `ClassType id must be a canonical uuid string, got "12345678123441238123123456789abc"`, ve.Message)
		})

		t.Run("a returned host class resolves to the class object when registered", func(t *testing.T) {
			shapeType := reflect.TypeFor[clsShape]()
			classType := clsType[clsShape](t, monty.ClassTypeOptions{})
			require.Same(t, classType, clsMustRun(t, b, "Shape", map[string]any{"Shape": classType}))

			x := clsInstance(t, &clsShape{Size: 1}, monty.ClassInstanceOptions{})
			v := clsMustRun(t, b, "type(x)", map[string]any{"x": x})
			ct, ok := v.(*monty.ClassType)
			require.True(t, ok, "%T", v)
			require.Equal(t, shapeType, ct.GoType())

			x = clsInstance(t, &clsShape{Size: 1}, monty.ClassInstanceOptions{})
			v = clsMustRun(t, b, "[type(x), [x.__class__]]", map[string]any{"x": x})
			items, ok := v.([]any)
			require.True(t, ok, "%T", v)
			require.Len(t, items, 2)
			first, ok := items[0].(*monty.ClassType)
			require.True(t, ok, "%T", items[0])
			require.Equal(t, shapeType, first.GoType())
			nested, ok := items[1].([]any)
			require.True(t, ok, "%T", items[1])
			require.Len(t, nested, 1)
			require.Same(t, first, nested[0])
		})

		t.Run("a returned host class stays a Type marker when the session never registered it", func(t *testing.T) {
			ctx := testCtx(t)
			first := newSession(t, b, monty.CheckoutOptions{})
			obj := clsInstance(t, &clsGreeter{Greeting: "hello"}, monty.ClassInstanceOptions{})
			_, err := first.FeedRun(ctx, "x = obj", &monty.FeedOptions{Inputs: map[string]any{"obj": obj}})
			require.NoError(t, err)
			blob, err := first.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			s := newSession(t, b, monty.CheckoutOptions{})
			require.NoError(t, s.LoadSession(ctx, blob))
			v, err := s.FeedRun(ctx, "type(x)", nil)
			require.NoError(t, err)
			marker, ok := v.(monty.Type)
			require.True(t, ok, "%T", v)
			require.Equal(t, "clsGreeter", marker.Name)
		})

		t.Run("a forged host-class Type marker is rejected, builtin type markers still pass prepare", func(t *testing.T) {
			forged := monty.Type{Name: "Shape", ID: "12345678-1234-4123-8123-123456789abc", Origin: monty.OriginHost}
			_, err := clsRun(t, b, "x", map[string]any{"x": []any{forged}})
			require.Equal(t, "raw Type markers are not accepted — pass the class through ClassType(...)", clsConversionMessage(t, err))
			require.Equal(t, true, clsMustRun(t, b, "x is int", map[string]any{"x": monty.Type{Name: "int", Origin: monty.OriginBuiltin}}))
		})
	})
}
