package montygo

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

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

// RuntimeOptions configure the sandbox every session of the runtime runs in.
type RuntimeOptions struct {
	// Host exposes functions, objects and classes to sandbox code by name;
	// FeedOptions.ExternalLookup entries shadow its names. nil exposes nothing.
	Host *host.Host
	// OS answers OS calls that no mount covers. nil answers NotHandled.
	OS host.OSHandler
	// Mounts are visible to every feed; FeedOptions.Mount adds to them.
	Mounts []*sandbox.MountDir
	// Print receives output when a feed sets no target. nil writes to the
	// process stdout and stderr.
	Print sandbox.PrintTarget
	// Limits apply to every session; CheckoutOptions.Limits replaces them per session.
	Limits    *ResourceLimits
	TypeCheck bool
	// TypeCheckStubs are stub declarations visible to the type checker.
	TypeCheckStubs  string
	TypeCheckFormat TypeCheckFormat
	TypeCheckColor  bool
	// AssertMessageAnnotations: nil keeps the default (120-byte operand reprs), 0 disables.
	AssertMessageAnnotations *uint32
	// PrintFlushInterval: nil keeps the 5ms default, 0 restores line buffering.
	PrintFlushInterval *time.Duration
	// MaxHostObjects bounds host objects a session keeps for identity: 0 means 10000, Unlimited disables.
	MaxHostObjects uint64
	// MaxPendingFutures bounds unresolved futures per feed: 0 means 1000, Unlimited disables.
	MaxPendingFutures uint64
}

// Runtime is an immutable sandbox configuration: the host extensions, mounts,
// print target, limits and type checking every session checked out with it
// shares. It is safe for concurrent use by any number of pools.
type Runtime struct {
	opts RuntimeOptions
	// config is the worker configuration without the script name and limits.
	config wire.Configure
}

// NewRuntime validates opts once and returns the runtime.
func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	opts.Mounts = slices.Clone(opts.Mounts)
	cfg := wire.Configure{TypeCheck: opts.TypeCheck, TypeCheckColor: opts.TypeCheckColor, AssertMessageAnnotations: opts.AssertMessageAnnotations}
	if opts.TypeCheckStubs != "" {
		stubs := opts.TypeCheckStubs
		cfg.TypeCheckStubs = &stubs
	}
	format := opts.TypeCheckFormat
	if format == "" {
		format = FormatFull
	}
	code, ok := typeCheckFormats[format]
	if !ok {
		return nil, &monterr.OptionError{Message: fmt.Sprintf("unknown typeCheckFormat '%s', expected one of: full, concise, azure, json, jsonlines, rdjson, pylint, gitlab, github", format)}
	}
	cfg.TypeCheckFormat = code
	if opts.PrintFlushInterval != nil {
		d := *opts.PrintFlushInterval
		if d < 0 {
			return nil, &monterr.OptionError{Message: fmt.Sprintf("invalid printFlushInterval: expected a non-negative duration, got %s", d)}
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
	if _, err := wireLimits(opts.Limits); err != nil {
		return nil, err
	}
	if _, _, err := sandbox.BuildMounts(opts.Mounts); err != nil {
		return nil, err
	}
	return &Runtime{opts: opts, config: cfg}, nil
}

// Options returns a copy of the runtime's options.
func (r *Runtime) Options() RuntimeOptions {
	opts := r.opts
	opts.Mounts = slices.Clone(opts.Mounts)
	return opts
}

// configure builds one session's worker configuration.
func (r *Runtime) configure(scriptName string, override *ResourceLimits) (wire.Configure, error) {
	cfg := r.config
	cfg.ScriptName = scriptName
	if cfg.ScriptName == "" {
		cfg.ScriptName = "main.py"
	}
	limits := r.opts.Limits
	if override != nil {
		limits = override
	}
	wl, err := wireLimits(limits)
	if err != nil {
		return cfg, err
	}
	cfg.Limits = wl
	return cfg, nil
}

func (r *Runtime) sessionLimits(poolStop, override StopPolicy) (sessionLimits, error) {
	l := sessionLimits{hostObjects: r.opts.MaxHostObjects, pendingFutures: r.opts.MaxPendingFutures}
	if l.hostObjects == 0 {
		l.hostObjects = defaultMaxHostObjects
	}
	if l.pendingFutures == 0 {
		l.pendingFutures = defaultMaxPendingFutures
	}
	stop, err := effectivePolicy(poolStop, []StopPolicy{override})
	if err != nil {
		return l, err
	}
	l.stop = stop
	return l, nil
}

// feedMounts builds one feed's mount table: the runtime's mounts, then extra.
// The first runtime mount, or the first extra one without any, is the
// working directory.
func (r *Runtime) feedMounts(extra []*sandbox.MountDir) (pool.MountTable, string, error) {
	if len(r.opts.Mounts) == 0 {
		return sandbox.BuildMounts(extra)
	}
	all := make([]*sandbox.MountDir, 0, len(r.opts.Mounts)+len(extra))
	all = append(all, r.opts.Mounts...)
	all = append(all, extra...)
	return sandbox.BuildMounts(all)
}

func wireLimits(l *ResourceLimits) (*wire.Limits, error) {
	limits := &wire.Limits{}
	recursion, suspensions := uint64(1000), uint64(1000)
	if l != nil {
		if l.MaxDuration < 0 {
			return nil, &monterr.OptionError{Message: "invalid maxDurationSecs: can not convert float seconds to Duration: value is negative"}
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
			return nil, &monterr.OptionError{Message: "maxRecursionDepth cannot be unlimited"}
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
	return limits, nil
}
