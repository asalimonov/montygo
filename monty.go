// Package monty runs untrusted Python in Monty sandbox workers: crash-isolated
// `monty subprocess` children, an embedded WebAssembly worker, or a remote
// worker over WebSocket.
package monty

const (
	// Version is the Monty release this binding tracks.
	Version = "0.0.23"
	// UpstreamRev is the upstream commit the protocol and tests were taken from.
	UpstreamRev = "f8acf4fa"
	// ProtocolVersion is the wire protocol version this parent speaks.
	ProtocolVersion uint32 = 3
	// MaxValueDepth is the deepest list-like nesting a value may have on the wire.
	MaxValueDepth = 48
)
