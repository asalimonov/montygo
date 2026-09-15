package value

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// StringRepr renders s the way CPython's repr(str) does.
func StringRepr(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteByte(quote)
		case r == utf8Invalid:
			b.WriteString(`�`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

const utf8Invalid = -1

// BytesRepr renders b the way CPython's repr(bytes) does.
func BytesRepr(data []byte) string {
	quote := byte('\'')
	if strings.IndexByte(string(data), '\'') >= 0 && strings.IndexByte(string(data), '"') < 0 {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte('b')
	b.WriteByte(quote)
	for _, c := range data {
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == quote:
			b.WriteByte('\\')
			b.WriteByte(quote)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// RustDebugString renders s the way Rust's `{:?}` formats a str.
func RustDebugString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		b.WriteString(rustEscape(r, '"'))
	}
	b.WriteByte('"')
	return b.String()
}

// RustDebugChar renders r the way Rust's `{:?}` formats a char.
func RustDebugChar(r rune) string {
	return "'" + rustEscape(r, '\'') + "'"
}

func rustEscape(r rune, quote rune) string {
	switch {
	case r == 0:
		return `\0`
	case r == '\t':
		return `\t`
	case r == '\n':
		return `\n`
	case r == '\r':
		return `\r`
	case r == '\\':
		return `\\`
	case r == quote:
		return `\` + string(quote)
	case unicode.IsPrint(r) || r == ' ':
		return string(r)
	default:
		return fmt.Sprintf(`\u{%x}`, r)
	}
}

// FloatRepr renders f the way CPython's repr(float) does.
func FloatRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	neg := strings.HasPrefix(mant, "-")
	mant = strings.TrimPrefix(mant, "-")
	digits := strings.Replace(mant, ".", "", 1)
	sign := ""
	if neg {
		sign = "-"
	}
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := fmt.Sprintf("%+03d", exp)
		return sign + m + "e" + es
	}
	if exp >= 0 {
		if len(digits) <= exp+1 {
			return sign + digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
		}
		return sign + digits[:exp+1] + "." + digits[exp+1:]
	}
	return sign + "0." + strings.Repeat("0", -exp-1) + digits
}

// Repr renders a value in Python repr style.
func Repr(v any) string {
	var b strings.Builder
	writeRepr(&b, v)
	return b.String()
}

func writeRepr(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("None")
	case bool:
		if x {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case *big.Int:
		b.WriteString(x.String())
	case float64:
		b.WriteString(FloatRepr(x))
	case float32:
		b.WriteString(FloatRepr(float64(x)))
	case string:
		b.WriteString(StringRepr(x))
	case []byte:
		b.WriteString(BytesRepr(x))
	case []any:
		b.WriteByte('[')
		writeItems(b, x)
		b.WriteByte(']')
	case Tuple:
		b.WriteByte('(')
		writeItems(b, x)
		if len(x) == 1 {
			b.WriteByte(',')
		}
		b.WriteByte(')')
	case *Dict:
		b.WriteByte('{')
		for i, p := range x.Pairs() {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, p.Key)
			b.WriteString(": ")
			writeRepr(b, p.Value)
		}
		b.WriteByte('}')
	case *Set:
		if x.Len() == 0 {
			b.WriteString("set()")
			return
		}
		b.WriteByte('{')
		writeItems(b, x.Items())
		b.WriteByte('}')
	case *FrozenSet:
		b.WriteString("frozenset(")
		if x.Len() > 0 {
			b.WriteByte('{')
			writeItems(b, x.Items())
			b.WriteByte('}')
		}
		b.WriteByte(')')
	case EllipsisType:
		b.WriteString("Ellipsis")
	case NotImplementedType:
		b.WriteString("NotImplemented")
	case Path:
		b.WriteString("PosixPath(" + StringRepr(string(x)) + ")")
	case NamedTuple:
		b.WriteString(x.TypeName + "(")
		for i, val := range x.Values {
			if i > 0 {
				b.WriteString(", ")
			}
			if i < len(x.FieldNames) {
				b.WriteString(x.FieldNames[i] + "=")
			}
			writeRepr(b, val)
		}
		b.WriteByte(')')
	case Exception:
		if x.Message == "" {
			b.WriteString(x.ExcType + "()")
		} else {
			b.WriteString(x.ExcType + "(" + StringRepr(x.Message) + ")")
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(StringRepr(k) + ": ")
			writeRepr(b, x[k])
		}
		b.WriteByte('}')
	case fmt.Stringer:
		b.WriteString(x.String())
	default:
		fmt.Fprintf(b, "%v", x)
	}
}

func writeItems(b *strings.Builder, items []any) {
	for i, it := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		writeRepr(b, it)
	}
}
