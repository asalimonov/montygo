// Package pool is the protocol parent: an elastic pool of Monty workers and
// the per-checkout turn engine (deadlines, suspension budget, crash
// classification, mount servicing).
package pool

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
)

// ErrorKind classifies a pool error.
type ErrorKind uint8

const (
	KindCrashed ErrorKind = iota + 1
	KindTimeout
	KindProtocol
	KindRuntime
	KindTyping
	KindExhausted
	KindSpawn
	KindFinished
	KindDisconnected
	KindShutdown
	KindCancelled
	KindClosed
)

// Error is a pool failure.
type Error struct {
	Kind        ErrorKind
	Message     string
	Announced   bool
	Status      worker.Status
	Timeout     time.Duration
	Exception   *wire.Exception
	Diagnostics string
	Dump        []byte
	HasDump     bool
	// WorkerLost is set when the failure took the worker with it.
	WorkerLost bool
	// PreSend is set when the request was rejected before any bytes were written.
	PreSend bool
	Cause   error
}

func (e *Error) Unwrap() error { return e.Cause }

func (e *Error) Error() string {
	switch e.Kind {
	case KindCrashed:
		var s string
		if e.Announced {
			s = "monty worker crashed: " + e.Message
		} else {
			s = "monty worker crashed while " + e.Message
		}
		if st := e.Status.String(); st != "" {
			s += " (" + st + ")"
		}
		return s
	case KindTimeout:
		return "monty worker killed after exceeding request timeout of " + FormatRustDuration(e.Timeout)
	case KindProtocol:
		return "monty worker protocol error: " + e.Message
	case KindRuntime:
		if e.Exception != nil {
			return e.Exception.Render()
		}
		return e.Message
	case KindTyping:
		return "type checking failed:\n" + e.Diagnostics
	case KindExhausted:
		return "no monty worker became available within the checkout timeout"
	case KindSpawn:
		return "failed to spawn monty worker: " + e.Message
	case KindFinished:
		return "this checkout has already been finished"
	case KindDisconnected:
		return "monty worker connection closed while " + e.Message
	case KindShutdown:
		if e.HasDump {
			return "monty server is shutting down; the request did not run (session dump attached)"
		}
		return "monty server is shutting down; the request did not run"
	case KindCancelled:
		return "a previous protocol turn was cancelled mid-flight; the worker was discarded"
	case KindClosed:
		return "the pool is closed"
	}
	return e.Message
}

func protocolError(format string, args ...any) *Error {
	return &Error{Kind: KindProtocol, Message: fmt.Sprintf(format, args...)}
}

func runtimeError(excType, message string) *Error {
	return &Error{Kind: KindRuntime, Exception: wire.NewException(excType, message), PreSend: true}
}

// FormatRustDuration renders d like Rust's `{:?}` for std::time::Duration.
func FormatRustDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ns := uint64(d)
	secs := ns / 1_000_000_000
	nanos := ns % 1_000_000_000
	switch {
	case secs > 0:
		return fmtFraction(secs, nanos, 100_000_000, "s")
	case nanos >= 1_000_000:
		return fmtFraction(nanos/1_000_000, nanos%1_000_000, 100_000, "ms")
	case nanos >= 1_000:
		return fmtFraction(nanos/1_000, nanos%1_000, 100, "µs")
	}
	return strconv.FormatUint(nanos, 10) + "ns"
}

func fmtFraction(integer, frac, divisor uint64, unit string) string {
	s := strconv.FormatUint(integer, 10)
	if frac > 0 {
		var digits strings.Builder
		for divisor > 0 && frac > 0 {
			digits.WriteByte(byte('0' + frac/divisor))
			frac %= divisor
			divisor /= 10
		}
		s += "." + digits.String()
	}
	return s + unit
}
