package engine

import (
	"fmt"
	"math"
	"time"

	"github.com/asalimonov/montygo/internal/wire"
)

// StopPolicy says how an execution is ended. Zero fields inherit from the
// level above: call, then CheckoutOptions.Stop, Options.Stop and DefaultStopPolicy.
type StopPolicy struct {
	// Drain waits this long for the run to end on its own before the request.
	Drain time.Duration
	// Timeout runs from the request until the worker is killed when the run
	// has not ended. A negative value kills at once (see KillNow).
	Timeout time.Duration
	// Join bounds, after a kill, the wait for a host callback that ignores
	// its context.
	Join time.Duration
	// Reason is raised in the sandbox; nil means KeyboardInterrupt.
	Reason error
	// Catchable delivers Reason as an ordinary exception at the next host
	// call or await instead of AbortFeed, so Python can catch it.
	Catchable bool
}

// DefaultStopPolicy is the process-wide default. Set it before creating pools.
var DefaultStopPolicy = StopPolicy{Timeout: 3 * time.Second, Join: 3 * time.Second}

// KillNow ends an execution without a request phase.
var KillNow = StopPolicy{Timeout: -1}

var keyboardInterrupt = Raise("KeyboardInterrupt", "")

func (p StopPolicy) over(base StopPolicy) StopPolicy {
	out := base
	if p.Drain != 0 {
		out.Drain = p.Drain
	}
	if p.Timeout != 0 {
		out.Timeout = p.Timeout
	}
	if p.Join != 0 {
		out.Join = p.Join
	}
	if p.Reason != nil {
		out.Reason = p.Reason
	}
	if p.Catchable {
		out.Catchable = true
	}
	return out
}

func (p StopPolicy) validate() error {
	if p.Drain < 0 || p.Join < 0 {
		return &OptionError{Message: "stop policy: Drain and Join must be non-negative"}
	}
	return nil
}

// effectivePolicy resolves at most one override against a base policy.
func effectivePolicy(base StopPolicy, policy []StopPolicy) (StopPolicy, error) {
	if len(policy) > 1 {
		return StopPolicy{}, &OptionError{Message: "at most one stop policy"}
	}
	out := base
	if len(policy) == 1 {
		out = policy[0].over(base)
	}
	if out.Reason == nil {
		out.Reason = keyboardInterrupt
	}
	return out, out.validate()
}

// Unlimited disables a limit that accepts it: MaxSuspensions, MaxMemory,
// MaxHostObjects and MaxPendingFutures.
const Unlimited uint64 = math.MaxUint64

// UnlimitedDuration disables MaxDuration explicitly.
const UnlimitedDuration time.Duration = math.MaxInt64

const (
	defaultMaxHostObjects    uint64 = 10_000
	defaultMaxPendingFutures uint64 = 1000
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
	// Stop overrides the pool's stop policy for this session; zero fields inherit.
	Stop StopPolicy
}

type sessionLimits struct {
	hostObjects    uint64
	pendingFutures uint64
	stop           StopPolicy
}

func (o CheckoutOptions) sessionLimits(poolStop StopPolicy) (sessionLimits, error) {
	l := sessionLimits{hostObjects: o.MaxHostObjects, pendingFutures: o.MaxPendingFutures}
	if l.hostObjects == 0 {
		l.hostObjects = defaultMaxHostObjects
	}
	if l.pendingFutures == 0 {
		l.pendingFutures = defaultMaxPendingFutures
	}
	stop, err := effectivePolicy(poolStop, []StopPolicy{o.Stop})
	if err != nil {
		return l, err
	}
	l.stop = stop
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
