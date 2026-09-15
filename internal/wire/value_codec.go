package wire

import (
	"errors"
	"fmt"
	"math"
	"math/big"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/asalimonov/montygo/internal/value"
)

const (
	nodeHostSize     = 88
	maxDecodeNesting = 100
)

// Budget bounds the host memory a decoded frame may occupy.
type Budget struct {
	remaining int64
}

// NewBudget returns a budget of n bytes.
func NewBudget(n int64) *Budget { return &Budget{remaining: n} }

func (b *Budget) charge(n int) error {
	if b == nil {
		return nil
	}
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return ErrDecodeBudget
	}
	return nil
}

// ConversionError reports a Go value the wire cannot carry.
type ConversionError struct {
	Message string
}

func (e *ConversionError) Error() string { return e.Message }

// AppendValue appends the encoded MontyObject message body for v.
func AppendValue(b []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return appendMessage(b, 2, nil), nil
	case value.EllipsisType:
		return appendMessage(b, 1, nil), nil
	case value.NotImplementedType:
		return appendMessage(b, 3, nil), nil
	case bool:
		b = protowire.AppendTag(b, 4, protowire.VarintType)
		return protowire.AppendVarint(b, protowire.EncodeBool(x)), nil
	case int:
		return appendInt(b, int64(x)), nil
	case int8:
		return appendInt(b, int64(x)), nil
	case int16:
		return appendInt(b, int64(x)), nil
	case int32:
		return appendInt(b, int64(x)), nil
	case int64:
		return appendInt(b, x), nil
	case uint:
		return appendUint(b, uint64(x)), nil
	case uint8:
		return appendInt(b, int64(x)), nil
	case uint16:
		return appendInt(b, int64(x)), nil
	case uint32:
		return appendInt(b, int64(x)), nil
	case uint64:
		return appendUint(b, x), nil
	case *big.Int:
		if x == nil {
			return appendMessage(b, 2, nil), nil
		}
		return appendBig(b, x), nil
	case float32:
		b = protowire.AppendTag(b, 7, protowire.Fixed64Type)
		return protowire.AppendFixed64(b, math.Float64bits(float64(x))), nil
	case float64:
		b = protowire.AppendTag(b, 7, protowire.Fixed64Type)
		return protowire.AppendFixed64(b, math.Float64bits(x)), nil
	case string:
		return appendStringAlways(b, 8, x), nil
	case []byte:
		return appendBytesAlways(b, 9, x), nil
	case []any:
		return appendObjectList(b, 11, x)
	case value.Tuple:
		return appendObjectList(b, 12, x)
	case value.NamedTuple:
		body := appendStringField(nil, 1, x.TypeName)
		for _, f := range x.FieldNames {
			body = appendStringAlways(body, 2, f)
		}
		var err error
		for _, item := range x.Values {
			if body, err = appendValueField(body, 3, item); err != nil {
				return b, err
			}
		}
		return appendMessage(b, 13, body), nil
	case *value.Dict:
		body, err := appendDictBody(nil, x)
		if err != nil {
			return b, err
		}
		return appendMessage(b, 14, body), nil
	case *value.Set:
		return appendObjectList(b, 15, x.Items())
	case *value.FrozenSet:
		return appendObjectList(b, 16, x.Items())
	case value.Date:
		body := appendInt32Field(nil, 1, x.Year)
		body = appendUintField(body, 2, uint64(x.Month))
		body = appendUintField(body, 3, uint64(x.Day))
		return appendMessage(b, 17, body), nil
	case value.Time:
		body := appendUintField(nil, 1, uint64(x.Hour))
		body = appendUintField(body, 2, uint64(x.Minute))
		body = appendUintField(body, 3, uint64(x.Second))
		body = appendUintField(body, 4, uint64(x.Microsecond))
		if x.OffsetSeconds != nil {
			body = appendVarintField(body, 5, uint64(int64(*x.OffsetSeconds)))
		}
		if x.TimezoneName != nil {
			body = appendStringAlways(body, 6, *x.TimezoneName)
		}
		body = appendUintField(body, 7, uint64(x.Fold))
		return appendMessage(b, 18, body), nil
	case value.DateTime:
		body := appendInt32Field(nil, 1, x.Year)
		body = appendUintField(body, 2, uint64(x.Month))
		body = appendUintField(body, 3, uint64(x.Day))
		body = appendUintField(body, 4, uint64(x.Hour))
		body = appendUintField(body, 5, uint64(x.Minute))
		body = appendUintField(body, 6, uint64(x.Second))
		body = appendUintField(body, 7, uint64(x.Microsecond))
		if x.OffsetSeconds != nil {
			body = appendVarintField(body, 8, uint64(int64(*x.OffsetSeconds)))
		}
		if x.TimezoneName != nil {
			body = appendStringAlways(body, 9, *x.TimezoneName)
		}
		return appendMessage(b, 19, body), nil
	case value.TimeDelta:
		body := appendInt32Field(nil, 1, x.Days)
		body = appendInt32Field(body, 2, x.Seconds)
		body = appendInt32Field(body, 3, x.Microseconds)
		return appendMessage(b, 20, body), nil
	case value.TimeZone:
		return appendMessage(b, 21, appendTimeZoneBody(nil, x)), nil
	case value.Exception:
		body := appendStringField(nil, 1, x.ExcType)
		if x.Message != "" {
			body = appendStringAlways(body, 2, x.Message)
		}
		return appendMessage(b, 22, body), nil
	case value.Type:
		body, err := appendTypeBody(nil, x)
		if err != nil {
			return b, err
		}
		return appendMessage(b, 23, body), nil
	case value.Instance:
		return appendInstance(b, x)
	case *value.Instance:
		return appendInstance(b, *x)
	case value.Function:
		body := appendStringField(nil, 1, x.Name)
		if x.Docstring != nil {
			body = appendStringAlways(body, 2, *x.Docstring)
		}
		return appendMessage(b, 25, body), nil
	case value.BuiltinFunction:
		return appendStringAlways(b, 26, string(x)), nil
	case value.Path:
		return appendStringAlways(b, 27, string(x)), nil
	case *value.FileHandle:
		return appendFileHandle(b, *x), nil
	case value.FileHandle:
		return appendFileHandle(b, x), nil
	case value.Cycle:
		return b, &ConversionError{Message: "Cannot convert cycle marker to Monty value"}
	}
	return b, &ConversionError{Message: fmt.Sprintf("Cannot convert Go %T to Monty value", v)}
}

func appendValueField(b []byte, num protowire.Number, v any) ([]byte, error) {
	body, err := AppendValue(nil, v)
	if err != nil {
		return b, err
	}
	return appendMessage(b, num, body), nil
}

func appendInt(b []byte, x int64) []byte {
	b = protowire.AppendTag(b, 5, protowire.VarintType)
	return protowire.AppendVarint(b, protowire.EncodeZigZag(x))
}

func appendUint(b []byte, x uint64) []byte {
	if x <= math.MaxInt64 {
		return appendInt(b, int64(x))
	}
	return appendBig(b, new(big.Int).SetUint64(x))
}

func appendBig(b []byte, x *big.Int) []byte {
	if x.IsInt64() {
		return appendInt(b, x.Int64())
	}
	body := appendBoolField(nil, 1, x.Sign() < 0)
	body = appendBytesAlways(body, 2, new(big.Int).Abs(x).Bytes())
	return appendMessage(b, 6, body)
}

func appendObjectList(b []byte, num protowire.Number, items []any) ([]byte, error) {
	var body []byte
	var err error
	for _, item := range items {
		if body, err = appendValueField(body, 1, item); err != nil {
			return b, err
		}
	}
	return appendMessage(b, num, body), nil
}

func appendDictBody(b []byte, d *value.Dict) ([]byte, error) {
	for _, p := range d.Pairs() {
		pair, err := appendPairBody(nil, p)
		if err != nil {
			return b, err
		}
		b = appendMessage(b, 1, pair)
	}
	return b, nil
}

func appendPairBody(b []byte, p value.Pair) ([]byte, error) {
	b, err := appendValueField(b, 1, p.Key)
	if err != nil {
		return b, err
	}
	return appendValueField(b, 2, p.Value)
}

func appendTimeZoneBody(b []byte, tz value.TimeZone) []byte {
	b = appendInt32Field(b, 1, tz.OffsetSeconds)
	if tz.Name != nil {
		b = appendStringAlways(b, 2, *tz.Name)
	}
	return b
}

func appendTypeBody(b []byte, t value.Type) ([]byte, error) {
	b = appendStringField(b, 1, t.Name)
	if t.ID != "" {
		var err error
		if b, err = appendUUIDMessage(b, 2, t.ID); err != nil {
			return b, &ConversionError{Message: err.Error()}
		}
	}
	b = appendUintField(b, 3, uint64(t.Origin))
	b = appendBoolField(b, 4, t.IsDataclass)
	if t.Attrs.Len() > 0 {
		attrs, err := appendDictBody(nil, t.Attrs)
		if err != nil {
			return b, err
		}
		b = appendMessage(b, 5, attrs)
	}
	return b, nil
}

func appendInstance(b []byte, inst value.Instance) ([]byte, error) {
	typ, err := appendTypeBody(nil, inst.Type)
	if err != nil {
		return b, err
	}
	body := appendMessage(nil, 1, typ)
	if body, err = appendUUIDMessage(body, 2, inst.ID); err != nil {
		return b, &ConversionError{Message: err.Error()}
	}
	attrs, err := appendDictBody(nil, inst.Attrs)
	if err != nil {
		return b, err
	}
	body = appendMessage(body, 3, attrs)
	return appendMessage(b, 24, body), nil
}

func appendFileHandle(b []byte, h value.FileHandle) []byte {
	body := appendStringField(nil, 1, h.Path)
	body = appendStringField(body, 2, h.Mode)
	body = appendUintField(body, 3, h.Position)
	return appendMessage(b, 28, body)
}

// DecodeValue decodes a MontyObject message body.
func DecodeValue(msg []byte, budget *Budget) (any, error) {
	return decodeValue(msg, budget, 0)
}

func decodeValue(msg []byte, budget *Budget, depth int) (any, error) {
	if depth > maxDecodeNesting {
		return nil, errors.New("value nesting exceeds the recursion limit")
	}
	if err := budget.charge(nodeHostSize); err != nil {
		return nil, err
	}
	r := &reader{b: msg}
	var out any
	seen := false
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		seen = true
		var err error
		switch num {
		case 1:
			r.bytes(num, typ)
			out = value.Ellipsis
		case 2:
			r.bytes(num, typ)
			out = nil
		case 3:
			r.bytes(num, typ)
			out = value.NotImplemented
		case 4:
			out = r.varint(num, typ) != 0
		case 5:
			out = protowire.DecodeZigZag(r.varint(num, typ))
		case 6:
			out, err = decodeBig(r.bytes(num, typ), budget)
		case 7:
			out = math.Float64frombits(r.fixed64(num, typ))
		case 8:
			s := r.bytes(num, typ)
			err = budget.charge(len(s))
			out = string(s)
		case 9:
			s := r.bytes(num, typ)
			err = budget.charge(len(s))
			out = append([]byte{}, s...)
		case 10:
			r.bytes(num, typ)
			err = errors.New("uuid values are not supported")
		case 11:
			var items []any
			items, err = decodeObjectList(r.bytes(num, typ), budget, depth)
			if items == nil {
				items = []any{}
			}
			out = items
		case 12:
			var items []any
			items, err = decodeObjectList(r.bytes(num, typ), budget, depth)
			if items == nil {
				items = value.Tuple{}
			}
			out = value.Tuple(items)
		case 13:
			out, err = decodeNamedTuple(r.bytes(num, typ), budget, depth)
		case 14:
			out, err = decodeDict(r.bytes(num, typ), budget, depth)
		case 15:
			var items []any
			items, err = decodeObjectList(r.bytes(num, typ), budget, depth)
			s := &value.Set{}
			for _, it := range items {
				s.Append(it)
			}
			out = s
		case 16:
			var items []any
			items, err = decodeObjectList(r.bytes(num, typ), budget, depth)
			s := &value.FrozenSet{}
			for _, it := range items {
				s.Append(it)
			}
			out = s
		case 17:
			out, err = decodeDate(r.bytes(num, typ))
		case 18:
			out, err = decodeTime(r.bytes(num, typ))
		case 19:
			out, err = decodeDateTime(r.bytes(num, typ))
		case 20:
			out, err = decodeTimeDelta(r.bytes(num, typ))
		case 21:
			out, err = decodeTimeZone(r.bytes(num, typ))
		case 22:
			out, err = decodeExceptionValue(r.bytes(num, typ))
		case 23:
			out, err = decodeType(r.bytes(num, typ), budget, depth)
		case 24:
			out, err = decodeInstance(r.bytes(num, typ), budget, depth)
		case 25:
			out, err = decodeFunction(r.bytes(num, typ))
		case 26:
			out = value.BuiltinFunction(r.str(num, typ))
		case 27:
			out = value.Path(r.str(num, typ))
		case 28:
			out, err = decodeFileHandle(r.bytes(num, typ))
		case 29:
			s := r.bytes(num, typ)
			err = budget.charge(len(s))
			out = string(s)
		case 30:
			out, err = decodeCycle(r.bytes(num, typ))
		default:
			r.skip(num, typ)
			seen = false
		}
		if err != nil {
			return nil, err
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if !seen {
		return nil, errors.New("MontyObject carried no kind")
	}
	return out, nil
}

func decodeBig(msg []byte, budget *Budget) (any, error) {
	r := &reader{b: msg}
	neg := false
	var mag []byte
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			neg = r.varint(num, typ) != 0
		case 2:
			mag = r.bytes(num, typ)
		default:
			r.skip(num, typ)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if err := budget.charge(len(mag)); err != nil {
		return nil, err
	}
	x := new(big.Int).SetBytes(mag)
	if neg {
		x.Neg(x)
	}
	return value.IntValue(x), nil
}

func decodeObjectList(msg []byte, budget *Budget, depth int) ([]any, error) {
	r := &reader{b: msg}
	var items []any
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		if num != 1 {
			r.skip(num, typ)
			continue
		}
		v, err := decodeValue(r.bytes(num, typ), budget, depth+1)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, r.err
}

func decodeNamedTuple(msg []byte, budget *Budget, depth int) (any, error) {
	r := &reader{b: msg}
	var nt value.NamedTuple
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			nt.TypeName = r.str(num, typ)
		case 2:
			nt.FieldNames = append(nt.FieldNames, r.str(num, typ))
		case 3:
			v, err := decodeValue(r.bytes(num, typ), budget, depth+1)
			if err != nil {
				return nil, err
			}
			nt.Values = append(nt.Values, v)
		default:
			r.skip(num, typ)
		}
	}
	return nt, r.err
}

func decodeDict(msg []byte, budget *Budget, depth int) (*value.Dict, error) {
	r := &reader{b: msg}
	d := &value.Dict{}
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		if num != 1 {
			r.skip(num, typ)
			continue
		}
		k, v, err := decodePair(r.bytes(num, typ), budget, depth)
		if err != nil {
			return nil, err
		}
		d.Append(k, v)
	}
	return d, r.err
}

func decodePair(msg []byte, budget *Budget, depth int) (any, any, error) {
	r := &reader{b: msg}
	var key, val any
	var err error
	hasKey, hasVal := false, false
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			key, err = decodeValue(r.bytes(num, typ), budget, depth+1)
			hasKey = true
		case 2:
			val, err = decodeValue(r.bytes(num, typ), budget, depth+1)
			hasVal = true
		default:
			r.skip(num, typ)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if r.err != nil {
		return nil, nil, r.err
	}
	if !hasKey || !hasVal {
		return nil, nil, errors.New("dict pair is missing its key or value")
	}
	return key, val, nil
}

func decodeDate(msg []byte) (any, error) {
	r := &reader{b: msg}
	var d value.Date
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			d.Year = int32(r.varint(num, typ))
		case 2:
			d.Month = uint8(clampU32(r.varint(num, typ)))
		case 3:
			d.Day = uint8(clampU32(r.varint(num, typ)))
		default:
			r.skip(num, typ)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if err := validateDate(d.Year, d.Month, d.Day); err != nil {
		return nil, err
	}
	return d, nil
}

func clampU32(v uint64) uint64 {
	if v > 255 {
		return 255
	}
	return v
}

func validateDate(year int32, month, day uint8) error {
	if year < 1 || year > 9999 {
		return fmt.Errorf("year %d is out of range", year)
	}
	if month < 1 || month > 12 {
		return fmt.Errorf("month %d is out of range", month)
	}
	days := [...]uint8{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[month-1]
	if month == 2 && (year%4 == 0 && (year%100 != 0 || year%400 == 0)) {
		days = 29
	}
	if day < 1 || day > days {
		return fmt.Errorf("day %d is out of range for month", day)
	}
	return nil
}

func validateClock(hour, minute, second uint8, micro uint32, offset *int32, name *string) error {
	if hour > 23 || minute > 59 || second > 59 || micro > 999_999 {
		return errors.New("time component out of range")
	}
	if offset != nil && (*offset <= -86400 || *offset >= 86400) {
		return errors.New("utc offset out of range")
	}
	if name != nil && offset == nil {
		return errors.New("timezone name requires an offset")
	}
	return nil
}

func decodeTime(msg []byte) (any, error) {
	r := &reader{b: msg}
	var t value.Time
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			t.Hour = uint8(clampU32(r.varint(num, typ)))
		case 2:
			t.Minute = uint8(clampU32(r.varint(num, typ)))
		case 3:
			t.Second = uint8(clampU32(r.varint(num, typ)))
		case 4:
			t.Microsecond = uint32(r.varint(num, typ))
		case 5:
			off := int32(r.varint(num, typ))
			t.OffsetSeconds = &off
		case 6:
			name := r.str(num, typ)
			t.TimezoneName = &name
		case 7:
			t.Fold = uint8(clampU32(r.varint(num, typ)))
		default:
			r.skip(num, typ)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if err := validateClock(t.Hour, t.Minute, t.Second, t.Microsecond, t.OffsetSeconds, t.TimezoneName); err != nil {
		return nil, err
	}
	if t.Fold > 1 {
		return nil, errors.New("fold must be 0 or 1")
	}
	return t, nil
}

func decodeDateTime(msg []byte) (any, error) {
	r := &reader{b: msg}
	var d value.DateTime
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			d.Year = int32(r.varint(num, typ))
		case 2:
			d.Month = uint8(clampU32(r.varint(num, typ)))
		case 3:
			d.Day = uint8(clampU32(r.varint(num, typ)))
		case 4:
			d.Hour = uint8(clampU32(r.varint(num, typ)))
		case 5:
			d.Minute = uint8(clampU32(r.varint(num, typ)))
		case 6:
			d.Second = uint8(clampU32(r.varint(num, typ)))
		case 7:
			d.Microsecond = uint32(r.varint(num, typ))
		case 8:
			off := int32(r.varint(num, typ))
			d.OffsetSeconds = &off
		case 9:
			name := r.str(num, typ)
			d.TimezoneName = &name
		default:
			r.skip(num, typ)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if err := validateDate(d.Year, d.Month, d.Day); err != nil {
		return nil, err
	}
	if err := validateClock(d.Hour, d.Minute, d.Second, d.Microsecond, d.OffsetSeconds, d.TimezoneName); err != nil {
		return nil, err
	}
	return d, nil
}

func decodeTimeDelta(msg []byte) (any, error) {
	r := &reader{b: msg}
	var t value.TimeDelta
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			t.Days = int32(r.varint(num, typ))
		case 2:
			t.Seconds = int32(r.varint(num, typ))
		case 3:
			t.Microseconds = int32(r.varint(num, typ))
		default:
			r.skip(num, typ)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if t.Seconds < 0 || t.Seconds >= 86400 || t.Microseconds < 0 || t.Microseconds >= 1_000_000 || t.Days < -999_999_999 || t.Days > 999_999_999 {
		return nil, errors.New("timedelta is not normalized")
	}
	return t, nil
}

func decodeTimeZone(msg []byte) (value.TimeZone, error) {
	r := &reader{b: msg}
	var tz value.TimeZone
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			tz.OffsetSeconds = int32(r.varint(num, typ))
		case 2:
			name := r.str(num, typ)
			tz.Name = &name
		default:
			r.skip(num, typ)
		}
	}
	if r.err != nil {
		return tz, r.err
	}
	if tz.OffsetSeconds <= -86400 || tz.OffsetSeconds >= 86400 {
		return tz, errors.New("timezone offset out of range")
	}
	return tz, nil
}

func decodeExceptionValue(msg []byte) (any, error) {
	r := &reader{b: msg}
	var e value.Exception
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			e.ExcType = r.str(num, typ)
		case 2:
			e.Message = r.str(num, typ)
		default:
			r.skip(num, typ)
		}
	}
	return e, r.err
}

func decodeType(msg []byte, budget *Budget, depth int) (value.Type, error) {
	r := &reader{b: msg}
	var t value.Type
	var err error
	hasID := false
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			t.Name = r.str(num, typ)
		case 2:
			t.ID, err = decodeUUIDMessage(r.bytes(num, typ))
			hasID = true
		case 3:
			t.Origin = value.TypeOrigin(r.varint(num, typ))
		case 4:
			t.IsDataclass = r.varint(num, typ) != 0
		case 5:
			var attrs *value.Dict
			attrs, err = decodeDict(r.bytes(num, typ), budget, depth)
			if attrs.Len() > 0 {
				t.Attrs = attrs
			}
		default:
			r.skip(num, typ)
		}
		if err != nil {
			return t, err
		}
	}
	if r.err != nil {
		return t, r.err
	}
	switch t.Origin {
	case value.OriginBuiltin:
		if hasID {
			return t, errors.New("builtin type must not carry an id")
		}
	case value.OriginSandbox, value.OriginHost:
		if !hasID {
			return t, errors.New("class type is missing its id")
		}
	default:
		return t, errors.New("type origin is unspecified")
	}
	return t, nil
}

func decodeInstance(msg []byte, budget *Budget, depth int) (any, error) {
	r := &reader{b: msg}
	var inst value.Instance
	var err error
	hasType, hasID := false, false
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			inst.Type, err = decodeType(r.bytes(num, typ), budget, depth+1)
			hasType = true
		case 2:
			inst.ID, err = decodeUUIDMessage(r.bytes(num, typ))
			hasID = true
		case 3:
			var attrs *value.Dict
			attrs, err = decodeDict(r.bytes(num, typ), budget, depth)
			if attrs.Len() > 0 {
				inst.Attrs = attrs
			}
		default:
			r.skip(num, typ)
		}
		if err != nil {
			return nil, err
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if !hasType || !hasID {
		return nil, errors.New("class instance is missing its type or id")
	}
	if inst.Type.Origin == value.OriginBuiltin {
		return nil, errors.New("class instance type must not be builtin")
	}
	return inst, nil
}

func decodeFunction(msg []byte) (any, error) {
	r := &reader{b: msg}
	var f value.Function
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			f.Name = r.str(num, typ)
		case 2:
			doc := r.str(num, typ)
			f.Docstring = &doc
		default:
			r.skip(num, typ)
		}
	}
	return f, r.err
}

func decodeFileHandle(msg []byte) (any, error) {
	r := &reader{b: msg}
	h := &value.FileHandle{}
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			h.Path = r.str(num, typ)
		case 2:
			h.Mode = r.str(num, typ)
		case 3:
			h.Position = r.varint(num, typ)
		default:
			r.skip(num, typ)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if !validHandleMode(h.Mode) {
		return nil, fmt.Errorf("invalid file handle mode %q", h.Mode)
	}
	return h, nil
}

func validHandleMode(mode string) bool {
	switch mode {
	case "r", "rb", "r+", "rb+", "w", "wb", "w+", "wb+", "a", "ab", "a+", "ab+":
		return true
	}
	return false
}

func decodeCycle(msg []byte) (any, error) {
	r := &reader{b: msg}
	var c value.Cycle
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			c.Identity = r.varint(num, typ)
		case 2:
			c.Placeholder = r.str(num, typ)
		default:
			r.skip(num, typ)
		}
	}
	return c, r.err
}
