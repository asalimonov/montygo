package network

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func TestAdmission_CapacityReturns503(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-sessions", "1"))
	ctx := testCtx(t)
	p := s.NewPool(wsOptions{MaxWorkers: 2})
	held := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err := held.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	_, err = p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "503")
	s.WaitMetric("monty_server_rejections_total", map[string]string{"reason": "capacity"}, 1, 5*time.Second)
}

func TestAdmission_ClientQuotaReturns429(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-sessions-per-client", "1"))
	ctx := testCtx(t)
	p := s.NewPool(wsOptions{MaxWorkers: 2})
	held := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err := held.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	_, err = p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "429")
	s.WaitMetric("monty_server_rejections_total", map[string]string{"reason": "client_quota"}, 1, 5*time.Second)
}

func TestAdmission_TrustForwardedForKeysQuota(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--max-sessions-per-client", "1", "--trust-forwarded-for"))
	ctx := testCtx(t)
	forwarded := func(ip string) *montygo.Pool {
		return s.NewPool(wsOptions{
			MaxWorkers: 2,
			ConnectHeaders: func(context.Context) (map[string]string, error) {
				return map[string]string{"X-Forwarded-For": "198.51.100.9, " + ip}, nil
			},
		})
	}
	first := s.Checkout(ctx, forwarded("10.0.0.1"), montygo.CheckoutOptions{})
	_, err := first.FeedRun(ctx, "1", nil)
	require.NoError(t, err)
	second := s.Checkout(ctx, forwarded("10.0.0.2"), montygo.CheckoutOptions{})
	_, err = second.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	_, err = forwarded("10.0.0.1").Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "429")
}

func TestAdmission_InfoHealthMetricsPages(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)

	info, err := fetchInfo(ctx, s.WSOptions())
	require.NoError(t, err)
	require.NotEmpty(t, info.Version)

	status, body, err := s.Get(ctx, "/")
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Contains(t, body, "monty-server "+info.Version+" (monty "+info.MontyRev+")")
	require.Contains(t, body, "WebSocket endpoint: ws://")

	status, _, err = s.Get(ctx, "/health")
	require.NoError(t, err)
	require.Equal(t, 200, status)

	status, body, err = s.Get(ctx, "/metrics")
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Contains(t, body, `monty_server_build_info{version="`+info.Version+`"`)
	require.Contains(t, body, "monty_server_sessions_active 0")

	require.NoError(t, checkHealth(ctx, s.WSOptions()))
}

func TestAdmission_InfoEndpoint(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs(
		"--idle-timeout", "45", "--keepalive", "0", "--session-timeout", "0", "--turn-timeout", "120",
		"--max-duration", "30", "--max-memory-mib", "32", "--max-recursion-depth", "500",
		"--max-sessions", "7", "--max-sessions-per-client", "3",
	))
	ctx := testCtx(t)

	info, err := fetchInfo(ctx, s.WSOptions())
	require.NoError(t, err)
	require.Equal(t, montygo.ProtocolVersion, info.ProtocolVersion)
	require.Len(t, info.MontyRev, 40)
	require.True(t, strings.HasPrefix(info.MontyRev, montygo.UpstreamRev), info.MontyRev)
	require.NotEmpty(t, info.Version)
	if want := os.Getenv("MONTYGO_BUILD_VERSION"); want != "" {
		require.Equal(t, want, info.Version)
	}
	require.Equal(t, montygo.ServerLimits{
		IdleTimeout:          45 * time.Second,
		Keepalive:            0,
		SessionTimeout:       0,
		TurnTimeout:          120 * time.Second,
		MaxDuration:          30 * time.Second,
		MaxMemory:            32 << 20,
		MaxRecursionDepth:    500,
		MaxSessions:          7,
		MaxSessionsPerClient: 3,
	}, info.Limits)

	status, body, err := s.Get(ctx, "/info")
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Contains(t, body, `"version":"`+info.Version+`"`)
}

func TestAdmission_ListenerRefusesDuringDrain(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "10"))
	ctx := testCtx(t)
	p := s.NewPool(wsOptions{MaxWorkers: 2})
	held := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err := held.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	s.Signal("TERM")
	require.Eventually(t, func() bool {
		return checkHealth(ctx, s.WSOptions()) != nil
	}, 5*time.Second, 50*time.Millisecond)
	_, err = p.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "429"), err.Error())
}
