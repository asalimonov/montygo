package montygo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

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
