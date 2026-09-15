package pool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
	pb "github.com/asalimonov/montygo/montypb"
)

func TestTerminateIdleLeaseFreesCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := turnPool(t, func() *turnWorker {
		return newTurnWorker(okThen(func(*pb.ParentRequest) turnReply {
			return turnReply{events: []*pb.ChildEvent{turnComplete(42, 0)}}
		}))
	}, nil)
	co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	require.True(t, co.Terminate(errors.New("stop"), "test"))
	require.False(t, co.Terminate(nil, "duplicate"))
	fresh, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	require.NotSame(t, co.worker, fresh.worker)
	ev, err := fresh.Feed(ctx, "42", nil, nil, "", nil, false, nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), ev.Value)
	require.NoError(t, fresh.Finish(ctx))
}

func TestLateTerminateCannotKillReusedWorker(t *testing.T) {
	ctx := context.Background()
	p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(silent)) }, nil)
	old, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	require.NoError(t, old.Finish(ctx))
	fresh, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	require.Same(t, old.worker, fresh.worker)
	require.False(t, old.Terminate(nil, "late"))
	old.Abandon()
	require.True(t, fresh.worker.Alive())
	require.Equal(t, Stats{Active: 1}, p.Stats())
	require.NoError(t, fresh.Finish(ctx))
}

func TestTerminateDoesNotWaitForPrintCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := turnPool(t, func() *turnWorker {
		return newTurnWorker(okThen(func(*pb.ParentRequest) turnReply {
			return turnReply{events: []*pb.ChildEvent{turnPrint("hello"), turnComplete(42, 0)}}
		}))
	}, nil)
	obs := newRecordingObserver()
	co, err := p.Checkout(ctx, wire.Configure{}, observe(obs))
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = co.Feed(ctx, "print('hello')", nil, nil, "", nil, false, func(uint8, string) {
			close(entered)
			<-release
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	terminated := make(chan bool, 1)
	go func() { terminated <- co.Terminate(nil, "test") }()
	select {
	case won := <-terminated:
		require.True(t, won)
	case <-ctx.Done():
		t.Fatal("termination waited for the callback")
	}
	_, closed := obs.snapshot()
	require.Zero(t, closed, "observer must outlive its callback")
	fresh, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err, "retirement must not wait for the callback")
	require.NoError(t, fresh.Finish(ctx))
	unblock()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	obs.waitClosed(t)
	_, closed = obs.snapshot()
	require.Equal(t, 1, closed)
}

func TestLeaseReleaseRacingTermination(t *testing.T) {
	for range 1000 {
		w := newTurnWorker(okThen(silent))
		lease := checkoutLease{worker: w, slot: &slot{w: w}}
		start := make(chan struct{})
		results := make(chan bool, 2)
		go func() { <-start; _, won := lease.finish(); results <- won }()
		go func() { <-start; _, won := lease.terminate(nil); results <- won }()
		close(start)
		first, second := <-results, <-results
		require.NotEqual(t, first, second, "exactly one path owns accounting")
		require.False(t, lease.active())
	}
}

type delayedExitWorker struct {
	*turnWorker
	exit    chan struct{}
	expired chan struct{}
}

type gatedSpawner struct {
	entered chan struct{}
	release chan struct{}
	w       worker.Worker
}

func (s *gatedSpawner) Spawn(context.Context) (worker.Worker, error) {
	close(s.entered)
	<-s.release
	return s.w, nil
}

func (*gatedSpawner) Kind() worker.Kind           { return worker.KindSubprocess }
func (*gatedSpawner) Close(context.Context) error { return nil }

func (w *delayedExitWorker) Wait(ctx context.Context) (worker.Status, bool) {
	select {
	case <-w.exit:
		return worker.Status{Known: true, Killed: true}, true
	case <-ctx.Done():
		select {
		case w.expired <- struct{}{}:
		default:
		}
		return worker.Status{}, false
	}
}

func TestRetiringCapacityWaitsForObservedExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := &delayedExitWorker{turnWorker: newTurnWorker(okThen(silent)), exit: make(chan struct{}), expired: make(chan struct{}, 1)}
	var exitOnce sync.Once
	exit := func() { exitOnce.Do(func() { close(w.exit) }) }
	t.Cleanup(exit)
	p, err := New(ctx, Config{Spawner: scriptedSpawner{w: w}, MaxProcesses: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close(ctx) })
	co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	require.True(t, co.Terminate(nil, "test"))
	select {
	case <-w.expired:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Equal(t, Stats{Retiring: 1}, p.Stats(), "a wait timeout is not an exit")
	exit()
	require.Eventually(t, func() bool { return p.Stats() == (Stats{}) }, time.Second, time.Millisecond)
}

func TestCloseKeepsUnobservedIdleWorkerRetiring(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := &delayedExitWorker{turnWorker: newTurnWorker(okThen(silent)), exit: make(chan struct{}), expired: make(chan struct{}, 1)}
	p, err := New(ctx, Config{Spawner: scriptedSpawner{w: w}, MaxProcesses: 1})
	require.NoError(t, err)
	co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	require.NoError(t, co.Finish(ctx))
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	require.ErrorIs(t, p.Close(short), context.DeadlineExceeded)
	require.Equal(t, Stats{Retiring: 1}, p.Stats())
	close(w.exit)
	require.NoError(t, p.Shutdown(ctx))
	require.Equal(t, Stats{}, p.Stats())
}

func TestSpawnFinishingAfterCloseIsRetired(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newTurnWorker(okThen(silent))
	spawner := &gatedSpawner{entered: make(chan struct{}), release: make(chan struct{}), w: w}
	p, err := New(ctx, Config{Spawner: spawner, MaxProcesses: 1})
	require.NoError(t, err)
	checked := make(chan error, 1)
	go func() {
		_, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
		checked <- err
	}()
	<-spawner.entered
	require.NoError(t, p.Close(ctx))
	close(spawner.release)
	var perr *Error
	require.ErrorAs(t, <-checked, &perr)
	require.Equal(t, KindClosed, perr.Kind)
	require.NoError(t, p.Shutdown(ctx))
	require.Equal(t, Stats{}, p.Stats())
	require.False(t, w.Alive())
}
