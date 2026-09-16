package network

import (
	"github.com/asalimonov/montygo/monterr"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func TestRepl_StatePersistsAcrossFeeds(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	session := s.Checkout(ctx, s.NewPool(wsOptions{}), montygo.CheckoutOptions{})

	_, err := session.FeedRun(ctx, "counter = 0", nil)
	require.NoError(t, err)
	for want := int64(1); want <= 3; want++ {
		_, err = session.FeedRun(ctx, "counter = counter + 1", nil)
		require.NoError(t, err)
		v, err := session.FeedRun(ctx, "counter", nil)
		require.NoError(t, err)
		require.Equal(t, want, v)
	}
}

func TestRepl_ErrorKeepsSession(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	session := s.Checkout(ctx, s.NewPool(wsOptions{}), montygo.CheckoutOptions{})

	_, err := session.FeedRun(ctx, "x = 1", nil)
	require.NoError(t, err)
	_, err = session.FeedRun(ctx, "1 / 0", nil)
	re := requireRuntimeError(t, err, "ZeroDivisionError")
	require.Equal(t, strings.Join([]string{
		"Traceback (most recent call last):",
		`  File "<python-input-1>", line 1, in <module>`,
		"    1 / 0",
		"    ~~~~~",
		"ZeroDivisionError: division by zero",
	}, "\n"), re.Display(monterr.DisplayTraceback))
	v, err := session.FeedRun(ctx, "x", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), v)
}

func TestRepl_DumpRestoreAcrossRecreate(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	session := s.Checkout(ctx, s.NewPool(wsOptions{}), montygo.CheckoutOptions{})
	_, err := session.FeedRun(ctx, "x = 40", nil)
	require.NoError(t, err)
	_, err = session.FeedRun(ctx, "x = x + 1", nil)
	require.NoError(t, err)
	state, err := session.Dump(ctx)
	require.NoError(t, err)

	s.Recreate()
	restored, err := loadInto(t, ctx, s, state)
	require.NoError(t, err)
	v, err := restored.FeedRun(ctx, "x", nil)
	require.NoError(t, err)
	require.Equal(t, int64(41), v)
}

func TestRepl_DrainRestoresSuspendedFeed(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "20"))
	ctx := testCtx(t)
	session := s.Checkout(ctx, s.NewPool(wsOptions{}), montygo.CheckoutOptions{})

	snap, err := session.FeedStart(ctx, "r = ext(7)\nr * 2", nil)
	require.NoError(t, err)
	if lookup, ok := snap.(*montygo.NameLookupSnapshot); ok {
		snap, err = lookup.ResumeFunction(ctx, "ext")
		require.NoError(t, err)
	}
	call, ok := snap.(*montygo.FunctionSnapshot)
	require.True(t, ok, "expected a function snapshot, got %T", snap)
	require.Equal(t, "ext", call.FunctionName)

	s.Signal("TERM")
	waitListenerClosed(t, s)
	_, err = call.Resume(ctx, 21)
	shutdown := requireShutdown(t, err)
	require.NotEmpty(t, shutdown.Dump)

	s.Recreate()
	fresh := s.Checkout(ctx, s.NewPool(wsOptions{}), montygo.CheckoutOptions{})
	restored, err := fresh.LoadSnapshot(ctx, shutdown.Dump, nil)
	require.NoError(t, err)
	again, ok := restored.(*montygo.FunctionSnapshot)
	require.True(t, ok, "expected a restored function snapshot, got %T", restored)
	require.Equal(t, "ext", again.FunctionName)
	final, err := again.Resume(ctx, 21)
	require.NoError(t, err)
	complete, ok := final.(*montygo.Complete)
	require.True(t, ok, "expected completion, got %T", final)
	require.Equal(t, int64(42), complete.Output)
}

func TestRepl_TypeCheckStubsAcrossFeeds(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	session := s.CheckoutRT(ctx, s.NewPool(wsOptions{}), mustRuntime(montygo.RuntimeOptions{
		TypeCheck:      true,
		TypeCheckStubs: "def ext(x: int) -> int: ...",
	}), montygo.CheckoutOptions{})

	_, err := session.FeedRun(ctx, "y: int = 1", &montygo.FeedOptions{
		ExternalLookup: map[string]any{"ext": func(x int) int { return x }},
	})
	require.NoError(t, err)
	_, err = session.FeedRun(ctx, "z: str = ext(y)", nil)
	var typing *monterr.TypingError
	require.ErrorAs(t, err, &typing)
	require.Contains(t, typing.Diagnostics, "str")
}
