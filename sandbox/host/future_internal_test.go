package host

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestDerivedFutureCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source, settle := NewFuture()
	derived := source.thenContext(ctx, func(v any) (any, error) { t.Error("conversion ran after cancellation"); return v, nil })
	cancel()
	wait, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_, err := derived.Wait(wait)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, channelClosed(source.Done()))
	settle(1, nil)
	panicking := source.thenContext(wait, func(any) (any, error) { panic("conversion failed") })
	_, err = panicking.Wait(wait)
	require.ErrorContains(t, err, "conversion failed")
}
