// Package value defines the Go representation of Python values crossing the
// Monty sandbox boundary.
package value

import (
	"fmt"
	"math/big"
	"strings"
	"time"
)

// EllipsisType is the type of the Python Ellipsis singleton.
type EllipsisType struct{}

// NotImplementedType is the type of the Python NotImplemented singleton.
type NotImplementedType struct{}

var (
	Ellipsis       = EllipsisType{}
	NotImplemented = NotImplementedType{}
)

func (EllipsisType) String() string       { return "Ellipsis" }
func (NotImplementedType) String() string { return "NotImplemented" }

// Tuple is a Python tuple.
type Tuple []any

// Date is a Python datetime.date.
type Date struct {
	Year  int32
	Month uint8
	Day   uint8
}

// DateTime is a Python datetime.datetime; OffsetSeconds is nil for naive values.
type DateTime struct {
	Year          int32
	Month         uint8
	Day           uint8
	Hour          uint8
	Minute        uint8
	Second        uint8
	Microsecond   uint32
	OffsetSeconds *int32
	TimezoneName  *string
}

// Time is a Python datetime.time.
type Time struct {
	Hour          uint8
	Minute        uint8
	Second        uint8
	Microsecond   uint32
	OffsetSeconds *int32
	TimezoneName  *string
	Fold          uint8
}

// TimeDelta is a normalized Python datetime.timedelta.
type TimeDelta struct {
	Days         int32
	Seconds      int32
	Microseconds int32
}

// TimeZone is a fixed-offset Python datetime.timezone.
type TimeZone struct {
	OffsetSeconds int32
	Name          *string
}

// Exception is a Python exception value without a traceback.
type Exception struct {
	ExcType string
	Message string
}

// TypeOrigin says where a Type was defined.
type TypeOrigin uint8

const (
	OriginUnspecified TypeOrigin = iota
	OriginBuiltin
	OriginSandbox
	OriginHost
)

// Type is a Python type object: a builtin, a sandbox class or a host class.
type Type struct {
	Name        string
	ID          string
	Origin      TypeOrigin
	IsDataclass bool
	Attrs       *Dict
}

// Instance is the wire form of a class instance.
type Instance struct {
	Type  Type
	ID    string
	Attrs *Dict
}

// Function is an external function value.
type Function struct {
	Name      string
	Docstring *string
}

// BuiltinFunction is a Python builtin function named by its Python name.
type BuiltinFunction string

// Path is a pathlib path (always a virtual POSIX path).
type Path string

// NamedTuple is a Python named tuple such as os.stat_result.
type NamedTuple struct {
	TypeName   string
	FieldNames []string
	Values     []any
}

// Get returns the value of a named field.
func (n NamedTuple) Get(field string) (any, bool) {
	for i, f := range n.FieldNames {
		if f == field && i < len(n.Values) {
			return n.Values[i], true
		}
	}
	return nil, false
}

// Cycle marks a reference cycle in a container result.
type Cycle struct {
	Identity    uint64
	Placeholder string
}

// FileHandle is a sandbox file handle described only by virtual state.
type FileHandle struct {
	Path     string
	Mode     string
	Position uint64
}

// MaxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER, the bound upstream applies to file positions.
const MaxSafeInteger = 1<<53 - 1

// NewFileHandle validates and canonicalizes a file handle.
func NewFileHandle(path, mode string, position uint64) (*FileHandle, error) {
	canonical, err := CanonicalFileMode(mode)
	if err != nil {
		return nil, err
	}
	if position > MaxSafeInteger {
		return nil, fmt.Errorf("MontyFileHandle position must be a non-negative safe integer")
	}
	return &FileHandle{Path: path, Mode: canonical, Position: position}, nil
}

func (h *FileHandle) Binary() bool { return strings.Contains(h.Mode, "b") }

func (h *FileHandle) Readable() bool {
	return strings.HasPrefix(h.Mode, "r") || strings.Contains(h.Mode, "+")
}

func (h *FileHandle) Writable() bool {
	return strings.HasPrefix(h.Mode, "w") || strings.HasPrefix(h.Mode, "a") || strings.Contains(h.Mode, "+")
}

// BigIntFromInt64 returns x as a *big.Int.
func BigIntFromInt64(x int64) *big.Int { return big.NewInt(x) }

// DateTimeFromTime converts a time.Time into an aware DateTime carrying its zone offset.
func DateTimeFromTime(t time.Time) DateTime {
	name, offset := t.Zone()
	off := int32(offset)
	dt := DateTime{
		Year: int32(t.Year()), Month: uint8(t.Month()), Day: uint8(t.Day()),
		Hour: uint8(t.Hour()), Minute: uint8(t.Minute()), Second: uint8(t.Second()),
		Microsecond:   uint32(t.Nanosecond() / 1000),
		OffsetSeconds: &off,
	}
	if name != "" && name != "UTC" && !strings.HasPrefix(name, "+") && !strings.HasPrefix(name, "-") {
		dt.TimezoneName = &name
	}
	return dt
}

// NaiveDateTimeFromTime converts the wall clock of t into a naive DateTime.
func NaiveDateTimeFromTime(t time.Time) DateTime {
	dt := DateTimeFromTime(t)
	dt.OffsetSeconds = nil
	dt.TimezoneName = nil
	return dt
}

// Time converts the DateTime into a time.Time; naive values are interpreted as UTC.
func (d DateTime) Time() time.Time {
	loc := time.UTC
	if d.OffsetSeconds != nil {
		name := ""
		if d.TimezoneName != nil {
			name = *d.TimezoneName
		}
		loc = time.FixedZone(name, int(*d.OffsetSeconds))
	}
	return time.Date(int(d.Year), time.Month(d.Month), int(d.Day), int(d.Hour), int(d.Minute), int(d.Second), int(d.Microsecond)*1000, loc)
}

// TimeDeltaFromDuration converts a duration into a normalized TimeDelta.
func TimeDeltaFromDuration(d time.Duration) TimeDelta {
	micros := d.Microseconds()
	return NormalizeTimeDelta(0, 0, micros)
}

// NormalizeTimeDelta applies Python's timedelta normalization.
func NormalizeTimeDelta(days, seconds, micros int64) TimeDelta {
	total := days*86400*1_000_000 + seconds*1_000_000 + micros
	d := floorDiv(total, 86400*1_000_000)
	rem := total - d*86400*1_000_000
	s := rem / 1_000_000
	us := rem % 1_000_000
	return TimeDelta{Days: int32(d), Seconds: int32(s), Microseconds: int32(us)}
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// Duration converts the TimeDelta into a time.Duration.
func (d TimeDelta) Duration() time.Duration {
	return time.Duration(d.Days)*24*time.Hour + time.Duration(d.Seconds)*time.Second + time.Duration(d.Microseconds)*time.Microsecond
}
