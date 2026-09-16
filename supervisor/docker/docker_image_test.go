package docker

import (
	"github.com/asalimonov/montygo/internal/buildinfo"
	"testing"

	pyrt "github.com/asalimonov/montygo/runtime"
	"github.com/stretchr/testify/require"
)

func noEnv(string) string { return "" }

func envFrom(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

func TestDockerImageCandidatesFromVersion(t *testing.T) {
	cases := []struct {
		name    string
		binding string
		want    []string
	}{
		{"release", "0.3.0", []string{"ghcr.io/asalimonov/monty-server:0.3.0"}},
		{"prerelease", "0.4.0-rc.1", []string{"ghcr.io/asalimonov/monty-server:0.4.0-rc.1"}},
		{"dev build", "0.3.0-3f2a9c1", []string{
			"ghcr.io/asalimonov/monty-server:0.3.0-3f2a9c1",
			"ghcr.io/asalimonov/monty-server:0.3.0",
		}},
		{"dirty dev build", "0.3.0-3f2a9c1-dirty", []string{
			"ghcr.io/asalimonov/monty-server:0.3.0-3f2a9c1-dirty",
			"ghcr.io/asalimonov/monty-server:0.3.0",
		}},
		{"dirty release", "0.3.0-dirty", []string{
			"ghcr.io/asalimonov/monty-server:0.3.0-dirty",
			"ghcr.io/asalimonov/monty-server:0.3.0",
		}},
		{"prerelease dev build", "0.4.0-rc.1-3f2a9c1", []string{
			"ghcr.io/asalimonov/monty-server:0.4.0-rc.1-3f2a9c1",
			"ghcr.io/asalimonov/monty-server:0.4.0-rc.1",
		}},
		{"pseudo-version after a release", "0.3.1-0.20260916120000-abcdef123456", []string{
			"ghcr.io/asalimonov/monty-server:0.3.0",
		}},
		{"pseudo-version after a prerelease", "0.4.0-rc.1.0.20260916120000-abcdef123456", []string{
			"ghcr.io/asalimonov/monty-server:0.4.0-rc.1",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := imageCandidates("", "", tc.binding, noEnv)
			require.NoError(t, err)
			require.Equal(t, tc.want, refs)
		})
	}
}

func TestDockerImageCandidatesWithoutADerivableTag(t *testing.T) {
	for _, binding := range []string{
		"(devel)",
		buildinfo.UnknownVersion,
		"",
		"0.0.0-20260916120000-abcdef123456",
		"not a version",
	} {
		t.Run(binding, func(t *testing.T) {
			_, err := imageCandidates("", "", binding, noEnv)
			require.ErrorAs(t, err, new(*pyrt.OptionError))
			require.Contains(t, err.Error(), VersionEnv)
			require.Contains(t, err.Error(), ImageEnv)
		})
	}
}

func TestDockerImageCandidatesOverrides(t *testing.T) {
	t.Run("option image keeps the derived tags", func(t *testing.T) {
		refs, err := imageCandidates("registry.local/monty-server", "", "0.3.0-3f2a9c1", noEnv)
		require.NoError(t, err)
		require.Equal(t, []string{"registry.local/monty-server:0.3.0-3f2a9c1", "registry.local/monty-server:0.3.0"}, refs)
	})
	t.Run("option version is the only candidate", func(t *testing.T) {
		refs, err := imageCandidates("", "0.2.0", "0.3.0-3f2a9c1", noEnv)
		require.NoError(t, err)
		require.Equal(t, []string{"ghcr.io/asalimonov/monty-server:0.2.0"}, refs)
	})
	t.Run("variables apply when options are empty", func(t *testing.T) {
		refs, err := imageCandidates("", "", "0.3.0", envFrom(map[string]string{
			ImageEnv:   "registry.local/monty",
			VersionEnv: "1.2.3",
		}))
		require.NoError(t, err)
		require.Equal(t, []string{"registry.local/monty:1.2.3"}, refs)
	})
	t.Run("options win over variables", func(t *testing.T) {
		refs, err := imageCandidates("opt/monty", "9.9.9", "0.3.0", envFrom(map[string]string{
			ImageEnv:   "env/monty",
			VersionEnv: "1.2.3",
		}))
		require.NoError(t, err)
		require.Equal(t, []string{"opt/monty:9.9.9"}, refs)
	})
	t.Run("a pinned tag is used verbatim", func(t *testing.T) {
		refs, err := imageCandidates("registry.local/monty-server:pinned", "", "0.3.0-3f2a9c1", noEnv)
		require.NoError(t, err)
		require.Equal(t, []string{"registry.local/monty-server:pinned"}, refs)
	})
	t.Run("a digest is used verbatim", func(t *testing.T) {
		ref := "registry.local/monty-server@sha256:" + "ab12cd34" // shortened; the reference is not parsed further
		refs, err := imageCandidates(ref, "", "0.3.0", noEnv)
		require.NoError(t, err)
		require.Equal(t, []string{ref}, refs)
	})
	t.Run("a port in the registry is not a tag", func(t *testing.T) {
		refs, err := imageCandidates("localhost:5000/monty-server", "", "0.3.0", noEnv)
		require.NoError(t, err)
		require.Equal(t, []string{"localhost:5000/monty-server:0.3.0"}, refs)
	})
	t.Run("a pinned reference rejects a version", func(t *testing.T) {
		_, err := imageCandidates("registry.local/monty-server:pinned", "0.3.0", "0.3.0", noEnv)
		require.ErrorAs(t, err, new(*pyrt.OptionError))
	})
	t.Run("a pinned reference rejects a version from the variable", func(t *testing.T) {
		_, err := imageCandidates("registry.local/monty-server:pinned", "", "0.3.0",
			envFrom(map[string]string{VersionEnv: "0.3.0"}))
		require.ErrorAs(t, err, new(*pyrt.OptionError))
	})
	t.Run("an invalid tag is rejected", func(t *testing.T) {
		_, err := imageCandidates("", "not a tag", "0.3.0", noEnv)
		require.ErrorAs(t, err, new(*pyrt.OptionError))
	})
}
