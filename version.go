package montygo

import (
	"runtime/debug"
	"strings"

	"github.com/asalimonov/montygo/internal/buildinfo"
	"github.com/asalimonov/montygo/internal/worker"
)

// Packages that must not import the root package read the binding version from
// internal/buildinfo, because the release stamp targets this package's
// buildVersion variable.
func init() { buildinfo.Set(BindingVersion()) }

// buildVersion is stamped by -ldflags "-X github.com/asalimonov/montygo.buildVersion=<version>".
var buildVersion string

const (
	unknownVersion = "0.0.0-unknown"
	modulePath     = "github.com/asalimonov/montygo"
)

// BindingVersion reports this module's release: the stamped build version,
// else the module version recorded in the binary's build info, else "0.0.0-unknown".
func BindingVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	return bindingVersionFrom(buildVersion, bi)
}

func bindingVersionFrom(stamped string, bi *debug.BuildInfo) string {
	if stamped != "" {
		return stamped
	}
	if v, ok := moduleVersion(bi); ok {
		return v
	}
	return unknownVersion
}

// moduleVersion reads this module's version from the build info: the dependency
// entry when montygo is imported, "(devel)" under a directory replace, else the
// main module when montygo is built directly.
func moduleVersion(bi *debug.BuildInfo) (string, bool) {
	if bi == nil {
		return "", false
	}
	for _, dep := range bi.Deps {
		if dep.Path != modulePath {
			continue
		}
		if dep.Replace != nil {
			if dep.Replace.Version == "" || dep.Replace.Version == "(devel)" {
				return "(devel)", true
			}
			return strings.TrimPrefix(dep.Replace.Version, "v"), true
		}
		if dep.Version == "" || dep.Version == "(devel)" {
			return "", false
		}
		return strings.TrimPrefix(dep.Version, "v"), true
	}
	if bi.Main.Path == modulePath && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v"), true
	}
	return "", false
}

// userAgent is the User-Agent of every WebSocket upgrade and HTTP request the pool sends.
func userAgent() string {
	return worker.DefaultUserAgent + " montygo/" + BindingVersion()
}
