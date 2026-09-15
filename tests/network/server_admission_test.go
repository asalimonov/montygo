package network

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func TestAdmission_CapacityReturns503(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-sessions", "1"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{MaxProcesses: 2})
	held := s.Checkout(ctx, p, monty.CheckoutOptions{})
	_, err := held.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	_, err = p.Checkout(ctx, monty.CheckoutOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "503")
	s.WaitMetric("monty_server_rejections_total", map[string]string{"reason": "capacity"}, 1, 5*time.Second)
}

func TestAdmission_ClientQuotaReturns429(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-sessions-per-client", "1"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{MaxProcesses: 2})
	held := s.Checkout(ctx, p, monty.CheckoutOptions{})
	_, err := held.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	_, err = p.Checkout(ctx, monty.CheckoutOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "429")
	s.WaitMetric("monty_server_rejections_total", map[string]string{"reason": "client_quota"}, 1, 5*time.Second)
}

func TestAdmission_TrustForwardedForKeysQuota(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-sessions-per-client", "1", "--trust-forwarded-for"))
	ctx := testCtx(t)
	forwarded := func(ip string) *monty.Pool {
		return s.NewPool(monty.WebSocketOptions{
			MaxProcesses: 2,
			ConnectHeaders: func(context.Context) (map[string]string, error) {
				return map[string]string{"X-Forwarded-For": "198.51.100.9, " + ip}, nil
			},
		})
	}
	first := s.Checkout(ctx, forwarded("10.0.0.1"), monty.CheckoutOptions{})
	_, err := first.FeedRun(ctx, "1", nil)
	require.NoError(t, err)
	second := s.Checkout(ctx, forwarded("10.0.0.2"), monty.CheckoutOptions{})
	_, err = second.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	_, err = forwarded("10.0.0.1").Checkout(ctx, monty.CheckoutOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "429")
}

func TestAdmission_InfoHealthMetricsPages(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)

	status, body, err := s.Get(ctx, "/")
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Contains(t, body, "monty-server "+monty.Version)
	require.Contains(t, body, "WebSocket endpoint: ws://")

	status, _, err = s.Get(ctx, "/health")
	require.NoError(t, err)
	require.Equal(t, 200, status)

	status, body, err = s.Get(ctx, "/metrics")
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Contains(t, body, `monty_server_build_info{version="`+monty.Version+`"`)
	require.Contains(t, body, "monty_server_sessions_active 0")

	require.NoError(t, monty.CheckWebSocketHealth(ctx, s.WSOptions()))
}

func TestAdmission_ListenerRefusesDuringDrain(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "10"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{MaxProcesses: 2})
	held := s.Checkout(ctx, p, monty.CheckoutOptions{})
	_, err := held.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	s.Signal("TERM")
	require.Eventually(t, func() bool {
		return monty.CheckWebSocketHealth(ctx, s.WSOptions()) != nil
	}, 5*time.Second, 50*time.Millisecond)
	_, err = p.Checkout(ctx, monty.CheckoutOptions{})
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "429"), err.Error())
}
