package monty

import (
	"fmt"
	"math"
	"time"

	"github.com/asalimonov/montygo/internal/wire"
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
	MaxDuration       time.Duration
	MaxMemory         uint64
	GCInterval        uint64
	MaxRecursionDepth uint64
	MaxSuspensions    uint64
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
		if l.MaxDuration > 0 {
			micros := uint64(l.MaxDuration / time.Microsecond)
			limits.MaxDurationMicros = &micros
		}
		if l.MaxMemory > 0 {
			mem := l.MaxMemory
			limits.MaxMemoryBytes = &mem
		}
		if l.GCInterval > 0 {
			gc := l.GCInterval
			limits.GCInterval = &gc
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
