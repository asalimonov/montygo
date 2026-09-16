package montygo

import mtel "github.com/asalimonov/montygo/telemetry"

// The OpenTelemetry surface. The implementation lives in montygo/telemetry.

// TelemetryComponents are the OpenTelemetry components Monty records into.
type TelemetryComponents = mtel.Components

// Instrumentation is the otel Instrumentation implementation of this binding.
type Instrumentation = mtel.Instrumentation

// InstrumentationConfig configures an Instrumentation.
type InstrumentationConfig = mtel.InstrumentationConfig

// Instrument installs the process-wide telemetry components.
var Instrument = mtel.Instrument

// Flush is a no-op kept for API compatibility with the TypeScript package.
var Flush = mtel.Flush

// NewInstrumentation builds an Instrumentation from a config.
var NewInstrumentation = mtel.NewInstrumentation
