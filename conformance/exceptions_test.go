package montygo_test

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func excAs[T error](t *testing.T, err error) T {
	t.Helper()
	require.Error(t, err)
	var target T
	require.True(t, errors.As(err, &target), "expected %T, got %T: %v", target, err, err)
	return target
}

func excNotAs[T error](t *testing.T, err error) {
	t.Helper()
	var target T
	require.False(t, errors.As(err, &target), "unexpected %T: %v", err, err)
}

func excRunErr(t *testing.T, b montygo.Backend, code string, opts runOptions) error {
	t.Helper()
	_, err := run(t, b, code, opts)
	require.Error(t, err)
	return err
}

func excRuntime(t *testing.T, b montygo.Backend, code string, opts runOptions) *montygo.RuntimeError {
	t.Helper()
	return excAs[*montygo.RuntimeError](t, excRunErr(t, b, code, opts))
}

func excSyntax(t *testing.T, b montygo.Backend, code string) *montygo.SyntaxError {
	t.Helper()
	return excAs[*montygo.SyntaxError](t, excRunErr(t, b, code, runOptions{}))
}

var excTypeCheck = runOptions{CheckoutOptions: montygo.CheckoutOptions{TypeCheck: true}}

func TestExceptions(t *testing.T) {
	t.Run("MontyError extends Error", func(t *testing.T) {
		var err error = &montygo.RuntimeError{TypeName: "ValueError", Message: "test message"}
		excAs[montygo.Error](t, err)
		excAs[montygo.Error](t, fmt.Errorf("wrapped: %w", err))
	})

	t.Run("MontyError constructor and properties", func(t *testing.T) {
		err := &montygo.RuntimeError{TypeName: "ValueError", Message: "test message"}
		require.Equal(t, montygo.ExceptionInfo{TypeName: "ValueError", Message: "test message"}, err.Exception())
		require.Equal(t, "ValueError: test message", err.Error())
	})

	t.Run("MontyError display()", func(t *testing.T) {
		var err montygo.Error = &montygo.RuntimeError{TypeName: "ValueError", Message: "test message"}
		require.Equal(t, "test message", err.Display(montygo.DisplayMsg))
		require.Equal(t, "ValueError: test message", err.Display(montygo.DisplayTypeMsg))
	})

	t.Run("MontyError with empty message", func(t *testing.T) {
		var err montygo.Error = &montygo.RuntimeError{TypeName: "TypeError"}
		require.Equal(t, "TypeError", err.Display(montygo.DisplayTypeMsg))
		require.Equal(t, "TypeError", err.Error())
	})

	t.Run("MontySyntaxError extends MontyError and Error", func(t *testing.T) {
		var err error = &montygo.SyntaxError{Message: "invalid syntax"}
		excAs[montygo.Error](t, err)
		excAs[*montygo.SyntaxError](t, fmt.Errorf("wrapped: %w", err))
		excNotAs[*montygo.RuntimeError](t, err)
		excNotAs[*montygo.TypingError](t, err)
	})

	t.Run("MontySyntaxError constructor and properties", func(t *testing.T) {
		err := &montygo.SyntaxError{Message: "invalid syntax"}
		require.Equal(t, montygo.ExceptionInfo{TypeName: "SyntaxError", Message: "invalid syntax"}, err.Exception())
		require.Equal(t, "SyntaxError: invalid syntax", err.Error())
	})

	t.Run("MontySyntaxError display()", func(t *testing.T) {
		err := &montygo.SyntaxError{Message: "unexpected token"}
		require.Equal(t, "unexpected token", err.Display(""))
		require.Equal(t, "unexpected token", err.Display(montygo.DisplayMsg))
		require.Equal(t, "SyntaxError: unexpected token", err.Display(montygo.DisplayTypeMsg))
	})

	t.Run("MontyTypingError extends MontyError and Error", func(t *testing.T) {
		var err error = &montygo.TypingError{Diagnostics: "type mismatch"}
		excAs[montygo.Error](t, err)
		excAs[*montygo.TypingError](t, fmt.Errorf("wrapped: %w", err))
		excNotAs[*montygo.RuntimeError](t, err)
		excNotAs[*montygo.SyntaxError](t, err)
	})

	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("zero division error", func(t *testing.T) {
			require.Equal(t, "ZeroDivisionError: division by zero", excRuntime(t, b, "1 / 0", runOptions{}).Error())
		})

		t.Run("value error", func(t *testing.T) {
			require.Equal(t, "ValueError: bad value", excRuntime(t, b, `raise ValueError("bad value")`, runOptions{}).Error())
		})

		t.Run("type error", func(t *testing.T) {
			require.Equal(t, `TypeError: can only concatenate str (not "int") to str`, excRuntime(t, b, "'string' + 1", runOptions{}).Error())
		})

		t.Run("unicode encode error", func(t *testing.T) {
			err := excRuntime(t, b, `"café".encode("ascii")`, runOptions{})
			require.Equal(t, "UnicodeEncodeError", err.Exception().TypeName)
			require.Equal(t, `UnicodeEncodeError: 'ascii' codec can't encode character '\xe9' in position 3: ordinal not in range(128)`, err.Error())
		})

		t.Run("unicode decode error", func(t *testing.T) {
			err := excRuntime(t, b, `b"\xe9".decode("ascii")`, runOptions{})
			require.Equal(t, "UnicodeDecodeError", err.Exception().TypeName)
			require.Equal(t, `UnicodeDecodeError: 'ascii' codec can't decode byte 0xe9 in position 0: ordinal not in range(128)`, err.Error())
		})

		t.Run("index error", func(t *testing.T) {
			require.Equal(t, "IndexError: list index out of range", excRuntime(t, b, "[1, 2, 3][10]", runOptions{}).Error())
		})

		t.Run("key error", func(t *testing.T) {
			require.Equal(t, "KeyError: b", excRuntime(t, b, `{"a": 1}["b"]`, runOptions{}).Error())
		})

		t.Run("attribute error", func(t *testing.T) {
			require.Equal(t, "AttributeError: no such attr", excRuntime(t, b, `raise AttributeError("no such attr")`, runOptions{}).Error())
		})

		t.Run("name error", func(t *testing.T) {
			require.Equal(t, "NameError: name 'undefined_variable' is not defined", excRuntime(t, b, "undefined_variable", runOptions{}).Error())
		})

		t.Run("assertion error", func(t *testing.T) {
			require.Equal(t, "AssertionError", excRuntime(t, b, "assert False", runOptions{}).Error())
		})

		t.Run("assertion error with message", func(t *testing.T) {
			require.Equal(t, "AssertionError: custom message", excRuntime(t, b, `assert False, "custom message"`, runOptions{}).Error())
		})

		t.Run("assertion error with introspected detail", func(t *testing.T) {
			require.Equal(t, "AssertionError: assert 1 == 2", excRuntime(t, b, "assert 1 == 2", runOptions{}).Error())
		})

		t.Run("assertMessageAnnotations: false restores CPython behavior", func(t *testing.T) {
			opts := runOptions{CheckoutOptions: montygo.CheckoutOptions{AssertMessageAnnotations: montygo.Uint32(0)}}
			require.Equal(t, "AssertionError", excRuntime(t, b, "assert 1 == 2", opts).Error())
		})

		t.Run("assertMessageAnnotations: integer customizes repr truncation", func(t *testing.T) {
			opts := runOptions{CheckoutOptions: montygo.CheckoutOptions{AssertMessageAnnotations: montygo.Uint32(6)}}
			require.Equal(t, "AssertionError: assert 'abcde… == ''", excRuntime(t, b, "assert 'abcdefghij' == ''", opts).Error())
		})

		t.Run("assertMessageAnnotations: invalid numbers are rejected", func(t *testing.T) {
			for _, value := range []uint32{1, math.MaxUint32} {
				opts := runOptions{CheckoutOptions: montygo.CheckoutOptions{AssertMessageAnnotations: montygo.Uint32(value)}}
				v, err := run(t, b, "assert True", opts)
				require.NoError(t, err, "assertMessageAnnotations %d", value)
				require.Nil(t, v)
			}
		})

		t.Run("runtime error", func(t *testing.T) {
			require.Equal(t, "RuntimeError: runtime error", excRuntime(t, b, `raise RuntimeError("runtime error")`, runOptions{}).Error())
		})

		t.Run("not implemented error", func(t *testing.T) {
			require.Equal(t, "NotImplementedError: not implemented", excRuntime(t, b, `raise NotImplementedError("not implemented")`, runOptions{}).Error())
		})

		t.Run("os.environ without os callback raises RuntimeError", func(t *testing.T) {
			err := excRuntime(t, b, "import os\nx = os.environ", runOptions{})
			require.Equal(t, "RuntimeError", err.Exception().TypeName)
			require.Equal(t, "'os.environ' is not supported in this environment", err.Exception().Message)
		})

		t.Run("os.getenv without os callback raises RuntimeError", func(t *testing.T) {
			err := excRuntime(t, b, "import os\nx = os.getenv('HOME')", runOptions{})
			require.Equal(t, "RuntimeError", err.Exception().TypeName)
			require.Equal(t, "'os.getenv' is not supported in this environment", err.Exception().Message)
		})

		t.Run("syntax error on run", func(t *testing.T) {
			require.Equal(t, "SyntaxError: Expected an identifier", excSyntax(t, b, "def").Error())
		})

		t.Run("syntax error unclosed paren", func(t *testing.T) {
			require.Equal(t, "SyntaxError: unexpected EOF while parsing", excSyntax(t, b, "print(1").Error())
		})

		t.Run("syntax error invalid syntax", func(t *testing.T) {
			require.Equal(t, "SyntaxError: Expected an expression", excSyntax(t, b, "x = = 1").Error())
		})

		t.Run("catch with base class", func(t *testing.T) {
			excAs[montygo.Error](t, excRunErr(t, b, "1 / 0", runOptions{}))
		})

		t.Run("catch syntax error with base class", func(t *testing.T) {
			excAs[montygo.Error](t, excRunErr(t, b, "def", runOptions{}))
		})

		t.Run("raise caught exception", func(t *testing.T) {
			code := `
try:
    1 / 0
except ZeroDivisionError as e:
    result = 'caught'
result
`
			require.Equal(t, "caught", mustRun(t, b, code, runOptions{}))
		})

		t.Run("exception in function", func(t *testing.T) {
			code := `
def fail():
    raise ValueError('from function')

fail()
`
			require.Equal(t, "ValueError: from function", excRuntime(t, b, code, runOptions{}).Error())
		})

		t.Run("display traceback", func(t *testing.T) {
			err := excRuntime(t, b, "1 / 0", runOptions{})
			require.Equal(t, `Traceback (most recent call last):
  File "<python-input-0>", line 1, in <module>
    1 / 0
    ~~~~~
ZeroDivisionError: division by zero`, err.Display(montygo.DisplayTraceback))
		})

		t.Run("display type msg", func(t *testing.T) {
			err := excRuntime(t, b, `raise ValueError("test message")`, runOptions{})
			require.Equal(t, "ValueError: test message", err.Display(montygo.DisplayTypeMsg))
		})

		t.Run("runtime display", func(t *testing.T) {
			err := excRuntime(t, b, `raise ValueError("test message")`, runOptions{})
			require.Equal(t, "test message", err.Display(montygo.DisplayMsg))
			require.Equal(t, "ValueError: test message", err.Display(montygo.DisplayTypeMsg))
			require.Equal(t, `Traceback (most recent call last):
  File "<python-input-0>", line 1, in <module>
    raise ValueError("test message")
ValueError: test message`, err.Display(montygo.DisplayTraceback))
		})

		t.Run("str returns type msg", func(t *testing.T) {
			err := excRuntime(t, b, `raise ValueError("test message")`, runOptions{})
			require.Equal(t, "ValueError: test message", err.Error())
		})

		t.Run("syntax error display", func(t *testing.T) {
			err := excSyntax(t, b, "def")
			require.Equal(t, "Expected an identifier", err.Display(""))
			require.Equal(t, "SyntaxError: Expected an identifier", err.Display(montygo.DisplayTypeMsg))
		})

		excNestedCode := `def inner():
    raise ValueError('error')

def outer():
    inner()

outer()
`

		t.Run("traceback frames", func(t *testing.T) {
			err := excRuntime(t, b, excNestedCode, runOptions{})
			require.Equal(t, `Traceback (most recent call last):
  File "<python-input-0>", line 7, in <module>
    outer()
    ~~~~~~~
  File "<python-input-0>", line 5, in outer
    inner()
    ~~~~~~~
  File "<python-input-0>", line 2, in inner
    raise ValueError('error')
ValueError: error`, err.Display(montygo.DisplayTraceback))
		})

		t.Run("traceback() returns structured frames", func(t *testing.T) {
			err := excRuntime(t, b, excNestedCode, runOptions{})
			require.Equal(t, []montygo.Frame{
				{Filename: "<python-input-0>", Line: 7, Column: 1, EndLine: 7, EndColumn: 8, FunctionName: "<module>", SourceLine: "outer()"},
				{Filename: "<python-input-0>", Line: 5, Column: 5, EndLine: 5, EndColumn: 12, FunctionName: "outer", SourceLine: "    inner()"},
				{Filename: "<python-input-0>", Line: 2, Column: 11, EndLine: 2, EndColumn: 30, FunctionName: "inner", SourceLine: "    raise ValueError('error')"},
			}, err.TracebackFrames())
		})

		t.Run("MontyRuntimeError display()", func(t *testing.T) {
			raw := excRunErr(t, b, "1 / 0", runOptions{})
			excAs[montygo.Error](t, raw)
			err := excAs[*montygo.RuntimeError](t, raw)
			require.Equal(t, "ZeroDivisionError: division by zero", err.Error())
			traceback := err.Display(montygo.DisplayTraceback)
			require.Equal(t, traceback, err.Display(""))
			require.Equal(t, `Traceback (most recent call last):
  File "<python-input-0>", line 1, in <module>
    1 / 0
    ~~~~~
ZeroDivisionError: division by zero`, traceback)
			require.Equal(t, "ZeroDivisionError: division by zero", err.Display(montygo.DisplayTypeMsg))
			require.Equal(t, "division by zero", err.Display(montygo.DisplayMsg))
		})

		t.Run("MontyRuntimeError can be caught with instanceof", func(t *testing.T) {
			err := excRunErr(t, b, "1 / 0", runOptions{})
			excAs[*montygo.RuntimeError](t, err)
			excAs[montygo.Error](t, err)
		})

		t.Run("MontyTypingError is thrown on type check failure", func(t *testing.T) {
			raw := excRunErr(t, b, `x: int = "not an int"`, excTypeCheck)
			excAs[montygo.Error](t, raw)
			err := excAs[*montygo.TypingError](t, raw)
			require.Equal(t, "TypeError: error[invalid-assignment]: Object of type `Literal[\"not an int\"]` is not assignable to `int`", err.Error())
			require.Equal(t, "error[invalid-assignment]: Object of type `Literal[\"not an int\"]` is not assignable to `int`\n"+
				" --> main.py:1:10\n"+
				"  |\n"+
				"1 | x: int = \"not an int\"\n"+
				"  |    ---   ^^^^^^^^^^^^ Incompatible value of type `Literal[\"not an int\"]`\n"+
				"  |    |\n"+
				"  |    Declared type\n\n", err.Display(""))
		})

		t.Run("MontyError catches all Monty exceptions", func(t *testing.T) {
			excAs[montygo.Error](t, excRunErr(t, b, "def", runOptions{}))
			excAs[montygo.Error](t, excRunErr(t, b, "1 / 0", runOptions{}))
			excAs[montygo.Error](t, excRunErr(t, b, `x: int = "str"`, excTypeCheck))
		})

		t.Run("can distinguish error types with instanceof", func(t *testing.T) {
			syntaxErr := excRunErr(t, b, "def", runOptions{})
			excAs[*montygo.SyntaxError](t, syntaxErr)
			excNotAs[*montygo.RuntimeError](t, syntaxErr)
			excNotAs[*montygo.TypingError](t, syntaxErr)

			runtimeErr := excRunErr(t, b, "1 / 0", runOptions{})
			excAs[*montygo.RuntimeError](t, runtimeErr)
			excNotAs[*montygo.SyntaxError](t, runtimeErr)
			excNotAs[*montygo.TypingError](t, runtimeErr)

			typingErr := excRunErr(t, b, `x: int = "str"`, excTypeCheck)
			excAs[*montygo.TypingError](t, typingErr)
			excNotAs[*montygo.SyntaxError](t, typingErr)
			excNotAs[*montygo.RuntimeError](t, typingErr)
		})

		t.Run("exception getter returns correct info for runtime error", func(t *testing.T) {
			err := excRuntime(t, b, `raise ValueError("test")`, runOptions{})
			require.Equal(t, "ValueError", err.Exception().TypeName)
			require.Equal(t, "test", err.Exception().Message)
		})

		t.Run("exception getter returns correct info for syntax error", func(t *testing.T) {
			require.Equal(t, "SyntaxError", excSyntax(t, b, "def").Exception().TypeName)
		})

		t.Run("display() works polymorphically on MontyTypingError", func(t *testing.T) {
			err := excAs[montygo.Error](t, excRunErr(t, b, `x: int = "str"`, excTypeCheck))
			require.True(t, strings.HasPrefix(err.Display(montygo.DisplayMsg), "error[invalid-assignment]:"), err.Display(montygo.DisplayMsg))
			require.Equal(t, "TypeError: error[invalid-assignment]: Object of type `Literal[\"str\"]` is not assignable to `int`", err.Error())
		})
	})
}
