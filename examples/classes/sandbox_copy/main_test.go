package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out))
	require.Equal(t, "sandbox copy saw x=99, host object still Point(x=1, y=2)\n", out.String())
}
