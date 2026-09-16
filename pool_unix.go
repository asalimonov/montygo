//go:build unix

package montygo

import (
	"io"

	"github.com/asalimonov/montygo/internal/worker"
)

const nativeSupported = true

func newSubprocessSpawner(bin string, stderr io.Writer, pending int64, observe worker.PendingBytesObserver) worker.Spawner {
	return &worker.SubprocessSpawner{BinaryPath: bin, Stderr: stderr, MaxPendingBytes: pending, PendingBytes: observe}
}
