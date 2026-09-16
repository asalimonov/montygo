package engine

import mtel "github.com/asalimonov/montygo/telemetry"

// Types.
type Components = mtel.Components
type Instrumentation = mtel.Instrumentation
type InstrumentationConfig = mtel.InstrumentationConfig

// Functions.
var Flush = mtel.Flush
var Instrument = mtel.Instrument
var NewInstrumentation = mtel.NewInstrumentation

// TelemetryComponents is the pre-0.4 name of telemetry.Components.
type TelemetryComponents = mtel.Components
