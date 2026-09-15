// Package wasmblob embeds the wasip1 Monty worker.
package wasmblob

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

//go:embed monty.wasm.zst
var compressed []byte

//go:embed blob.sha256
var checksum string

var (
	once    sync.Once
	decoded []byte
	errBlob error
)

// SHA256 returns the hex digest of the uncompressed worker.
func SHA256() string { return strings.TrimSpace(checksum) }

// Bytes returns the uncompressed worker, decoding it once per process.
func Bytes() ([]byte, error) {
	once.Do(func() {
		dec, err := zstd.NewReader(bytes.NewReader(compressed))
		if err != nil {
			errBlob = err
			return
		}
		defer dec.Close()
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(dec); err != nil {
			errBlob = err
			return
		}
		sum := sha256.Sum256(buf.Bytes())
		if hex.EncodeToString(sum[:]) != SHA256() {
			errBlob = errors.New("wasm worker blob checksum mismatch")
			return
		}
		decoded = buf.Bytes()
	})
	return decoded, errBlob
}

// DefaultCacheDir is the wazero compilation cache directory for this blob.
func DefaultCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "montygo", "wazero", SHA256()), nil
}
