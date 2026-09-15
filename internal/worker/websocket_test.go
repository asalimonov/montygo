package worker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
)

func wsHandler(handler func(conn *websocket.Conn, r *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		handler(conn, r)
	})
}

// wsServe runs handler for every upgrade and returns the ws:// URL.
func wsServe(t *testing.T, handler func(conn *websocket.Conn, r *http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(wsHandler(handler))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// wsServeTLS runs handler for every upgrade over TLS and returns the wss:// URL
// with a client config trusting the server certificate.
func wsServeTLS(t *testing.T, handler func(conn *websocket.Conn, r *http.Request)) (string, *tls.Config) {
	t.Helper()
	srv := httptest.NewTLSServer(wsHandler(handler))
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	return "wss" + strings.TrimPrefix(srv.URL, "https"), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func wsCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func wsSpawn(t *testing.T, ctx context.Context, url string) Worker {
	t.Helper()
	w, err := (&WebSocketDialer{URL: url}).Spawn(ctx)
	require.NoError(t, err)
	t.Cleanup(w.Kill)
	return w
}

func wsRequireEnded(t *testing.T, w Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, ok := w.Wait(ctx)
	require.True(t, ok)
	require.False(t, status.Known)
	require.False(t, w.Alive())
}

func TestWebSocketWorker(t *testing.T) {
	t.Run("malformed connect headers fail before any I/O", func(t *testing.T) {
		d := &WebSocketDialer{URL: "ws://127.0.0.1:9"}
		cases := []struct {
			pair [2]string
			want string
		}{
			{[2]string{"bad name", "v"}, `ws://127.0.0.1:9: connect header "bad name": invalid HTTP header name`},
			{[2]string{"", "v"}, `ws://127.0.0.1:9: connect header "": invalid HTTP header name`},
			{[2]string{"TraceParent", "line\nbreak"}, `ws://127.0.0.1:9: connect header "traceparent" value: failed to parse header value`},
		}
		for _, tc := range cases {
			_, err := d.Spawn(WithConnectHeaders(wsCtx(t), [][2]string{tc.pair}))
			require.EqualError(t, err, tc.want)
		}
	})

	t.Run("unparsable URL fails the dial", func(t *testing.T) {
		_, err := (&WebSocketDialer{URL: "not a url"}).Spawn(wsCtx(t))
		require.Error(t, err)
		require.True(t, strings.HasPrefix(err.Error(), "not a url: "), err.Error())
	})

	t.Run("upgrade carries user agent and last-wins connect headers", func(t *testing.T) {
		seen := make(chan http.Header, 1)
		url := wsServe(t, func(conn *websocket.Conn, r *http.Request) {
			seen <- r.Header.Clone()
			_, _, _ = conn.Read(context.Background())
		})
		ctx := wsCtx(t)
		wsSpawn(t, ctx, url)
		h := <-seen
		require.Equal(t, []string{DefaultUserAgent}, h.Values("User-Agent"))

		ctx = WithConnectHeaders(ctx, [][2]string{{"user-agent", "my-app/1.0"}, {"X-Token", "a"}, {"x-token", "b"}})
		wsSpawn(t, ctx, url)
		h = <-seen
		require.Equal(t, []string{"my-app/1.0"}, h.Values("User-Agent"))
		require.Equal(t, []string{"b"}, h.Values("X-Token"))
	})

	t.Run("one binary message per frame and a close frame on Close", func(t *testing.T) {
		closed := make(chan error, 1)
		url := wsServe(t, func(conn *websocket.Conn, _ *http.Request) {
			typ, data, err := conn.Read(context.Background())
			if err != nil || typ != websocket.MessageBinary {
				closed <- errors.New("expected a binary message")
				return
			}
			_ = conn.Write(context.Background(), websocket.MessageBinary, append([]byte("echo:"), data...))
			_, _, err = conn.Read(context.Background())
			closed <- err
		})
		ctx := wsCtx(t)
		w := wsSpawn(t, ctx, url)
		require.Equal(t, KindWebSocket, w.Kind())
		_, hasPID := w.PID()
		require.False(t, hasPID)
		require.True(t, w.Alive())
		require.NoError(t, w.Send(ctx, []byte("frame")))
		got, err := w.Recv(ctx)
		require.NoError(t, err)
		require.Equal(t, "echo:frame", string(got))
		start := time.Now()
		w.Close()
		require.Less(t, time.Since(start), 2*time.Second)
		require.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(<-closed))
		wsRequireEnded(t, w)
		_, err = w.Recv(ctx)
		require.ErrorIs(t, err, wire.ErrTruncated)
	})

	t.Run("Kill drops the connection without a close frame", func(t *testing.T) {
		closed := make(chan error, 1)
		url := wsServe(t, func(conn *websocket.Conn, _ *http.Request) {
			_, _, err := conn.Read(context.Background())
			closed <- err
		})
		w := wsSpawn(t, wsCtx(t), url)
		w.Kill()
		err := <-closed
		require.Error(t, err)
		require.Equal(t, websocket.StatusCode(-1), websocket.CloseStatus(err))
		wsRequireEnded(t, w)
	})

	t.Run("a frame sent before the peer closes is still delivered", func(t *testing.T) {
		url := wsServe(t, func(conn *websocket.Conn, _ *http.Request) {
			_ = conn.Write(context.Background(), websocket.MessageBinary, []byte("last"))
		})
		ctx := wsCtx(t)
		w := wsSpawn(t, ctx, url)
		wsRequireEnded(t, w)
		got, err := w.Recv(ctx)
		require.NoError(t, err)
		require.Equal(t, "last", string(got))
		_, err = w.Recv(ctx)
		require.ErrorIs(t, err, wire.ErrTruncated)
	})

	t.Run("a text message ends the stream", func(t *testing.T) {
		url := wsServe(t, func(conn *websocket.Conn, _ *http.Request) {
			_ = conn.Write(context.Background(), websocket.MessageText, []byte("hello"))
			_, _, _ = conn.Read(context.Background())
		})
		ctx := wsCtx(t)
		w := wsSpawn(t, ctx, url)
		_, err := w.Recv(ctx)
		require.ErrorIs(t, err, wire.ErrTruncated)
		wsRequireEnded(t, w)
	})

	t.Run("pings are answered while idle", func(t *testing.T) {
		pinged := make(chan error, 1)
		url := wsServe(t, func(conn *websocket.Conn, _ *http.Request) {
			go func() { _, _, _ = conn.Read(context.Background()) }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pinged <- conn.Ping(ctx)
		})
		wsSpawn(t, wsCtx(t), url)
		require.NoError(t, <-pinged)
	})

	t.Run("connect headers round-trip through the context", func(t *testing.T) {
		require.Nil(t, ConnectHeaders(context.Background()))
		pairs := [][2]string{{"a", "b"}}
		require.Equal(t, pairs, ConnectHeaders(WithConnectHeaders(context.Background(), pairs)))
	})

	t.Run("TLSConfig is used for wss dials", func(t *testing.T) {
		url, cfg := wsServeTLS(t, func(conn *websocket.Conn, _ *http.Request) {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			_ = conn.Write(context.Background(), websocket.MessageBinary, data)
			_, _, _ = conn.Read(context.Background())
		})
		ctx := wsCtx(t)
		_, err := (&WebSocketDialer{URL: url}).Spawn(ctx)
		require.Error(t, err)
		require.True(t, strings.HasPrefix(err.Error(), url+": "), err.Error())

		w, err := (&WebSocketDialer{URL: url, TLSConfig: cfg}).Spawn(ctx)
		require.NoError(t, err)
		t.Cleanup(w.Kill)
		require.NoError(t, w.Send(ctx, []byte("frame")))
		got, err := w.Recv(ctx)
		require.NoError(t, err)
		require.Equal(t, "frame", string(got))
		require.Empty(t, cfg.NextProtos, "the caller's config must not be mutated")
	})

	t.Run("DialContext opens the connection", func(t *testing.T) {
		url := wsServe(t, func(conn *websocket.Conn, _ *http.Request) {
			_, _, _ = conn.Read(context.Background())
		})
		var dials atomic.Int32
		d := &WebSocketDialer{URL: url, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}
		w, err := d.Spawn(wsCtx(t))
		require.NoError(t, err)
		t.Cleanup(w.Kill)
		require.Equal(t, int32(1), dials.Load())

		errRefused := errors.New("dial refused")
		d.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, errRefused }
		_, err = d.Spawn(wsCtx(t))
		require.ErrorIs(t, err, errRefused)
	})

	t.Run("Kill unblocks a pending Recv over TLS", func(t *testing.T) {
		url, cfg := wsServeTLS(t, func(conn *websocket.Conn, _ *http.Request) {
			_, _, _ = conn.Read(context.Background())
		})
		ctx := wsCtx(t)
		w, err := (&WebSocketDialer{URL: url, TLSConfig: cfg}).Spawn(ctx)
		require.NoError(t, err)
		t.Cleanup(w.Kill)
		require.IsType(t, &net.TCPConn{}, w.(*wsWorker).raw)
		got := make(chan error, 1)
		go func() {
			_, err := w.Recv(ctx)
			got <- err
		}()
		time.Sleep(20 * time.Millisecond)
		w.Kill()
		select {
		case err := <-got:
			require.ErrorIs(t, err, wire.ErrTruncated)
		case <-time.After(5 * time.Second):
			t.Fatal("Recv did not return after Kill")
		}
		wsRequireEnded(t, w)
	})

	t.Run("health URL derivation", func(t *testing.T) {
		for raw, want := range map[string]string{
			"ws://h:1/":           "http://h:1/health",
			"wss://h/p":           "https://h/p/health",
			"ws://h:1":            "http://h:1/health",
			"http://h/p/?q=1#top": "http://h/p/health",
			"https://h/a/b/":      "https://h/a/b/health",
		} {
			got, err := healthURL(raw)
			require.NoError(t, err, raw)
			require.Equal(t, want, got, raw)
		}
		_, err := healthURL("ftp://h/")
		require.EqualError(t, err, `unsupported URL scheme "ftp"`)

		ctx := wsCtx(t)
		d := &WebSocketDialer{URL: "ftp://127.0.0.1:9"}
		healthErr := d.HealthCheck(ctx, nil)
		_, spawnErr := d.Spawn(ctx)
		require.EqualError(t, healthErr, `ftp://127.0.0.1:9: unsupported URL scheme "ftp"`)
		require.EqualError(t, spawnErr, healthErr.Error())
	})

	t.Run("HealthCheck reports non-200 and sends connect headers", func(t *testing.T) {
		requests := make(chan *http.Request, 4)
		var status atomic.Int32
		status.Store(http.StatusServiceUnavailable)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r.Clone(context.Background())
			w.WriteHeader(int(status.Load()))
		}))
		t.Cleanup(srv.Close)
		ctx := wsCtx(t)
		d := &WebSocketDialer{URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/prefix/?token=1"}

		err := d.HealthCheck(ctx, [][2]string{{"X-Token", "a"}, {"x-token", "b"}, {"Host", "monty.test"}})
		require.EqualError(t, err, srv.URL+"/prefix/health: health check returned 503")
		r := <-requests
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/prefix/health", r.URL.Path)
		require.Empty(t, r.URL.RawQuery)
		require.Equal(t, []string{"b"}, r.Header.Values("X-Token"))
		require.Equal(t, DefaultUserAgent, r.Header.Get("User-Agent"))
		require.Equal(t, "monty.test", r.Host)

		status.Store(http.StatusOK)
		require.NoError(t, d.HealthCheck(ctx, nil))
		<-requests

		err = d.HealthCheck(ctx, [][2]string{{"bad name", "v"}})
		require.EqualError(t, err, d.URL+`: connect header "bad name": invalid HTTP header name`)
	})

	t.Run("HealthCheck reports its timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) })
		d := &WebSocketDialer{URL: srv.URL, DialTimeout: 50 * time.Millisecond}
		require.EqualError(t, d.HealthCheck(wsCtx(t), nil), srv.URL+"/health: health check timed out after 50ms")

		ctx, cancel := context.WithCancel(wsCtx(t))
		time.AfterFunc(50*time.Millisecond, cancel)
		d.DialTimeout = -1
		require.ErrorIs(t, d.HealthCheck(ctx, nil), context.Canceled)
	})
}
