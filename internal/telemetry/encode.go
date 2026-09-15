package telemetry

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

// AttrSizeLimit caps one attribute value in bytes.
const AttrSizeLimit = 64 * 1024

// Truncate cuts s to AttrSizeLimit bytes on a rune boundary; the bool reports a cut.
func Truncate(s string) (string, bool) {
	if len(s) <= AttrSizeLimit {
		return s, false
	}
	end := AttrSizeLimit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end], true
}

// BytesText renders b as its repr without the b” wrapper, capped at AttrSizeLimit.
func BytesText(b []byte) (string, bool) {
	head := b[:min(len(b), AttrSizeLimit)]
	text, cut := Truncate(bytesContent(head))
	return text, cut || len(head) < len(b)
}

func bytesContent(b []byte) string {
	repr := value.BytesRepr(b)
	return repr[2 : len(repr)-1]
}

// NonFinite is Python's str() of a NaN or infinite float.
func NonFinite(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case f > 0:
		return "inf"
	}
	return "-inf"
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// AttrValue renders v as a typed attribute value: booleans, integers, finite
// floats and strings keep their type, everything else becomes JSON text.
func AttrValue(v any) (attribute.Value, bool) {
	switch x := v.(type) {
	case bool:
		return attribute.BoolValue(x), false
	case int64:
		return attribute.Int64Value(x), false
	case int:
		return attribute.Int64Value(int64(x)), false
	case float64:
		if !finite(x) {
			return attribute.StringValue(NonFinite(x)), false
		}
		return attribute.Float64Value(x), false
	case string:
		s, cut := Truncate(x)
		return attribute.StringValue(s), cut
	case []byte:
		s, cut := BytesText(x)
		return attribute.StringValue(s), cut
	}
	s, cut := JSON(v)
	return attribute.StringValue(s), cut
}

// JSON encodes v the way the logfire SDKs encode attributes, capped at
// AttrSizeLimit; the bool reports a cut, after which the text is not valid JSON.
func JSON(v any) (string, bool) { return encode(func(e *encoder) { e.value(v) }) }

// JSONSeq encodes items as a JSON array.
func JSONSeq(items []any) (string, bool) { return encode(func(e *encoder) { e.seq(items) }) }

// JSONPairs encodes dict pairs as a JSON object.
func JSONPairs(pairs []value.Pair) (string, bool) {
	return encode(func(e *encoder) { e.pairs(pairs) })
}

// JSONNamed encodes named values as a JSON object.
func JSONNamed(values []wire.NamedValue) (string, bool) {
	return encode(func(e *encoder) {
		e.write("{")
		for i, nv := range values {
			if i > 0 {
				e.write(",")
			}
			e.str(nv.Name)
			e.write(":")
			e.value(nv.Value)
		}
		e.write("}")
	})
}

// JSONUint32s encodes ids as a JSON array.
func JSONUint32s(ids []uint32) (string, bool) {
	return encode(func(e *encoder) {
		e.write("[")
		for i, id := range ids {
			if i > 0 {
				e.write(",")
			}
			e.write(strconv.FormatUint(uint64(id), 10))
		}
		e.write("]")
	})
}

func encode(fn func(*encoder)) (string, bool) {
	e := &encoder{limit: AttrSizeLimit}
	fn(e)
	return string(e.buf), e.cut
}

type encoder struct {
	buf   []byte
	limit int
	cut   bool
}

func (e *encoder) write(s string) {
	if e.cut {
		return
	}
	if len(e.buf)+len(s) > e.limit {
		e.cut = true
		return
	}
	e.buf = append(e.buf, s...)
}

const hexDigits = "0123456789abcdef"

func (e *encoder) str(s string) {
	e.write(`"`)
	start := 0
	for i := 0; i < len(s) && !e.cut; i++ {
		var esc string
		switch c := s[i]; {
		case c == '"':
			esc = `\"`
		case c == '\\':
			esc = `\\`
		case c == '\n':
			esc = `\n`
		case c == '\r':
			esc = `\r`
		case c == '\t':
			esc = `\t`
		case c == '\b':
			esc = `\b`
		case c == '\f':
			esc = `\f`
		case c < 0x20:
			esc = string([]byte{'\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf]})
		default:
			continue
		}
		if start < i {
			e.write(s[start:i])
		}
		e.write(esc)
		start = i + 1
	}
	if start < len(s) {
		e.write(s[start:])
	}
	e.write(`"`)
}

func (e *encoder) float(f float64) {
	if finite(f) {
		e.write(value.FloatRepr(f))
		return
	}
	e.str(NonFinite(f))
}

func (e *encoder) seq(items []any) {
	e.write("[")
	for i, it := range items {
		if i > 0 {
			e.write(",")
		}
		e.value(it)
	}
	e.write("]")
}

func (e *encoder) pairs(pairs []value.Pair) {
	e.write("{")
	for i, p := range pairs {
		if i > 0 {
			e.write(",")
		}
		if k, ok := p.Key.(string); ok {
			e.str(k)
		} else {
			e.str(value.Repr(p.Key))
		}
		e.write(":")
		e.value(p.Value)
	}
	e.write("}")
}

func (e *encoder) dict(d *value.Dict) {
	if d == nil {
		e.write("{}")
		return
	}
	e.pairs(d.Pairs())
}

func (e *encoder) classType(t value.Type) {
	e.write(`{"name":`)
	e.str(t.Name)
	e.write(`,"id":`)
	e.str(t.ID)
	e.write(`,"host_defined":` + strconv.FormatBool(t.Origin == value.OriginHost))
	e.write(`,"is_dataclass":` + strconv.FormatBool(t.IsDataclass))
	e.write(`,"attrs":`)
	e.dict(t.Attrs)
	e.write("}")
}

func (e *encoder) value(v any) {
	if e.cut {
		return
	}
	switch x := v.(type) {
	case nil:
		e.write("null")
	case bool:
		e.write(strconv.FormatBool(x))
	case int:
		e.write(strconv.FormatInt(int64(x), 10))
	case int8:
		e.write(strconv.FormatInt(int64(x), 10))
	case int16:
		e.write(strconv.FormatInt(int64(x), 10))
	case int32:
		e.write(strconv.FormatInt(int64(x), 10))
	case int64:
		e.write(strconv.FormatInt(x, 10))
	case uint:
		e.write(strconv.FormatUint(uint64(x), 10))
	case uint8:
		e.write(strconv.FormatUint(uint64(x), 10))
	case uint16:
		e.write(strconv.FormatUint(uint64(x), 10))
	case uint32:
		e.write(strconv.FormatUint(uint64(x), 10))
	case uint64:
		e.write(strconv.FormatUint(x, 10))
	case *big.Int:
		switch {
		case x == nil:
			e.write("null")
		case x.BitLen() < 128:
			e.write(x.String())
		default:
			e.str(x.String())
		}
	case float64:
		e.float(x)
	case float32:
		e.float(float64(x))
	case string:
		e.str(x)
	case []byte:
		e.str(bytesContent(x[:min(len(x), e.limit)]))
	case []any:
		e.seq(x)
	case value.Tuple:
		e.seq(x)
	case *value.Set:
		e.seq(x.Items())
	case *value.FrozenSet:
		e.seq(x.Items())
	case value.NamedTuple:
		e.seq(x.Values)
	case *value.Dict:
		e.dict(x)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		e.write("{")
		for i, k := range keys {
			if i > 0 {
				e.write(",")
			}
			e.str(k)
			e.write(":")
			e.value(x[k])
		}
		e.write("}")
	case value.Instance:
		e.write(`{"type":`)
		e.classType(x.Type)
		e.write(`,"id":`)
		e.str(x.ID)
		e.write(`,"attrs":`)
		e.dict(x.Attrs)
		e.write("}")
	case value.Type:
		if x.Origin == value.OriginSandbox || x.Origin == value.OriginHost {
			e.classType(x)
		} else {
			e.str("<class '" + x.Name + "'>")
		}
	case value.Date:
		e.str(fmt.Sprintf("%04d-%02d-%02d", x.Year, x.Month, x.Day))
	case value.DateTime:
		e.str(datetimeISO(x))
	case value.Time:
		e.str(clockISO(x.Hour, x.Minute, x.Second, x.Microsecond, x.OffsetSeconds))
	case value.TimeDelta:
		e.float(float64(x.Days)*86400 + float64(x.Seconds) + float64(x.Microseconds)/1e6)
	case value.Exception:
		e.str(x.Message)
	case value.Path:
		e.str(string(x))
	case value.Cycle:
		e.str("<circular reference>")
	default:
		e.str(value.Repr(x))
	}
}

func datetimeISO(d value.DateTime) string {
	return fmt.Sprintf("%04d-%02d-%02dT", d.Year, d.Month, d.Day) + clockISO(d.Hour, d.Minute, d.Second, d.Microsecond, d.OffsetSeconds)
}

func clockISO(hour, minute, second uint8, micro uint32, offset *int32) string {
	s := fmt.Sprintf("%02d:%02d:%02d", hour, minute, second)
	if micro != 0 {
		s += fmt.Sprintf(".%06d", micro)
	}
	if offset != nil {
		sign, abs := '+', int64(*offset)
		if abs < 0 {
			sign, abs = '-', -abs
		}
		s += fmt.Sprintf("%c%02d:%02d", sign, abs/3600, abs%3600/60)
		if abs%60 != 0 {
			s += fmt.Sprintf(":%02d", abs%60)
		}
	}
	return s
}

// JSONStrings encodes items as a JSON array.
func JSONStrings(items []string) (string, bool) {
	return encode(func(e *encoder) {
		e.write("[")
		for i, it := range items {
			if i > 0 {
				e.write(",")
			}
			e.str(it)
		}
		e.write("]")
	})
}

type cappedText struct {
	buf []byte
	cut bool
}

func (c *cappedText) write(s string) {
	if c.cut {
		return
	}
	if remaining := AttrSizeLimit - len(c.buf); len(s) > remaining {
		end := remaining
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		c.buf = append(c.buf, s[:end]...)
		c.cut = true
		return
	}
	c.buf = append(c.buf, s...)
}

func (c *cappedText) String() string { return string(c.buf) }
