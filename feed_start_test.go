package montygo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

const fstAsyncGo = "import asyncio\nasync def main():\n    return await go()\nasyncio.run(main())"

func fstResumeAuto(ctx context.Context, t *testing.T, snap montygo.Snapshot) (montygo.Snapshot, error) {
	t.Helper()
	switch s := snap.(type) {
	case *montygo.FunctionSnapshot:
		return s.ResumeAuto(ctx)
	case *montygo.NameLookupSnapshot:
		return s.ResumeAuto(ctx)
	case *montygo.FutureSnapshot:
		return s.ResumeAuto(ctx)
	}
	t.Fatalf("snapshot %T cannot be resumed", snap)
	return nil, nil
}

func fstAs[T montygo.Snapshot](t *testing.T) func(montygo.Snapshot, error) T {
	return func(snap montygo.Snapshot, err error) T {
		t.Helper()
		require.NoError(t, err)
		typed, ok := snap.(T)
		require.Truef(t, ok, "expected %T, got %T", *new(T), snap)
		return typed
	}
}

func fstComplete(t *testing.T) func(montygo.Snapshot, error) *montygo.Complete {
	return fstAs[*montygo.Complete](t)
}

func fstFunction(t *testing.T) func(montygo.Snapshot, error) *montygo.FunctionSnapshot {
	return fstAs[*montygo.FunctionSnapshot](t)
}

func fstNameLookup(t *testing.T) func(montygo.Snapshot, error) *montygo.NameLookupSnapshot {
	return fstAs[*montygo.NameLookupSnapshot](t)
}

func fstRuntimeError(t *testing.T, err error) *montygo.RuntimeError {
	t.Helper()
	require.Error(t, err)
	var rerr *montygo.RuntimeError
	require.ErrorAsf(t, err, &rerr, "expected *montygo.RuntimeError, got %T: %v", err, err)
	return rerr
}

func fstDrive(ctx context.Context, t *testing.T, snap montygo.Snapshot, err error) (*montygo.Complete, int) {
	t.Helper()
	steps := 0
	for {
		require.NoError(t, err)
		if done, ok := snap.(*montygo.Complete); ok {
			return done, steps
		}
		snap, err = fstResumeAuto(ctx, t, snap)
		steps++
	}
}

func TestFeedStart(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("feedStart suspends at a function call, then completes", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			call := fstFunction(t)(session.FeedStart(ctx, "x = add(2, 3)\nx * 10", nil))
			require.Equal(t, "add", call.FunctionName)
			require.Equal(t, []any{int64(2), int64(3)}, call.Args)
			require.False(t, call.IsOSFunction)
			done := fstComplete(t)(call.Resume(ctx, 5))
			require.Equal(t, int64(50), done.Output)
		})

		t.Run("feedStart surfaces a name lookup", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			lookup := fstNameLookup(t)(session.FeedStart(ctx, "missing + 1", nil))
			require.Equal(t, "missing", lookup.VariableName)
		})

		t.Run("a snapshot resumes at most once", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(session.FeedStart(ctx, "f()", nil))
			_, err := snap.Resume(ctx, 1)
			require.NoError(t, err)
			_, err = snap.Resume(ctx, 2)
			require.ErrorIs(t, err, montygo.ErrSnapshotResumed)
			require.EqualError(t, err, "snapshot has already been resumed")
		})

		t.Run("os handler is used by resumeAuto, not auto-dispatched", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			var names []string
			snap := fstFunction(t)(session.FeedStart(ctx, "from pathlib import Path\nPath('/data/x').read_text()", &montygo.FeedOptions{
				OS: func(_ context.Context, name string, _ []any, _ montygo.Kwargs) (any, error) {
					names = append(names, name)
					return "file body", nil
				},
			}))
			require.True(t, snap.IsOSFunction)
			require.Empty(t, names)
			done := fstComplete(t)(snap.ResumeAuto(ctx))
			require.Equal(t, "file body", done.Output)
			require.Equal(t, []string{"Path.read_text"}, names)
		})

		t.Run("the sandbox future mechanism is caller-driven", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			call := fstFunction(t)(session.FeedStart(ctx, fstAsyncGo, nil))
			require.Equal(t, "go", call.FunctionName)
			snap, err := call.ResumeFuture(ctx)
			require.NoError(t, err)
			futures, ok := snap.(*montygo.FutureSnapshot)
			require.Truef(t, ok, "expected *montygo.FutureSnapshot, got %T", snap)
			require.Equal(t, []uint32{call.CallID}, futures.PendingCallIDs)
			done := fstComplete(t)(futures.Resume(ctx, []montygo.FutureResolution{{CallID: call.CallID, Value: 99}}))
			require.Equal(t, int64(99), done.Output)
		})

		t.Run("dump at a suspension, then loadSnapshot and resume", func(t *testing.T) {
			ctx := testCtx(t)
			first := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(first.FeedStart(ctx, "y = fetch()\ny + 1", nil))
			blob, err := snap.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			session := newSession(t, b, montygo.CheckoutOptions{})
			restored := fstFunction(t)(session.LoadSnapshot(ctx, blob, nil))
			done := fstComplete(t)(restored.Resume(ctx, 41))
			require.Equal(t, int64(42), done.Output)
		})

		t.Run("loadSession restores an idle session", func(t *testing.T) {
			ctx := testCtx(t)
			first := newSession(t, b, montygo.CheckoutOptions{})
			_, err := first.FeedRun(ctx, "kept = 7", nil)
			require.NoError(t, err)
			blob, err := first.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			session := newSession(t, b, montygo.CheckoutOptions{})
			require.NoError(t, session.LoadSession(ctx, blob))
			v, err := session.FeedRun(ctx, "kept + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(8), v)
		})

		t.Run("loadSession and loadSnapshot reject the wrong dump kind", func(t *testing.T) {
			ctx := testCtx(t)
			idleSession := newSession(t, b, montygo.CheckoutOptions{})
			idle, err := idleSession.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, idleSession.Close(ctx))

			suspendedSession := newSession(t, b, montygo.CheckoutOptions{})
			_, err = suspendedSession.FeedStart(ctx, "f()", nil)
			require.NoError(t, err)
			suspended, err := suspendedSession.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, suspendedSession.Close(ctx))

			{
				session := newSession(t, b, montygo.CheckoutOptions{})
				_, err := session.LoadSnapshot(ctx, idle, nil)
				require.ErrorIs(t, err, montygo.ErrDumpIsIdle)
				require.EqualError(t, err, "this dump is an idle session — use loadSession() to restore it")
				_, err = session.FeedRun(ctx, "1 + 1", nil)
				require.Error(t, err)
				require.NoError(t, session.Close(ctx))
			}
			{
				session := newSession(t, b, montygo.CheckoutOptions{})
				err := session.LoadSession(ctx, suspended)
				require.ErrorIs(t, err, montygo.ErrDumpIsSuspended)
				require.EqualError(t, err, "this dump is a suspended snapshot — use loadSnapshot() to resume it")
				_, err = session.FeedRun(ctx, "1 + 1", nil)
				require.Error(t, err)
				require.NoError(t, session.Close(ctx))
			}
		})

		t.Run("load after a feed is rejected", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			blob, err := session.Dump(ctx)
			require.NoError(t, err)
			_, err = session.FeedRun(ctx, "x = 1", nil)
			require.NoError(t, err)
			_, err = session.LoadSnapshot(ctx, blob, nil)
			require.ErrorIs(t, err, montygo.ErrNotFresh)
			require.EqualError(t, err, "loadSession / loadSnapshot is only valid on a fresh session, before any feedRun / feedStart / loadSession / loadSnapshot")
		})

		t.Run("resumeAuto answers a function call from externalLookup", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(session.FeedStart(ctx, "add(2, 3) * 10", &montygo.FeedOptions{
				ExternalLookup: map[string]any{"add": func(a, b int64) int64 { return a + b }},
			}))
			done := fstComplete(t)(snap.ResumeAuto(ctx))
			require.Equal(t, int64(50), done.Output)
		})

		t.Run("resumeAuto drives a snippet to completion", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			code := "total = base\nfor i in [1, 2]:\n    total = add(total, i)\ntotal"
			lookup := map[string]any{"base": 10, "add": func(a, b int64) int64 { return a + b }}
			snap, err := session.FeedStart(ctx, code, &montygo.FeedOptions{ExternalLookup: lookup})
			done, steps := fstDrive(ctx, t, snap, err)
			require.Equal(t, int64(13), done.Output)
			require.Equal(t, 3, steps)
		})

		t.Run("resumeAuto resolves a name lookup to a value", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstNameLookup(t)(session.FeedStart(ctx, "missing + 1", &montygo.FeedOptions{
				ExternalLookup: map[string]any{"missing": 41},
			}))
			done := fstComplete(t)(snap.ResumeAuto(ctx))
			require.Equal(t, int64(42), done.Output)
		})

		t.Run("resumeAuto resolves a name lookup to a function", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap, err := session.FeedStart(ctx, "g = greet\ng(\"hi\")", &montygo.FeedOptions{
				ExternalLookup: map[string]any{"greet": func(s string) string { return s + "!" }},
			})
			done, _ := fstDrive(ctx, t, snap, err)
			require.Equal(t, "hi!", done.Output)
		})

		t.Run("resumeAuto with a missing name raises NameError", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstNameLookup(t)(session.FeedStart(ctx, "missing + 1", &montygo.FeedOptions{ExternalLookup: map[string]any{}}))
			_, err := snap.ResumeAuto(ctx)
			rerr := fstRuntimeError(t, err)
			require.Equal(t, "NameError: name 'missing' is not defined", rerr.Error())
		})

		t.Run("resumeAuto with a function absent from the lookup raises NameError", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(session.FeedStart(ctx, "add(2, 3)", &montygo.FeedOptions{ExternalLookup: map[string]any{}}))
			_, err := snap.ResumeAuto(ctx)
			rerr := fstRuntimeError(t, err)
			require.Equal(t, "NameError: name 'add' is not defined", rerr.Error())
		})

		t.Run("resumeAuto answers an OS call with the default unhandled error", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			code := "from pathlib import Path\n" +
				"try:\n" +
				"    Path('/etc/secret').read_text()\n" +
				"    r = 'unexpected'\n" +
				"except Exception as e:\n" +
				"    r = type(e).__name__\n" +
				"r"
			snap := fstFunction(t)(session.FeedStart(ctx, code, nil))
			require.True(t, snap.IsOSFunction)
			done := fstComplete(t)(snap.ResumeAuto(ctx))
			require.Equal(t, "PermissionError", done.Output)
		})

		t.Run("resumeAuto settles an immediately awaited promise without a FutureSnapshot", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(session.FeedStart(ctx, fstAsyncGo, &montygo.FeedOptions{
				ExternalLookup: map[string]any{"go": func() *montygo.Future {
					return montygo.Async(func() (any, error) { return 99, nil })
				}},
			}))
			require.True(t, snap.AllowEagerAwait)
			done := fstComplete(t)(snap.ResumeAuto(ctx))
			require.Equal(t, int64(99), done.Output)
		})

		t.Run("resumeAuto drives multiple pending promises via gather", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			code := "import asyncio\nasync def main():\n    return await asyncio.gather(go(1), go(2))\nasyncio.run(main())"
			snap, err := session.FeedStart(ctx, code, &montygo.FeedOptions{
				ExternalLookup: map[string]any{"go": func(n int64) *montygo.Future {
					return montygo.Async(func() (any, error) { return n * 10, nil })
				}},
			})
			done, _ := fstDrive(ctx, t, snap, err)
			require.Equal(t, []any{int64(10), int64(20)}, done.Output)
		})

		t.Run("resumeAuto and manual resume share the captured lookup", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(session.FeedStart(ctx, "a = first()\nb = second()\na + b", &montygo.FeedOptions{
				ExternalLookup: map[string]any{"second": func() int64 { return 20 }},
			}))
			require.Equal(t, "first", snap.FunctionName)
			next := fstFunction(t)(snap.Resume(ctx, 5))
			require.Equal(t, "second", next.FunctionName)
			done := fstComplete(t)(next.ResumeAuto(ctx))
			require.Equal(t, int64(25), done.Output)
		})

		t.Run("resumeAuto resumes at most once", func(t *testing.T) {
			ctx := testCtx(t)
			session := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(session.FeedStart(ctx, "add(1, 2)", &montygo.FeedOptions{
				ExternalLookup: map[string]any{"add": func(a, b int64) int64 { return a + b }},
			}))
			_, err := snap.ResumeAuto(ctx)
			require.NoError(t, err)
			_, err = snap.ResumeAuto(ctx)
			require.ErrorIs(t, err, montygo.ErrSnapshotResumed)
			require.EqualError(t, err, "snapshot has already been resumed")
		})

		t.Run("loadSnapshot captures externalLookup for resumeAuto", func(t *testing.T) {
			ctx := testCtx(t)
			first := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(first.FeedStart(ctx, "y = fetch()\ny + 1", nil))
			blob, err := snap.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			session := newSession(t, b, montygo.CheckoutOptions{})
			restored := fstFunction(t)(session.LoadSnapshot(ctx, blob, &montygo.LoadSnapshotOptions{
				ExternalLookup: map[string]any{"fetch": func() int64 { return 41 }},
			}))
			done := fstComplete(t)(restored.ResumeAuto(ctx))
			require.Equal(t, int64(42), done.Output)
		})
	})
}
