package monty

import (
	"context"
	"crypto/tls"
	"net"
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
	// TLSConfig configures wss:// and https:// dials; nil uses the system roots. It is cloned per dial.
	TLSConfig *tls.Config
	// DialContext opens TCP connections; nil uses a net.Dialer.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
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
	p, err := newPool(ctx, Options{
		MaxProcesses:    opts.MaxProcesses,
		CheckoutTimeout: opts.CheckoutTimeout,
		RequestTimeout:  timeout,
	}, opts.dialer(timeout), BackendWebSocket, "", true)
	if err != nil {
		return nil, err
	}
	p.connectHeaders = opts.ConnectHeaders
	return p, nil
}

// CheckWebSocketHealth reports nil when the server behind opts.URL answers
// GET <path>/health with 200. It sends the ConnectHeaders and is bounded by
// RequestTimeout: 0 means 10s, NoRequestTimeout leaves only ctx.
func CheckWebSocketHealth(ctx context.Context, opts WebSocketOptions) error {
	timeout := opts.RequestTimeout
	if timeout == 0 {
		timeout = defaultWebSocketRequestTimeout
	}
	var headers [][2]string
	if opts.ConnectHeaders != nil {
		extra, err := opts.ConnectHeaders(ctx)
		if err != nil {
			return err
		}
		headers = sortedHeaders(extra)
	}
	return opts.dialer(timeout).HealthCheck(ctx, headers)
}

func (o WebSocketOptions) dialer(timeout time.Duration) *worker.WebSocketDialer {
	return &worker.WebSocketDialer{
		URL:         o.URL,
		DialTimeout: timeout,
		TLSConfig:   o.TLSConfig,
		DialContext: o.DialContext,
	}
}
