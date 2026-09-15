package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out))
	require.Regexp(t, `^freed object rejected: \w+: .+\nproxy id [0-9a-f-]+ resolved to the original sandbox object\n$`, out.String())
}
