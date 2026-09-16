// Package buildinfo carries the binding version to packages that must not
// import the root package.
//
// The version itself is resolved in the root package, because the release
// stamp is -ldflags "-X github.com/asalimonov/montygo.buildVersion=<version>"
// and that path is part of the build contract.
package buildinfo

import "sync/atomic"

var version atomic.Value // string

// Set records the binding version. The root package calls it during init.
func Set(v string) {
	if v != "" {
		version.Store(v)
	}
}

// Version is the binding version, or "0.0.0-unknown" before Set has run.
func Version() string {
	if v, ok := version.Load().(string); ok {
		return v
	}
	return "0.0.0-unknown"
}
