package montygo_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

type extAsyncOutput struct {
	mu      sync.Mutex
	chunks  []string
	streams []sandbox.Stream
}

func (o *extAsyncOutput) Print(stream sandbox.Stream, text string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.streams = append(o.streams, stream)
	o.chunks = append(o.chunks, text)
	return nil
}

func (o *extAsyncOutput) joined() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.chunks, "")
}

func (o *extAsyncOutput) allStdout(t *testing.T) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	require.NotEmpty(t, o.streams)
	for _, s := range o.streams {
		require.Equal(t, sandbox.Stdout, s)
	}
}

func extAsyncAfter(delay time.Duration, v any, err error) *host.Future {
	return host.Async(func() (any, error) {
		time.Sleep(delay)
		return v, err
	})
}

func TestAsync(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("sequential coroutines use one suspension per call", func(t *testing.T) {
			v := mustRun(t, b, "a = await fetch()\nb = await fetch()\na[0] + b[0]", runOptions{
				CheckoutOptions: montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxSuspensions: 2}},
				FeedOptions: montygo.FeedOptions{ExternalLookup: map[string]any{
					"fetch": func() *host.Future { return extAsyncAfter(0, []int{21}, nil) },
				}},
			})
			require.Equal(t, int64(42), v)
		})

		t.Run("run with sync external function", func(t *testing.T) {
			v := mustRun(t, b, "get_value()", extLookup(map[string]any{
				"get_value": func() int { return 42 },
			}))
			require.Equal(t, int64(42), v)
		})

		t.Run("run with async external function", func(t *testing.T) {
			v := mustRun(t, b, "await fetch_data()", extLookup(map[string]any{
				"fetch_data": func() *host.Future { return extAsyncAfter(10*time.Millisecond, "async result", nil) },
			}))
			require.Equal(t, "async result", v)
		})

		t.Run("run with multiple async calls", func(t *testing.T) {
			code := `
a = await fetch_a()
b = await fetch_b()
a + b
`
			v := mustRun(t, b, code, extLookup(map[string]any{
				"fetch_a": func() *host.Future { return extAsyncAfter(5*time.Millisecond, 10, nil) },
				"fetch_b": func() *host.Future { return extAsyncAfter(5*time.Millisecond, 20, nil) },
			}))
			require.Equal(t, int64(30), v)
		})

		t.Run("run async external function with inputs", func(t *testing.T) {
			v := mustRun(t, b, "await multiply(x)", runOptions{FeedOptions: montygo.FeedOptions{
				Inputs: map[string]any{"x": 5},
				ExternalLookup: map[string]any{
					"multiply": func(n int64) *host.Future { return extAsyncAfter(0, n*2, nil) },
				},
			}})
			require.Equal(t, int64(10), v)
		})

		t.Run("run async external function with args and kwargs", func(t *testing.T) {
			v := mustRun(t, b, `await process(1, 2, name="test")`, extLookup(map[string]any{
				"process": func(a, b int64, kw host.Kwargs) *host.Future {
					return host.Async(func() (any, error) {
						return kw["name"].(string) + ": " + sandbox.Repr(a+b), nil
					})
				},
			}))
			require.Equal(t, "test: 3", v)
		})

		t.Run("sync external function throws exception", func(t *testing.T) {
			_, err := run(t, b, "fail_sync()", extLookup(map[string]any{
				"fail_sync": func() error { return monterr.Raise("ValueError", "sync error") },
			}))
			extRequireRuntimeMessage(t, err, "ValueError: sync error")
		})

		t.Run("async external function throws exception", func(t *testing.T) {
			_, err := run(t, b, "await fail_async()", extLookup(map[string]any{
				"fail_async": func() *host.Future {
					return extAsyncAfter(5*time.Millisecond, nil, monterr.Raise("ValueError", "async error"))
				},
			}))
			extRequireRuntimeMessage(t, err, "ValueError: async error")
		})

		t.Run("async external function exception caught in try/except", func(t *testing.T) {
			code := `
try:
    await might_fail()
except ValueError:
    result = 'caught'
result
`
			v := mustRun(t, b, code, extLookup(map[string]any{
				"might_fail": func() *host.Future {
					return extAsyncAfter(0, nil, monterr.Raise("ValueError", "expected error"))
				},
			}))
			require.Equal(t, "caught", v)
		})

		t.Run("missing external function raises NameError", func(t *testing.T) {
			_, err := run(t, b, "missing_func()", extLookup(map[string]any{}))
			extRequireRuntimeMessage(t, err, "NameError: name 'missing_func' is not defined")
		})

		t.Run("missing external function caught in try/except", func(t *testing.T) {
			code := `
try:
    missing()
except NameError:
    result = 'caught'
result
`
			require.Equal(t, "caught", mustRun(t, b, code, extLookup(map[string]any{})))
		})

		t.Run("async external function returns complex types", func(t *testing.T) {
			v := mustRun(t, b, "await get_data()", extLookup(map[string]any{
				"get_data": func() *host.Future {
					return extAsyncAfter(0, []any{1, 2, map[string]any{"key": "value"}}, nil)
				},
			}))
			result, ok := v.([]any)
			require.True(t, ok, "list result arrives as []any, got %T", v)
			require.Len(t, result, 3)
			require.Equal(t, int64(1), result[0])
			require.Equal(t, int64(2), result[1])
			d, ok := result[2].(*sandbox.Dict)
			require.True(t, ok, "dict item arrives as *sandbox.Dict, got %T", result[2])
			got, found := d.Get("key")
			require.True(t, found)
			require.Equal(t, "value", got)
		})

		t.Run("async external function with list input", func(t *testing.T) {
			v := mustRun(t, b, "await sum_list(items)", runOptions{FeedOptions: montygo.FeedOptions{
				Inputs: map[string]any{"items": []int{1, 2, 3, 4, 5}},
				ExternalLookup: map[string]any{
					"sum_list": func(items []int64) *host.Future {
						return host.Async(func() (any, error) {
							var total int64
							for _, it := range items {
								total += it
							}
							return total, nil
						})
					},
				},
			}})
			require.Equal(t, int64(15), v)
		})

		t.Run("mixed sync and async external functions", func(t *testing.T) {
			code := `
sync_result = sync_func()
async_result = await async_func()
sync_result + async_result
`
			v := mustRun(t, b, code, extLookup(map[string]any{
				"sync_func":  func() int { return 100 },
				"async_func": func() *host.Future { return extAsyncAfter(5*time.Millisecond, 200, nil) },
			}))
			require.Equal(t, int64(300), v)
		})

		t.Run("chained async external calls", func(t *testing.T) {
			code := `
first = await get_first()
second = await process(first)
await finalize(second)
`
			v := mustRun(t, b, code, extLookup(map[string]any{
				"get_first": func() *host.Future { return extAsyncAfter(0, "hello", nil) },
				"process":   func(s string) *host.Future { return extAsyncAfter(0, strings.ToUpper(s), nil) },
				"finalize":  func(s string) *host.Future { return extAsyncAfter(0, s+"!", nil) },
			}))
			require.Equal(t, "HELLO!", v)
		})

		t.Run("run without external functions", func(t *testing.T) {
			require.Equal(t, int64(3), mustRun(t, b, "1 + 2", runOptions{}))
		})

		t.Run("run pure computation", func(t *testing.T) {
			code := `
def factorial(n):
    if n <= 1:
        return 1
    return n * factorial(n - 1)
factorial(5)
`
			require.Equal(t, int64(120), mustRun(t, b, code, runOptions{}))
		})

		t.Run("run with printCallback", func(t *testing.T) {
			out := &extAsyncOutput{}
			v, err := run(t, b, `print("hello from async")`, runOptions{FeedOptions: montygo.FeedOptions{Print: out}})
			require.NoError(t, err)
			require.Nil(t, v)
			out.allStdout(t)
			require.Equal(t, "hello from async\n", out.joined())
		})

		t.Run("printCallback with external functions", func(t *testing.T) {
			out := &extAsyncOutput{}
			v := mustRun(t, b, "x = get_value()\nprint(f\"got {x}\")\nx", runOptions{FeedOptions: montygo.FeedOptions{
				ExternalLookup: map[string]any{"get_value": func() int { return 42 }},
				Print:          out,
			}})
			require.Equal(t, int64(42), v)
			out.allStdout(t)
			require.Equal(t, "got 42\n", out.joined())
		})

		t.Run("printCallback with multiple prints", func(t *testing.T) {
			out := &extAsyncOutput{}
			mustRun(t, b, "print(\"a\")\nprint(\"b\")\nprint(\"c\")", runOptions{FeedOptions: montygo.FeedOptions{Print: out}})
			require.Equal(t, "a\nb\nc\n", out.joined())
		})
	})
}
