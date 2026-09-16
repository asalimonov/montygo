package montygo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func TestBasic(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("simple expression", func(t *testing.T) {
			require.Equal(t, int64(3), mustRun(t, b, "1 + 2", runOptions{}))
		})

		t.Run("arithmetic", func(t *testing.T) {
			require.Equal(t, int64(47), mustRun(t, b, "10 * 5 - 3", runOptions{}))
		})

		t.Run("string concatenation", func(t *testing.T) {
			require.Equal(t, "hello world", mustRun(t, b, `"hello" + " " + "world"`, runOptions{}))
		})

		t.Run("syntax error", func(t *testing.T) {
			_, err := run(t, b, "def", runOptions{})
			var syntaxErr *montygo.SyntaxError
			require.ErrorAs(t, err, &syntaxErr)
			require.Contains(t, err.Error(), "SyntaxError")
		})

		t.Run("multiline code", func(t *testing.T) {
			code := `
x = 1
y = 2
x + y
`
			require.Equal(t, int64(3), mustRun(t, b, code, runOptions{}))
		})

		t.Run("function definition and call", func(t *testing.T) {
			code := `
def add(a, b):
    return a + b

add(3, 4)
`
			require.Equal(t, int64(7), mustRun(t, b, code, runOptions{}))
		})

		t.Run("session state persists across feeds", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			v, err := session.FeedRun(ctx, "x = 5", nil)
			require.NoError(t, err)
			require.Nil(t, v)
			v, err = session.FeedRun(ctx, "x * 2", nil)
			require.NoError(t, err)
			require.Equal(t, int64(10), v)
		})

		t.Run("sessions are isolated from each other", func(t *testing.T) {
			ctx := testCtx(t)
			a := newSession(t, b, montygo.CheckoutOptions{})
			other := newSession(t, b, montygo.CheckoutOptions{})
			_, err := a.FeedRun(ctx, "secret = 42", nil)
			require.NoError(t, err)
			_, err = other.FeedRun(ctx, "secret", nil)
			require.EqualError(t, err, "NameError: name 'secret' is not defined")
		})

		t.Run("await using closes the session", func(t *testing.T) {
			ctx := testCtx(t)
			var result any
			var session *montygo.Session
			func() {
				s, err := sharedPool(t, b).Checkout(ctx, montygo.CheckoutOptions{})
				require.NoError(t, err)
				session = s
				defer func() { require.NoError(t, session.Close(ctx)) }()
				result, err = session.FeedRun(ctx, "21 * 2", nil)
				require.NoError(t, err)
			}()
			require.Equal(t, int64(42), result)
			_, err := session.FeedRun(ctx, "21 * 2", nil)
			require.ErrorIs(t, err, montygo.ErrSessionClosed)
		})
	})
}
