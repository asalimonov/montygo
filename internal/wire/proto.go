package wire

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

type reader struct {
	b   []byte
	err error
}

func (r *reader) next() (protowire.Number, protowire.Type, bool) {
	if r.err != nil || len(r.b) == 0 {
		return 0, 0, false
	}
	num, typ, n := protowire.ConsumeTag(r.b)
	if n < 0 {
		r.err = protowire.ParseError(n)
		return 0, 0, false
	}
	r.b = r.b[n:]
	return num, typ, true
}

func (r *reader) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}

func (r *reader) wantType(num protowire.Number, got, want protowire.Type) bool {
	if got != want {
		r.fail("field %d: wire type %d, expected %d", num, got, want)
		return false
	}
	return true
}

func (r *reader) bytes(num protowire.Number, typ protowire.Type) []byte {
	if !r.wantType(num, typ, protowire.BytesType) {
		return nil
	}
	v, n := protowire.ConsumeBytes(r.b)
	if n < 0 {
		r.err = protowire.ParseError(n)
		return nil
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) str(num protowire.Number, typ protowire.Type) string {
	return string(r.bytes(num, typ))
}

func (r *reader) varint(num protowire.Number, typ protowire.Type) uint64 {
	if !r.wantType(num, typ, protowire.VarintType) {
		return 0
	}
	v, n := protowire.ConsumeVarint(r.b)
	if n < 0 {
		r.err = protowire.ParseError(n)
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) fixed64(num protowire.Number, typ protowire.Type) uint64 {
	if !r.wantType(num, typ, protowire.Fixed64Type) {
		return 0
	}
	v, n := protowire.ConsumeFixed64(r.b)
	if n < 0 {
		r.err = protowire.ParseError(n)
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) skip(num protowire.Number, typ protowire.Type) {
	n := protowire.ConsumeFieldValue(num, typ, r.b)
	if n < 0 {
		r.err = protowire.ParseError(n)
		return
	}
	r.b = r.b[n:]
}

// uint32s reads a repeated uint32 field in packed or unpacked form.
func (r *reader) uint32s(num protowire.Number, typ protowire.Type, dst []uint32) []uint32 {
	if typ == protowire.VarintType {
		return append(dst, uint32(r.varint(num, typ)))
	}
	packed := r.bytes(num, typ)
	for len(packed) > 0 {
		v, n := protowire.ConsumeVarint(packed)
		if n < 0 {
			r.err = protowire.ParseError(n)
			return dst
		}
		dst = append(dst, uint32(v))
		packed = packed[n:]
	}
	return dst
}

func appendVarintField(b []byte, num protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendBoolField(b []byte, num protowire.Number, v bool) []byte {
	if !v {
		return b
	}
	return appendVarintField(b, num, 1)
}

func appendUintField(b []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	return appendVarintField(b, num, v)
}

func appendInt32Field(b []byte, num protowire.Number, v int32) []byte {
	if v == 0 {
		return b
	}
	return appendVarintField(b, num, uint64(int64(v)))
}

func appendStringField(b []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendStringAlways(b []byte, num protowire.Number, s string) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendBytesAlways(b []byte, num protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func appendMessage(b []byte, num protowire.Number, msg []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, msg)
}

// ParseUUID converts a canonical uuid string into 16 bytes.
func ParseUUID(s string) ([]byte, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return nil, fmt.Errorf("invalid uuid %q", s)
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(raw) != 16 {
		return nil, fmt.Errorf("invalid uuid %q", s)
	}
	return raw, nil
}

// FormatUUID renders 16 bytes as a canonical lowercase uuid string.
func FormatUUID(b []byte) (string, error) {
	if len(b) != 16 {
		return "", errors.New("uuid must be exactly 16 bytes")
	}
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func decodeUUIDMessage(msg []byte) (string, error) {
	r := &reader{b: msg}
	var data []byte
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		if num == 1 {
			data = r.bytes(num, typ)
		} else {
			r.skip(num, typ)
		}
	}
	if r.err != nil {
		return "", r.err
	}
	return FormatUUID(data)
}

func appendUUIDMessage(b []byte, num protowire.Number, id string) ([]byte, error) {
	raw, err := ParseUUID(id)
	if err != nil {
		return b, err
	}
	return appendMessage(b, num, appendBytesAlways(nil, 1, raw)), nil
}
