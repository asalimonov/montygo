package monty

import (
	"context"
	"time"

	"github.com/asalimonov/montygo/internal/worker"
)

// NoRequestTimeout disables the per-turn deadline of a WebSocket pool.
const NoRequestTimeout time.Duration = -1

const defaultWebSocketRequestTimeout = 10 * time.Second

// WebSocketOptions configure a pool of remote workers reached over WebSocket.
type WebSocketOptions struct {
	// URL is dialed verbatim for every checkout.
	URL string
	// MaxProcesses caps concurrent connections: 0 means runtime.NumCPU().
	MaxProcesses int
	// CheckoutTimeout bounds waiting for capacity: 0 waits forever.
	CheckoutTimeout time.Duration
	// RequestTimeout is the per-turn deadline and the dial budget: 0 means 10s, NoRequestTimeout disables it.
	RequestTimeout time.Duration
	// ConnectHeaders supplies upgrade headers. Checkout calls it once with its
	// ctx, before waiting for capacity and dialing; its error fails the checkout unchanged.
	ConnectHeaders func(ctx context.Context) (map[string]string, error)
}

// NewWebSocket creates a pool whose sessions each dial a fresh, single-use
// connection to a remote worker.
func NewWebSocket(ctx context.Context, opts WebSocketOptions) (*Pool, error) {
	timeout := opts.RequestTimeout
	switch {
	case timeout == 0:
		timeout = defaultWebSocketRequestTimeout
	case timeout < 0:
		timeout = 0
	}
	dialer := &worker.WebSocketDialer{URL: opts.URL, DialTimeout: timeout}
	p, err := newPool(ctx, Options{
		MaxProcesses:    opts.MaxProcesses,
		CheckoutTimeout: opts.CheckoutTimeout,
		RequestTimeout:  timeout,
	}, dialer, BackendWebSocket, "", true)
	if err != nil {
		return nil, err
	}
	p.connectHeaders = opts.ConnectHeaders
	return p, nil
}
