package pool

import (
	"sync"

	"github.com/asalimonov/montygo/internal/worker"
)

// checkoutLease arbitrates normal release against out-of-band termination.
// Protocol state and callbacks never hold this mutex.
type checkoutLease struct {
	mu     sync.Mutex
	slot   *slot
	worker worker.Worker
	cause  error
}

func (l *checkoutLease) active() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.slot != nil
}

func (l *checkoutLease) finish() (*slot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.slot
	l.slot = nil
	return s, s != nil
}

func (l *checkoutLease) terminate(cause error) (worker.Worker, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.slot == nil {
		return nil, false
	}
	l.slot = nil
	l.cause = cause
	return l.worker, true
}
