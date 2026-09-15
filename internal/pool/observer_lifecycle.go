package pool

import "sync"

// observerLifetime defers Closed until the last protocol owner, including its
// callbacks, has returned. Worker retirement does not wait for that owner.
type observerLifetime struct {
	mu      sync.Mutex
	users   int
	closing bool
	closed  bool
}

func (o *observer) acquire() bool {
	if o == nil {
		return true
	}
	o.life.mu.Lock()
	defer o.life.mu.Unlock()
	if o.life.closing {
		return false
	}
	o.life.users++
	return true
}

func (o *observer) release() {
	if o == nil {
		return
	}
	o.life.mu.Lock()
	o.life.users--
	closeNow := o.life.closing && o.life.users == 0 && !o.life.closed
	if closeNow {
		o.life.closed = true
	}
	o.life.mu.Unlock()
	if closeNow {
		o.o.Closed()
	}
}

func (o *observer) close() {
	if o == nil {
		return
	}
	o.life.mu.Lock()
	o.life.closing = true
	closeNow := o.life.users == 0 && !o.life.closed
	if closeNow {
		o.life.closed = true
	}
	o.life.mu.Unlock()
	if closeNow {
		o.o.Closed()
	}
}
