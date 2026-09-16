package network

import (
	"context"
	"github.com/asalimonov/montygo/monterr"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func requireDisconnect(t *testing.T, err error) {
	t.Helper()
	var de *monterr.DisconnectError
	require.ErrorAs(t, err, &de)
}

func TestTimeouts_IdleClosesSession(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--idle-timeout", "1"))
	ctx := testCtx(t)
	p := s.NewPool(wsOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err := session.FeedRun(ctx, "x = 1", nil)
	require.NoError(t, err)

	time.Sleep(2500 * time.Millisecond)
	_, err = session.FeedRun(ctx, "x", nil)
	var de *monterr.DisconnectError
	require.ErrorAs(t, err, &de)
	require.Equal(t, 1008, de.Code)
	require.Equal(t, "idle timeout of 1s exceeded", de.Reason)
	require.ErrorIs(t, err, monterr.ErrSessionLost)
	s.WaitMetric("monty_server_timeouts_total", map[string]string{"kind": "idle"}, 1, 5*time.Second)
}

func TestTimeouts_TurnTimeoutIncludesHostCallback(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--turn-timeout", "1"))
	ctx := testCtx(t)
	p := s.NewPool(wsOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})

	slow := func() int {
		time.Sleep(2500 * time.Millisecond)
		return 1
	}
	_, err := session.FeedRun(ctx, "slow() + 1", &montygo.FeedOptions{ExternalLookup: map[string]any{"slow": slow}})
	requireDisconnect(t, err)
	s.WaitMetric("monty_server_timeouts_total", map[string]string{"kind": "turn"}, 1, 5*time.Second)
}

func TestTimeouts_ServerCloseDuringStop(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--turn-timeout", "1"))
	ctx := testCtx(t)
	p := s.NewPool(wsOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})

	started := make(chan struct{})
	stubborn := func() int {
		close(started)
		time.Sleep(2500 * time.Millisecond)
		return 1
	}
	run := session.Go(ctx, "stubborn()", &montygo.FeedOptions{ExternalLookup: map[string]any{"stubborn": stubborn}})
	<-started
	stopped, err := run.Stop(ctx, montygo.StopPolicy{Timeout: 10 * time.Second})
	require.NoError(t, err)
	require.Equal(t, montygo.StopFinished, stopped.How, "the server ended the run, not the stop")
	var de *monterr.DisconnectError
	require.ErrorAs(t, stopped.Err, &de)
	require.False(t, stopped.SessionKept())
	require.Equal(t, montygo.SessionClosed, session.State())
}

func TestTimeouts_SessionTimeout(t *testing.T) {
	t.Parallel()
	requireSlowTests(t)
	s := SetupServer(t, WithArgs("--session-timeout", "2"))
	ctx := testCtx(t)
	p := s.NewPool(wsOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	for range 2 {
		_, err := session.FeedRun(ctx, "1", nil)
		require.NoError(t, err)
		time.Sleep(time.Second)
	}
	time.Sleep(1500 * time.Millisecond)
	_, err := session.FeedRun(ctx, "1", nil)
	requireDisconnect(t, err)
	s.WaitMetric("monty_server_timeouts_total", map[string]string{"kind": "session"}, 1, 5*time.Second)
}

// freezeProxy forwards TCP to target until frozen; then it swallows client bytes, pongs included.
type freezeProxy struct {
	ln     net.Listener
	frozen atomic.Bool
}

func startFreezeProxy(t *testing.T, target string) *freezeProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	fp := &freezeProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go fp.bridge(client, target)
		}
	}()
	return fp
}

func (fp *freezeProxy) bridge(client net.Conn, target string) {
	defer client.Close()
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer upstream.Close()
	go func() { _, _ = io.Copy(client, upstream) }()
	buf := make([]byte, 32*1024)
	for {
		n, err := client.Read(buf)
		if n > 0 && !fp.frozen.Load() {
			if _, werr := upstream.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (fp *freezeProxy) URL() string { return "ws://" + fp.ln.Addr().String() + "/" }

func TestTimeouts_KeepaliveDropsFrozenClient(t *testing.T) {
	t.Parallel()
	requireSlowTests(t)
	s := SetupServer(t, WithArgs("--keepalive", "1", "--idle-timeout", "0"))
	ctx := testCtx(t)
	proxy := startFreezeProxy(t, strings.TrimPrefix(s.Unit.HTTPBase(), "http://"))
	p := s.NewPool(wsOptions{URL: proxy.URL()})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err := session.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	proxy.frozen.Store(true)
	s.WaitMetric("monty_server_timeouts_total", map[string]string{"kind": "keepalive"}, 1, 10*time.Second)
	s.WaitMetric("monty_server_sessions_active", nil, 0, 5*time.Second)
	_ = session.Close(context.Background())
}
