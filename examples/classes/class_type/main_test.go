package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out))
	require.Equal(t, "constructed in the sandbox: 'hi Samuel'\n"+
		"construction denied: TypeError: cannot instantiate host class 'Person'\n", out.String())
}
