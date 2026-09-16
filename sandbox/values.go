package sandbox

import (
	"time"

	"github.com/asalimonov/montygo/internal/value"
	monterr "github.com/asalimonov/montygo/monterr"
)

type (
	Dict               = value.Dict
	Pair               = value.Pair
	Tuple              = value.Tuple
	Set                = value.Set
	FrozenSet          = value.FrozenSet
	Date               = value.Date
	DateTime           = value.DateTime
	Time               = value.Time
	TimeDelta          = value.TimeDelta
	TimeZone           = value.TimeZone
	Exception          = value.Exception
	Type               = value.Type
	TypeOrigin         = value.TypeOrigin
	BuiltinFunction    = value.BuiltinFunction
	Path               = value.Path
	NamedTuple         = value.NamedTuple
	FileHandle         = value.FileHandle
	Cycle              = value.Cycle
	ExternalFunction   = value.Function
	EllipsisType       = value.EllipsisType
	NotImplementedType = value.NotImplementedType
)

const (
	OriginBuiltin = value.OriginBuiltin
	OriginSandbox = value.OriginSandbox
	OriginHost    = value.OriginHost
)

var (
	Ellipsis       = value.Ellipsis
	NotImplemented = value.NotImplemented
)

// NewDict builds an insertion-ordered dict.
func NewDict(pairs ...Pair) *Dict { return value.NewDict(pairs...) }

// NewSet builds a set.
func NewSet(items ...any) *Set { return value.NewSet(items...) }

// NewFrozenSet builds a frozenset.
func NewFrozenSet(items ...any) *FrozenSet { return value.NewFrozenSet(items...) }

// NewFileHandle validates a file handle; mode is canonicalized ("br" → "rb").
func NewFileHandle(path, mode string, position uint64) (*FileHandle, error) {
	h, err := value.NewFileHandle(path, mode, position)
	if err != nil {
		return nil, &monterr.ValueError{Message: err.Error()}
	}
	return h, nil
}

// DateTimeFromTime converts t into an aware datetime.
func DateTimeFromTime(t time.Time) DateTime { return value.DateTimeFromTime(t) }

// TimeDeltaFromDuration converts d into a normalized timedelta.
func TimeDeltaFromDuration(d time.Duration) TimeDelta { return value.TimeDeltaFromDuration(d) }

// Equal compares two values with Python equality.
func Equal(a, b any) bool { return value.Equal(a, b) }

// Repr renders a value in Python repr style.
func Repr(v any) string { return value.Repr(v) }
