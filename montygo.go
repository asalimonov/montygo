// Package montygo runs untrusted Python in Monty sandbox workers: crash-isolated
// `monty subprocess` children, an embedded WebAssembly worker, or a remote
// worker over WebSocket.
//
// The implementation lives in packages: runtime holds the Python value model,
// runtime/host the host objects, supervisor the server contract and its
// implementations, telemetry the OpenTelemetry mirror. This package is the
// public surface over them.
package montygo

import "github.com/asalimonov/montygo/internal/buildinfo"

const (
	// MontyVersion is the Monty release this binding tracks. BindingVersion
	// reports this module's own release.
	MontyVersion = buildinfo.MontyVersion
	// Version is MontyVersion.
	//
	// Deprecated: use MontyVersion for the upstream release or BindingVersion for this module.
	Version = MontyVersion
	// UpstreamRev is the upstream commit the protocol and tests were taken from.
	UpstreamRev = buildinfo.UpstreamRev
	// ProtocolVersion is the wire protocol version this parent speaks.
	ProtocolVersion = buildinfo.ProtocolVersion
	// MaxValueDepth is the deepest list-like nesting a value may have on the wire.
	MaxValueDepth = buildinfo.MaxValueDepth
)
