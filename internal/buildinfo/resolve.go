package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Resolve reports this module's release: the stamped build version, else the
// module version recorded in the binary's build info, else UnknownVersion.
func Resolve(stamped string, bi *debug.BuildInfo) string {
	if stamped != "" {
		return stamped
	}
	if v, ok := moduleVersion(bi); ok {
		return v
	}
	return UnknownVersion
}

// moduleVersion reads this module's version from the build info: the dependency
// entry when montygo is imported, "(devel)" under a directory replace, else the
// main module when montygo is built directly.
func moduleVersion(bi *debug.BuildInfo) (string, bool) {
	if bi == nil {
		return "", false
	}
	for _, dep := range bi.Deps {
		if dep.Path != ModulePath {
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
	if bi.Main.Path == ModulePath && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v"), true
	}
	return "", false
}
