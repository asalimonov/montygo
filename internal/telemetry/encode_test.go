package telemetry

import (
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/asalimonov/montygo/internal/value"
)

func TestJSON(t *testing.T) {
	offset := int32(-5400)
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"none", nil, "null"},
		{"bool", true, "true"},
		{"int", int64(-42), "-42"},
		{"float", 1.5, "1.5"},
		{"whole float", 3.0, "3.0"},
		{"nan", math.NaN(), `"nan"`},
		{"negative infinity", math.Inf(-1), `"-inf"`},
		{"string escapes", "a\"b\\c\n\x01é", "\"a\\\"b\\\\c\\n\\u0001é\""},
		{"bytes", []byte("ab\x00"), `"ab\\x00"`},
		{"big int", new(big.Int).Lsh(big.NewInt(1), 70), "1180591620717411303424"},
		{"huge int", new(big.Int).Lsh(big.NewInt(1), 130), `"1361129467683753853853498429727072845824"`},
		{"list", []any{int64(1), "a", nil}, `[1,"a",null]`},
		{"tuple", value.Tuple{true}, `[true]`},
		{"named tuple", value.NamedTuple{TypeName: "t", FieldNames: []string{"a"}, Values: []any{int64(1)}}, `[1]`},
		{"dict", value.NewDict(value.Pair{Key: "a", Value: int64(1)}, value.Pair{Key: int64(2), Value: "b"}), `{"a":1,"2":"b"}`},
		{"date", value.Date{Year: 2024, Month: 1, Day: 2}, `"2024-01-02"`},
		{"datetime", value.DateTime{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Microsecond: 6, OffsetSeconds: &offset}, `"2024-01-02T03:04:05.000006-01:30"`},
		{"timedelta", value.TimeDelta{Days: 1, Seconds: 1, Microseconds: 500000}, "86401.5"},
		{"path", value.Path("/a"), `"/a"`},
		{"cycle", value.Cycle{}, `"<circular reference>"`},
		{"exception", value.Exception{ExcType: "ValueError", Message: "bad"}, `"bad"`},
		{"builtin type", value.Type{Name: "int", Origin: value.OriginBuiltin}, `"<class 'int'>"`},
		{"instance", value.Instance{Type: value.Type{Name: "P", ID: "t1", Origin: value.OriginHost}, ID: "i1", Attrs: value.NewDict(value.Pair{Key: "x", Value: int64(1)})}, `{"type":{"name":"P","id":"t1","host_defined":true,"is_dataclass":false,"attrs":{}},"id":"i1","attrs":{"x":1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, cut := JSON(tc.in)
			require.False(t, cut)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestJSONCap(t *testing.T) {
	got, cut := JSON([]any{strings.Repeat("a", AttrSizeLimit)})
	require.True(t, cut)
	require.LessOrEqual(t, len(got), AttrSizeLimit)

	got, cut = JSON(strings.Repeat("\x00", AttrSizeLimit))
	require.True(t, cut)
	require.LessOrEqual(t, len(got), AttrSizeLimit)
}

func TestTruncate(t *testing.T) {
	got, cut := Truncate(strings.Repeat("\x00", 70000))
	require.True(t, cut)
	require.Len(t, got, AttrSizeLimit)

	got, cut = Truncate(strings.Repeat("€", 30000))
	require.True(t, cut)
	require.Len(t, got, AttrSizeLimit-1)

	got, cut = Truncate("short")
	require.False(t, cut)
	require.Equal(t, "short", got)
}

func TestBytesText(t *testing.T) {
	got, cut := BytesText([]byte("a\x01"))
	require.False(t, cut)
	require.Equal(t, `a\x01`, got)

	got, cut = BytesText(make([]byte, AttrSizeLimit+1))
	require.True(t, cut)
	require.LessOrEqual(t, len(got), AttrSizeLimit)
}

func TestAttrValue(t *testing.T) {
	v, cut := AttrValue(int64(3))
	require.False(t, cut)
	require.Equal(t, attribute.INT64, v.Type())

	v, _ = AttrValue(math.Inf(1))
	require.Equal(t, "inf", v.AsString())

	v, cut = AttrValue(strings.Repeat("x", 70000))
	require.True(t, cut)
	require.Len(t, v.AsString(), AttrSizeLimit)

	v, _ = AttrValue([]any{int64(1)})
	require.Equal(t, "[1]", v.AsString())
}
