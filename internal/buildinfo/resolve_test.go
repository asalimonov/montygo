package buildinfo

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	dep := func(version string, replace *debug.Module) *debug.BuildInfo {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "example.com/app", Version: "(devel)"},
			Deps: []*debug.Module{
				{Path: "example.com/other", Version: "v2.0.0"},
				{Path: ModulePath, Version: version, Replace: replace},
			},
		}
	}
	cases := []struct {
		name    string
		stamped string
		bi      *debug.BuildInfo
		want    string
	}{
		{"stamped value wins", "0.1.0-3f2a9c1", dep("v0.9.0", nil), "0.1.0-3f2a9c1"},
		{"no build info", "", nil, UnknownVersion},
		{"tagged dependency", "", dep("v0.1.0", nil), "0.1.0"},
		{"prerelease dependency", "", dep("v0.2.0-rc.1", nil), "0.2.0-rc.1"},
		{"pseudo-version dependency", "", dep("v0.1.1-0.20260915120000-3f2a9c1abcde", nil), "0.1.1-0.20260915120000-3f2a9c1abcde"},
		{"directory replace", "", dep("v0.0.0-00010101000000-000000000000", &debug.Module{Path: "../", Version: "(devel)"}), "(devel)"},
		{"directory replace of v0.0.0", "", dep("v0.0.0", &debug.Module{Path: "../"}), "(devel)"},
		{"module replace", "", dep("v0.1.0", &debug.Module{Path: "example.com/fork", Version: "v0.3.0"}), "0.3.0"},
		{"main module tagged", "", &debug.BuildInfo{Main: debug.Module{Path: ModulePath, Version: "v0.4.0"}}, "0.4.0"},
		{"main module devel", "", &debug.BuildInfo{Main: debug.Module{Path: ModulePath, Version: "(devel)"}}, UnknownVersion},
		{"not a dependency", "", &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{{Path: "example.com/other", Version: "v1.0.0"}}}, UnknownVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, Resolve(tc.stamped, tc.bi))
		})
	}
}
