//go:build unix

package monty

import (
	"io"

	"github.com/asalimonov/montygo/internal/worker"
)

const nativeSupported = true

func newSubprocessSpawner(bin string, stderr io.Writer) worker.Spawner {
	return &worker.SubprocessSpawner{BinaryPath: bin, Stderr: stderr}
}
