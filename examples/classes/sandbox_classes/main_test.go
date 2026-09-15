package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out))
	require.Regexp(t, `^host received: MontyClassProxy\(name='Point', id='[^']+', attributes=\{'x': 3, 'y': 4\}\)\n$`, out.String())
}
