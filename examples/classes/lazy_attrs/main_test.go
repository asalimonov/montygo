package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out))
	require.Equal(t, "denied as expected: AttributeError: 'Config' object has no attribute 'api_key'\n", out.String())
}
