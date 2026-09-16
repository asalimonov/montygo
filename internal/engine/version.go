package engine

import (
	"github.com/asalimonov/montygo/internal/buildinfo"
	"github.com/asalimonov/montygo/internal/worker"
)

// The pins and the module identity, read from the leaf that carries them. The
// root package exports them; the engine keeps its own unexported names so the
// facade can alias the root spelling without colliding.
const (
	montyVersion    = buildinfo.MontyVersion
	protocolVersion = buildinfo.ProtocolVersion
	upstreamRev     = buildinfo.UpstreamRev
	modulePath      = buildinfo.ModulePath
	unknownVersion  = buildinfo.UnknownVersion
)

// bindingVersion is this module's release. The root package resolves it,
// including the -ldflags stamp, and publishes it through internal/buildinfo.
func bindingVersion() string { return buildinfo.Version() }

// userAgent is the User-Agent of every WebSocket upgrade and HTTP request the pool sends.
func userAgent() string {
	return worker.DefaultUserAgent + " montygo/" + bindingVersion()
}
