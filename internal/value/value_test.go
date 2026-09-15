package value

import (
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEqualUsesPythonSemantics(t *testing.T) {
	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	for _, c := range []struct {
		a, b any
		want bool
	}{
		{true, int64(1), true},
		{int64(1), 1.0, true},
		{int(3), uint8(3), true},
		{huge, new(big.Int).Set(huge), true},
		{huge, 1.2345678901234568e29, false},
		{1.5, int64(1), false},
		{"a", "a", true},
		{"a", []byte("a"), false},
		{Tuple{int64(1), "x"}, Tuple{1.0, "x"}, true},
		{[]any{int64(1)}, Tuple{int64(1)}, false},
		{NewDict(Pair{"a", int64(1)}, Pair{"b", int64(2)}), NewDict(Pair{"b", 2.0}, Pair{"a", true}), true},
		{NewSet(int64(1), int64(2)), NewFrozenSet(int64(2), int64(1)), true},
		{nil, nil, true},
		{nil, int64(0), false},
		{Date{2024, 1, 2}, Date{2024, 1, 2}, true},
		{math.NaN(), math.NaN(), false},
	} {
		require.Equal(t, c.want, Equal(c.a, c.b), "%#v == %#v", c.a, c.b)
	}
}

func TestDictKeepsInsertionOrder(t *testing.T) {
	d := NewDict(Pair{"b", int64(1)}, Pair{"a", int64(2)}, Pair{"b", int64(3)})
	require.Equal(t, []any{"b", "a"}, d.Keys())
	require.Equal(t, []any{int64(3), int64(2)}, d.Values())
	v, ok := d.Get(true)
	require.False(t, ok)
	require.Nil(t, v)
	d.Set(int64(1), "one")
	v, ok = d.Get(true)
	require.True(t, ok)
	require.Equal(t, "one", v)
	require.True(t, d.Delete(1.0))
	require.False(t, d.Has(int64(1)))
	m, ok := d.StringMap()
	require.True(t, ok)
	require.Equal(t, map[string]any{"b": int64(3), "a": int64(2)}, m)
	require.Equal(t, 0, (*Dict)(nil).Len())
	require.Equal(t, NewDict(), func() *Dict { x := NewDict(Pair{"k", nil}); x.Delete("k"); return x }())
}

func TestRepr(t *testing.T) {
	d := NewDict(Pair{"s", "it's"}, Pair{int64(1), Tuple{1.0}}, Pair{"b", []byte("a\x00'")})
	require.Equal(t, `{'s': "it's", 1: (1.0,), 'b': b"a\x00'"}`, Repr(d))
	require.Equal(t, "set()", Repr(NewSet()))
	require.Equal(t, "frozenset({1})", Repr(NewFrozenSet(int64(1))))
	require.Equal(t, "[None, True, Ellipsis, PosixPath('/x')]", Repr([]any{nil, true, Ellipsis, Path("/x")}))
	require.Equal(t, `'a\nb\t\\'`, StringRepr("a\nb\t\\"))
	require.Equal(t, `"/data\0"`, RustDebugString("/data\x00"))
	require.Equal(t, `'q'`, RustDebugChar('q'))
	for f, want := range map[float64]string{
		1: "1.0", 0.1: "0.1", 1e16: "1e+16", 1e15: "1000000000000000.0", 0.0001: "0.0001", 1e-05: "1e-05",
		-2.5: "-2.5", math.Inf(1): "inf", 123456789.125: "123456789.125",
	} {
		require.Equal(t, want, FloatRepr(f), "%v", f)
	}
}

func TestCanonicalFileMode(t *testing.T) {
	for mode, want := range map[string]string{"r": "r", "rt": "r", "br": "rb", "w": "w", "ab": "ab", "tw": "w"} {
		got, err := CanonicalFileMode(mode)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for mode, msg := range map[string]string{
		"":    "Must have exactly one of create/read/write/append mode and at most one plus",
		"b":   "Must have exactly one of create/read/write/append mode and at most one plus",
		"rw":  "must have exactly one of create/read/write/append mode",
		"x":   "exclusive creation mode is not supported",
		"rbb": "invalid mode: binary mode specified twice",
		"rtt": "invalid mode: text mode specified twice",
		"r+":  "update modes ('+') are not yet supported",
		"q":   "invalid mode: 'q'",
		"rbt": "can't have text and binary mode at once",
	} {
		_, err := CanonicalFileMode(mode)
		require.EqualError(t, err, msg, "mode %q", mode)
	}
	h, err := NewFileHandle("/f", "br", 3)
	require.NoError(t, err)
	require.True(t, h.Binary())
	require.True(t, h.Readable())
	require.False(t, h.Writable())
	_, err = NewFileHandle("/f", "r", MaxSafeInteger+1)
	require.Error(t, err)
}

func TestDepthBudget(t *testing.T) {
	nest := func(n int, wrap func(any) any) any {
		var v any = int64(1)
		for i := 0; i < n; i++ {
			v = wrap(v)
		}
		return v
	}
	list := func(v any) any { return []any{v} }
	dict := func(v any) any { return NewDict(Pair{"k", v}) }
	require.False(t, ExceedsMaxDepth(nest(MaxValueDepth, list)))
	require.True(t, ExceedsMaxDepth(nest(MaxValueDepth+1, list)))
	require.False(t, ExceedsMaxDepth(nest(32, dict)))
	require.True(t, ExceedsMaxDepth(nest(33, dict)))
	require.Equal(t, 48, MaxValueDepth)
}

func TestVirtualPaths(t *testing.T) {
	for in, want := range map[string]string{
		"/mnt/subdir/../hello.txt": "/mnt/hello.txt", "/mnt/./subdir": "/mnt/subdir", "/mnt/": "/mnt",
		"": "/", "..": "/", "/../..": "/", "data": "/data", "/": "/", "/a//b": "/a/b",
	} {
		require.Equal(t, want, NormalizeVirtualPath(in), in)
	}
	got, err := ValidateCwd("/work//")
	require.NoError(t, err)
	require.Equal(t, "/work", got)
	got, err = ValidateCwd("///")
	require.NoError(t, err)
	require.Equal(t, "/", got)
	_, err = ValidateCwd("data")
	require.EqualError(t, err, `cwd must be an absolute POSIX path: "data"`)
	_, err = ValidateCwd("/data\x00")
	require.EqualError(t, err, `cwd must not contain NUL bytes: "/data\0"`)
}

func TestTemporalHelpers(t *testing.T) {
	require.Equal(t, TimeDelta{Days: -1, Seconds: 86399, Microseconds: 999_999}, TimeDeltaFromDuration(-time.Microsecond))
	require.Equal(t, TimeDelta{Days: 1, Seconds: 1, Microseconds: 0}, NormalizeTimeDelta(0, 86401, 0))
	require.Equal(t, 25*time.Hour, TimeDelta{Days: 1, Seconds: 3600}.Duration())
	ts := time.Date(2024, 2, 29, 10, 30, 5, 123456000, time.FixedZone("CET", 3600))
	dt := DateTimeFromTime(ts)
	require.Equal(t, int32(3600), *dt.OffsetSeconds)
	require.Equal(t, "CET", *dt.TimezoneName)
	require.True(t, dt.Time().Equal(ts))
	naive := NaiveDateTimeFromTime(ts)
	require.Nil(t, naive.OffsetSeconds)
	require.Equal(t, uint32(123456), naive.Microsecond)
}

func TestPyTypeName(t *testing.T) {
	for v, want := range map[any]string{nil: "NoneType", true: "bool", int64(1): "int", 1.5: "float", "s": "str", Ellipsis: "ellipsis", Path("/"): "PosixPath"} {
		require.Equal(t, want, PyTypeName(v))
	}
	require.Equal(t, "tuple", PyTypeName(Tuple{}))
	require.Equal(t, "list", PyTypeName([]string{}))
	require.Equal(t, "dict", PyTypeName(map[string]int{}))
	require.Equal(t, "datetime", PyTypeName(DateTime{}))
	require.Equal(t, "Point", PyTypeName(Instance{Type: Type{Name: "Point"}}))
}
