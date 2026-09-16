//go:build !unix

package engine

import (
	"context"
	"errors"
	"io"

	"github.com/asalimonov/montygo/internal/worker"
)

const nativeSupported = false

type unsupportedSpawner struct{}

func (unsupportedSpawner) Spawn(context.Context) (worker.Worker, error) {
	return nil, errors.New("the native backend is not supported on this platform")
}
func (unsupportedSpawner) Kind() worker.Kind           { return worker.KindSubprocess }
func (unsupportedSpawner) Close(context.Context) error { return nil }

func newSubprocessSpawner(string, io.Writer, int64, worker.PendingBytesObserver) worker.Spawner {
	return unsupportedSpawner{}
}
