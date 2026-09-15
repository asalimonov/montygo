package montygo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

type ntPoint struct {
	X      int
	Y      int
	Label  string `monty:"name"`
	hidden int
}

func TestNamedTupleHelpers(t *testing.T) {
	t.Run("AsNamedTuple takes exported fields in order", func(t *testing.T) {
		nt, err := montygo.AsNamedTuple(&ntPoint{X: 1, Y: 2, Label: "p", hidden: 3})
		require.NoError(t, err)
		require.Equal(t, "ntPoint", nt.TypeName)
		require.Equal(t, []string{"x", "y", "name"}, nt.FieldNames)
		require.Equal(t, []any{1, 2, "p"}, nt.Values)
		_, err = montygo.AsNamedTuple(42)
		require.Error(t, err)
		_, err = montygo.AsNamedTuple((*ntPoint)(nil))
		require.Error(t, err)
	})
	t.Run("NewNamedTuple validates names", func(t *testing.T) {
		nt, err := montygo.NewNamedTuple("Pt", montygo.Pair{Key: "x", Value: 1}, montygo.Pair{Key: "y", Value: 2})
		require.NoError(t, err)
		require.Equal(t, montygo.NamedTuple{TypeName: "Pt", FieldNames: []string{"x", "y"}, Values: []any{1, 2}}, nt)
		_, err = montygo.NewNamedTuple("", montygo.Pair{Key: "x", Value: 1})
		require.Error(t, err)
		_, err = montygo.NewNamedTuple("Pt", montygo.Pair{Key: 1, Value: 1})
		require.Error(t, err)
		_, err = montygo.NewNamedTuple("Pt", montygo.Pair{Key: "x", Value: 1}, montygo.Pair{Key: "x", Value: 2})
		require.Error(t, err)
	})
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("a named tuple round-trips through the sandbox", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			nt, err := montygo.AsNamedTuple(ntPoint{X: 1, Y: 2, Label: "p"})
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "[p.x + p.y, p.name, p]", &montygo.FeedOptions{Inputs: map[string]any{"p": nt}})
			require.NoError(t, err)
			list, ok := v.([]any)
			require.True(t, ok, "%T", v)
			require.Equal(t, int64(3), list[0])
			require.Equal(t, "p", list[1])
			back, ok := list[2].(montygo.NamedTuple)
			require.True(t, ok, "%T", list[2])
			require.Equal(t, []string{"x", "y", "name"}, back.FieldNames)
		})
	})
}
