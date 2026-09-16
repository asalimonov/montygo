package main

import (
	"context"
	"fmt"
	"github.com/asalimonov/montygo/sandbox"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
)

// Expected modes are the results of upstream detect_repl_continuation_mode at
// f8acf4fa, including every case of repl_detects_continuation_mode_for_common_cases.
var upstreamContinuationCases = []struct {
	source string
	want   mode
}{
	{"value = 1\n", complete},
	{"if True:\n", incompleteBlock},
	{"[1,\n", incompleteImplicit},
	{"foo(1,\n", incompleteImplicit},
	{"{\n", incompleteImplicit},
	{"x = \\\n", incompleteImplicit},
	{"value = '''first line\n", incompleteImplicit},
	{"value = \"\"\"first line\n", incompleteImplicit},
	{"value = r\"\"\"first line\n", incompleteImplicit},
	{"value = b\"\"\"first line\n", incompleteImplicit},
	{"value = f\"\"\"first line\n", incompleteImplicit},
	{"value = t\"\"\"first line\n", incompleteImplicit},
	{"value = f\"\"\"first {1}\n", incompleteImplicit},
	{"value = b'''bytes\n", incompleteImplicit},
	{"value = r'''raw\n", incompleteImplicit},
	{"value = rb\"\"\"raw\n", incompleteImplicit},
	{"value = '''a''' + '''b\n", incompleteImplicit},
	{"\"\"\"doc\n", incompleteImplicit},
	{"value = 'first line\n", complete},
	{"value = \"first line\n", complete},
	{"value = \"\"\"first line\nsecond line\"\"\"\n", complete},
	{"f'{\n", complete},
	{"@decorator\n", incompleteImplicit},
	{"@first\n@second\n", incompleteImplicit},
	{"@decorator\nvalue = 1", complete},
	{"@decorator\nvalue = 1\n", complete},
	{"@decorator\nclass SearchResult:\n", incompleteBlock},
	{"@decorator\ndef search():\n", incompleteBlock},
	{"@decorator\nasync def search():\n", incompleteBlock},
	{"@\n", complete},
	{"def f(x):\n", incompleteBlock},
	{"class A:\n", incompleteBlock},
	{"for i in range(3):\n", incompleteBlock},
	{"while False:\n", incompleteBlock},
	{"with open('x') as f:\n", incompleteBlock},
	{"try:\n", incompleteBlock},
	{"try:\n    pass\n", complete},
	{"if True:\n    x = 1\n", complete},
	{"if True:\n    x = 1\nelse:\n", incompleteBlock},
	{"async def f():\n", incompleteBlock},
	{"match x:\n", incompleteBlock},
	{"match x:\n    case 1:\n", incompleteBlock},
	{"def f(x)\n", complete},
	{"x = 1 +\n", complete},
	{"1 +* 2\n", complete},
	{"print(1,\n 2\n", incompleteImplicit},
	{"x = [\n  1,\n  2,\n]\n", complete},
	{"def f(\n", incompleteImplicit},
	{"def f(\n  x):\n", incompleteBlock},
	{"if True:\npass\n", incompleteBlock},
	{"   x = 1\n", complete},
	{"else:\n", complete},
	{"x = (1,\n2\n", incompleteImplicit},
	{"x = {'a': [1, (2,\n", incompleteImplicit},
}

func TestContinuationModeMatchesUpstream(t *testing.T) {
	ctx := t.Context()
	pool, err := montygo.NewPool(ctx, montyenv.PoolOptions())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(context.Background()) })
	rt, err := montygo.NewRuntime(montygo.RuntimeOptions{})
	require.NoError(t, err)
	session, err := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	quiet := &montygo.FeedOptions{Print: sandbox.PrintFunc(func(sandbox.Stream, string) error { return nil })}
	for _, tc := range upstreamContinuationCases {
		t.Run(fmt.Sprintf("%q", tc.source), func(t *testing.T) {
			_, err := session.FeedRun(ctx, tc.source, quiet)
			require.Equal(t, tc.want, continuationMode(tc.source, err), "feed error: %v", err)
		})
	}
}

func TestUnterminatedTripleQuote(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   bool
	}{
		{"'''abc\n", true},
		{"x = \"\"\"abc\n", true},
		{"'''a''' + '''b\n", true},
		{"'''a\\'''\n", true},
		{"'abc\n", false},
		{"'abc\nx = '''def\n", false},
		{"'it''s'\n", false},
		{"'a' + \"b\"\n", false},
		{"# don't '''\nx = 1\n", false},
		{"'a\\\nb'\n", false},
		{"", false},
	} {
		t.Run(fmt.Sprintf("%q", tc.source), func(t *testing.T) {
			require.Equal(t, tc.want, unterminatedTripleQuote(tc.source))
		})
	}
}
