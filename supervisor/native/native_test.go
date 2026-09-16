package native_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/supervisor/native"
)

// repoFile resolves a path relative to the repository root.
func repoFile(parts ...string) string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(append([]string{root}, parts...)...)
}

// serverOptions skips the test unless both binaries this supervisor needs are
// available: monty-server itself, and the worker it spawns per session.
func serverOptions(t *testing.T) native.Options {
	t.Helper()
	server := os.Getenv(native.BinaryEnv)
	if server == "" {
		server = repoFile("server", "target", "debug", "monty-server")
	}
	if _, err := os.Stat(server); err != nil {
		t.Skipf("monty-server not built: %v", err)
	}
	worker := os.Getenv("MONTY_BIN")
	if worker == "" {
		worker = repoFile("..", "monty", "target", "debug", "monty")
	}
	if _, err := os.Stat(worker); err != nil {
		t.Skipf("monty worker not built: %v", err)
	}
	return native.Options{
		Binary:       server,
		Env:          map[string]string{"MONTY_BIN": worker},
		MaxProcesses: 2,
		StartTimeout: 30 * time.Second,
		StopTimeout:  5 * time.Second,
	}
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestNativeSupervisorRunsSessions(t *testing.T) {
	ctx := testCtx(t)
	pool, err := native.NewPool(ctx, serverOptions(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Shutdown(context.Background()) })

	session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = session.Close(context.Background()) }()

	value, err := session.FeedRun(ctx, "sum(range(10))", nil)
	require.NoError(t, err)
	require.EqualValues(t, 45, value)

	_, err = session.FeedRun(ctx, "x = 7", nil)
	require.NoError(t, err)
	value, err = session.FeedRun(ctx, "x + 1", nil)
	require.NoError(t, err)
	require.EqualValues(t, 8, value, "state persists across feeds")
}

func TestNativeSupervisorReportsItsEndpointAndLimits(t *testing.T) {
	ctx := testCtx(t)
	sup, err := native.New(ctx, serverOptions(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sup.Close(context.Background()) })

	endpoint, err := sup.Endpoint(ctx)
	require.NoError(t, err)
	require.Contains(t, endpoint.URL, "ws://127.0.0.1:")

	pid, ok := sup.PID()
	require.True(t, ok)
	require.NotZero(t, pid)

	info := sup.ServerInfo()
	require.Equal(t, montygo.ProtocolVersion, info.ProtocolVersion)
	require.Zero(t, info.Limits.IdleTimeout, "an idle session must survive")
	require.Zero(t, info.Limits.MaxMemory, "the caller's limits govern")
	require.Zero(t, info.Limits.MaxDuration)
	require.Zero(t, info.Limits.MaxSessionsPerClient)
	require.Equal(t, 4, info.Limits.MaxSessions, "twice MaxProcesses")
}

func TestNativeSupervisorRestartMovesTheEndpoint(t *testing.T) {
	ctx := testCtx(t)
	sup, err := native.New(ctx, serverOptions(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sup.Close(context.Background()) })

	before, err := sup.Endpoint(ctx)
	require.NoError(t, err)
	require.NoError(t, sup.Restart(ctx, before))

	after, err := sup.Endpoint(ctx)
	require.NoError(t, err)
	require.NotEqual(t, before.URL, after.URL, "a respawned server binds a new port")

	require.NoError(t, sup.Restart(ctx, before), "a stale endpoint restarts nothing")
}

func TestNativeSupervisorCloseIsIdempotent(t *testing.T) {
	ctx := testCtx(t)
	sup, err := native.New(ctx, serverOptions(t))
	require.NoError(t, err)

	require.NoError(t, sup.Close(ctx))
	require.NoError(t, sup.Close(ctx))
	_, err = sup.Endpoint(ctx)
	require.ErrorIs(t, err, montygo.ErrSupervisorClosed)
}

func TestNativeSupervisorRejectsAMissingBinary(t *testing.T) {
	_, err := native.New(context.Background(), native.Options{Binary: filepath.Join(t.TempDir(), "absent")})
	require.ErrorAs(t, err, new(*montygo.OptionError))
	require.ErrorContains(t, err, "monty-server not found at")
}
