package montygo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

// hostBag has a variadic and a keyword-only method for the stub renderer.
type hostBag struct {
	Items []string
}

func (b *hostBag) Add(n int) int { return n + len(b.Items) }

func (b *hostBag) Put(items ...string) int { b.Items = append(b.Items, items...); return len(b.Items) }

func (b *hostBag) Configure(montygo.Kwargs) {}

func TestHostParameterNames(t *testing.T) {
	t.Run("names are copied and positional-only stubs match the signature", func(t *testing.T) {
		h := montygo.NewHost()
		names := []string{"first", "values"}
		require.NoError(t, h.Func("sum_values", func(context.Context, int, ...int) int { return 0 }, montygo.HostFuncOptions{ParameterNames: names}))
		names[0] = "changed"
		require.Contains(t, h.Stubs(), "def sum_values(first: int, /, *values: int) -> int: ...")
		require.NoError(t, h.Func("fetch", func(context.Context, string, montygo.Kwargs) string { return "" }, montygo.HostFuncOptions{ParameterNames: []string{"key"}}))
		require.Contains(t, h.Stubs(), "def fetch(key: str, /, **kwargs: Any) -> str: ...")
	})
	t.Run("invalid labels and keywords fail before registration", func(t *testing.T) {
		for _, names := range [][]string{{}, {"one"}, {"same", "same"}, {"ok", "return"}, {"bad name", "ok"}} {
			h := montygo.NewHost()
			require.Error(t, h.Func("f", func(int, int) {}, montygo.HostFuncOptions{ParameterNames: names}))
			require.Empty(t, h.Names())
		}
		h := montygo.NewHost()
		require.Error(t, h.Func("f", func(int, montygo.Kwargs) {}, montygo.HostFuncOptions{ParameterNames: []string{"kwargs"}}))
		require.Error(t, h.Func("f", func() {}, montygo.HostFuncOptions{}, montygo.HostFuncOptions{}))
		for _, keyword := range []string{"class", "return", "None", "True", "False", "async", "await"} {
			require.Error(t, h.Func(keyword, func() {}))
			require.Error(t, h.Object("obj", &hostCounter{}, montygo.ClassInstanceOptions{Name: keyword}))
		}
		require.NoError(t, h.Func("match", func() {}), "soft keywords remain valid identifiers")
	})
	t.Run("direct Function does not invent a signature", func(t *testing.T) {
		h := montygo.NewHost()
		fn := montygo.FunctionFunc(func(context.Context, []any, montygo.Kwargs) (any, error) { return nil, nil })
		require.Error(t, h.Func("f", fn, montygo.HostFuncOptions{ParameterNames: []string{"x"}}))
		require.NoError(t, h.Func("f", fn))
		require.Contains(t, h.Stubs(), "def f(*args: Any, **kwargs: Any) -> Any: ...")
	})
	t.Run("the positional-only marker follows a fixed parameter only", func(t *testing.T) {
		h := montygo.NewHost()
		require.NoError(t, h.Func("ping", func() {}))
		require.NoError(t, h.Func("ctx_only", func(context.Context) error { return nil }))
		require.NoError(t, h.Func("spread", func(...int) int { return 0 }))
		require.NoError(t, h.Func("options", func(montygo.Kwargs) {}))
		require.NoError(t, h.Func("both", func(context.Context, montygo.Kwargs) {}))
		require.NoError(t, h.Object("counter", &hostCounter{}, montygo.ClassInstanceOptions{AllowedMethods: montygo.All()}))
		require.NoError(t, h.Object("bag", &hostBag{}, montygo.ClassInstanceOptions{AllowedMethods: montygo.All()}))
		stubs := h.Stubs()
		for _, line := range []string{
			"def ping() -> None: ...",
			"def ctx_only() -> None: ...",
			"def spread(*args: int) -> int: ...",
			"def options(**kwargs: Any) -> None: ...",
			"def both(**kwargs: Any) -> None: ...",
			"    def add(self, arg0: int, /) -> int: ...",
			"    def reset(self) -> None: ...",
			"    def put(self, *args: str) -> int: ...",
			"    def configure(self, **kwargs: Any) -> None: ...",
		} {
			require.Contains(t, stubs, line+"\n")
		}
		require.NotContains(t, stubs, "(/")
		require.NotContains(t, stubs, "(self, /)")
	})
	t.Run("method names label instance stubs", func(t *testing.T) {
		h := montygo.NewHost()
		names := map[string][]string{"add": {"amount"}, "put": {"items"}}
		require.NoError(t, h.Object("bag", &hostBag{}, montygo.ClassInstanceOptions{AllowedMethods: montygo.All(), ParameterNames: names}))
		names["add"][0] = "changed"
		delete(names, "put")
		stubs := h.Stubs()
		require.Contains(t, stubs, "    def add(self, amount: int, /) -> int: ...\n")
		require.Contains(t, stubs, "    def put(self, *items: str) -> int: ...\n")
		require.Contains(t, stubs, "    def configure(self, **kwargs: Any) -> None: ...\n")

		ci, err := montygo.NewClassInstance(&hostCounter{}, montygo.ClassInstanceOptions{
			AllowedMethods: montygo.Expose[hostAdder](),
			ParameterNames: map[string][]string{"add": {"amount"}},
		})
		require.NoError(t, err)
		require.Equal(t, "hostCounter", ci.Name())
	})
	t.Run("class type names label statics and instance methods", func(t *testing.T) {
		build := func(n int) *hostCounter { return &hostCounter{Count: n} }
		ct, err := montygo.NewClassType[hostBag](montygo.ClassTypeOptions{
			AllowedMethods:         montygo.Names("make"),
			Statics:                map[string]any{"make": build, "LIMIT": 3},
			InstanceAllowedMethods: montygo.All(),
			ParameterNames:         map[string][]string{"make": {"count"}, "add": {"amount"}, "put": {"items"}},
		})
		require.NoError(t, err)
		h := montygo.NewHost()
		require.NoError(t, h.Object("bag", &hostBag{}, montygo.ClassInstanceOptions{
			AllowedMethods: montygo.All(),
			ClassType:      ct,
			ParameterNames: map[string][]string{"put": {"values"}},
		}))
		stubs := h.Stubs()
		require.Contains(t, stubs, "    def add(self, amount: int, /) -> int: ...\n")
		require.Contains(t, stubs, "    def put(self, *values: str) -> int: ...\n", "instance names win over class names")

		_, err = montygo.NewClassType[hostBag](montygo.ClassTypeOptions{
			AllowedMethods: montygo.All(),
			Statics:        map[string]any{"make": build},
			ParameterNames: map[string][]string{"make": {}},
		})
		require.Error(t, err, "static names must match the static's signature")
		_, err = montygo.NewClassType[hostBag](montygo.ClassTypeOptions{
			AllowedMethods: montygo.All(),
			Statics:        map[string]any{"make": montygo.FunctionFunc(func(context.Context, []any, montygo.Kwargs) (any, error) { return nil, nil })},
			ParameterNames: map[string][]string{"make": {"count"}},
		})
		require.Error(t, err, "a direct Function static has no signature")
		_, err = montygo.NewClassType[hostBag](montygo.ClassTypeOptions{
			AllowedMethods: montygo.All(),
			Statics:        map[string]any{"LIMIT": 3},
			ParameterNames: map[string][]string{"LIMIT": {}},
		})
		require.Error(t, err, "a constant is not a static method")
	})
	t.Run("unknown method keys and count mismatches fail at registration", func(t *testing.T) {
		var ve *montygo.ValueError
		_, err := montygo.NewClassInstance(&hostCounter{}, montygo.ClassInstanceOptions{
			AllowedMethods: montygo.Expose[hostAdder](),
			ParameterNames: map[string][]string{"reset": {}},
		})
		require.ErrorAs(t, err, &ve, "a method outside the policy is unknown")
		require.Equal(t, `parameter names: unknown method "reset"`, ve.Message)
		_, err = montygo.NewClassInstance(&hostCounter{}, montygo.ClassInstanceOptions{
			AllowedMethods: montygo.All(),
			ParameterNames: map[string][]string{"add": {"a", "b"}},
		})
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "expected 1 parameter names, got 2", ve.Message)
		require.EqualError(t, err, `parameter names for "add": expected 1 parameter names, got 2`)
		_, err = montygo.NewClassInstance(&hostCounter{}, montygo.ClassInstanceOptions{
			AllowedMethods: montygo.All(),
			ParameterNames: map[string][]string{"add": {"class"}},
		})
		require.ErrorAs(t, err, &ve, "labels must be identifiers")

		h := montygo.NewHost()
		require.Error(t, h.Object("counter", &hostCounter{}, montygo.ClassInstanceOptions{
			AllowedMethods: montygo.All(),
			ParameterNames: map[string][]string{"missing": {"x"}},
		}))
		require.Empty(t, h.Names())

		_, err = montygo.NewClassType[hostCounter](montygo.ClassTypeOptions{
			InstanceAllowedMethods: montygo.All(),
			ParameterNames:         map[string][]string{"missing": {"x"}},
		})
		require.ErrorAs(t, err, &ve)
		require.Equal(t, `parameter names: unknown method "missing"`, ve.Message)
		_, err = montygo.NewClassType[hostCounter](montygo.ClassTypeOptions{
			ParameterNames: map[string][]string{"add": {"amount"}},
		})
		require.ErrorAs(t, err, &ve, "instance methods count only under InstanceAllowedMethods")
		_, err = montygo.NewClassType[hostCounter](montygo.ClassTypeOptions{
			InstanceAllowedMethods: montygo.All(),
			ParameterNames:         map[string][]string{"add": {}},
		})
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "expected 1 parameter names, got 0", ve.Message)
	})
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		h := montygo.NewHost()
		require.NoError(t, h.Func("double", func(n int) int { return n * 2 }, montygo.HostFuncOptions{ParameterNames: []string{"number"}}))
		ctx := testCtx(t)
		for _, typeCheck := range []bool{false, true} {
			s := newSession(t, b, montygo.CheckoutOptions{Host: h, TypeCheck: typeCheck, TypeCheckStubs: h.Stubs()})
			v, err := s.FeedRun(ctx, "double(21)", nil)
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
			_, err = s.FeedRun(ctx, "double(number=21)", nil)
			if typeCheck {
				var te *montygo.TypingError
				require.ErrorAs(t, err, &te)
			} else {
				var re *montygo.RuntimeError
				require.ErrorAs(t, err, &re)
				require.Equal(t, "TypeError", re.TypeName)
			}
		}
	})
}
