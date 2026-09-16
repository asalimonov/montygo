package montygo

import (
	"runtime/debug"

	"github.com/asalimonov/montygo/internal/buildinfo"
)

// buildVersion is stamped by -ldflags "-X github.com/asalimonov/montygo.buildVersion=<version>".
var buildVersion string

// Packages that must not import the root package read the binding version from
// internal/buildinfo, because the release stamp targets this package's
// buildVersion variable.
func init() { buildinfo.Set(BindingVersion()) }

// BindingVersion reports this module's release: the stamped build version,
// else the module version recorded in the binary's build info, else "0.0.0-unknown".
func BindingVersion() string {
	bi, _ := debug.ReadBuildInfo()
	return buildinfo.Resolve(buildVersion, bi)
}
