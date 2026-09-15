package network

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func TestProtocol_FeedRunAndIsolation(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	p := s.NewPool(montygo.WebSocketOptions{})

	first := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	v, err := first.FeedRun(ctx, "leaked = 123\nleaked + 1", nil)
	require.NoError(t, err)
	require.Equal(t, int64(124), v)
	require.NoError(t, first.Close(ctx))

	second := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err = second.FeedRun(ctx, "leaked", nil)
	var re *montygo.RuntimeError
	require.ErrorAs(t, err, &re)
	require.Equal(t, "name 'leaked' is not defined", re.Display(montygo.DisplayMsg))
}

func TestProtocol_VersionSkewIsFatal(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	c, _, err := s.RawDial(ctx, nil)
	require.NoError(t, err)
	defer c.CloseNow()

	require.NoError(t, sendRequest(ctx, c, configureRequest(2)))
	ev, err := readEvent(ctx, c)
	require.NoError(t, err)
	require.Equal(t,
		"unsupported protocol version 2 (server supports protocol version 3, try updating to a newer client version)",
		ev.GetFatalError().GetMessage())
	_, err = readEvent(ctx, c)
	requireClose(t, err, websocket.StatusNormalClosure, "")
}

func TestProtocol_FirstRequestMustBeConfigure(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	c, _, err := s.RawDial(ctx, nil)
	require.NoError(t, err)
	defer c.CloseNow()

	require.NoError(t, sendRequest(ctx, c, feedRequest("1")))
	_, err = readEvent(ctx, c)
	requireClose(t, err, websocket.StatusPolicyViolation, "expected Configure as the first request")
}

func TestProtocol_LifecycleRequestsClose1008(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	base := s.Baseline()
	c := rawConfigured(t, ctx, s)

	require.NoError(t, sendRequest(ctx, c, resetRequest()))
	_, err := readEvent(ctx, c)
	requireClose(t, err, websocket.StatusPolicyViolation, "lifecycle requests (Reset/Shutdown) are not accepted")
	s.WaitMetricDelta(base, "monty_server_sessions_total", map[string]string{"outcome": "error"}, 1, 5*time.Second)
}

func TestProtocol_TextMessageCloses1008(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	c := rawConfigured(t, ctx, s)

	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("1 + 1")))
	_, err := readEvent(ctx, c)
	requireClose(t, err, websocket.StatusPolicyViolation, "text messages are not part of the protocol")
}

func TestProtocol_LargeFrameAccepted(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-memory-mib", "512"))
	ctx := testCtx(t)
	p := s.NewPool(montygo.WebSocketOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})

	v, err := session.FeedRun(ctx, "len(x)", &montygo.FeedOptions{
		Inputs: map[string]any{"x": strings.Repeat("a", 16*1024*1024)},
	})
	require.NoError(t, err)
	require.Equal(t, int64(16*1024*1024), v)
}

func TestProtocol_MemoryKillIsMemoryError(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	p := s.NewPool(montygo.WebSocketOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxMemory: 1024}})

	_, err := session.FeedRun(ctx, "# "+strings.Repeat("a", 16*1024*1024), nil)
	var re *montygo.RuntimeError
	require.ErrorAs(t, err, &re)
	require.Equal(t, "MemoryError: the worker exceeded its memory limit and was terminated", re.Error())

	next := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	v, err := next.FeedRun(ctx, "1 + 1", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), v)
}

func TestProtocol_RemoteDialByContainerIP(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("container bridge addresses are routable from the host only on Linux")
	}
	s := SetupServer(t)
	ctx := testCtx(t)
	ip, err := s.Unit.ContainerIP(ctx)
	require.NoError(t, err)
	p := s.NewPool(montygo.WebSocketOptions{URL: "ws://" + ip + ":8000/"})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	v, err := session.FeedRun(ctx, "6 * 7", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), v)
}
