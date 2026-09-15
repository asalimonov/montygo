package montygo

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
		t.Cleanup(func() { _ = s.CloseNow() })
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
			req, _, err := s.requestInterrupt(e, InterruptOptions{Grace: DurationPtr(time.Hour)})
			require.NoError(t, err)
			if preparing {
				err = s.beginSend(e)
			} else {
				err = s.beginExecution(e)
			}
			var raised *RuntimeError
			require.ErrorAs(t, err, &raised)
			require.False(t, e.sent)
			s.finishExecution(e, nil, err)
			result, err := s.waitInterrupt(ctx, e, req)
			require.NoError(t, err)
			require.Equal(t, InterruptBeforeStart, result.Outcome)
			require.True(t, result.RunDone)
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
			req, _, err := s.requestInterrupt(e, InterruptOptions{Reason: reason, Grace: DurationPtr(time.Hour)})
			require.NoError(t, err)
			s.life.mu.Lock()
			req.deadline = time.Now().Add(-time.Second)
			s.life.mu.Unlock()
			if forced {
				require.True(t, s.forceExecution(e, req))
				require.ErrorIs(t, s.Err(), reason)
				require.False(t, channelClosed(e.done))
			}
			s.finishExecution(e, 42, nil)
			if forced {
				require.Same(t, s.Err(), e.err)
				require.Equal(t, InterruptKilled, req.outcome)
			} else {
				require.Equal(t, InterruptFinished, req.outcome)
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
		req, _, err := s.requestInterruptForStep(e, InterruptOptions{Grace: DurationPtr(0)}, oldStep)
		require.NoError(t, err)
		require.Nil(t, req)
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
		req, _, err := s.requestInterrupt(e, InterruptOptions{Reason: reason, Grace: DurationPtr(time.Hour)})
		require.NoError(t, err)
		deadline := req.deadline
		var group sync.WaitGroup
		for range 32 {
			group.Go(func() {
				joined, _, err := s.requestInterrupt(e, InterruptOptions{Reason: errors.New("later"), Grace: DurationPtr(2 * time.Hour)})
				require.NoError(t, err)
				require.Same(t, req, joined)
			})
		}
		group.Wait()
		require.Equal(t, deadline, req.deadline)
		require.Same(t, reason, req.reason)
		_, _, err = s.requestInterrupt(e, InterruptOptions{Grace: DurationPtr(time.Minute)})
		require.NoError(t, err)
		require.True(t, req.deadline.Before(deadline))
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
		req, _, err := s.requestInterrupt(call.token.exec, InterruptOptions{Grace: DurationPtr(time.Hour)})
		require.NoError(t, err)
		release()
		result, err := s.waitInterrupt(ctx, call.token.exec, req)
		require.NoError(t, err)
		require.Equal(t, InterruptAborted, result.Outcome)
		_, err = call.Resume(ctx, 1)
		var raised *RuntimeError
		require.ErrorAs(t, err, &raised)
		require.NoError(t, s.Err())
	})
	t.Run("invalid and cancelled requests do not register a stop", func(t *testing.T) {
		s := newSession(t)
		e, err := s.reserveExecution(ctx)
		require.NoError(t, err)
		negative := -time.Nanosecond
		result, err := (&Run{s: s, exec: e}).Interrupt(ctx, InterruptOptions{Grace: &negative})
		require.Error(t, err)
		require.Equal(t, InterruptResult{}, result)
		require.Nil(t, e.stop)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		result, err = (&Run{s: s, exec: e}).Interrupt(cancelled, InterruptOptions{})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, InterruptResult{}, result)
		require.Nil(t, e.stop)
		s.finishExecution(e, 1, nil)
	})
	t.Run("first-send commitment beats a later stop", func(t *testing.T) {
		s := newSession(t)
		e, err := s.reserveExecution(ctx)
		require.NoError(t, err)
		require.NoError(t, s.beginExecution(e))
		require.NoError(t, s.beginSend(e))
		req, _, err := s.requestInterrupt(e, InterruptOptions{Grace: DurationPtr(time.Hour)})
		require.NoError(t, err)
		require.True(t, e.sent)
		require.False(t, e.beforeStart)
		s.finishExecution(e, 1, nil)
		result, err := s.waitInterrupt(ctx, e, req)
		require.NoError(t, err)
		require.Equal(t, InterruptFinished, result.Outcome)
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
		req, _, err := s.requestInterrupt(e, InterruptOptions{Grace: DurationPtr(time.Hour)})
		require.NoError(t, err)
		require.ErrorIs(t, callback.Err(), context.Canceled)
		require.ErrorIs(t, s.endCallback(e), errExecutionInterrupted)
		s.life.mu.Lock()
		e.aborted = true
		s.life.mu.Unlock()
		s.finishExecution(e, nil, interruptException(req.reason))
		result, err := s.waitInterrupt(ctx, e, req)
		require.NoError(t, err)
		require.Equal(t, InterruptAborted, result.Outcome)
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
					require.NoError(t, s.CloseNow())
					s.finishExecution(e, nil, s.Err())
				case "force":
					req, _, err := s.requestInterrupt(e, InterruptOptions{Grace: DurationPtr(time.Hour)})
					require.NoError(t, err)
					s.life.mu.Lock()
					req.deadline = time.Now().Add(-time.Second)
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

func TestCheckoutPublicationRacesShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := New(ctx, Options{Backend: BackendWasm, MaxProcesses: 1})
	require.NoError(t, err)
	defer p.Shutdown(ctx)
	h := NewHost()
	h.mu.Lock()
	locked := true
	defer func() {
		if locked {
			h.mu.Unlock()
		}
	}()
	checked := make(chan error, 1)
	go func() {
		s, err := p.Checkout(ctx, CheckoutOptions{Host: h})
		if s != nil {
			_ = s.CloseNow()
		}
		checked <- err
	}()
	require.Eventually(t, func() bool { return p.Stats().Active == 1 }, time.Second, time.Millisecond)
	shut := make(chan error, 1)
	go func() { shut <- p.Shutdown(ctx) }()
	require.Eventually(t, p.closed.Load, time.Second, time.Millisecond)
	h.mu.Unlock()
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
	defer s.CloseNow()

	cancelled, stop := context.WithCancel(ctx)
	stop()
	_, err = s.LoadSnapshot(cancelled, []byte("not inspected"), nil)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, s.driven)
	require.Nil(t, s.life.current)

	h.mu.Lock()
	locked := true
	defer func() {
		if locked {
			h.mu.Unlock()
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
	result, err := s.Interrupt(wait, InterruptOptions{Grace: DurationPtr(time.Hour)})
	waitCancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, InterruptPending, result.Outcome)
	h.mu.Unlock()
	locked = false
	err = <-loaded
	var raised *RuntimeError
	require.ErrorAs(t, err, &raised)
	require.Equal(t, "KeyboardInterrupt", raised.TypeName)
	require.False(t, s.driven)
	require.NoError(t, s.Err())
}
