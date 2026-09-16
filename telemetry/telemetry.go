// Package telemetry builds the OpenTelemetry components a montygo Pool records
// into. Nothing here is global: an application creates an Instrumentation,
// points it at its providers and passes Components to PoolOptions.
package telemetry

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/asalimonov/montygo/internal/buildinfo"
)

const instrumentationName = "github.com/asalimonov/montygo"

// Components are the OpenTelemetry components Monty records into.
// Passing them to a pool opts in to recording fed code, inputs, call
// arguments, results, exceptions and print output. Each signal is optional.
type Components struct {
	Tracer trace.Tracer
	Meter  metric.Meter
	Logger log.Logger
}

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

// Instrumentation builds Monty telemetry components from OpenTelemetry
// providers, the global providers unless replaced.
type Instrumentation struct {
	mu             sync.Mutex
	cfg            InstrumentationConfig
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	loggerProvider log.LoggerProvider
}

// NewInstrumentation creates an instrumentation over the global providers.
func NewInstrumentation(cfg InstrumentationConfig) (*Instrumentation, error) {
	return &Instrumentation{
		cfg:            cfg,
		tracerProvider: otel.GetTracerProvider(),
		meterProvider:  otel.GetMeterProvider(),
		loggerProvider: global.GetLoggerProvider(),
	}, nil
}

// Name is the instrumentation scope name.
func (i *Instrumentation) Name() string { return instrumentationName }

// Version is the instrumentation scope version.
func (i *Instrumentation) Version() string { return buildinfo.Version() }

// SetTracerProvider replaces the tracer provider; nil records no spans.
func (i *Instrumentation) SetTracerProvider(p trace.TracerProvider) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tracerProvider = p
}

// SetMeterProvider replaces the meter provider; nil records no metrics.
func (i *Instrumentation) SetMeterProvider(p metric.MeterProvider) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.meterProvider = p
}

// SetLoggerProvider replaces the logger provider; nil records no logs.
func (i *Instrumentation) SetLoggerProvider(p log.LoggerProvider) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.loggerProvider = p
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

// SetConfig replaces the configuration. Pools built from earlier Components
// keep recording; call Components again for the new selection.
func (i *Instrumentation) SetConfig(cfg InstrumentationConfig) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cfg = cfg
}

// Components builds the components from the providers and configuration set
// so far. It returns nil when the instrumentation is disabled or no signal
// has a provider.
func (i *Instrumentation) Components() *Components {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !teleOn(i.cfg.Enabled) {
		return nil
	}
	var c Components
	version := buildinfo.Version()
	if teleOn(i.cfg.Traces) && i.tracerProvider != nil {
		c.Tracer = i.tracerProvider.Tracer(instrumentationName, trace.WithInstrumentationVersion(version))
	}
	if teleOn(i.cfg.Metrics) && i.meterProvider != nil {
		c.Meter = i.meterProvider.Meter(instrumentationName, metric.WithInstrumentationVersion(version))
	}
	if teleOn(i.cfg.Logs) && i.loggerProvider != nil {
		c.Logger = i.loggerProvider.Logger(instrumentationName, log.WithInstrumentationVersion(version))
	}
	if c.Tracer == nil && c.Meter == nil && c.Logger == nil {
		return nil
	}
	return &c
}
