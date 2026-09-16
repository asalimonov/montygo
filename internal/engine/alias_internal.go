package engine

import (
	"fmt"

	"github.com/asalimonov/montygo/internal/telemetryhooks"
	rt "github.com/asalimonov/montygo/runtime"
	"github.com/asalimonov/montygo/runtime/host"
	"github.com/asalimonov/montygo/supervisor"
)

// Telemetry plumbing the engine uses under its old names.
var (
	resolveRecorder     = telemetryhooks.ResolveRecorder
	poolMetrics         = telemetryhooks.PoolMetrics
	traceContextHeaders = telemetryhooks.TraceContextHeaders
)

// Supervisor plumbing the engine uses under its old names.
type (
	recoverer  = supervisor.Recoverer
	noopReaper = supervisor.NoopReaper
)

var (
	newRecoverer  = supervisor.NewRecoverer
	sortedHeaders = supervisor.SortedHeaders
)

// Names the engine uses while it still lives in the root package. They
// disappear when the engine moves to internal/engine.
type (
	instanceStore = host.InstanceStore
	wrapper       = host.Wrapper
	attrError     = host.AttrError
)

var (
	exceptionParts     = rt.ExceptionParts
	errorFromException = rt.ErrorFromException
	buildMounts        = rt.BuildMounts
	newProtocolError   = rt.NewProtocolError
	hostTypeName       = host.TypeName
	prepareValue       = host.PrepareValue
	restoreValue       = host.RestoreValue
	kwargsRecord       = host.KwargsRecord
	newInstanceStore   = host.NewInstanceStore
	callWrapperMethod  = host.CallWrapperMethod
	wrapperLazyAttr    = host.WrapperLazyAttr
	wrapperName        = host.WrapperName

	// blockHostRegistration is a test seam; see host.BlockRegistration.
	blockHostRegistration = host.BlockRegistration
)

// panicError turns a recovered panic value into the error a host call reports.
func panicError(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	return fmt.Errorf("%v", r)
}
