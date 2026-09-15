package telemetry

import (
	"context"
	"sync"

	"github.com/asalimonov/montygo/internal/wire"
)

// Checkout observes one pool checkout: its span mirror while spans or logs
// are recorded, and its turn metrics while its pool is metered.
type Checkout struct {
	mu      sync.Mutex
	spans   *Spans
	metrics *TurnMetrics
}

// NewCheckout returns the observer of a checkout whose session span is a
// child of the span in parent, or nil when it would record nothing.
func NewCheckout(parent context.Context, pid int, hasPID, metered bool) *Checkout {
	c := &Checkout{}
	if r := Current(); r.Tracing() || r.Logging() {
		c.spans = NewSpans(r, parent, pid, hasPID)
	}
	if metered {
		c.metrics = &TurnMetrics{}
	}
	if c.spans == nil && c.metrics == nil {
		return nil
	}
	return c
}

// Sent records a request that reached the wire.
func (c *Checkout) Sent(req wire.Request, frameLen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spans != nil {
		c.spans.Sent(req)
	}
	if c.metrics != nil {
		c.metrics.Sent(req, frameLen)
	}
}

// Received records a decoded event.
func (c *Checkout) Received(ev *wire.Event, frameLen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spans != nil {
		c.spans.Received(ev)
	}
	if c.metrics != nil {
		c.metrics.Received(ev, frameLen)
	}
}

// Closed ends open spans innermost-first and releases an open suspension.
func (c *Checkout) Closed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spans != nil {
		c.spans.Close()
	}
	if c.metrics != nil {
		c.metrics.Close()
	}
}

// CallbackContext returns ctx carrying the checkout's innermost open span.
func (c *Checkout) CallbackContext(ctx context.Context) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spans == nil {
		return ctx
	}
	return c.spans.CallbackContext(ctx)
}
