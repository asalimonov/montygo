package montygo

import (
	"time"

	"github.com/asalimonov/montygo/monterr"
)

// StopPolicy says how an execution is ended. Zero fields inherit from the
// level above: call, then CheckoutOptions.Stop, PoolOptions.Stop and the
// built-in policy of Timeout 3s and Join 3s.
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

// builtinStopPolicy is the last level of stop policy resolution; it is never reassigned.
var builtinStopPolicy = StopPolicy{Timeout: 3 * time.Second, Join: 3 * time.Second}

// KillNow ends an execution without a request phase.
var KillNow = StopPolicy{Timeout: -1}

var keyboardInterrupt = monterr.Raise("KeyboardInterrupt", "")

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
		return &monterr.OptionError{Message: "stop policy: Drain and Join must be non-negative"}
	}
	return nil
}

// effectivePolicy resolves at most one override against a base policy.
func effectivePolicy(base StopPolicy, policy []StopPolicy) (StopPolicy, error) {
	if len(policy) > 1 {
		return StopPolicy{}, &monterr.OptionError{Message: "at most one stop policy"}
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

// CheckoutOptions configure one session.
type CheckoutOptions struct {
	// ScriptName names the script in tracebacks; defaults to main.py.
	ScriptName string
	// Limits replace the runtime's limits for this session; nil inherits them.
	Limits *ResourceLimits
	// Stop overrides the pool's stop policy for this session; zero fields inherit.
	Stop StopPolicy
}

type sessionLimits struct {
	hostObjects    uint64
	pendingFutures uint64
	stop           StopPolicy
}

// Uint32 returns a pointer to v, for AssertMessageAnnotations.
func Uint32(v uint32) *uint32 { return &v }

// DurationPtr returns a pointer to d, for PrintFlushInterval.
func DurationPtr(d time.Duration) *time.Duration { return &d }
