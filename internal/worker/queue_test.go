package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
)

func TestFrameQueue(t *testing.T) {
	t.Run("push blocks at the byte bound until a pop frees space", func(t *testing.T) {
		q := newFrameQueue(10, nil)
		require.NoError(t, q.push(make([]byte, 6)))
		pushed := make(chan error, 1)
		go func() { pushed <- q.push(make([]byte, 6)) }()
		select {
		case <-pushed:
			t.Fatal("push did not block")
		case <-time.After(50 * time.Millisecond):
		}
		_, err := q.pop(context.Background())
		require.NoError(t, err)
		require.NoError(t, <-pushed)
	})

	t.Run("one frame larger than the bound passes when the queue is empty", func(t *testing.T) {
		q := newFrameQueue(4, nil)
		require.NoError(t, q.push(make([]byte, 100)))
		f, err := q.pop(context.Background())
		require.NoError(t, err)
		require.Len(t, f, 100)
	})

	t.Run("close releases a blocked push and fails later pops", func(t *testing.T) {
		q := newFrameQueue(1, nil)
		require.NoError(t, q.push([]byte{1}))
		pushed := make(chan error, 1)
		go func() { pushed <- q.push([]byte{2}) }()
		time.Sleep(20 * time.Millisecond)
		q.close()
		require.ErrorIs(t, <-pushed, ErrWorkerGone)
		f, err := q.pop(context.Background())
		require.NoError(t, err)
		require.Equal(t, []byte{1}, f)
		_, err = q.pop(context.Background())
		require.ErrorIs(t, err, wire.ErrTruncated)
		require.NoError(t, q.terminalErr())
	})

	t.Run("observer sees the byte delta", func(t *testing.T) {
		var total int64
		q := newFrameQueue(0, func(delta int64) { total += delta })
		require.NoError(t, q.push(make([]byte, 7)))
		require.Equal(t, int64(7), total)
		_, err := q.pop(context.Background())
		require.NoError(t, err)
		require.Equal(t, int64(0), total)
	})

	t.Run("a read failure is reported by terminalErr", func(t *testing.T) {
		q := newFrameQueue(0, nil)
		boom := errors.New("boom")
		q.fail(boom)
		_, err := q.pop(context.Background())
		require.ErrorIs(t, err, boom)
		require.ErrorIs(t, q.terminalErr(), boom)
	})
}
