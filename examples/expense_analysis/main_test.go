package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out))
	require.Equal(t, "{'total_team_members_analyzed': 5, 'count_exceeded_budget': 1, "+
		"'over_budget_details': [{'name': 'Carol Jones', 'total_spent': 6740.0, 'budget': 5000, 'amount_over': 1740.0}]}\n", out.String())
}

func TestGetExpensesBindsKeywords(t *testing.T) {
	fut, err := getExpenses(t.Context(), nil, map[string]any{"user_id": int64(99), "quarter": "Q3", "category": "travel"})
	require.NoError(t, err)
	_, err = getExpenses(t.Context(), []any{int64(1)}, map[string]any{"user_id": int64(1), "quarter": "Q3", "category": "travel"})
	require.EqualError(t, err, "TypeError: get_expenses() got multiple values for argument 'user_id'")
	require.NotNil(t, fut)
}
