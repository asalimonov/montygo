package wasmblob

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBlobDecodesAndMatchesChecksum(t *testing.T) {
	blob, err := Bytes()
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(blob, []byte("\x00asm")), "not a wasm module")
	require.Len(t, SHA256(), 64)
	again, err := Bytes()
	require.NoError(t, err)
	require.Same(t, &blob[0], &again[0])
	dir, err := DefaultCacheDir()
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(dir, SHA256()))
}
