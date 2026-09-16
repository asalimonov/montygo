package montygo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
)

func coreAInputs(inputs map[string]any) runOptions {
	return runOptions{FeedOptions: montygo.FeedOptions{Inputs: inputs}}
}

func TestInputs(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("single input", func(t *testing.T) {
			require.Equal(t, int64(42), mustRun(t, b, "x", coreAInputs(map[string]any{"x": 42})))
		})

		t.Run("multiple inputs", func(t *testing.T) {
			require.Equal(t, int64(6), mustRun(t, b, "x + y + z", coreAInputs(map[string]any{"x": 1, "y": 2, "z": 3})))
		})

		t.Run("input used in expression", func(t *testing.T) {
			require.Equal(t, int64(13), mustRun(t, b, "x * 2 + y", coreAInputs(map[string]any{"x": 5, "y": 3})))
		})

		t.Run("input string", func(t *testing.T) {
			v := mustRun(t, b, `greeting + " " + name`, coreAInputs(map[string]any{"greeting": "Hello", "name": "World"}))
			require.Equal(t, "Hello World", v)
		})

		t.Run("input list", func(t *testing.T) {
			require.Equal(t, int64(30), mustRun(t, b, "data[0] + data[1]", coreAInputs(map[string]any{"data": []any{10, 20}})))
		})

		t.Run("input dict", func(t *testing.T) {
			v := mustRun(t, b, `config["a"] * config["b"]`, coreAInputs(map[string]any{"config": map[string]any{"a": 3, "b": 4}}))
			require.Equal(t, int64(12), v)
		})

		t.Run("missing input raises", func(t *testing.T) {
			_, err := run(t, b, "x + y", coreAInputs(map[string]any{"x": 1}))
			var runtimeErr *monterr.RuntimeError
			require.ErrorAs(t, err, &runtimeErr)
			require.EqualError(t, err, "NameError: name 'y' is not defined")
		})

		t.Run("all inputs missing raises", func(t *testing.T) {
			_, err := run(t, b, "x", runOptions{})
			var runtimeErr *monterr.RuntimeError
			require.ErrorAs(t, err, &runtimeErr)
			require.EqualError(t, err, "NameError: name 'x' is not defined")
		})

		t.Run("unused inputs are allowed", func(t *testing.T) {
			require.Equal(t, int64(2), mustRun(t, b, "1 + 1", coreAInputs(map[string]any{"x": 1})))
		})

		t.Run("inputs order independent", func(t *testing.T) {
			require.Equal(t, int64(7), mustRun(t, b, "a - b", coreAInputs(map[string]any{"b": 3, "a": 10})))
		})

		t.Run("function param shadows input", func(t *testing.T) {
			code := `
def foo(x):
    return x + 1

foo(x * 2)
`
			require.Equal(t, int64(11), mustRun(t, b, code, coreAInputs(map[string]any{"x": 5})))
		})

		t.Run("function param shadows input multiple params", func(t *testing.T) {
			code := `
def add(x, y):
    return x + y

add(x * 10, y * 100)
`
			require.Equal(t, int64(320), mustRun(t, b, code, coreAInputs(map[string]any{"x": 2, "y": 3})))
		})

		t.Run("input accessible outside shadowing function", func(t *testing.T) {
			code := `
def double(x):
    return x * 2

result = double(10) + x
result
`
			require.Equal(t, int64(25), mustRun(t, b, code, coreAInputs(map[string]any{"x": 5})))
		})

		t.Run("function param shadows input with default", func(t *testing.T) {
			code := `
def foo(x=100):
    return x + 1

foo(x * 2)
`
			require.Equal(t, int64(11), mustRun(t, b, code, coreAInputs(map[string]any{"x": 5})))
		})

		t.Run("function uses input directly", func(t *testing.T) {
			code := `
def foo(y):
    return x + y

foo(10)
`
			require.Equal(t, int64(15), mustRun(t, b, code, coreAInputs(map[string]any{"x": 5})))
		})

		t.Run("complex input types", func(t *testing.T) {
			require.Equal(t, int64(5), mustRun(t, b, "len(items)", coreAInputs(map[string]any{"items": []any{1, 2, 3, 4, 5}})))
		})
	})
}
