package worker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

const (
	// DefaultUserAgent identifies the pool on every WebSocket upgrade request.
	DefaultUserAgent = "monty-pool/0.0.23"
	// DefaultDialTimeout bounds a dial when the dialer sets no timeout.
	DefaultDialTimeout = 30 * time.Second
	// closeWriteBudget bounds how long teardown waits for the close handshake.
	closeWriteBudget = time.Second
)

type connectHeadersKey struct{}

// WithConnectHeaders attaches per-checkout WebSocket upgrade headers; later pairs win.
func WithConnectHeaders(ctx context.Context, headers [][2]string) context.Context {
	return context.WithValue(ctx, connectHeadersKey{}, headers)
}

// ConnectHeaders returns headers attached by WithConnectHeaders.
func ConnectHeaders(ctx context.Context) [][2]string {
	h, _ := ctx.Value(connectHeadersKey{}).([][2]string)
	return h
}

// WebSocketDialer reaches a remote protocol child over a WebSocket: one binary
// message per protocol frame, no length prefix.
type WebSocketDialer struct {
	URL string
	// DialTimeout bounds DNS, TCP, TLS and the upgrade, or a health check:
	// 0 means DefaultDialTimeout, a negative value leaves only ctx.
	DialTimeout time.Duration
	// UserAgent replaces DefaultUserAgent when set.
	UserAgent string
	// TLSConfig is cloned into the transport; nil keeps http.DefaultTransport's settings.
	TLSConfig *tls.Config
	// DialContext replaces net.Dialer.DialContext for the TCP connection.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d *WebSocketDialer) Kind() Kind                  { return KindWebSocket }
func (d *WebSocketDialer) Close(context.Context) error { return nil }

func (d *WebSocketDialer) Spawn(ctx context.Context) (Worker, error) {
	u, err := url.Parse(d.URL)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("%s: %w", d.URL, err)
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
	default:
		return nil, fmt.Errorf("%s: unsupported URL scheme %q", d.URL, u.Scheme)
	}
	header, host, err := d.upgradeHeader(ConnectHeaders(ctx))
	if err != nil {
		return nil, err
	}
	dctx, timeout, cancel := d.bound(ctx)
	defer cancel()
	var raw atomic.Pointer[net.Conn]
	transport := d.transport(&raw)
	defer transport.CloseIdleConnections()
	conn, _, err := websocket.Dial(dctx, d.URL, &websocket.DialOptions{
		HTTPClient:      &http.Client{Transport: transport},
		HTTPHeader:      header,
		Host:            host,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if dctx.Err() != nil {
			return nil, fmt.Errorf("%s: connect timed out after %s", d.URL, timeout)
		}
		return nil, fmt.Errorf("%s: %w", d.URL, err)
	}
	conn.SetReadLimit(wire.MaxFrameLen)
	w := &wsWorker{
		conn:   conn,
		frames: make(chan []byte, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if p := raw.Load(); p != nil {
		w.raw = *p
	}
	go w.read()
	return w, nil
}

func (d *WebSocketDialer) bound(ctx context.Context) (context.Context, time.Duration, context.CancelFunc) {
	timeout := d.DialTimeout
	if timeout == 0 {
		timeout = DefaultDialTimeout
	}
	if timeout < 0 {
		bounded, cancel := context.WithCancel(ctx)
		return bounded, timeout, cancel
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	return bounded, timeout, cancel
}

func (d *WebSocketDialer) transport(raw *atomic.Pointer[net.Conn]) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if d.TLSConfig != nil {
		t.TLSClientConfig = d.TLSConfig.Clone()
	}
	dial := d.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err == nil && raw != nil {
			raw.Store(&c)
		}
		return c, err
	}
	return t
}

// HealthCheck reports nil when GET <path>/health on the dialer's server answers 200.
func (d *WebSocketDialer) HealthCheck(ctx context.Context, headers [][2]string) error {
	target, err := healthURL(d.URL)
	if err != nil {
		return fmt.Errorf("%s: %w", d.URL, err)
	}
	header, host, err := d.upgradeHeader(headers)
	if err != nil {
		return err
	}
	hctx, timeout, cancel := d.bound(ctx)
	defer cancel()
	transport := d.transport(nil)
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	req.Header = header
	if host != "" {
		req.Host = host
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	switch {
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil && hctx.Err() != nil:
		return fmt.Errorf("%s: health check timed out after %s", target, timeout)
	case err != nil:
		return fmt.Errorf("%s: %w", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: health check returned %d", target, resp.StatusCode)
	}
	return nil
}

func healthURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return "", err
	}
	switch u.Scheme {
	case "ws", "http":
		u.Scheme = "http"
	case "wss", "https":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/health"
	u.RawPath = ""
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	return u.String(), nil
}

func (d *WebSocketDialer) upgradeHeader(pairs [][2]string) (http.Header, string, error) {
	header := http.Header{}
	ua := d.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	header.Set("User-Agent", ua)
	host := ""
	for _, pair := range pairs {
		name, val := pair[0], pair[1]
		if !validHeaderName(name) {
			return nil, "", fmt.Errorf("%s: connect header %s: invalid HTTP header name", d.URL, value.RustDebugString(name))
		}
		if !validHeaderValue(val) {
			return nil, "", fmt.Errorf("%s: connect header %s value: failed to parse header value", d.URL, value.RustDebugString(strings.ToLower(name)))
		}
		if strings.EqualFold(name, "Host") {
			host = val
			continue
		}
		header.Set(name, val)
	}
	return header, host, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

type wsWorker struct {
	conn *websocket.Conn
	// raw is the dialed TCP connection; closing it unblocks every pending operation.
	raw       net.Conn
	frames    chan []byte
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	dropOnce  sync.Once
	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
}

// ClosedError reports a close frame received from the peer.
type ClosedError struct {
	Code   int
	Reason string
}

func (e *ClosedError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("closed by server (%d)", e.Code)
	}
	return fmt.Sprintf("closed by server (%d): %s", e.Code, e.Reason)
}

func (w *wsWorker) read() {
	defer close(w.done)
	for {
		typ, data, err := w.conn.Read(context.Background())
		if err != nil || typ != websocket.MessageBinary {
			w.setErr(err, typ)
			w.drop()
			return
		}
		select {
		case w.frames <- data:
		case <-w.stop:
			return
		}
	}
}

func (w *wsWorker) setErr(err error, typ websocket.MessageType) {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	var ce websocket.CloseError
	switch {
	case errors.As(err, &ce):
		if ce.Code == websocket.StatusNormalClosure && w.stopped() {
			return
		}
		w.err = &ClosedError{Code: int(ce.Code), Reason: ce.Reason}
	case err != nil:
		if !w.stopped() {
			w.err = err
		}
	default:
		w.err = fmt.Errorf("unexpected %s message", typ)
	}
}

func (w *wsWorker) Done() <-chan struct{} { return w.done }

func (w *wsWorker) Err() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.err
}

func (w *wsWorker) stopped() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

func (w *wsWorker) signalStop() { w.stopOnce.Do(func() { close(w.stop) }) }

func (w *wsWorker) Send(ctx context.Context, payload []byte) error {
	if len(payload) > wire.MaxFrameLen {
		return &wire.FrameTooLargeError{Len: len(payload), Max: wire.MaxFrameLen}
	}
	select {
	case <-w.done:
		if cause := w.Err(); cause != nil {
			return fmt.Errorf("%w: %w", ErrWorkerGone, cause)
		}
		return ErrWorkerGone
	default:
	}
	if w.stopped() {
		return ErrWorkerGone
	}
	err := w.conn.Write(ctx, websocket.MessageBinary, payload)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (w *wsWorker) Recv(ctx context.Context) ([]byte, error) {
	if w.stopped() {
		return nil, wire.ErrTruncated
	}
	select {
	case f := <-w.frames:
		return f, nil
	case <-w.done:
		select {
		case f := <-w.frames:
			return f, nil
		default:
			return nil, wire.ErrTruncated
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *wsWorker) drop() {
	w.dropOnce.Do(func() {
		if w.raw != nil {
			_ = w.raw.Close()
		}
		go func() { _ = w.conn.CloseNow() }()
	})
}

// Kill drops the connection without a close handshake.
func (w *wsWorker) Kill() {
	w.signalStop()
	w.drop()
}

// Close sends a normal close frame, waiting at most closeWriteBudget for the handshake, then drops the connection.
func (w *wsWorker) Close() {
	w.closeOnce.Do(func() {
		w.signalStop()
		finished := make(chan struct{})
		go func() {
			_ = w.conn.Close(websocket.StatusNormalClosure, "")
			close(finished)
		}()
		timer := time.NewTimer(closeWriteBudget)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
		}
		w.Kill()
	})
}

func (w *wsWorker) Wait(ctx context.Context) (Status, bool) {
	select {
	case <-w.done:
		return Status{}, true
	case <-ctx.Done():
		return Status{}, false
	}
}

func (w *wsWorker) PID() (int, bool) { return 0, false }
func (w *wsWorker) Kind() Kind       { return KindWebSocket }

func (w *wsWorker) Alive() bool {
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}
