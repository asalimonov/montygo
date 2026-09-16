package supervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/telemetryhooks"
	pyrt "github.com/asalimonov/montygo/runtime"
)

// fakeSupervisor counts endpoint and restart calls and renames its endpoint on
// every restart, like a container that publishes a new port.
type fakeSupervisor struct {
	mu          sync.Mutex
	generation  int
	restarts    int
	endpointErr error
	restartErr  error
	restartHook func()
}

func (f *fakeSupervisor) Endpoint(context.Context) (ServerEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.endpointErr != nil {
		return ServerEndpoint{}, f.endpointErr
	}
	return ServerEndpoint{URL: f.urlLocked()}, nil
}

func (f *fakeSupervisor) urlLocked() string {
	return "ws://127.0.0.1:" + string(rune('a'+f.generation)) + "/"
}

func (f *fakeSupervisor) Restart(_ context.Context, _ ServerEndpoint) error {
	if f.restartHook != nil {
		f.restartHook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	if f.restartErr != nil {
		return f.restartErr
	}
	f.generation++
	return nil
}

func (f *fakeSupervisor) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generation, f.restarts
}

func spawnFailure() error {
	return &pool.Error{Kind: pool.KindSpawn, Message: "dial refused"}
}

func fastPolicy(restart bool) RecoveryPolicy {
	return RecoveryPolicy{Attempts: 3, AttemptTimeout: 50 * time.Millisecond, RestartServer: restart}
}

func TestRecovererRetriesUntilAttemptsAreSpent(t *testing.T) {
	sup := &fakeSupervisor{}
	r := NewRecoverer(sup, fastPolicy(false), telemetryhooks.ResolveRecorder(nil))
	var calls atomic.Int32
	_, attempts, err := r.Do(context.Background(), func(context.Context, ServerEndpoint) (*pool.Checkout, error) {
		calls.Add(1)
		return nil, spawnFailure()
	})
	require.Error(t, err)
	require.Equal(t, 3, attempts)
	require.Equal(t, int32(3), calls.Load())
	_, restarts := sup.counts()
	require.Zero(t, restarts, "RestartServer is off")
}

func TestRecovererSucceedsOnALaterAttempt(t *testing.T) {
	r := NewRecoverer(&fakeSupervisor{}, fastPolicy(false), telemetryhooks.ResolveRecorder(nil))
	var calls atomic.Int32
	_, attempts, err := r.Do(context.Background(), func(context.Context, ServerEndpoint) (*pool.Checkout, error) {
		if calls.Add(1) < 3 {
			return nil, spawnFailure()
		}
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, attempts)
}

func TestRecovererStopsAtANonRetryableError(t *testing.T) {
	r := NewRecoverer(&fakeSupervisor{}, fastPolicy(true), telemetryhooks.ResolveRecorder(nil))
	stop := &pyrt.OptionError{Message: "bad option"}
	_, attempts, err := r.Do(context.Background(), func(context.Context, ServerEndpoint) (*pool.Checkout, error) {
		return nil, stop
	})
	require.ErrorIs(t, err, error(stop))
	require.Equal(t, 1, attempts)
}

func TestRecovererRestartsOnceThenRetries(t *testing.T) {
	sup := &fakeSupervisor{}
	r := NewRecoverer(sup, fastPolicy(true), telemetryhooks.ResolveRecorder(nil))
	var seen []string
	_, attempts, err := r.Do(context.Background(), func(_ context.Context, ep ServerEndpoint) (*pool.Checkout, error) {
		seen = append(seen, ep.URL)
		return nil, spawnFailure()
	})
	require.Error(t, err)
	require.Equal(t, 6, attempts, "three attempts, one restart, three more")
	_, restarts := sup.counts()
	require.Equal(t, 1, restarts)
	require.NotEqual(t, seen[0], seen[len(seen)-1], "the endpoint is resolved again after the restart")
}

func TestRecovererReportsAFailedRestart(t *testing.T) {
	sup := &fakeSupervisor{restartErr: errors.New("daemon refused")}
	r := NewRecoverer(sup, fastPolicy(true), telemetryhooks.ResolveRecorder(nil))
	_, attempts, err := r.Do(context.Background(), func(context.Context, ServerEndpoint) (*pool.Checkout, error) {
		return nil, spawnFailure()
	})
	require.ErrorContains(t, err, "restart server")
	require.ErrorContains(t, err, "daemon refused")
	require.Equal(t, 3, attempts, "no second round after a failed restart")
}

func TestRecovererRestartsOncePerIncident(t *testing.T) {
	release := make(chan struct{})
	sup := &fakeSupervisor{restartHook: func() { <-release }}
	r := NewRecoverer(sup, fastPolicy(true), telemetryhooks.ResolveRecorder(nil))
	failed := ServerEndpoint{URL: "ws://127.0.0.1:a/"}
	failedAt := time.Now()

	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Go(func() {
			errs[i] = r.restartOnce(context.Background(), failed, failedAt)
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	_, restarts := sup.counts()
	require.Equal(t, 1, restarts, "concurrent callers share one restart")

	// A failure observed before that restart completed needs no further restart.
	require.NoError(t, r.restartOnce(context.Background(), failed, failedAt))
	_, restarts = sup.counts()
	require.Equal(t, 1, restarts)

	// A failure observed afterwards is a new incident.
	require.NoError(t, r.restartOnce(context.Background(), failed, time.Now()))
	_, restarts = sup.counts()
	require.Equal(t, 2, restarts)
}

func TestRecovererCountsAnEndpointErrorAsAnAttempt(t *testing.T) {
	sup := &fakeSupervisor{endpointErr: errors.New("no endpoint")}
	r := NewRecoverer(sup, fastPolicy(false), telemetryhooks.ResolveRecorder(nil))
	_, attempts, err := r.Do(context.Background(), func(context.Context, ServerEndpoint) (*pool.Checkout, error) {
		t.Fatal("bind must not run without an endpoint")
		return nil, nil
	})
	require.ErrorContains(t, err, "no endpoint")
	require.Equal(t, 3, attempts)
}

func TestRecovererStopsWhenTheCallerContextEnds(t *testing.T) {
	r := NewRecoverer(&fakeSupervisor{}, fastPolicy(true), telemetryhooks.ResolveRecorder(nil))
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := r.Do(ctx, func(context.Context, ServerEndpoint) (*pool.Checkout, error) {
		cancel()
		return nil, spawnFailure()
	})
	require.ErrorIs(t, err, context.Canceled)
}
