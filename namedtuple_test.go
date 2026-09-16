package montygo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

type ntPoint struct {
	X      int
	Y      int
	Label  string `monty:"name"`
	hidden int
}

func TestNamedTupleHelpers(t *testing.T) {
	t.Run("AsNamedTuple takes exported fields in order", func(t *testing.T) {
		nt, err := host.AsNamedTuple(&ntPoint{X: 1, Y: 2, Label: "p", hidden: 3})
		require.NoError(t, err)
		require.Equal(t, "ntPoint", nt.TypeName)
		require.Equal(t, []string{"x", "y", "name"}, nt.FieldNames)
		require.Equal(t, []any{1, 2, "p"}, nt.Values)
		_, err = host.AsNamedTuple(42)
		require.Error(t, err)
		_, err = host.AsNamedTuple((*ntPoint)(nil))
		require.Error(t, err)
	})
	t.Run("NewNamedTuple validates names", func(t *testing.T) {
		nt, err := host.NewNamedTuple("Pt", sandbox.Pair{Key: "x", Value: 1}, sandbox.Pair{Key: "y", Value: 2})
		require.NoError(t, err)
		require.Equal(t, sandbox.NamedTuple{TypeName: "Pt", FieldNames: []string{"x", "y"}, Values: []any{1, 2}}, nt)
		_, err = host.NewNamedTuple("", sandbox.Pair{Key: "x", Value: 1})
		require.Error(t, err)
		_, err = host.NewNamedTuple("Pt", sandbox.Pair{Key: 1, Value: 1})
		require.Error(t, err)
		_, err = host.NewNamedTuple("Pt", sandbox.Pair{Key: "x", Value: 1}, sandbox.Pair{Key: "x", Value: 2})
		require.Error(t, err)
	})
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("a named tuple round-trips through the sandbox", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			nt, err := host.AsNamedTuple(ntPoint{X: 1, Y: 2, Label: "p"})
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "[p.x + p.y, p.name, p]", &montygo.FeedOptions{Inputs: map[string]any{"p": nt}})
			require.NoError(t, err)
			list, ok := v.([]any)
			require.True(t, ok, "%T", v)
			require.Equal(t, int64(3), list[0])
			require.Equal(t, "p", list[1])
			back, ok := list[2].(sandbox.NamedTuple)
			require.True(t, ok, "%T", list[2])
			require.Equal(t, []string{"x", "y", "name"}, back.FieldNames)
		})
	})
}
