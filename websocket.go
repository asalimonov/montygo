package montygo

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"

	"github.com/asalimonov/montygo/internal/worker"
	"github.com/asalimonov/montygo/supervisor"
)

// NoRequestTimeout disables the per-turn deadline of a WebSocket pool.
const NoRequestTimeout time.Duration = -1

const defaultWebSocketRequestTimeout = 10 * time.Second

// WebSocketOptions configure a pool of remote workers reached over WebSocket.
type WebSocketOptions struct {
	// URL is dialed verbatim for every checkout. Supervisor replaces it.
	URL string
	// Supervisor resolves the endpoint before every dial and MAY restart the
	// server; it replaces URL and ConnectHeaders. NewDocker supplies one.
	Supervisor ServerSupervisor
	// Recovery bounds the retries of a supervised dial.
	Recovery RecoveryPolicy
	// RotateSessions moves each session to a fresh connection before the server's
	// session timeout closes it. It reads GET /info once; a server without the
	// endpoint, or without usable timeouts, leaves rotation off.
	RotateSessions bool
	// RotationMargin is the lead time of a rotation: 0 means 30s.
	RotationMargin time.Duration
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
	// Telemetry selects this pool's telemetry; nil uses the process-wide installation.
	Telemetry *TelemetryComponents
	// Stop is the default stop policy of this pool's sessions; zero fields inherit DefaultStopPolicy.
	Stop StopPolicy
}

func (o WebSocketOptions) validate() error {
	switch {
	case o.Supervisor != nil && (o.URL != "" || o.ConnectHeaders != nil):
		return &OptionError{Message: "supervisor replaces url and connectHeaders"}
	case o.Supervisor == nil && o.URL == "":
		return &OptionError{Message: "url or supervisor is required"}
	case o.Recovery.Attempts < 0:
		return &OptionError{Message: "recovery.attempts must not be negative"}
	case o.Recovery.AttemptTimeout < 0:
		return &OptionError{Message: "recovery.attemptTimeout must not be negative"}
	case o.RotationMargin < 0:
		return &OptionError{Message: "rotationMargin must not be negative"}
	}
	return nil
}

// supervisor is the pool's endpoint source: the caller's, or a fixed URL.
func (o WebSocketOptions) supervisor() ServerSupervisor {
	if o.Supervisor != nil {
		return o.Supervisor
	}
	return supervisor.NewStatic(o.URL, o.TLSConfig, o.ConnectHeaders)
}

// resolve inlines a supervisor's current endpoint, so the HTTP helpers reach the
// same server a checkout would dial.
func (o WebSocketOptions) resolve(ctx context.Context) (WebSocketOptions, error) {
	if o.Supervisor == nil {
		return o, nil
	}
	ep, err := o.Supervisor.Endpoint(ctx)
	if err != nil {
		return o, err
	}
	o.URL = ep.URL
	if ep.TLSConfig != nil {
		o.TLSConfig = ep.TLSConfig
	}
	if len(ep.Headers) > 0 {
		headers := ep.Headers
		o.ConnectHeaders = func(context.Context) (map[string]string, error) { return headers, nil }
	}
	o.Supervisor = nil
	return o, nil
}

// NewWebSocket creates a pool whose sessions each dial a fresh, single-use
// connection to a remote worker.
func NewWebSocket(ctx context.Context, opts WebSocketOptions) (*Pool, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	var info *ServerInfo
	if opts.RotateSessions {
		fetched, err := FetchServerInfo(ctx, opts)
		switch {
		case errors.Is(err, ErrNoServerInfo):
		case err != nil:
			return nil, err
		default:
			info = fetched
		}
	}
	return newWebSocketPool(ctx, opts, BackendWebSocket, info)
}

// newWebSocketPool builds the pool NewWebSocket and NewDocker share. info is the
// server's reported limits, or nil when rotation is off or unavailable.
func newWebSocketPool(ctx context.Context, opts WebSocketOptions, backend Backend, info *ServerInfo) (*Pool, error) {
	timeout := opts.RequestTimeout
	switch {
	case timeout == 0:
		timeout = defaultWebSocketRequestTimeout
	case timeout < 0:
		timeout = 0
	}
	rec := resolveRecorder(opts.Telemetry)
	p, err := newPool(ctx, Options{
		MaxProcesses:    opts.MaxProcesses,
		CheckoutTimeout: opts.CheckoutTimeout,
		RequestTimeout:  timeout,
		Stop:            opts.Stop,
	}, opts.dialer(timeout), backend, "", true, rec)
	if err != nil {
		return nil, err
	}
	p.connectHeaders = opts.ConnectHeaders
	p.supervised = opts.Supervisor != nil
	p.recovery = newRecoverer(opts.supervisor(), opts.Recovery, rec)
	if opts.RotateSessions {
		p.rotation = newRotationPolicy(info, opts.RotationMargin)
	}
	return p, nil
}

// CheckWebSocketHealth reports nil when the server behind opts.URL, or behind
// opts.Supervisor's endpoint, answers GET <path>/health with 200. It sends the
// ConnectHeaders and is bounded by RequestTimeout: 0 means 10s, NoRequestTimeout
// leaves only ctx.
func CheckWebSocketHealth(ctx context.Context, opts WebSocketOptions) error {
	opts, err := opts.resolve(ctx)
	if err != nil {
		return err
	}
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
		UserAgent:   userAgent(),
		TLSConfig:   o.TLSConfig,
		DialContext: o.DialContext,
	}
}
