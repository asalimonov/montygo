package network

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func TestParallel_ManySessionsOneUnit(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{MaxProcesses: 16})
	base := s.Baseline()

	const sessions = 32
	var wg sync.WaitGroup
	errs := make([]error, sessions)
	for i := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, err := p.Checkout(ctx, monty.CheckoutOptions{})
			if err != nil {
				errs[i] = err
				return
			}
			defer session.Close(context.Background())
			v, err := session.FeedRun(ctx, fmt.Sprintf("sum(range(%d))", i+10), nil)
			if err != nil {
				errs[i] = err
				return
			}
			if want := int64((i + 10) * (i + 9) / 2); v != want {
				errs[i] = fmt.Errorf("session %d: got %v, want %d", i, v, want)
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	s.WaitMetric("monty_server_sessions_active", nil, 0, 10*time.Second)
	s.WaitMetricDelta(base, "monty_server_sessions_total", map[string]string{"outcome": "closed"}, sessions, 10*time.Second)
	s.WaitMetricDelta(base, "monty_server_workers_spawned_total", nil, sessions, 10*time.Second)
}

func TestParallel_AbandonedTurnsReleaseSessions(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{MaxProcesses: 4})

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, err := p.Checkout(ctx, monty.CheckoutOptions{})
			if err != nil {
				return
			}
			defer session.Close(context.Background())
			turnCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			defer cancel()
			_, _ = session.FeedRun(turnCtx, "while True:\n    pass", nil)
		}()
	}
	wg.Wait()
	s.WaitMetric("monty_server_sessions_active", nil, 0, 15*time.Second)
}
