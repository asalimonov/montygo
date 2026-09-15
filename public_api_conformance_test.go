package monty_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func TestPublicAPI(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		coreARunMontyAPITests(t, b.String()+" public API", func(ctx context.Context) (*monty.Pool, error) {
			return monty.New(ctx, monty.Options{Backend: b})
		})
	})
}

func coreARunMontyAPITests(t *testing.T, name string, create func(ctx context.Context) (*monty.Pool, error)) {
	t.Run(name+": evaluates code and keeps session state", func(t *testing.T) {
		coreAUsingPoolSession(t, create, func(ctx context.Context, session *monty.Session) {
			_, err := session.FeedRun(ctx, "x = 21", nil)
			require.NoError(t, err)

			v, err := session.FeedRun(ctx, "x * 2", nil)
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
		})
	})

	t.Run(name+": accepts inputs", func(t *testing.T) {
		coreAUsingPoolSession(t, create, func(ctx context.Context, session *monty.Session) {
			v, err := session.FeedRun(ctx, "x + 1", &monty.FeedOptions{Inputs: map[string]any{"x": 4}})
			require.NoError(t, err)
			require.Equal(t, int64(5), v)
		})
	})

	t.Run(name+": forwards prints", func(t *testing.T) {
		var printed []string
		coreAUsingPoolSession(t, create, func(ctx context.Context, session *monty.Session) {
			result, err := session.FeedRun(ctx, "print('hello')\n123", &monty.FeedOptions{
				Print: monty.PrintFunc(func(stream monty.Stream, text string) error {
					printed = append(printed, string(stream)+":"+text)
					return nil
				}),
			})
			require.NoError(t, err)

			require.Equal(t, int64(123), result)
			require.Equal(t, []string{"stdout:hello\n"}, printed)
		})
	})
}

func coreAUsingPoolSession(t *testing.T, create func(ctx context.Context) (*monty.Pool, error), fn func(ctx context.Context, session *monty.Session)) {
	t.Helper()
	ctx := testCtx(t)
	var pool *monty.Pool
	var session *monty.Session
	func() {
		p, err := create(ctx)
		require.NoError(t, err)
		pool = p
		defer func() { require.NoError(t, pool.Close(ctx)) }()
		s, err := pool.Checkout(ctx, monty.CheckoutOptions{})
		require.NoError(t, err)
		session = s
		defer func() { require.NoError(t, session.Close(ctx)) }()
		fn(ctx, session)
	}()
	_, err := session.FeedRun(ctx, "1", nil)
	require.ErrorIs(t, err, monty.ErrSessionClosed)
	_, err = pool.Checkout(ctx, monty.CheckoutOptions{})
	require.ErrorIs(t, err, monty.ErrPoolClosed)
}
