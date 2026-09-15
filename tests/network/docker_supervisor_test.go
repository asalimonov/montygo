package network

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/asalimonov/montygo"
)

// Limits short enough for a test, and still long enough for the rotation policy,
// which needs a session to outlive two lead times.
const (
	supervisorSessionTimeout = "10"
	supervisorTurnTimeout    = "2"
	supervisorMargin         = time.Second
)

// newSupervisor starts a container from the image under test and closes it at
// test end. The image reference is pinned, so no tag is derived or pulled.
func newSupervisor(t *testing.T, env map[string]string) *montygo.DockerSupervisor {
	t.Helper()
	sup, err := montygo.NewDockerSupervisor(testCtx(t), montygo.DockerOptions{
		Image:        GetPool().Image(),
		Env:          env,
		MaxProcesses: 4,
		StopTimeout:  2 * time.Second,
	})
	require.NoError(t, err, "start a monty-server container")
	t.Cleanup(func() { _ = sup.Close(context.Background()) })
	return sup
}

func supervisorPool(t *testing.T, sup *montygo.DockerSupervisor, recovery montygo.RecoveryPolicy) *montygo.Pool {
	t.Helper()
	p, err := montygo.NewWebSocket(testCtx(t), montygo.WebSocketOptions{
		Supervisor:     sup,
		Recovery:       recovery,
		RotateSessions: true,
		RotationMargin: supervisorMargin,
		MaxProcesses:   2,
		RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// supervisorURL is where the supervisor's server currently listens.
func supervisorURL(t *testing.T, sup *montygo.DockerSupervisor) string {
	t.Helper()
	ep, err := sup.Endpoint(context.Background())
	require.NoError(t, err)
	return ep.URL
}

func containerRunning(t *testing.T, id string) bool {
	t.Helper()
	cli, err := testcontainers.NewDockerClientWithOpts(context.Background())
	require.NoError(t, err)
	defer func() { _ = cli.Close() }()
	info, err := cli.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
	if err != nil {
		return false
	}
	return info.Container.State != nil && info.Container.State.Running
}

func killContainer(t *testing.T, id string) {
	t.Helper()
	cli, err := testcontainers.NewDockerClientWithOpts(context.Background())
	require.NoError(t, err)
	defer func() { _ = cli.Close() }()
	_, err = cli.ContainerKill(context.Background(), id, client.ContainerKillOptions{Signal: "SIGKILL"})
	require.NoError(t, err)
}

func TestDockerSupervisor_RunsSessions(t *testing.T) {
	sup := newSupervisor(t, nil)
	ctx := testCtx(t)
	pool := supervisorPool(t, sup, montygo.RecoveryPolicy{})

	session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = session.Close(context.Background()) }()

	value, err := session.FeedRun(ctx, "sum(range(10))", nil)
	require.NoError(t, err)
	require.EqualValues(t, 45, value)

	info := sup.ServerInfo()
	require.Equal(t, montygo.ProtocolVersion, info.ProtocolVersion)
	require.Zero(t, info.Limits.IdleTimeout, "an idle session must survive")
	require.Zero(t, info.Limits.MaxMemory, "the caller's limits govern")
	require.Zero(t, info.Limits.MaxDuration)
	require.Zero(t, info.Limits.MaxSessionsPerClient)
	require.Equal(t, 8, info.Limits.MaxSessions, "twice MaxProcesses")
	require.True(t, strings.HasPrefix(supervisorURL(t, sup), "ws://127.0.0.1:"))
}

func TestDockerSupervisor_CloseRemovesTheContainer(t *testing.T) {
	sup := newSupervisor(t, nil)
	id := sup.ContainerID()
	require.True(t, containerRunning(t, id))

	require.NoError(t, sup.Close(context.Background()))
	require.False(t, containerRunning(t, id), "close stops and removes the container")
}

func TestDockerSupervisor_RotationKeepsStateAcrossTheSessionTimeout(t *testing.T) {
	sup := newSupervisor(t, map[string]string{
		"MONTY_SERVER_SESSION_TIMEOUT": supervisorSessionTimeout,
		"MONTY_SERVER_TURN_TIMEOUT":    supervisorTurnTimeout,
	})
	ctx := testCtx(t)
	pool := supervisorPool(t, sup, montygo.RecoveryPolicy{})

	session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = session.Close(context.Background()) }()

	_, err = session.FeedRun(ctx, "x = 41", nil)
	require.NoError(t, err)

	// Past the server's session deadline, which would have closed the original
	// connection without a dump.
	time.Sleep(12 * time.Second)

	value, err := session.FeedRun(ctx, "x + 1", nil)
	require.NoError(t, err, "the session moved to a fresh connection")
	require.EqualValues(t, 42, value)
	require.NoError(t, session.Err())
}

func TestDockerSupervisor_KilledContainerFailsWithoutRestart(t *testing.T) {
	sup := newSupervisor(t, nil)
	ctx := testCtx(t)
	pool := supervisorPool(t, sup, montygo.RecoveryPolicy{Attempts: 2, AttemptTimeout: 2 * time.Second})

	killContainer(t, sup.ContainerID())

	_, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
	require.ErrorAs(t, err, new(*montygo.SpawnError))
}

func TestDockerSupervisor_KilledContainerRestartsWhenEnabled(t *testing.T) {
	sup := newSupervisor(t, nil)
	ctx := testCtx(t)
	pool := supervisorPool(t, sup, montygo.RecoveryPolicy{
		Attempts: 2, AttemptTimeout: 2 * time.Second, RestartServer: true,
	})

	before := supervisorURL(t, sup)
	killContainer(t, sup.ContainerID())

	session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
	require.NoError(t, err, "the supervisor restarted the container")
	defer func() { _ = session.Close(context.Background()) }()

	value, err := session.FeedRun(ctx, "1 + 1", nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, value)
	require.NotEqual(t, before, supervisorURL(t, sup), "a restarted container publishes a new port")
}
