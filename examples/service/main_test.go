package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceStopsTheLoop(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, service(t.Context(), &out, []string{"-run-for", "500ms"}))
	text := out.String()
	require.True(t, strings.HasPrefix(text, "tick 1: total 1\n"), text)
	require.True(t, strings.HasSuffix(text, "stopped: aborted\n"), text)
}
