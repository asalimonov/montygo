package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/osaccess"
)

func TestSentimentScore(t *testing.T) {
	cases := map[string]float64{
		"This product is amazing!":                     0.3,
		"Delivery arrived on Tuesday":                  0.0,
		"Great service, thank you":                     0.6,
		"Support was A+":                               0.3,
		"great but bad":                                0.0,
		"goodness gracious":                            0.3,
		"amazing great love thank helpful":             1.0,
		"bad angry hate terrible worst":                -1.0,
		"Disappointed, poor and useless. Worst ever!!": -1.0,
	}
	for text, want := range cases {
		require.Equal(t, want, sentimentScore(text), text)
	}
}

func awaitResult(t *testing.T) func(v any, err error) any {
	return func(v any, err error) any {
		t.Helper()
		require.NoError(t, err)
		fut, ok := v.(*monty.Future)
		require.True(t, ok, "expected a future, got %T", v)
		result, err := fut.Wait(t.Context())
		require.NoError(t, err)
		return result
	}
}

func TestExternalFunctionsReadThroughOSAccess(t *testing.T) {
	fs, err := osaccess.New([]osaccess.File{
		osaccess.NewMemoryFile("/data/c.csv", customersFixture),
		osaccess.NewMemoryFile("/data/t.json", `[{"user": "bmelator", "id": 3731785240073317438, "big": 123456789012345678901, "at": 1448221456.5, "tags": ["a", true, null]}]`),
	}, nil)
	require.NoError(t, err)
	ext := &ExternalFunctions{fs: fs}
	await := awaitResult(t)

	rows := await(ext.queryCSV(t.Context(), []any{monty.Path("/data/c.csv")}, monty.Kwargs{
		"sql":        `SELECT "First" FROM data WHERE "Email" IN $emails`,
		"parameters": monty.NewDict(monty.Pair{Key: "emails", Value: []any{"afresco@dayrep.com"}}),
	}))
	require.Equal(t, []any{monty.NewDict(monty.Pair{Key: "First", Value: "Al"})}, rows)

	tweets := await(ext.readJSON(t.Context(), nil, monty.Kwargs{"filepath": monty.Path("/data/t.json")})).([]any)
	tweet := tweets[0].(*monty.Dict)
	require.Equal(t, []any{"user", "id", "big", "at", "tags"}, tweet.Keys())
	require.Equal(t, "{'user': 'bmelator', 'id': 3731785240073317438, 'big': 123456789012345678901, 'at': 1448221456.5, 'tags': ['a', True, None]}", monty.Repr(tweet))

	require.Equal(t, 0.3, await(analyzeSentiment(t.Context(), nil, monty.Kwargs{"text": "Glad I bought it"})))

	fut, err := ext.readJSON(t.Context(), []any{monty.Path("/data/missing.json")}, nil)
	require.NoError(t, err)
	_, err = fut.(*monty.Future).Wait(t.Context())
	require.Error(t, err)

	_, err = ext.queryCSV(t.Context(), []any{monty.Path("/data/c.csv")}, nil)
	require.EqualError(t, err, "TypeError: query_csv() missing 1 required positional argument: 'sql'")
}
