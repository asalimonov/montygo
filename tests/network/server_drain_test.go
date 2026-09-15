package network

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

// waitListenerClosed returns once the drained server refuses new HTTP connections.
func waitListenerClosed(t *testing.T, s *TestServer) {
	t.Helper()
	require.Eventually(t, func() bool {
		return monty.CheckWebSocketHealth(context.Background(), monty.WebSocketOptions{URL: s.URL(), RequestTimeout: time.Second}) != nil
	}, 10*time.Second, 50*time.Millisecond)
}

func requireShutdown(t *testing.T, err error) *monty.ShutdownError {
	t.Helper()
	var se *monty.ShutdownError
	require.ErrorAs(t, err, &se)
	return se
}

func TestDrain_IdleSessionGetsShutdownDump(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "10"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	session := s.Checkout(ctx, p, monty.CheckoutOptions{})
	_, err := session.FeedRun(ctx, "x = 41", nil)
	require.NoError(t, err)

	s.Signal("TERM")
	waitListenerClosed(t, s)
	_, err = session.FeedRun(ctx, "x + 1", nil)
	shutdown := requireShutdown(t, err)
	require.Equal(t, "MTYD", string(shutdown.Dump[:4]))
	require.Equal(t, 0, s.WaitExited(15*time.Second))

	s.Recreate()
	restored, err := loadInto(t, ctx, s, shutdown.Dump)
	require.NoError(t, err)
	v, err := restored.FeedRun(ctx, "x + 1", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), v)
}

func TestDrain_BeforeConfigureCarriesNoDump(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "10"))
	ctx := testCtx(t)
	c, _, err := s.RawDial(ctx, nil)
	require.NoError(t, err)
	defer c.CloseNow()

	s.Signal("TERM")
	waitListenerClosed(t, s)
	require.NoError(t, sendRequest(ctx, c, configureRequest(monty.ProtocolVersion)))
	ev, err := readEvent(ctx, c)
	require.NoError(t, err)
	require.NotNil(t, ev.GetShutdown(), "expected ShutdownDump, got %v", ev)
	require.Nil(t, ev.GetShutdown().GetDump())
	_, err = readEvent(ctx, c)
	requireClose(t, err, websocket.StatusGoingAway, "server is shutting down")
}

func TestDrain_InFlightTurnFinishesFirst(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "20"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	session := s.Checkout(ctx, p, monty.CheckoutOptions{})

	type result struct {
		value any
		err   error
	}
	done := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		v, err := session.FeedRun(ctx, "total = 0\nfor i in range(4000000):\n    total += i\ntotal", &monty.FeedOptions{})
		done <- result{v, err}
	}()
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(started)
	}()
	<-started
	s.Signal("TERM")
	r := <-done
	require.NoError(t, r.err)
	require.Equal(t, int64(4000000*3999999/2), r.value)

	waitListenerClosed(t, s)
	_, err := session.FeedRun(ctx, "total", nil)
	requireShutdown(t, err)
}

func TestDrain_SilentSessionDroppedAfterGrace(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "1"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	session := s.Checkout(ctx, p, monty.CheckoutOptions{})
	_, err := session.FeedRun(ctx, "x = 1", nil)
	require.NoError(t, err)

	s.Signal("TERM")
	require.Equal(t, 0, s.WaitExited(15*time.Second))
	_, err = session.FeedRun(ctx, "x", nil)
	requireDisconnect(t, err)
}

func TestDrain_SecondSignalDropsImmediately(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "60"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	session := s.Checkout(ctx, p, monty.CheckoutOptions{})
	_, err := session.FeedRun(ctx, "x = 1", nil)
	require.NoError(t, err)

	s.Signal("TERM")
	waitListenerClosed(t, s)
	begin := time.Now()
	s.Signal("TERM")
	require.Equal(t, 0, s.WaitExited(20*time.Second))
	require.Less(t, time.Since(begin), 20*time.Second)
	_, err = session.FeedRun(ctx, "x", nil)
	requireDisconnect(t, err)
}

func TestDrain_ExitsZero(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	s.Signal("TERM")
	require.Equal(t, 0, s.WaitExited(15*time.Second))
}
