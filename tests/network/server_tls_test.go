package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

// tlsProxy terminates TLS in the test process and forwards to the server container.
func tlsProxy(t *testing.T, s *TestServer) (string, *tls.Config) {
	t.Helper()
	target, err := url.Parse(s.Unit.HTTPBase())
	require.NoError(t, err)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(proxy.Close)
	roots := x509.NewCertPool()
	roots.AddCert(proxy.Certificate())
	return "wss://" + strings.TrimPrefix(proxy.URL, "https://") + "/", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func TestTLS_WSSThroughReverseProxy(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	wss, tlsConfig := tlsProxy(t, s)

	p := s.NewPool(wsOptions{URL: wss, TLSConfig: tlsConfig})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	v, err := session.FeedRun(ctx, "x = 20\nx + 22", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), v)

	untrusted := s.NewPool(wsOptions{URL: wss})
	_, err = untrusted.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "certificate")
}

func TestTLS_HealthCheckOverTLS(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	wss, tlsConfig := tlsProxy(t, s)

	require.NoError(t, checkHealth(ctx, wsOptions{URL: wss, TLSConfig: tlsConfig}))
	require.Error(t, checkHealth(ctx, wsOptions{URL: wss}))
}

func TestTLS_DialContextIsUsed(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	var dials atomic.Int32
	dialer := &net.Dialer{}
	p := s.NewPool(wsOptions{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return dialer.DialContext(ctx, network, addr)
		},
	})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err := session.FeedRun(ctx, "1", nil)
	require.NoError(t, err)
	require.Positive(t, dials.Load())
}
