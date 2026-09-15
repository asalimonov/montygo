package monty_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func TestInstallDependencies(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("installDependencies is rejected by the sandbox worker, session survives", func(t *testing.T) {
			ctx := testCtx(t)
			pool, err := monty.New(ctx, monty.Options{Backend: b})
			require.NoError(t, err)
			defer func() { require.NoError(t, pool.Close(ctx)) }()
			session, err := pool.Checkout(ctx, monty.CheckoutOptions{})
			require.NoError(t, err)
			err = session.InstallDependencies(ctx, []string{"httpx>=0.27"})
			var runtimeErr *monty.RuntimeError
			require.ErrorAs(t, err, &runtimeErr)
			require.EqualError(t, err, "RuntimeError: dependency installation is only supported by the CPython worker")
			v, err := session.FeedRun(ctx, "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})

		t.Run("installDependencies with an empty list is a no-op", func(t *testing.T) {
			ctx := testCtx(t)
			pool, err := monty.New(ctx, monty.Options{Backend: b})
			require.NoError(t, err)
			defer func() { require.NoError(t, pool.Close(ctx)) }()
			session, err := pool.Checkout(ctx, monty.CheckoutOptions{})
			require.NoError(t, err)
			require.NoError(t, session.InstallDependencies(ctx, []string{}))
			v, err := session.FeedRun(ctx, "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
		})
	})
}
