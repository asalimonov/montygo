// Package montygo runs untrusted Python in Monty sandbox workers: crash-isolated
// `monty subprocess` children, an embedded WebAssembly worker, or a remote
// worker over WebSocket to a supervised monty-server.
//
// A Runtime is the sandbox configuration and its host extensions; a Pool
// manages the workers; Pool.Checkout joins the two into a Session. The value
// model lives in sandbox, host objects in sandbox/host, the error contract in
// monterr and the OpenTelemetry surface in telemetry.
package montygo

const (
	// MontyVersion is the Monty release this binding tracks. BindingVersion
	// reports this module's own release.
	MontyVersion = "0.0.23"
	// Version is MontyVersion.
	//
	// Deprecated: use MontyVersion for the upstream release or BindingVersion for this module.
	Version = MontyVersion
	// UpstreamRev is the upstream commit the protocol and tests were taken from.
	UpstreamRev = "f8acf4fa"
	// ProtocolVersion is the wire protocol version this parent speaks.
	ProtocolVersion uint32 = 3
	// MaxValueDepth is the deepest list-like nesting a value may have on the wire.
	MaxValueDepth = 48
)
