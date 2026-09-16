package montygo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

type extPoint struct {
	X, Y int
}

type extCall struct {
	args   []any
	kwargs montygo.Kwargs
}

func extLookup(entries map[string]any) runOptions {
	return runOptions{FeedOptions: montygo.FeedOptions{ExternalLookup: entries}}
}

func extRecorder(calls *[]extCall, result any) montygo.FunctionFunc {
	return func(_ context.Context, args []any, kwargs montygo.Kwargs) (any, error) {
		*calls = append(*calls, extCall{args: args, kwargs: kwargs})
		return result, nil
	}
}

func extRaiser(excType, message string) func() error {
	return func() error { return montygo.Raise(excType, message) }
}

func extRuntimeError(t *testing.T, err error) *montygo.RuntimeError {
	t.Helper()
	var re *montygo.RuntimeError
	require.ErrorAs(t, err, &re)
	return re
}

func extRequireRuntimeMessage(t *testing.T, err error, message string) {
	t.Helper()
	extRuntimeError(t, err)
	require.Equal(t, message, err.Error())
}

func TestExternal(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("external function no args", func(t *testing.T) {
			var calls []extCall
			v := mustRun(t, b, "noop()", extLookup(map[string]any{"noop": extRecorder(&calls, "called")}))
			require.Equal(t, "called", v)
			require.Len(t, calls, 1)
			require.Empty(t, calls[0].args)
			require.Empty(t, calls[0].kwargs)
		})

		t.Run("external function positional args", func(t *testing.T) {
			var calls []extCall
			v := mustRun(t, b, "func(1, 2, 3)", extLookup(map[string]any{"func": extRecorder(&calls, "ok")}))
			require.Equal(t, "ok", v)
			require.Len(t, calls, 1)
			require.Equal(t, []any{int64(1), int64(2), int64(3)}, calls[0].args)
			require.Empty(t, calls[0].kwargs)
		})

		t.Run("external function kwargs only", func(t *testing.T) {
			var calls []extCall
			v := mustRun(t, b, `func(a=1, b="two")`, extLookup(map[string]any{"func": extRecorder(&calls, "ok")}))
			require.Equal(t, "ok", v)
			require.Len(t, calls, 1)
			require.Empty(t, calls[0].args)
			require.Equal(t, montygo.Kwargs{"a": int64(1), "b": "two"}, calls[0].kwargs)
		})

		t.Run("external function mixed args kwargs", func(t *testing.T) {
			var calls []extCall
			v := mustRun(t, b, `func(1, 2, x="hello", y=True)`, extLookup(map[string]any{"func": extRecorder(&calls, "ok")}))
			require.Equal(t, "ok", v)
			require.Len(t, calls, 1)
			require.Equal(t, []any{int64(1), int64(2)}, calls[0].args)
			require.Equal(t, montygo.Kwargs{"x": "hello", "y": true}, calls[0].kwargs)
		})

		t.Run("external function complex types", func(t *testing.T) {
			var calls []extCall
			v := mustRun(t, b, `func([1, 2], {"key": "value"})`, extLookup(map[string]any{"func": extRecorder(&calls, "ok")}))
			require.Equal(t, "ok", v)
			require.Len(t, calls, 1)
			require.Len(t, calls[0].args, 2)
			require.Equal(t, []any{int64(1), int64(2)}, calls[0].args[0])
			d, ok := calls[0].args[1].(*montygo.Dict)
			require.True(t, ok, "dict argument arrives as *montygo.Dict, got %T", calls[0].args[1])
			got, found := d.Get("key")
			require.True(t, found)
			require.Equal(t, "value", got)
		})

		t.Run("external function returns none", func(t *testing.T) {
			doNothing := func() {}
			v, err := run(t, b, "do_nothing()", extLookup(map[string]any{"do_nothing": doNothing}))
			require.NoError(t, err)
			require.Nil(t, v)
		})

		t.Run("external function returns complex type", func(t *testing.T) {
			getData := func() any {
				return map[string]any{"a": []int{1, 2, 3}, "b": map[string]any{"nested": true}}
			}
			v := mustRun(t, b, "get_data()", extLookup(map[string]any{"get_data": getData}))
			result, ok := v.(*montygo.Dict)
			require.True(t, ok, "map result arrives as *montygo.Dict, got %T", v)
			a, found := result.Get("a")
			require.True(t, found)
			require.Equal(t, []any{int64(1), int64(2), int64(3)}, a)
			bv, found := result.Get("b")
			require.True(t, found)
			nested, ok := bv.(*montygo.Dict)
			require.True(t, ok, "nested map arrives as *montygo.Dict, got %T", bv)
			n, found := nested.Get("nested")
			require.True(t, found)
			require.Equal(t, true, n)
		})

		t.Run("multiple external functions", func(t *testing.T) {
			var addArgs, mulArgs [2]int64
			add := func(a, b int64) int64 {
				addArgs = [2]int64{a, b}
				return a + b
			}
			mul := func(a, b int64) int64 {
				mulArgs = [2]int64{a, b}
				return a * b
			}
			v := mustRun(t, b, "add(1, 2) + mul(3, 4)", extLookup(map[string]any{"add": add, "mul": mul}))
			require.Equal(t, int64(15), v)
			require.Equal(t, [2]int64{1, 2}, addArgs)
			require.Equal(t, [2]int64{3, 4}, mulArgs)
		})

		t.Run("external function called multiple times", func(t *testing.T) {
			callCount := 0
			counter := func() int {
				callCount++
				return callCount
			}
			v := mustRun(t, b, "counter() + counter() + counter()", extLookup(map[string]any{"counter": counter}))
			require.Equal(t, int64(6), v)
			require.Equal(t, 3, callCount)
		})

		t.Run("external function with input", func(t *testing.T) {
			var seen int64
			process := func(x int64) int64 {
				seen = x
				return x * 10
			}
			v := mustRun(t, b, "process(x)", runOptions{FeedOptions: montygo.FeedOptions{
				Inputs:         map[string]any{"x": 5},
				ExternalLookup: map[string]any{"process": process},
			}})
			require.Equal(t, int64(50), v)
			require.Equal(t, int64(5), seen)
		})

		t.Run("undeclared external function raises name error", func(t *testing.T) {
			_, err := run(t, b, "missing()", runOptions{})
			extRequireRuntimeMessage(t, err, "NameError: name 'missing' is not defined")
		})

		t.Run("undeclared function raises name error", func(t *testing.T) {
			_, err := run(t, b, "unknown_func()", runOptions{})
			extRequireRuntimeMessage(t, err, "NameError: name 'unknown_func' is not defined")
		})

		t.Run("inherited property name is not resolved as a host value", func(t *testing.T) {
			_, err := run(t, b, "toString", extLookup(map[string]any{"present": 1}))
			extRequireRuntimeMessage(t, err, "NameError: name 'toString' is not defined")
		})

		t.Run("inherited property name is not dispatched as a host function", func(t *testing.T) {
			_, err := run(t, b, "hasOwnProperty()", extLookup(map[string]any{"present": 1}))
			extRequireRuntimeMessage(t, err, "NameError: name 'hasOwnProperty' is not defined")
		})

		t.Run("external function raises exception", func(t *testing.T) {
			_, err := run(t, b, "fail()", extLookup(map[string]any{"fail": extRaiser("ValueError", "intentional error")}))
			extRequireRuntimeMessage(t, err, "ValueError: intentional error")
		})

		t.Run("external function wrong name raises name error", func(t *testing.T) {
			bar := func() int { return 1 }
			_, err := run(t, b, "foo()", extLookup(map[string]any{"bar": bar}))
			extRequireRuntimeMessage(t, err, "NameError: name 'foo' is not defined")
		})

		t.Run("external function exception caught by try except", func(t *testing.T) {
			code := `
try:
    fail()
except ValueError:
    caught = True
caught
`
			v := mustRun(t, b, code, extLookup(map[string]any{"fail": extRaiser("ValueError", "caught error")}))
			require.Equal(t, true, v)
		})

		t.Run("external function exception type preserved", func(t *testing.T) {
			_, err := run(t, b, "fail()", extLookup(map[string]any{"fail": extRaiser("TypeError", "type error message")}))
			extRequireRuntimeMessage(t, err, "TypeError: type error message")
		})

		exceptionTypes := [][2]string{
			{"ZeroDivisionError", "ZeroDivisionError"},
			{"OverflowError", "OverflowError"},
			{"ArithmeticError", "ArithmeticError"},
			{"NotImplementedError", "NotImplementedError"},
			{"RecursionError", "RecursionError"},
			{"RuntimeError", "RuntimeError"},
			{"KeyError", "KeyError"},
			{"IndexError", "IndexError"},
			{"LookupError", "LookupError"},
			{"ValueError", "ValueError"},
			{"TypeError", "TypeError"},
			{"AttributeError", "AttributeError"},
			{"NameError", "NameError"},
			{"AssertionError", "AssertionError"},
			{"json.JSONDecodeError", "json.JSONDecodeError"},
			{"re.PatternError", "re.PatternError"},
			{"binascii.Error", "binascii.Error"},
			{"binascii.Incomplete", "binascii.Incomplete"},
			{"SomeCustomError", "RuntimeError"},
		}
		for _, pair := range exceptionTypes {
			hostName, pythonType := pair[0], pair[1]
			t.Run("external function exception hierarchy - "+hostName, func(t *testing.T) {
				_, err := run(t, b, "fail()", extLookup(map[string]any{"fail": extRaiser(hostName, "test message")}))
				re := extRuntimeError(t, err)
				require.Equal(t, pythonType, re.TypeName)
				require.Equal(t, "test message", re.Message)
			})
		}

		parentChildPairs := [][2]string{
			{"ZeroDivisionError", "ArithmeticError"},
			{"OverflowError", "ArithmeticError"},
			{"NotImplementedError", "RuntimeError"},
			{"RecursionError", "RuntimeError"},
			{"KeyError", "LookupError"},
			{"IndexError", "LookupError"},
		}
		for _, pair := range parentChildPairs {
			childType, parentType := pair[0], pair[1]
			t.Run(fmt.Sprintf("external function exception caught by parent - %s caught by %s", childType, parentType), func(t *testing.T) {
				code := fmt.Sprintf(`
try:
    fail()
except %s:
    caught = 'parent'
except %s:
    caught = 'child'
caught
`, parentType, childType)
				v := mustRun(t, b, code, extLookup(map[string]any{"fail": extRaiser(childType, "test")}))
				require.Equal(t, "parent", v)
			})
		}

		dottedTypes := [][3]string{
			{"binascii.Error", "binascii", "ValueError"},
			{"binascii.Incomplete", "binascii", "Exception"},
			{"json.JSONDecodeError", "json", "ValueError"},
			{"re.PatternError", "re", "Exception"},
		}
		for _, triple := range dottedTypes {
			dottedName, module, parentType := triple[0], triple[1], triple[2]
			t.Run("external function dotted exception caught by own name - "+dottedName, func(t *testing.T) {
				code := fmt.Sprintf(`
import %[2]s
try:
    fail()
except %[1]s as exc:
    caught = f'%[1]s: {exc}'
except %[3]s as exc:
    caught = f'%[3]s: {exc}'
caught
`, dottedName, module, parentType)
				v := mustRun(t, b, code, extLookup(map[string]any{"fail": extRaiser(dottedName, "test message")}))
				require.Equal(t, dottedName+": test message", v)
			})
		}

		t.Run("external function exception in expression", func(t *testing.T) {
			_, err := run(t, b, "1 + fail() + 2", extLookup(map[string]any{"fail": extRaiser("RuntimeError", "mid-expression error")}))
			extRequireRuntimeMessage(t, err, "RuntimeError: mid-expression error")
		})

		t.Run("external function exception after successful call", func(t *testing.T) {
			code := `
a = success()
b = fail()
a + b
`
			success := func() int { return 10 }
			_, err := run(t, b, code, extLookup(map[string]any{"success": success, "fail": extRaiser("ValueError", "second call fails")}))
			extRequireRuntimeMessage(t, err, "ValueError: second call fails")
		})

		t.Run("external function exception with finally", func(t *testing.T) {
			code := `
finally_ran = False
try:
    fail()
except ValueError:
    pass
finally:
    finally_ran = True
finally_ran
`
			v := mustRun(t, b, code, extLookup(map[string]any{"fail": extRaiser("ValueError", "error")}))
			require.Equal(t, true, v)
		})

		t.Run("external function returning a malformed marker object", func(t *testing.T) {
			code := `
try:
    bad()
except TypeError as exc:
    caught = str(exc)
caught
`
			bad := func() any { return montygo.Type{Name: "Broken", Origin: montygo.OriginHost} }
			v := mustRun(t, b, code, extLookup(map[string]any{"bad": bad}))
			require.Equal(t, "raw Type markers are not accepted — pass the class through ClassType(...)", v)
		})

		t.Run("external function returning a symbol", func(t *testing.T) {
			bad := func() chan int { return make(chan int) }
			_, err := run(t, b, "bad()", extLookup(map[string]any{"bad": bad}))
			extRequireRuntimeMessage(t, err, "TypeError: Cannot convert Go chan int to Monty value")
		})

		t.Run("externalLookup resolves a bare name to a value", func(t *testing.T) {
			require.Equal(t, int64(42), mustRun(t, b, "x + 1", extLookup(map[string]any{"x": 41})))
		})

		t.Run("externalLookup resolves a container value", func(t *testing.T) {
			v := mustRun(t, b, "data", extLookup(map[string]any{"data": map[string]any{"a": 1, "b": 2}}))
			result, ok := v.(*montygo.Dict)
			require.True(t, ok, "map value arrives as *montygo.Dict, got %T", v)
			a, _ := result.Get("a")
			require.Equal(t, int64(1), a)
			bv, _ := result.Get("b")
			require.Equal(t, int64(2), bv)
		})

		t.Run("externalLookup mixes a function and a value", func(t *testing.T) {
			double := func(n int64) int64 { return n * 2 }
			require.Equal(t, int64(42), mustRun(t, b, "double(n)", extLookup(map[string]any{"double": double, "n": 21})))
		})

		t.Run("externalLookup caches a resolved value within a feed", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			snap, err := s.FeedStart(ctx, "x + x", &montygo.FeedOptions{ExternalLookup: map[string]any{"x": 21}})
			require.NoError(t, err)
			reads := 0
			for {
				lookup, ok := snap.(*montygo.NameLookupSnapshot)
				if !ok {
					break
				}
				require.Equal(t, "x", lookup.VariableName)
				reads++
				snap, err = lookup.ResumeAuto(ctx)
				require.NoError(t, err)
			}
			done, ok := snap.(*montygo.Complete)
			require.True(t, ok, "expected completion, got %T", snap)
			require.Equal(t, int64(42), done.Output)
			require.Equal(t, 1, reads)
		})

		t.Run("externalLookup resolves null and undefined values to None", func(t *testing.T) {
			require.Equal(t, true, mustRun(t, b, "x is None", extLookup(map[string]any{"x": nil})))
			var typedNil *int
			require.Equal(t, true, mustRun(t, b, "y is None", extLookup(map[string]any{"y": typedNil})))
		})

		t.Run("externalLookup absent name raises name error", func(t *testing.T) {
			_, err := run(t, b, "missing", extLookup(map[string]any{"present": 1}))
			extRequireRuntimeMessage(t, err, "NameError: name 'missing' is not defined")
		})

		t.Run("calling a stale proxy whose entry is now non-callable raises TypeError", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			_, err := s.FeedRun(ctx, "f = double", &montygo.FeedOptions{ExternalLookup: map[string]any{"double": func(x int64) int64 { return x * 2 }}})
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "f(2)", &montygo.FeedOptions{ExternalLookup: map[string]any{"double": 5}})
			extRequireRuntimeMessage(t, err, "TypeError: 'int' object is not callable")
		})

		t.Run("stale proxy TypeError names tuple-marked and __monty_type__ values", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, montygo.CheckoutOptions{})
			_, err := s.FeedRun(ctx, "f = fn", &montygo.FeedOptions{ExternalLookup: map[string]any{"fn": func() int { return 1 }}})
			require.NoError(t, err)

			_, err = s.FeedRun(ctx, "f()", &montygo.FeedOptions{ExternalLookup: map[string]any{"fn": montygo.Tuple{int64(1), int64(2)}}})
			extRequireRuntimeMessage(t, err, "TypeError: 'tuple' object is not callable")

			datetime := montygo.DateTime{Year: 2020, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Microsecond: 6}
			_, err = s.FeedRun(ctx, "f()", &montygo.FeedOptions{ExternalLookup: map[string]any{"fn": datetime}})
			extRequireRuntimeMessage(t, err, "TypeError: 'datetime' object is not callable")

			instance := montygo.MustClassInstance(&extPoint{X: 1, Y: 2}, montygo.ClassInstanceOptions{Name: "Point"})
			_, err = s.FeedRun(ctx, "f()", &montygo.FeedOptions{ExternalLookup: map[string]any{"fn": instance}})
			extRequireRuntimeMessage(t, err, "TypeError: 'Point' object is not callable")
		})

		t.Run("stale proxy TypeError survives a throwing getter on the entry", func(t *testing.T) {
			t.Skip("JS-only: Go lookup entries have no property getters or Proxy traps that can throw while the type is named")
		})

		t.Run("externalLookup unconvertible value rejects the turn", func(t *testing.T) {
			_, err := run(t, b, "x", extLookup(map[string]any{"x": make(chan int)}))
			require.Error(t, err)
			require.Equal(t, "Cannot convert Go chan int to Monty value", err.Error())
		})
	})
}
