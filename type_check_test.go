package monty_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

const excAnsiEscape = "\x1b["

func excUnsupportedOperatorDiagnostics(scriptName string) string {
	return strings.Join([]string{
		"error[unsupported-operator]: Unsupported `+` operation",
		" --> " + scriptName + ":1:1",
		"  |",
		`1 | "hello" + 1`,
		"  | -------^^^-",
		"  | |         |",
		"  | |         Has type `Literal[1]`",
		"  | Has type `Literal[\"hello\"]`",
		"",
		"",
	}, "\n")
}

func excTyping(t *testing.T, b monty.Backend, code string, opts runOptions) *monty.TypingError {
	t.Helper()
	return excAs[*monty.TypingError](t, excRunErr(t, b, code, opts))
}

func excChecked(co monty.CheckoutOptions) runOptions {
	co.TypeCheck = true
	return runOptions{CheckoutOptions: co}
}

func TestTypeCheck(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("type check no errors", func(t *testing.T) {
			require.Nil(t, mustRun(t, b, "x = 1", excChecked(monty.CheckoutOptions{})))
		})

		t.Run("type check with errors", func(t *testing.T) {
			err := excTyping(t, b, `"hello" + 1`, excChecked(monty.CheckoutOptions{}))
			require.Equal(t, "TypeError: error[unsupported-operator]: Unsupported `+` operation", err.Error())
			require.Equal(t, excUnsupportedOperatorDiagnostics("main.py"), err.Display(""))
		})

		t.Run("type check format", func(t *testing.T) {
			err := excTyping(t, b, `"hello" + 1`, excChecked(monty.CheckoutOptions{TypeCheckFormat: monty.FormatConcise}))
			require.Equal(t, "main.py:1:1: error[unsupported-operator] Operator `+` is not supported between objects of type `Literal[\"hello\"]` and `Literal[1]`\n", err.Display(""))
		})

		t.Run("type check format json", func(t *testing.T) {
			err := excTyping(t, b, `"hello" + 1`, excChecked(monty.CheckoutOptions{TypeCheckFormat: monty.FormatJSON}))
			var diagnostics []struct {
				Name     string `json:"name"`
				Location struct {
					Row    int `json:"row"`
					Column int `json:"column"`
				} `json:"location"`
			}
			require.NoError(t, json.Unmarshal([]byte(err.Display("")), &diagnostics))
			require.NotEmpty(t, diagnostics)
			require.Equal(t, "unsupported-operator", diagnostics[0].Name)
			require.Equal(t, 1, diagnostics[0].Location.Row)
			require.Equal(t, 1, diagnostics[0].Location.Column)
		})

		t.Run("type check color", func(t *testing.T) {
			err := excTyping(t, b, `"hello" + 1`, excChecked(monty.CheckoutOptions{TypeCheckFormat: monty.FormatConcise, TypeCheckColor: true}))
			require.True(t, strings.HasPrefix(err.Display(""), excAnsiEscape), "%q", err.Display(""))
		})

		t.Run("type check format rejects inherited property names", func(t *testing.T) {
			pool := sharedPool(t, b)
			for _, bogus := range []string{"toString", "constructor", "__proto__", "hasOwnProperty", "nonsense"} {
				s, err := pool.Checkout(testCtx(t), monty.CheckoutOptions{TypeCheckFormat: monty.TypeCheckFormat(bogus)})
				require.Nil(t, s)
				optErr := excAs[*monty.OptionError](t, err)
				require.Equal(t, "unknown typeCheckFormat '"+bogus+"', expected one of: full, concise, azure, json, jsonlines, rdjson, pylint, gitlab, github", optErr.Error())
			}
			s, err := pool.Checkout(testCtx(t), monty.CheckoutOptions{TypeCheckFormat: monty.FormatJSONLines})
			require.NoError(t, err)
			require.NoError(t, s.Close(context.Background()))
		})

		t.Run("type check function return type", func(t *testing.T) {
			code := `
def foo() -> int:
    return "not an int"
`
			err := excTyping(t, b, code, excChecked(monty.CheckoutOptions{}))
			require.Equal(t, "TypeError: error[invalid-return-type]: Return type does not match returned value", err.Error())
		})

		t.Run("type check undefined variable", func(t *testing.T) {
			err := excTyping(t, b, "print(undefined_var)", excChecked(monty.CheckoutOptions{}))
			require.Equal(t, "TypeError: error[unresolved-reference]: Name `undefined_var` used when not defined", err.Error())
		})

		t.Run("type check valid function", func(t *testing.T) {
			code := `
def add(a: int, b: int) -> int:
    return a + b

add(1, 2)
`
			require.Equal(t, int64(3), mustRun(t, b, code, excChecked(monty.CheckoutOptions{})))
		})

		t.Run("type check disabled by default", func(t *testing.T) {
			err := excRuntime(t, b, `"hello" + 1`, runOptions{})
			require.Equal(t, `TypeError: can only concatenate str (not "int") to str`, err.Error())
		})

		t.Run("type check explicit false", func(t *testing.T) {
			excRuntime(t, b, `"hello" + 1`, runOptions{CheckoutOptions: monty.CheckoutOptions{TypeCheck: false}})
		})

		t.Run("default allows run with inputs", func(t *testing.T) {
			v := mustRun(t, b, "x + 1", runOptions{FeedOptions: monty.FeedOptions{Inputs: map[string]any{"x": 5}}})
			require.Equal(t, int64(6), v)
		})

		t.Run("earlier feeds join the type-check context", func(t *testing.T) {
			excTyping(t, b, "result = x + 1", excChecked(monty.CheckoutOptions{}))
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{TypeCheck: true})
			_, err := s.FeedRun(ctx, "x = 0", nil)
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "result = x + 1\nresult", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})

		t.Run("type check stubs with external function", func(t *testing.T) {
			opts := excChecked(monty.CheckoutOptions{TypeCheckStubs: "def fetch(url: str) -> str: ..."})
			opts.ExternalLookup = map[string]any{"fetch": func(string) string { return "response data" }}
			require.Equal(t, "response data", mustRun(t, b, "result = fetch(\"https://example.com\")\nresult", opts))
		})

		t.Run("type check stubs invalid", func(t *testing.T) {
			err := excTyping(t, b, "result: int = x + 1", excChecked(monty.CheckoutOptions{TypeCheckStubs: `x = "hello"`}))
			require.Equal(t, "TypeError: error[unsupported-operator]: Unsupported `+` operation", err.Error())
		})

		t.Run("failing snippet does not execute and session survives", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{TypeCheck: true})
			_, err := s.FeedRun(ctx, "x = 1", nil)
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "x = 2\n\"hello\" + 1", nil)
			excAs[*monty.TypingError](t, err)
			v, err := s.FeedRun(ctx, "x", nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), v)
		})

		t.Run("skipTypeCheck skips checking without joining the context", func(t *testing.T) {
			ctx := testCtx(t)
			s := newSession(t, b, monty.CheckoutOptions{TypeCheck: true})
			v, err := s.FeedRun(ctx, "y = 41\ny", &monty.FeedOptions{SkipTypeCheck: true})
			require.NoError(t, err)
			require.Equal(t, int64(41), v)
			_, err = s.FeedRun(ctx, "y + 1", nil)
			typingErr := excAs[*monty.TypingError](t, err)
			require.Equal(t, "TypeError: error[unresolved-reference]: Name `y` used when not defined", typingErr.Error())
		})

		t.Run("scriptName appears in diagnostics", func(t *testing.T) {
			err := excTyping(t, b, `"hello" + 1`, excChecked(monty.CheckoutOptions{ScriptName: "my_script.py"}))
			require.Equal(t, excUnsupportedOperatorDiagnostics("my_script.py"), err.Display(""))
		})

		t.Run("monty typing error is monty error subclass", func(t *testing.T) {
			err := excRunErr(t, b, `"hello" + 1`, excChecked(monty.CheckoutOptions{}))
			excAs[*monty.TypingError](t, err)
			excAs[monty.Error](t, err)
		})

		t.Run("monty typing error message is first diagnostic line", func(t *testing.T) {
			err := excTyping(t, b, `"hello" + 1`, excChecked(monty.CheckoutOptions{}))
			first, _, _ := strings.Cut(err.Display(""), "\n")
			require.Equal(t, "TypeError: "+first, err.Error())
			require.Equal(t, monty.ExceptionInfo{TypeName: "TypeError", Message: "error[unsupported-operator]: Unsupported `+` operation"}, err.Exception())
		})
	})
}
