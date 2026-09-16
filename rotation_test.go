package montygo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
)

// rotationLimits are short enough for a test and still satisfy the policy's
// requirement that a session outlives two lead times.
const (
	rotationSessionTimeout = 4 * time.Second
	rotationTurnTimeout    = time.Second
	rotationMargin         = time.Second
)

// rotationPool serves sessions that must rotate after rotationSessionTimeout
// minus one lead time, which is 2s after the dial.
func rotationPool(t *testing.T, relay *wsRelay, recovery montygo.RecoveryPolicy) *montygo.Pool {
	t.Helper()
	p, err := montygo.NewPool(testCtx(t), montygo.PoolOptions{
		Workers: montygo.Remote(montygo.StaticServer(relay.URL, nil, nil), montygo.RemoteOptions{
			RotateSessions: true,
			RotationMargin: rotationMargin,
			Recovery:       recovery,
		}),
		MaxWorkers:     4,
		RequestTimeout: montygo.NoRequestTimeout,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close(testCtx(t)) })
	return p
}

func TestRotationKeepsSandboxState(t *testing.T) {
	relay := wsStartRelayInfo(t, false, relayInfo{
		sessionTimeoutS: int(rotationSessionTimeout.Seconds()),
		turnTimeoutS:    int(rotationTurnTimeout.Seconds()),
	})
	ctx := testCtx(t)
	pool := rotationPool(t, relay, montygo.RecoveryPolicy{})

	session, err := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = session.Close(ctx) }()

	_, err = session.FeedRun(ctx, "x = 41", nil)
	require.NoError(t, err)
	require.Equal(t, 1, relay.upgradeCount())

	// Past this point less than turn+margin of the session's life remains, so
	// the next feed rotates first.
	time.Sleep(rotationSessionTimeout - rotationTurnTimeout - rotationMargin + 200*time.Millisecond)

	value, err := session.FeedRun(ctx, "x + 1", nil)
	require.NoError(t, err)
	require.EqualValues(t, 42, value)
	require.Equal(t, 2, relay.upgradeCount(), "the session moved to a second connection")
	require.NoError(t, session.Err())
	require.Equal(t, montygo.SessionIdle, session.State())
}

func TestRotationHappensWhileTheSessionIsIdle(t *testing.T) {
	relay := wsStartRelayInfo(t, false, relayInfo{
		sessionTimeoutS: int(rotationSessionTimeout.Seconds()),
		turnTimeoutS:    int(rotationTurnTimeout.Seconds()),
	})
	ctx := testCtx(t)
	pool := rotationPool(t, relay, montygo.RecoveryPolicy{})

	session, err := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = session.Close(ctx) }()
	_, err = session.FeedRun(ctx, "x = 7", nil)
	require.NoError(t, err)

	// The idle timer fires one margin before the deadline, without a call.
	require.Eventually(t, func() bool { return relay.upgradeCount() == 2 },
		rotationSessionTimeout, 100*time.Millisecond, "the idle session did not rotate")

	value, err := session.FeedRun(ctx, "x", nil)
	require.NoError(t, err)
	require.EqualValues(t, 7, value)
}

func TestRotationFailureReturnsTheDump(t *testing.T) {
	relay := wsStartRelayInfo(t, false, relayInfo{
		sessionTimeoutS: int(rotationSessionTimeout.Seconds()),
		turnTimeoutS:    int(rotationTurnTimeout.Seconds()),
	})
	ctx := testCtx(t)
	pool := rotationPool(t, relay, montygo.RecoveryPolicy{Attempts: 1, AttemptTimeout: 500 * time.Millisecond})

	session, err := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = session.Close(ctx) }()
	_, err = session.FeedRun(ctx, "x = 41", nil)
	require.NoError(t, err)

	relay.setRefuse(true)
	time.Sleep(rotationSessionTimeout - rotationTurnTimeout - rotationMargin + 200*time.Millisecond)

	_, err = session.FeedRun(ctx, "x + 1", nil)
	var rotation *monterr.RotationError
	require.ErrorAs(t, err, &rotation)
	require.ErrorIs(t, err, monterr.ErrSessionLost)
	require.NotEmpty(t, rotation.Dump, "the state captured before the failure is returned")

	// The dump restores the sandbox on a session of a working pool.
	relay.setRefuse(false)
	restored, err := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = restored.Close(ctx) }()
	require.NoError(t, restored.LoadSession(ctx, rotation.Dump))
	value, err := restored.FeedRun(ctx, "x + 1", nil)
	require.NoError(t, err)
	require.EqualValues(t, 42, value)
}

func TestRotationStaysOffWithoutServerInfo(t *testing.T) {
	relay := wsStartRelay(t, false)
	ctx := testCtx(t)
	pool, err := montygo.NewPool(ctx, montygo.PoolOptions{
		Workers:    montygo.Remote(montygo.StaticServer(relay.URL, nil, nil), montygo.RemoteOptions{RotateSessions: true}),
		MaxWorkers: 2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(ctx) })

	session, err := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = session.Close(ctx) }()
	_, err = session.FeedRun(ctx, "1 + 1", nil)
	require.NoError(t, err)
	require.Equal(t, 1, relay.upgradeCount())
}

func TestRotationStaysOffWhenTheSessionIsTooShort(t *testing.T) {
	// A session shorter than two lead times would rotate continuously.
	relay := wsStartRelayInfo(t, false, relayInfo{sessionTimeoutS: 2, turnTimeoutS: 1})
	ctx := testCtx(t)
	pool := rotationPool(t, relay, montygo.RecoveryPolicy{})

	session, err := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer func() { _ = session.Close(ctx) }()
	_, err = session.FeedRun(ctx, "x = 1", nil)
	require.NoError(t, err)
	time.Sleep(1200 * time.Millisecond)
	_, err = session.FeedRun(ctx, "x", nil)
	require.NoError(t, err)
	require.Equal(t, 1, relay.upgradeCount())
}
