package monty_test

import (
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func coreABytes(t *testing.T, v any) []byte {
	t.Helper()
	data, ok := v.([]byte)
	require.Truef(t, ok, "expected bytes-like value, got %T", v)
	return data
}

func coreARequireBigInt(t *testing.T, want *big.Int, got any) {
	t.Helper()
	g, ok := got.(*big.Int)
	require.Truef(t, ok, "expected *big.Int, got %T (%v)", got, got)
	require.Zerof(t, want.Cmp(g), "expected %s, got %s", want, g)
}

func coreARequirePyEqual(t *testing.T, want, got any) {
	t.Helper()
	require.Truef(t, monty.Equal(want, got), "expected %s, got %s", monty.Repr(want), monty.Repr(got))
}

func coreATwoPow(n uint) *big.Int {
	return new(big.Int).Lsh(big.NewInt(1), n)
}

func coreAInt32(v int32) *int32 { return &v }

func coreAString(v string) *string { return &v }

func TestTypes(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("none input", func(t *testing.T) {
			require.Equal(t, true, mustRun(t, b, "x is None", coreAInputs(map[string]any{"x": nil})))
		})

		t.Run("none output", func(t *testing.T) {
			require.Nil(t, mustRun(t, b, "None", runOptions{}))
		})

		t.Run("bool true", func(t *testing.T) {
			require.Equal(t, true, mustRun(t, b, "x", coreAInputs(map[string]any{"x": true})))
		})

		t.Run("bool false", func(t *testing.T) {
			require.Equal(t, false, mustRun(t, b, "x", coreAInputs(map[string]any{"x": false})))
		})

		t.Run("int", func(t *testing.T) {
			require.Equal(t, int64(42), mustRun(t, b, "x", coreAInputs(map[string]any{"x": 42})))
			require.Equal(t, int64(-100), mustRun(t, b, "x", coreAInputs(map[string]any{"x": -100})))
			require.Equal(t, int64(0), mustRun(t, b, "x", coreAInputs(map[string]any{"x": 0})))
		})

		t.Run("float", func(t *testing.T) {
			require.Equal(t, 3.14, mustRun(t, b, "x", coreAInputs(map[string]any{"x": 3.14})))
			require.Equal(t, -2.5, mustRun(t, b, "x", coreAInputs(map[string]any{"x": -2.5})))
			require.Equal(t, 0.0, mustRun(t, b, "x", coreAInputs(map[string]any{"x": 0.0})))
		})

		t.Run("string", func(t *testing.T) {
			require.Equal(t, "hello", mustRun(t, b, "x", coreAInputs(map[string]any{"x": "hello"})))
			require.Equal(t, "", mustRun(t, b, "x", coreAInputs(map[string]any{"x": ""})))
			require.Equal(t, "unicode: éè", mustRun(t, b, "x", coreAInputs(map[string]any{"x": "unicode: éè"})))
		})

		t.Run("bytes", func(t *testing.T) {
			result := mustRun(t, b, "x", coreAInputs(map[string]any{"x": []byte{104, 101, 108, 108, 111}}))
			require.Equal(t, []byte{104, 101, 108, 108, 111}, coreABytes(t, result))
		})

		t.Run("bytes empty", func(t *testing.T) {
			result := mustRun(t, b, "x", coreAInputs(map[string]any{"x": []byte{}}))
			require.Empty(t, coreABytes(t, result))
		})

		t.Run("bytes result", func(t *testing.T) {
			result := mustRun(t, b, `b"hello"`, runOptions{})
			require.Equal(t, []byte{104, 101, 108, 108, 111}, coreABytes(t, result))
		})

		t.Run("str encode ascii ignore then bytes decode ascii round-trips", func(t *testing.T) {
			result := mustRun(t, b, `"café — 日本語 test".encode("ascii", "ignore").decode("ascii")`, runOptions{})
			require.Equal(t, "caf   test", result)
		})

		t.Run("str encode ascii replace", func(t *testing.T) {
			result := mustRun(t, b, `"héllo".encode("ascii", "replace")`, runOptions{})
			require.Equal(t, []byte{104, 63, 108, 108, 111}, coreABytes(t, result))
		})

		t.Run("bytes decode ascii backslashreplace", func(t *testing.T) {
			result := mustRun(t, b, `b"h\xe9llo".decode("ascii", "backslashreplace")`, runOptions{})
			require.Equal(t, `h\xe9llo`, result)
		})

		t.Run("list", func(t *testing.T) {
			require.Equal(t, []any{int64(1), int64(2), int64(3)}, mustRun(t, b, "x", coreAInputs(map[string]any{"x": []any{1, 2, 3}})))
			require.Equal(t, []any{}, mustRun(t, b, "x", coreAInputs(map[string]any{"x": []any{}})))
			require.Equal(t, []any{"a", "b"}, mustRun(t, b, "x", coreAInputs(map[string]any{"x": []any{"a", "b"}})))
		})

		t.Run("list output", func(t *testing.T) {
			require.Equal(t, []any{int64(1), int64(2), int64(3)}, mustRun(t, b, "[1, 2, 3]", runOptions{}))
		})

		t.Run("tuple", func(t *testing.T) {
			result := mustRun(t, b, "(1, 2, 3)", runOptions{})
			require.IsType(t, monty.Tuple{}, result)
			require.Equal(t, monty.Tuple{int64(1), int64(2), int64(3)}, result)
		})

		t.Run("tuple empty", func(t *testing.T) {
			result := mustRun(t, b, "()", runOptions{})
			require.IsType(t, monty.Tuple{}, result)
			require.Equal(t, monty.Tuple{}, result)
		})

		t.Run("dict", func(t *testing.T) {
			result := mustRun(t, b, `{"a": 1, "b": 2}`, runOptions{})
			require.IsType(t, &monty.Dict{}, result)
			dict := result.(*monty.Dict)
			a, ok := dict.Get("a")
			require.True(t, ok)
			require.Equal(t, int64(1), a)
			bv, ok := dict.Get("b")
			require.True(t, ok)
			require.Equal(t, int64(2), bv)
			require.Equal(t, 2, dict.Len())
		})

		t.Run("dict empty", func(t *testing.T) {
			result := mustRun(t, b, "{}", runOptions{})
			require.IsType(t, &monty.Dict{}, result)
			require.Equal(t, 0, result.(*monty.Dict).Len())
		})

		t.Run("set", func(t *testing.T) {
			result := mustRun(t, b, "{1, 2, 3}", runOptions{})
			require.IsType(t, &monty.Set{}, result)
			coreARequirePyEqual(t, monty.NewSet(int64(1), int64(2), int64(3)), result)
		})

		t.Run("set empty", func(t *testing.T) {
			result := mustRun(t, b, "set()", runOptions{})
			require.IsType(t, &monty.Set{}, result)
			coreARequirePyEqual(t, monty.NewSet(), result)
		})

		t.Run("frozenset", func(t *testing.T) {
			result := mustRun(t, b, "frozenset([1, 2, 3])", runOptions{})
			require.IsType(t, &monty.FrozenSet{}, result)
			coreARequirePyEqual(t, monty.NewFrozenSet(int64(1), int64(2), int64(3)), result)
		})

		t.Run("frozenset empty", func(t *testing.T) {
			result := mustRun(t, b, "frozenset()", runOptions{})
			require.IsType(t, &monty.FrozenSet{}, result)
			coreARequirePyEqual(t, monty.NewFrozenSet(), result)
		})

		t.Run("ellipsis input", func(t *testing.T) {
			require.Equal(t, true, mustRun(t, b, "x is ...", coreAInputs(map[string]any{"x": monty.Ellipsis})))
		})

		t.Run("ellipsis output", func(t *testing.T) {
			require.Equal(t, monty.Ellipsis, mustRun(t, b, "...", runOptions{}))
		})

		t.Run("nested list", func(t *testing.T) {
			nested := []any{
				[]any{1, 2},
				[]any{3, []any{4, 5}},
			}
			require.Equal(t, []any{
				[]any{int64(1), int64(2)},
				[]any{int64(3), []any{int64(4), int64(5)}},
			}, mustRun(t, b, "x", coreAInputs(map[string]any{"x": nested})))
		})

		t.Run("nested dict", func(t *testing.T) {
			result := mustRun(t, b, `{"list": [1, 2], "nested": {"a": 1}}`, runOptions{})
			require.IsType(t, &monty.Dict{}, result)
			dict := result.(*monty.Dict)
			list, _ := dict.Get("list")
			require.Equal(t, []any{int64(1), int64(2)}, list)
			nested, _ := dict.Get("nested")
			require.IsType(t, &monty.Dict{}, nested)
			a, _ := nested.(*monty.Dict).Get("a")
			require.Equal(t, int64(1), a)
		})

		t.Run("mixed nested", func(t *testing.T) {
			result := mustRun(t, b, `{"list": [1, 2], "tuple": (3, 4), "nested": {"set": {5, 6}}}`, runOptions{})
			require.IsType(t, &monty.Dict{}, result)
			dict := result.(*monty.Dict)
			list, _ := dict.Get("list")
			require.Equal(t, []any{int64(1), int64(2)}, list)
			tuple, _ := dict.Get("tuple")
			require.IsType(t, monty.Tuple{}, tuple)
			require.Equal(t, monty.Tuple{int64(3), int64(4)}, tuple)
			nested, _ := dict.Get("nested")
			require.IsType(t, &monty.Dict{}, nested)
			set, _ := nested.(*monty.Dict).Get("set")
			require.IsType(t, &monty.Set{}, set)
		})

		t.Run("nested set in list", func(t *testing.T) {
			result := mustRun(t, b, "[{1, 2}, {3, 4}]", runOptions{})
			require.IsType(t, []any{}, result)
			list := result.([]any)
			require.Len(t, list, 2)
			require.IsType(t, &monty.Set{}, list[0])
			require.IsType(t, &monty.Set{}, list[1])
			coreARequirePyEqual(t, monty.NewSet(int64(1), int64(2)), list[0])
			coreARequirePyEqual(t, monty.NewSet(int64(3), int64(4)), list[1])
		})

		t.Run("nested bytes in dict", func(t *testing.T) {
			result := mustRun(t, b, `{"data": b"abc"}`, runOptions{})
			require.IsType(t, &monty.Dict{}, result)
			data, _ := result.(*monty.Dict).Get("data")
			require.Equal(t, []byte{97, 98, 99}, coreABytes(t, data))
		})

		t.Run("tuple containing set", func(t *testing.T) {
			result := mustRun(t, b, `({1, 2}, "hello")`, runOptions{})
			require.IsType(t, monty.Tuple{}, result)
			tuple := result.(monty.Tuple)
			require.Len(t, tuple, 2)
			require.IsType(t, &monty.Set{}, tuple[0])
			coreARequirePyEqual(t, monty.NewSet(int64(1), int64(2)), tuple[0])
			require.Equal(t, "hello", tuple[1])
		})

		t.Run("datetime input preserves timezone presence", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, monty.CheckoutOptions{})
			datetime := monty.DateTime{Year: 2020, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Microsecond: 6}
			v, err := session.FeedRun(ctx, "x", &monty.FeedOptions{Inputs: map[string]any{"x": datetime}})
			require.NoError(t, err)
			require.Equal(t, datetime, v)
			v, err = session.FeedRun(ctx, "x.tzinfo is None", &monty.FeedOptions{Inputs: map[string]any{"x": datetime}})
			require.NoError(t, err)
			require.Equal(t, true, v)

			orphaned := datetime
			orphaned.TimezoneName = coreAString("orphaned")
			_, err = session.FeedRun(ctx, "x", &monty.FeedOptions{Inputs: map[string]any{"x": orphaned}})
			var conversionErr *monty.ConversionError
			require.ErrorAs(t, err, &conversionErr)
			require.EqualError(t, err, "MontyDateTime timezoneName requires offsetSeconds")
		})

		t.Run("bigint input", func(t *testing.T) {
			huge := coreATwoPow(100)
			coreARequireBigInt(t, huge, mustRun(t, b, "x", coreAInputs(map[string]any{"x": huge})))
		})

		t.Run("bigint output", func(t *testing.T) {
			coreARequireBigInt(t, coreATwoPow(100), mustRun(t, b, "2**100", runOptions{}))
		})

		t.Run("bigint negative input", func(t *testing.T) {
			bigNeg := new(big.Int).Neg(coreATwoPow(100))
			coreARequireBigInt(t, bigNeg, mustRun(t, b, "x", coreAInputs(map[string]any{"x": bigNeg})))
		})

		t.Run("int overflow to bigint", func(t *testing.T) {
			maxI64 := int64(math.MaxInt64)
			want := new(big.Int).Add(big.NewInt(maxI64), big.NewInt(1))
			coreARequireBigInt(t, want, mustRun(t, b, "x + 1", coreAInputs(map[string]any{"x": maxI64})))
		})

		t.Run("bigint arithmetic", func(t *testing.T) {
			huge := coreATwoPow(100)
			want := new(big.Int).Add(new(big.Int).Mul(huge, big.NewInt(2)), huge)
			coreARequireBigInt(t, want, mustRun(t, b, "x * 2 + y", coreAInputs(map[string]any{"x": huge, "y": huge})))
		})

		t.Run("bigint comparison", func(t *testing.T) {
			huge := coreATwoPow(100)
			require.Equal(t, true, mustRun(t, b, "x > y", coreAInputs(map[string]any{"x": huge, "y": 42})))
			require.Equal(t, false, mustRun(t, b, "x > y", coreAInputs(map[string]any{"x": 42, "y": huge})))
		})

		t.Run("bigint in collection", func(t *testing.T) {
			huge := coreATwoPow(100)
			double := new(big.Int).Mul(huge, big.NewInt(2))
			result := mustRun(t, b, "x", coreAInputs(map[string]any{"x": []any{huge, 42, double}}))
			require.IsType(t, []any{}, result)
			list := result.([]any)
			require.Len(t, list, 3)
			coreARequireBigInt(t, huge, list[0])
			require.Equal(t, int64(42), list[1])
			coreARequireBigInt(t, double, list[2])
		})

		t.Run("number at the i64 boundary", func(t *testing.T) {
			require.Equal(t, "float", mustRun(t, b, "type(x).__name__", coreAInputs(map[string]any{"x": math.Pow(2, 63)})))
			require.Equal(t, "int", mustRun(t, b, "type(x).__name__", coreAInputs(map[string]any{"x": int64(math.MinInt64)})))
			require.Equal(t, int64(math.MinInt64), mustRun(t, b, "x", coreAInputs(map[string]any{"x": int64(math.MinInt64)})))
			require.Equal(t, int64(math.MaxInt64), mustRun(t, b, "x", coreAInputs(map[string]any{"x": big.NewInt(math.MaxInt64)})))
			require.Equal(t, "int", mustRun(t, b, "type(x).__name__", coreAInputs(map[string]any{"x": uint64(1 << 63)})))
			coreARequireBigInt(t, coreATwoPow(63), mustRun(t, b, "x", coreAInputs(map[string]any{"x": uint64(1 << 63)})))
		})

		t.Run("time output from sandbox", func(t *testing.T) {
			require.Equal(t, monty.Time{Hour: 1, Minute: 2, Second: 3, Microsecond: 4, Fold: 0},
				mustRun(t, b, "import datetime\ndatetime.time(1, 2, 3, 4)", runOptions{}))
		})

		t.Run("aware time output from sandbox", func(t *testing.T) {
			code := `import datetime
datetime.time(6, 7, tzinfo=datetime.timezone(datetime.timedelta(hours=2), "P2"))`
			require.Equal(t, monty.Time{
				Hour:          6,
				Minute:        7,
				Second:        0,
				Microsecond:   0,
				OffsetSeconds: coreAInt32(7200),
				TimezoneName:  coreAString("P2"),
				Fold:          0,
			}, mustRun(t, b, code, runOptions{}))
		})

		t.Run("time input round-trips", func(t *testing.T) {
			time := monty.Time{Hour: 10, Minute: 20, Second: 30, Microsecond: 40, Fold: 1}
			require.Equal(t, time, mustRun(t, b, "x", coreAInputs(map[string]any{"x": time})))
		})

		t.Run("time input is a real sandbox time", func(t *testing.T) {
			time := monty.Time{Hour: 10, Minute: 20, Second: 0, Microsecond: 0}
			require.Equal(t, "time", mustRun(t, b, "type(x).__name__", coreAInputs(map[string]any{"x": time})))
			require.Equal(t, int64(620), mustRun(t, b, "x.hour * 60 + x.minute", coreAInputs(map[string]any{"x": time})))
			require.Equal(t, "10:20:00", mustRun(t, b, "x.isoformat()", coreAInputs(map[string]any{"x": time})))
		})

		t.Run("omitted fold defaults to zero", func(t *testing.T) {
			time := monty.Time{Hour: 1, Minute: 2, Second: 0, Microsecond: 0}
			require.Equal(t, int64(0), mustRun(t, b, "x.fold", coreAInputs(map[string]any{"x": time})))
		})
	})
}
