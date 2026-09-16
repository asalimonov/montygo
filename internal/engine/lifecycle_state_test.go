package engine

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var hourPolicy = StopPolicy{Timeout: time.Hour}

// These tests own the driver directly to make transition ordering deterministic.
func TestLifecycleStateTransitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := New(ctx, Options{Backend: BackendWasm, MaxProcesses: 1})
	require.NoError(t, err)
	defer p.Shutdown(ctx)
	newSession := func(t *testing.T) *Session {
		s, err := p.Checkout(ctx, CheckoutOptions{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close(ctx, KillNow) })
		return s
	}
	for _, preparing := range []bool{false, true} {
		name := "reserved"
		if preparing {
			name = "preparing"
		}
		t.Run(name+" stop wins before first send", func(t *testing.T) {
			s := newSession(t)
			e, err := s.reserveExecution(ctx)
			require.NoError(t, err)
			if preparing {
				require.NoError(t, s.beginExecution(e))
			}
			req, _, done := s.requestStop(e, hourPolicy)
			require.False(t, done)
			if preparing {
				err = s.beginSend(e)
			} else {
				err = s.beginExecution(e)
			}
			var raised *RuntimeError
			require.ErrorAs(t, err, &raised)
			require.False(t, e.sent)
			s.finishExecution(e, nil, err)
			require.True(t, channelClosed(req.resolved))
			result, err := (&Run{s: s, exec: e}).Stop(ctx, hourPolicy)
			require.NoError(t, err)
			require.Equal(t, StopAborted, result.How)
			require.True(t, result.SessionKept())
			require.NoError(t, s.Err())
		})
	}
	for _, forced := range []bool{false, true} {
		name := "completion first"
		if forced {
			name = "force first"
		}
		t.Run(name, func(t *testing.T) {
			s := newSession(t)
			e, err := s.reserveExecution(ctx)
			require.NoError(t, err)
			reason := errors.New("original stop")
			req, _, done := s.requestStop(e, StopPolicy{Reason: reason, Timeout: time.Hour})
			require.False(t, done)
			s.life.mu.Lock()
			req.killAt = time.Now().Add(-time.Second)
			s.life.mu.Unlock()
			if forced {
				require.True(t, s.forceExecution(e, req))
				require.ErrorIs(t, s.Err(), reason)
				require.False(t, channelClosed(e.done))
			}
			s.finishExecution(e, 42, nil)
			if forced {
				require.Same(t, s.Err(), e.err)
				require.Equal(t, StopKilled, req.kind)
			} else {
				require.Equal(t, StopFinished, req.kind)
				next, err := s.reserveExecution(ctx)
				require.NoError(t, err)
				require.True(t, s.forceExecution(e, req), "late watchdog exits without touching next execution")
				require.NoError(t, s.Err())
				require.Same(t, next, s.life.current)
				s.finishExecution(next, 43, nil)
			}
		})
	}
	t.Run("old step cancellation cannot cancel a resumed owner", func(t *testing.T) {
		s := newSession(t)
		e, err := s.reserveExecution(ctx)
		require.NoError(t, err)
		oldStep := e.step
		s.life.mu.Lock()
		s.installStepLocked(e, ctx)
		s.life.mu.Unlock()
		s.requestStopForStep(e, oldStep)
		require.Nil(t, e.stop)
		require.NoError(t, e.callbackCtx.Err())
		s.finishExecution(e, 1, nil)
	})
	t.Run("ID exhaustion rejects without wrapping", func(t *testing.T) {
		s := newSession(t)
		s.life.nextID = math.MaxUint64
		_, err := s.reserveExecution(ctx)
		require.ErrorContains(t, err, "ID exhausted")
		require.Nil(t, s.life.current)
	})
	t.Run("duplicate future ID is a protocol error", func(t *testing.T) {
		s := newSession(t)
		e, err := s.reserveExecution(ctx)
		require.NoError(t, err)
		f, _ := NewFuture()
		require.NoError(t, s.addFuture(e, 1, f))
		var protocol *ProtocolError
		require.ErrorAs(t, s.addFuture(e, 1, f), &protocol)
		require.NoError(t, s.addFuture(e, 2, f))
		require.Equal(t, 2, s.pendingCount())
		s.finishExecution(e, nil, errors.New("typing or runtime failure"))
		require.Zero(t, s.pendingCount())
		require.False(t, channelClosed(f.Done()))
	})
	t.Run("concurrent callers coalesce and cannot extend grace", func(t *testing.T) {
		s := newSession(t)
		e, err := s.reserveExecution(ctx)
		require.NoError(t, err)
		reason := errors.New("first")
		req, _, done := s.requestStop(e, StopPolicy{Reason: reason, Timeout: time.Hour})
		require.False(t, done)
		deadline := req.killAt
		var group sync.WaitGroup
		for range 32 {
			group.Go(func() {
				joined, _, done := s.requestStop(e, StopPolicy{Reason: errors.New("later"), Timeout: 2 * time.Hour})
				require.False(t, done)
				require.Same(t, req, joined)
			})
		}
		group.Wait()
		require.Equal(t, deadline, req.killAt)
		require.Same(t, reason, req.policy.Reason)
		_, _, done = s.requestStop(e, StopPolicy{Timeout: time.Minute})
		require.False(t, done)
		require.True(t, req.killAt.Before(deadline))
		s.finishExecution(e, 1, nil)
	})
	t.Run("Dump ownership makes Resume busy without consuming its cursor", func(t *testing.T) {
		s := newSession(t)
		snap, err := s.FeedStart(ctx, "f()", nil)
		require.NoError(t, err)
		call := snap.(*FunctionSnapshot)
		release, err := s.reserveControl(ctx, true)
		require.NoError(t, err)
		_, err = call.Resume(ctx, 1)
		require.ErrorIs(t, err, ErrSessionBusy)
		require.False(t, call.token.used)
		_, _, done := s.requestStop(call.token.exec, hourPolicy)
		require.False(t, done)
		release()
		result, err := (&Run{s: s, exec: call.token.exec}).Stop(ctx, hourPolicy)
		require.NoError(t, err)
		require.Equal(t, StopAborted, result.How)
		_, err = call.Resume(ctx, 1)
		var raised *RuntimeError
		require.ErrorAs(t, err, &raised)
		require.NoError(t, s.Err())
	})
	t.Run("invalid and cancelled requests do not register a stop", func(t *testing.T) {
		s := newSession(t)
		e, err := s.reserveExecution(ctx)
		require.NoError(t, err)
		result, err := (&Run{s: s, exec: e}).Stop(ctx, StopPolicy{Join: -time.Nanosecond})
		require.Error(t, err)
		require.Equal(t, Stopped{}, result)
		require.Nil(t, e.stop)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		result, err = (&Run{s: s, exec: e}).Stop(cancelled)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, StopPending, result.How)
		require.Nil(t, e.stop)
		s.finishExecution(e, 1, nil)
	})
	t.Run("first-send commitment beats a later stop", func(t *testing.T) {
		s := newSession(t)
		e, err := s.reserveExecution(ctx)
		require.NoError(t, err)
		require.NoError(t, s.beginExecution(e))
		require.NoError(t, s.beginSend(e))
		_, _, done := s.requestStop(e, hourPolicy)
		require.False(t, done)
		require.True(t, e.sent)
		require.False(t, e.beforeStart)
		s.finishExecution(e, 1, nil)
		result, err := (&Run{s: s, exec: e}).Stop(ctx, hourPolicy)
		require.NoError(t, err)
		require.Equal(t, StopFinished, result.How)
	})
	t.Run("callback return observes an accepted stop", func(t *testing.T) {
		s := newSession(t)
		e, err := s.reserveExecution(ctx)
		require.NoError(t, err)
		require.NoError(t, s.beginExecution(e))
		require.NoError(t, s.beginSend(e))
		s.receivedTurn(e)
		callback, err := s.beginCallback(e, ctx)
		require.NoError(t, err)
		req, _, done := s.requestStop(e, hourPolicy)
		require.False(t, done)
		require.Eventually(t, func() bool { return callback.Err() != nil }, time.Second, time.Millisecond)
		require.ErrorIs(t, s.endCallback(e), errExecutionInterrupted)
		s.life.mu.Lock()
		e.aborted = true
		s.life.mu.Unlock()
		s.finishExecution(e, nil, interruptException(req.policy.Reason))
		result, err := (&Run{s: s, exec: e}).Stop(ctx, hourPolicy)
		require.NoError(t, err)
		require.Equal(t, StopAborted, result.How)
	})
	t.Run("future subscriptions clear on every terminal class", func(t *testing.T) {
		for _, terminal := range []string{"runtime", "typing", "close now", "force"} {
			t.Run(terminal, func(t *testing.T) {
				s := newSession(t)
				e, err := s.reserveExecution(ctx)
				require.NoError(t, err)
				f, _ := NewFuture()
				require.NoError(t, s.addFuture(e, 1, f))
				switch terminal {
				case "runtime":
					s.finishExecution(e, nil, &RuntimeError{TypeName: "RuntimeError", Message: "failed"})
				case "typing":
					s.finishExecution(e, nil, &TypingError{Diagnostics: "type mismatch"})
				case "close now":
					require.NoError(t, s.Close(ctx, KillNow))
					s.finishExecution(e, nil, s.Err())
				case "force":
					req, _, done := s.requestStop(e, hourPolicy)
					require.False(t, done)
					s.life.mu.Lock()
					req.killAt = time.Now().Add(-time.Second)
					s.life.mu.Unlock()
					require.True(t, s.forceExecution(e, req))
					s.finishExecution(e, nil, s.Err())
				}
				require.Zero(t, s.pendingCount())
				require.False(t, channelClosed(f.Done()), "execution cleanup must not settle a caller-owned future")
			})
		}
	})
}

func TestCheckoutPublicationRacesShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := New(ctx, Options{Backend: BackendWasm, MaxProcesses: 1})
	require.NoError(t, err)
	defer p.Shutdown(ctx)
	h := NewHost()
	unblockHost := blockHostRegistration(h)
	locked := true
	defer func() {
		if locked {
			unblockHost()
		}
	}()
	checked := make(chan error, 1)
	go func() {
		s, err := p.Checkout(ctx, CheckoutOptions{Host: h})
		if s != nil {
			_ = s.Close(ctx, KillNow)
		}
		checked <- err
	}()
	require.Eventually(t, func() bool { return p.Stats().Active == 1 }, time.Second, time.Millisecond)
	shut := make(chan error, 1)
	go func() { shut <- p.Shutdown(ctx) }()
	require.Eventually(t, p.closed.Load, time.Second, time.Millisecond)
	unblockHost()
	locked = false
	require.ErrorIs(t, <-checked, ErrPoolClosed)
	require.NoError(t, <-shut)
	require.Equal(t, PoolStats{}, p.Stats())
	p.sessionsMu.Lock()
	defer p.sessionsMu.Unlock()
	require.Empty(t, p.sessions)
}

func TestIdleDeathRetiresSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := New(ctx, Options{Backend: BackendWasm, MaxProcesses: 1})
	require.NoError(t, err)
	defer p.Shutdown(ctx)
	s, err := p.Checkout(ctx, CheckoutOptions{})
	require.NoError(t, err)
	s.co.Terminate(nil, "test_idle_death")
	select {
	case <-s.Done():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var crashed *CrashedError
	require.ErrorAs(t, s.Err(), &crashed)
	fresh, err := p.Checkout(ctx, CheckoutOptions{})
	require.NoError(t, err)
	defer fresh.Close(ctx)
	v, err := fresh.FeedRun(ctx, "42", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), v)
}

func TestLoadSnapshotInterruptionBoundaries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := New(ctx, Options{Backend: BackendWasm, MaxProcesses: 1})
	require.NoError(t, err)
	defer p.Shutdown(ctx)
	h := NewHost()
	s, err := p.Checkout(ctx, CheckoutOptions{Host: h})
	require.NoError(t, err)
	defer s.Close(ctx, KillNow)

	cancelled, stop := context.WithCancel(ctx)
	stop()
	_, err = s.LoadSnapshot(cancelled, []byte("not inspected"), nil)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, s.driven)
	require.Nil(t, s.life.current)

	unblockHost := blockHostRegistration(h)
	locked := true
	defer func() {
		if locked {
			unblockHost()
		}
	}()
	loaded := make(chan error, 1)
	go func() {
		_, err := s.LoadSnapshot(ctx, []byte("not sent"), nil)
		loaded <- err
	}()
	require.Eventually(t, func() bool {
		s.life.mu.Lock()
		defer s.life.mu.Unlock()
		return s.life.current != nil
	}, time.Second, time.Millisecond)
	wait, waitCancel := context.WithTimeout(ctx, 10*time.Millisecond)
	result, err := s.Stop(wait, hourPolicy)
	waitCancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, StopPending, result.How)
	unblockHost()
	locked = false
	err = <-loaded
	var raised *RuntimeError
	require.ErrorAs(t, err, &raised)
	require.Equal(t, "KeyboardInterrupt", raised.TypeName)
	require.False(t, s.driven)
	require.NoError(t, s.Err())
}
