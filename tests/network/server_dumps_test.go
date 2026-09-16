package network

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func dumpSession(t *testing.T, ctx context.Context, s *TestServer, code string) []byte {
	t.Helper()
	p := s.NewPool(wsOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err := session.FeedRun(ctx, code, nil)
	require.NoError(t, err)
	state, err := session.Dump(ctx)
	require.NoError(t, err)
	require.Equal(t, "MTYD", string(state[:4]))
	return state
}

func loadInto(t *testing.T, ctx context.Context, s *TestServer, state []byte) (*montygo.Session, error) {
	t.Helper()
	p := s.NewPool(wsOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	return session, session.LoadSession(ctx, state)
}

func requireInvalidDump(t *testing.T, err error) {
	t.Helper()
	re := requireRuntimeError(t, err, "ValueError")
	require.Equal(t, "invalid session dump signature", re.Exception().Message)
}

func TestDumps_SignedDumpRestoresAfterRecreate(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	state := dumpSession(t, ctx, s, "x = 41")

	s.Recreate()
	session, err := loadInto(t, ctx, s, state)
	require.NoError(t, err)
	v, err := session.FeedRun(ctx, "x + 1", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), v)
}

func TestDumps_TamperedDumpIsValueError(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	state := dumpSession(t, ctx, s, "x = 41")
	state[len(state)-1] ^= 0xff

	_, err := loadInto(t, ctx, s, state)
	requireInvalidDump(t, err)
}

func TestDumps_LocalWasmDumpRejected(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	local, err := montygo.NewPool(ctx, montygo.PoolOptions{Workers: montygo.Wasm(montygo.WasmOptions{}), MaxWorkers: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	session, err := local.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	_, err = session.FeedRun(ctx, "x = 1", nil)
	require.NoError(t, err)
	state, err := session.Dump(ctx)
	require.NoError(t, err)

	_, err = loadInto(t, ctx, s, state)
	requireInvalidDump(t, err)
}

func TestDumps_PreviousKeyVerifies(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	state := dumpSession(t, ctx, s, "x = 7")

	s.Recreate(WithEnv("MONTY_SERVER_DUMP_KEY", TestDumpKeyRotated), WithEnv("MONTY_SERVER_DUMP_KEY_PREVIOUS", TestDumpKey))
	session, err := loadInto(t, ctx, s, state)
	require.NoError(t, err)
	v, err := session.FeedRun(ctx, "x * 6", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), v)
}

func TestDumps_UnknownKeyRejected(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	state := dumpSession(t, ctx, s, "x = 7")

	s.Recreate(WithEnv("MONTY_SERVER_DUMP_KEY", TestDumpKeyRotated))
	_, err := loadInto(t, ctx, s, state)
	requireInvalidDump(t, err)
}

func TestDumps_MetricsCountSignVerifyReject(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	base := s.Baseline()
	state := dumpSession(t, ctx, s, "x = 1")
	_, err := loadInto(t, ctx, s, state)
	require.NoError(t, err)
	_, err = loadInto(t, ctx, s, []byte("forged"))
	requireInvalidDump(t, err)

	s.WaitMetricDelta(base, "monty_server_dumps_total", map[string]string{"op": "signed"}, 1, 5*time.Second)
	s.WaitMetricDelta(base, "monty_server_dumps_total", map[string]string{"op": "verified"}, 1, 5*time.Second)
	s.WaitMetricDelta(base, "monty_server_dumps_total", map[string]string{"op": "rejected"}, 1, 5*time.Second)
}
