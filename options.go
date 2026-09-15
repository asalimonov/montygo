package montygo

import (
	"fmt"
	"math"
	"time"

	"github.com/asalimonov/montygo/internal/wire"
)

// Unlimited disables a limit that accepts it: MaxSuspensions, MaxMemory,
// MaxHostObjects and MaxPendingFutures.
const Unlimited uint64 = math.MaxUint64

// UnlimitedDuration disables MaxDuration explicitly.
const UnlimitedDuration time.Duration = math.MaxInt64

const (
	defaultMaxHostObjects    uint64 = 10_000
	defaultMaxPendingFutures uint64 = 1000
	defaultInterruptGrace           = 100 * time.Millisecond
)

// TypeCheckFormat selects how typing diagnostics render.
type TypeCheckFormat string

const (
	FormatFull      TypeCheckFormat = "full"
	FormatConcise   TypeCheckFormat = "concise"
	FormatAzure     TypeCheckFormat = "azure"
	FormatJSON      TypeCheckFormat = "json"
	FormatJSONLines TypeCheckFormat = "jsonlines"
	FormatRDJSON    TypeCheckFormat = "rdjson"
	FormatPylint    TypeCheckFormat = "pylint"
	FormatGitLab    TypeCheckFormat = "gitlab"
	FormatGitHub    TypeCheckFormat = "github"
)

var typeCheckFormats = map[TypeCheckFormat]int32{
	FormatFull: 1, FormatConcise: 2, FormatAzure: 3, FormatJSON: 4, FormatJSONLines: 5,
	FormatRDJSON: 6, FormatPylint: 7, FormatGitLab: 8, FormatGitHub: 9,
}

// ResourceLimits bounds a session; zero values mean unlimited or the default.
type ResourceLimits struct {
	// MaxDuration bounds sandbox execution per session: 0 means no limit, UnlimitedDuration is explicit.
	MaxDuration time.Duration
	// MaxMemory bounds allocator bytes: 0 means the worker default, Unlimited disables.
	MaxMemory  uint64
	GCInterval uint64
	// MaxRecursionDepth: 0 means 1000; Unlimited is rejected because the worker's stack is finite.
	MaxRecursionDepth uint64
	// MaxSuspensions bounds host round trips per session, reset by LoadSession and
	// LoadSnapshot: 0 means 1000, Unlimited disables.
	MaxSuspensions uint64
}

// CheckoutOptions configure one session.
type CheckoutOptions struct {
	// ScriptName names the script in tracebacks; defaults to main.py.
	ScriptName string
	Limits     *ResourceLimits
	TypeCheck  bool
	// TypeCheckStubs are stub declarations visible to the type checker.
	TypeCheckStubs  string
	TypeCheckFormat TypeCheckFormat
	TypeCheckColor  bool
	// AssertMessageAnnotations: nil keeps the default (120-byte operand reprs), 0 disables.
	AssertMessageAnnotations *uint32
	// PrintFlushInterval: nil keeps the 5ms default, 0 restores line buffering.
	PrintFlushInterval *time.Duration
	// Host is a validated registry of functions and objects the session exposes;
	// FeedOptions.ExternalLookup entries override its names.
	Host *Host
	// MaxHostObjects bounds host objects the session keeps for identity: 0 means 10000, Unlimited disables.
	MaxHostObjects uint64
	// MaxPendingFutures bounds unresolved futures per feed: 0 means 1000, Unlimited disables.
	MaxPendingFutures uint64
	// InterruptGrace is how long Interrupt waits for a suspension before killing the worker: 0 means 100ms.
	InterruptGrace time.Duration
}

type sessionLimits struct {
	hostObjects    uint64
	pendingFutures uint64
	interruptGrace time.Duration
}

func (o CheckoutOptions) sessionLimits() (sessionLimits, error) {
	l := sessionLimits{hostObjects: o.MaxHostObjects, pendingFutures: o.MaxPendingFutures, interruptGrace: o.InterruptGrace}
	if l.hostObjects == 0 {
		l.hostObjects = defaultMaxHostObjects
	}
	if l.pendingFutures == 0 {
		l.pendingFutures = defaultMaxPendingFutures
	}
	if l.interruptGrace < 0 {
		return l, &OptionError{Message: fmt.Sprintf("invalid interruptGrace: expected a non-negative duration, got %s", l.interruptGrace)}
	}
	if l.interruptGrace == 0 {
		l.interruptGrace = defaultInterruptGrace
	}
	return l, nil
}

// Uint32 returns a pointer to v, for AssertMessageAnnotations.
func Uint32(v uint32) *uint32 { return &v }

// DurationPtr returns a pointer to d, for PrintFlushInterval.
func DurationPtr(d time.Duration) *time.Duration { return &d }

func (o CheckoutOptions) configure() (wire.Configure, error) {
	cfg := wire.Configure{ScriptName: o.ScriptName, TypeCheck: o.TypeCheck, TypeCheckColor: o.TypeCheckColor}
	if cfg.ScriptName == "" {
		cfg.ScriptName = "main.py"
	}
	if o.TypeCheckStubs != "" {
		stubs := o.TypeCheckStubs
		cfg.TypeCheckStubs = &stubs
	}
	format := o.TypeCheckFormat
	if format == "" {
		format = FormatFull
	}
	code, ok := typeCheckFormats[format]
	if !ok {
		return cfg, &OptionError{Message: fmt.Sprintf("unknown typeCheckFormat '%s', expected one of: full, concise, azure, json, jsonlines, rdjson, pylint, gitlab, github", format)}
	}
	cfg.TypeCheckFormat = code
	cfg.AssertMessageAnnotations = o.AssertMessageAnnotations
	if o.PrintFlushInterval != nil {
		d := *o.PrintFlushInterval
		if d < 0 {
			return cfg, &OptionError{Message: fmt.Sprintf("invalid printFlushInterval: expected a non-negative duration, got %s", d)}
		}
		var ms uint32
		if d > 0 {
			v := d.Milliseconds()
			switch {
			case v < 1:
				ms = 1
			case v > math.MaxUint32:
				ms = math.MaxUint32
			default:
				ms = uint32(v)
			}
		}
		cfg.PrintFlushIntervalMs = &ms
	}
	limits := &wire.Limits{}
	recursion, suspensions := uint64(1000), uint64(1000)
	if l := o.Limits; l != nil {
		if l.MaxDuration < 0 {
			return cfg, &OptionError{Message: "invalid maxDurationSecs: can not convert float seconds to Duration: value is negative"}
		}
		if l.MaxDuration > 0 && l.MaxDuration != UnlimitedDuration {
			micros := uint64(l.MaxDuration / time.Microsecond)
			limits.MaxDurationMicros = &micros
		}
		if l.MaxMemory > 0 && l.MaxMemory != Unlimited {
			mem := l.MaxMemory
			limits.MaxMemoryBytes = &mem
		}
		if l.GCInterval > 0 {
			gc := l.GCInterval
			limits.GCInterval = &gc
		}
		if l.MaxRecursionDepth == Unlimited {
			return cfg, &OptionError{Message: "maxRecursionDepth cannot be unlimited"}
		}
		if l.MaxRecursionDepth > 0 {
			recursion = l.MaxRecursionDepth
		}
		if l.MaxSuspensions > 0 {
			suspensions = l.MaxSuspensions
		}
	}
	limits.MaxRecursionDepth = &recursion
	limits.MaxSuspensions = &suspensions
	cfg.Limits = limits
	return cfg, nil
}
