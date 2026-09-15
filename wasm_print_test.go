package montygo_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func wsmCheckout(t *testing.T, p *montygo.Pool, opts montygo.CheckoutOptions) *montygo.Session {
	t.Helper()
	s, err := p.Checkout(testCtx(t), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func TestWasmPrint(t *testing.T) {
	t.Run("stdout and stderr keep their labels and order over the wasm transport", func(t *testing.T) {
		p := newPool(t, montygo.BackendWasm, montygo.Options{})
		s := wsmCheckout(t, p, montygo.CheckoutOptions{})
		var mu sync.Mutex
		var received [][2]string
		_, err := s.FeedRun(testCtx(t), "import sys\nprint('a')\nprint('b', file=sys.stderr)\nprint('c')", &montygo.FeedOptions{
			Print: montygo.PrintFunc(func(stream montygo.Stream, text string) error {
				mu.Lock()
				defer mu.Unlock()
				received = append(received, [2]string{string(stream), text})
				return nil
			}),
		})
		require.NoError(t, err)
		require.Equal(t, [][2]string{{"stdout", "a\n"}, {"stderr", "b\n"}, {"stdout", "c\n"}}, received)
	})
}
