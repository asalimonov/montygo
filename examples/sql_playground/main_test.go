package main

import (
	"bytes"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func requireDatasets(t *testing.T) {
	t.Helper()
	if info, err := os.Stat(defaultDatasetsDir()); err != nil || !info.IsDir() {
		t.Skipf("mafudge_datasets not found at %s", defaultDatasetsDir())
	}
}

func TestRun(t *testing.T) {
	requireDatasets(t)
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out, nil))
	require.Equal(t, "getting top customers...\n"+
		"getting twitter handles...\n"+
		"processing 10 customers...\n"+
		"Bill Melator - avg_sentiment=0.11249999999999999\n"+
		"  Bill Melator\n"+
		"    Purchases: $6,090\n"+
		"    Twitter: @bmelator\n"+
		"    Tweets: 24\n"+
		"    Sentiment: +0.11 😊\n"+
		"\n", out.String())
}

func TestRunTypeCheckRejectsUpstreamStubImport(t *testing.T) {
	requireDatasets(t)
	err := run(t.Context(), io.Discard, []string{"-type-check"})
	var typing *montygo.TypingError
	require.ErrorAs(t, err, &typing)
	diagnostics := typing.Display(montygo.DisplayTraceback)
	require.True(t, strings.HasPrefix(diagnostics, "error[unresolved-import]: Cannot resolve imported module `type_stubs`\n  --> sql_playground.py:11:10\n"), diagnostics)
	require.Equal(t, 1, strings.Count(diagnostics, "error["), diagnostics)
}

func TestRunDatasetsFlag(t *testing.T) {
	requireDatasets(t)
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out, []string{"-datasets", defaultDatasetsDir()}))
	require.Contains(t, out.String(), "    Purchases: $6,090\n")
}

func TestRunMissingDatasets(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	err := run(t.Context(), io.Discard, []string{"-datasets", missing})
	require.EqualError(t, err, "mafudge_datasets directory not found at "+missing+". ")

	t.Setenv(datasetsEnv, missing)
	require.Equal(t, missing, defaultDatasetsDir())
	require.EqualError(t, run(t.Context(), io.Discard, nil), "mafudge_datasets directory not found at "+missing+". ")
}

func TestPrintReport(t *testing.T) {
	row := func(name string, purchases any, avg float64) *montygo.Dict {
		return montygo.NewDict(
			montygo.Pair{Key: "name", Value: name},
			montygo.Pair{Key: "total_purchases", Value: purchases},
			montygo.Pair{Key: "twitter", Value: "h"},
			montygo.Pair{Key: "tweet_count", Value: int64(2)},
			montygo.Pair{Key: "avg_sentiment", Value: avg},
		)
	}
	big, _ := new(big.Int).SetString("12345678901234567890", 10)
	var out bytes.Buffer
	require.NoError(t, printReport(&out, []any{row("A", int64(1234567), -0.305), row("B", 950.5, 0.0), row("C", big, 1.0)}))
	require.Equal(t, "  A\n    Purchases: $1,234,567\n    Twitter: @h\n    Tweets: 2\n    Sentiment: -0.30 😞\n\n"+
		"  B\n    Purchases: $950.5\n    Twitter: @h\n    Tweets: 2\n    Sentiment: +0.00 😐\n\n"+
		"  C\n    Purchases: $12,345,678,901,234,567,890\n    Twitter: @h\n    Tweets: 2\n    Sentiment: +1.00 😊\n\n", out.String())

	out.Reset()
	require.NoError(t, printReport(&out, []any{}))
	require.Equal(t, "No results found. Check if customers have matching Twitter handles and tweets.\n", out.String())

	out.Reset()
	require.EqualError(t, printReport(&out, nil), "TypeError: 'NoneType' object is not iterable")
	require.Equal(t, "No results found. Check if customers have matching Twitter handles and tweets.\n", out.String())
}
