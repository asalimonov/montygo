// Package telemetry records Monty sessions, runs, suspensions, output and
// pool health into process-wide OpenTelemetry components.
package telemetry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Components are the OpenTelemetry components telemetry records into.
type Components struct {
	Tracer trace.Tracer
	Meter  metric.Meter
	Logger log.Logger
}

// Recorder records into one installation of Components. A component that
// panics or errors disables only its own signal.
type Recorder struct {
	c          Components
	closed     atomic.Bool
	tracesOff  atomic.Bool
	logsOff    atomic.Bool
	metricsOff atomic.Bool

	mu          sync.Mutex
	instruments map[string]*handle
}

var (
	installMu sync.Mutex
	owner     any
	current   atomic.Pointer[Recorder]
)

var errNilSpan = errors.New("tracer returned no span")

// Install makes c the process-wide components of newOwner. It reports false
// when another owner holds the installation, or when newOwner holds it and
// replace is false.
func Install(newOwner any, c Components, replace bool) bool {
	installMu.Lock()
	defer installMu.Unlock()
	if owner != nil && (owner != newOwner || !replace) {
		return false
	}
	owner = newOwner
	swap(&Recorder{c: c, instruments: map[string]*handle{}})
	return true
}

// Uninstall removes the installation held by o.
func Uninstall(o any) {
	installMu.Lock()
	defer installMu.Unlock()
	if owner != nil && owner == o {
		owner = nil
		swap(nil)
	}
}

// Reset removes any installation.
func Reset() {
	installMu.Lock()
	defer installMu.Unlock()
	owner = nil
	swap(nil)
}

// Installed reports whether an owner holds the installation.
func Installed() bool {
	installMu.Lock()
	defer installMu.Unlock()
	return owner != nil
}

func swap(r *Recorder) {
	if old := current.Swap(r); old != nil {
		old.closed.Store(true)
	}
}

// Current returns the installed recorder, or nil.
func Current() *Recorder { return current.Load() }

// Tracing reports whether spans are recorded.
func (r *Recorder) Tracing() bool {
	return r != nil && r.c.Tracer != nil && !r.closed.Load() && !r.tracesOff.Load()
}

// Logging reports whether log records are emitted.
func (r *Recorder) Logging() bool {
	return r != nil && r.c.Logger != nil && !r.closed.Load() && !r.logsOff.Load()
}

// Metering reports whether measurements are recorded.
func (r *Recorder) Metering() bool {
	return r != nil && r.c.Meter != nil && !r.closed.Load() && !r.metricsOff.Load()
}

// Emit sends one log record named event, correlated with the span in ctx.
func (r *Recorder) Emit(ctx context.Context, severity log.Severity, event, body string, attrs []attribute.KeyValue) {
	if !r.Logging() {
		return
	}
	now := time.Now()
	var rec log.Record
	rec.SetTimestamp(now)
	rec.SetObservedTimestamp(now)
	rec.SetSeverity(severity)
	rec.SetEventName(event)
	rec.SetBody(attribute.StringValue(body))
	rec.AddAttributes(attrs...)
	guard(&r.logsOff, func() error {
		r.c.Logger.Emit(ctx, rec)
		return nil
	})
}

func guard(off *atomic.Bool, fn func() error) (ok bool) {
	defer func() {
		if recover() != nil {
			off.Store(true)
			ok = false
		}
	}()
	if err := fn(); err != nil {
		off.Store(true)
		return false
	}
	return true
}
