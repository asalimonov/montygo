package monty

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/asalimonov/montygo/internal/telemetry"
)

const instrumentationName = "github.com/asalimonov/montygo"

var errNoTelemetryComponents = errors.New("at least one OpenTelemetry component is required")

// TelemetryComponents are the OpenTelemetry components Monty records into.
// Installing them opts in to recording fed code, inputs, call arguments,
// results, exceptions and print output.
type TelemetryComponents struct {
	Tracer trace.Tracer
	Meter  metric.Meter
	Logger log.Logger
}

var (
	teleMu          sync.Mutex
	teleDirectOwner = new(byte)
)

// Instrument installs components process-wide; pools created afterwards
// record into them. Each signal is optional.
func Instrument(c TelemetryComponents) error {
	teleMu.Lock()
	defer teleMu.Unlock()
	if telemetry.Installed() {
		return ErrTelemetryPresent
	}
	if c.Tracer == nil && c.Meter == nil && c.Logger == nil {
		return errNoTelemetryComponents
	}
	telemetry.Install(teleDirectOwner, telemetry.Components(c), false)
	return nil
}

// Flush returns once recorded telemetry has reached the installed components.
func Flush(context.Context) error { return nil }

// InstrumentationConfig selects what an Instrumentation records; nil means true.
type InstrumentationConfig struct {
	Enabled *bool
	Traces  *bool
	Metrics *bool
	Logs    *bool
}

func teleOn(b *bool) bool { return b == nil || *b }

func teleCopy(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

// Instrumentation installs Monty telemetry built from OpenTelemetry providers,
// the global providers unless replaced.
type Instrumentation struct {
	mu             sync.Mutex
	cfg            InstrumentationConfig
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	loggerProvider log.LoggerProvider
	active         bool
}

// NewInstrumentation creates an instrumentation and enables it unless cfg.Enabled is false.
func NewInstrumentation(cfg InstrumentationConfig) (*Instrumentation, error) {
	i := &Instrumentation{
		cfg:            cfg,
		tracerProvider: otel.GetTracerProvider(),
		meterProvider:  otel.GetMeterProvider(),
		loggerProvider: global.GetLoggerProvider(),
	}
	if teleOn(cfg.Enabled) {
		if err := i.Enable(); err != nil {
			return nil, err
		}
	}
	return i, nil
}

// Name is the instrumentation scope name.
func (i *Instrumentation) Name() string { return instrumentationName }

// Version is the instrumentation scope version.
func (i *Instrumentation) Version() string { return Version }

// Enable installs the instrumentation; it fails when other telemetry is installed.
func (i *Instrumentation) Enable() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.active = true
	if err := i.refreshLocked(); err != nil {
		i.active = false
		return err
	}
	return nil
}

// Disable uninstalls the instrumentation; open sessions stop recording.
func (i *Instrumentation) Disable() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.active = false
	teleMu.Lock()
	defer teleMu.Unlock()
	telemetry.Uninstall(i)
}

// SetTracerProvider replaces the tracer provider; nil records no spans.
func (i *Instrumentation) SetTracerProvider(p trace.TracerProvider) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tracerProvider = p
	_ = i.refreshLocked()
}

// SetMeterProvider replaces the meter provider; nil records no metrics.
func (i *Instrumentation) SetMeterProvider(p metric.MeterProvider) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.meterProvider = p
	_ = i.refreshLocked()
}

// SetLoggerProvider replaces the logger provider; nil records no logs.
func (i *Instrumentation) SetLoggerProvider(p log.LoggerProvider) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.loggerProvider = p
	_ = i.refreshLocked()
}

// Config returns a copy of the configuration with Enabled filled in.
func (i *Instrumentation) Config() InstrumentationConfig {
	i.mu.Lock()
	defer i.mu.Unlock()
	cfg := InstrumentationConfig{Enabled: teleCopy(i.cfg.Enabled), Traces: teleCopy(i.cfg.Traces), Metrics: teleCopy(i.cfg.Metrics), Logs: teleCopy(i.cfg.Logs)}
	if cfg.Enabled == nil {
		enabled := true
		cfg.Enabled = &enabled
	}
	return cfg
}

// SetConfig replaces the configuration, enabling or disabling as it changes.
func (i *Instrumentation) SetConfig(cfg InstrumentationConfig) {
	i.mu.Lock()
	was := teleOn(i.cfg.Enabled)
	i.cfg = cfg
	now := teleOn(cfg.Enabled)
	if was == now {
		_ = i.refreshLocked()
	}
	i.mu.Unlock()
	switch {
	case was && !now:
		i.Disable()
	case !was && now:
		_ = i.Enable()
	}
}

// ForceFlush flushes Monty telemetry, then every provider that supports flushing.
func (i *Instrumentation) ForceFlush(ctx context.Context) error {
	if err := Flush(ctx); err != nil {
		return err
	}
	i.mu.Lock()
	providers := []any{i.tracerProvider, i.meterProvider, i.loggerProvider}
	i.mu.Unlock()
	var errs []error
	for _, p := range providers {
		if f, ok := p.(interface{ ForceFlush(context.Context) error }); ok {
			errs = append(errs, f.ForceFlush(ctx))
		}
	}
	return errors.Join(errs...)
}

func (i *Instrumentation) refreshLocked() error {
	if !i.active {
		return nil
	}
	var c telemetry.Components
	if teleOn(i.cfg.Traces) && i.tracerProvider != nil {
		c.Tracer = i.tracerProvider.Tracer(instrumentationName, trace.WithInstrumentationVersion(Version))
	}
	if teleOn(i.cfg.Metrics) && i.meterProvider != nil {
		c.Meter = i.meterProvider.Meter(instrumentationName, metric.WithInstrumentationVersion(Version))
	}
	if teleOn(i.cfg.Logs) && i.loggerProvider != nil {
		c.Logger = i.loggerProvider.Logger(instrumentationName, log.WithInstrumentationVersion(Version))
	}
	teleMu.Lock()
	defer teleMu.Unlock()
	if !telemetry.Install(i, c, true) {
		return ErrTelemetryPresent
	}
	return nil
}
