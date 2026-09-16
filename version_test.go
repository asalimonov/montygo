package montygo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/internal/buildinfo"
)

func TestBindingVersion(t *testing.T) {
	version := montygo.BindingVersion()
	require.NotEmpty(t, version)
	require.Equal(t, buildinfo.Version(), version, "the root package publishes its version to internal/buildinfo")
}
