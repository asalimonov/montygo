package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, buf *syncBuffer, cond func(string) bool) {
	t.Helper()
	require.Eventually(t, func() bool { return cond(buf.String()) }, 30*time.Second, 5*time.Millisecond, "output so far: %q", buf.String())
}

func runREPL(t *testing.T, input string, args ...string) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	require.NoError(t, run(t.Context(), console{in: strings.NewReader(input), out: &out, errOut: &errOut}, args))
	return out.String(), errOut.String()
}

func TestStatePersistsAcrossSnippets(t *testing.T) {
	out, errOut := runREPL(t, "x = 40\nx + 2\n'text'\nNone\nint\n[1, 'a', {2: None}]\nexit\nprint('after exit')\n")
	require.Equal(t, "42\ntext\n<class 'int'>\n[1, 'a', {2: None}]\n", out)
	require.Equal(t, banner, errOut)
}

func TestPrintStreams(t *testing.T) {
	out, errOut := runREPL(t, "import sys\nprint('to stdout')\nprint('to stderr', file=sys.stderr)\n")
	require.Equal(t, "to stdout\n", out)
	require.Equal(t, banner+"to stderr\n", errOut)
}

func TestMultiLineInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"function block", "def add(a, b):\n    return a + b\n\nadd(1, 2)\n", "3\n"},
		{"loop block", "for i in range(2):\n    print(i)\n\n", "0\n1\n"},
		{"if else", "if False:\n    print('a')\nelse:\n    print('b')\n\n", "b\n"},
		{"brackets", "items = [\n  1,\n  2,\n]\nitems\n", "[1, 2]\n"},
		{"call arguments", "print(1,\n      2)\n", "1 2\n"},
		{"backslash", "total = 1 + \\\n  2\ntotal\n", "3\n"},
		{"triple-quoted string", "text = '''a\nb'''\ntext\n", "a\nb\n"},
		{"decorator", "def deco(f):\n    return f\n\n@deco\ndef seven():\n    return 7\n\nseven()\n", "7\n"},
		{"class", "class Point:\n    def __init__(self, x):\n        self.x = x\n\nPoint(4).x\n", "4\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut := runREPL(t, tc.input)
			require.Equal(t, tc.want, out)
			require.Equal(t, banner, errOut)
		})
	}
}

func TestBlockWaitsForBlankLine(t *testing.T) {
	var out, errOut bytes.Buffer
	input := "if True:\n    print('inside')\nprint('still inside')\n\nexit\n"
	require.NoError(t, run(t.Context(), console{in: strings.NewReader(input), out: &out, errOut: &errOut, interactive: true}, nil))
	require.Equal(t, "❯ … … … inside\nstill inside\n❯ ", out.String())
}

func TestImplicitContinuationPrompts(t *testing.T) {
	var out, errOut bytes.Buffer
	input := "items = [\n  1,\n]\nitems\nexit\n"
	require.NoError(t, run(t.Context(), console{in: strings.NewReader(input), out: &out, errOut: &errOut, interactive: true}, nil))
	require.Equal(t, "❯ … … ❯ [1]\n❯ ", out.String())
}

func TestErrorsKeepTheSession(t *testing.T) {
	out, errOut := runREPL(t, "x = 3\n1 / 0\nx\n")
	require.Equal(t, "3\n", out)
	require.Contains(t, errOut, "error: Traceback (most recent call last):\n")
	require.Contains(t, errOut, "    1 / 0\n    ~~~~~\nZeroDivisionError: division by zero\n")
	require.NotContains(t, errOut, restartNotice)
}

func TestSyntaxErrorsAreReportedImmediately(t *testing.T) {
	out, errOut := runREPL(t, "x = 1 +\n'abc\n@\n1\n")
	require.Equal(t, "1\n", out)
	require.Contains(t, errOut, "SyntaxError: Expected an expression\n")
	require.Contains(t, errOut, "SyntaxError: missing closing quote in string literal\n")
	require.Equal(t, 3, strings.Count(errOut, "error: "), errOut)
}

func TestBlankLinesExitAndEndOfInput(t *testing.T) {
	out, _ := runREPL(t, "\n   \n1 + 1")
	require.Equal(t, "2\n", out)
	out, errOut := runREPL(t, "x = [\n")
	require.Empty(t, out)
	require.Equal(t, banner, errOut)
	out, _ = runREPL(t, "exit\n1 + 1\n")
	require.Empty(t, out)
}

func TestInitialCode(t *testing.T) {
	out, _ := runREPL(t, "z\n", "-c", "z = 9")
	require.Equal(t, "9\n", out)

	out, errOut := runREPL(t, "5\n", "-c", "1 / 0")
	require.Equal(t, "5\n", out)
	require.True(t, strings.Index(errOut, "ZeroDivisionError") < strings.Index(errOut, banner), errOut)

	script := filepath.Join(t.TempDir(), "setup.py")
	require.NoError(t, os.WriteFile(script, []byte("greeting = 'hi'\nprint('loaded')\n"), 0o600))
	out, _ = runREPL(t, "greeting\n", script)
	require.Equal(t, "loaded\nhi\n", out)
}

func TestArgumentErrors(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-c", "1", "file.py"}, "cannot specify both -c and a file"},
		{[]string{"a.py", "b.py"}, "unexpected arguments: b.py"},
		{[]string{"--max-memory", "lots"}, "invalid memory size 'lots'"},
		{[]string{"--max-duration", "-1"}, "invalid max duration '-1': expected a non-negative number of seconds"},
		{[]string{"-m", dir}, "invalid mount spec '" + dir + "': expected host_path::virtual_path[::mode[::write_limit_bytes]]"},
		{[]string{"-m", "::/data"}, "invalid mount spec '::/data': host and virtual paths must not be empty"},
		{[]string{"--mount", dir + "::/data::rwx"}, "invalid mount mode 'rwx' in '" + dir + "::/data::rwx': expected 'ro', 'rw', or 'overlay'"},
		{[]string{"-m", dir + "::/data::rw::"}, "invalid write limit in '" + dir + "::/data::rw::': value must not be empty"},
		{[]string{"-m", dir + "::/data::rw::-5"}, "invalid write limit '-5' in '" + dir + "::/data::rw::-5': expected a non-negative integer"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			err := run(t.Context(), console{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard}, tc.args)
			require.EqualError(t, err, tc.want)
		})
	}
}

func TestParseMemorySize(t *testing.T) {
	for input, want := range map[string]uint64{
		"1024":   1024,
		"512KB":  512 << 10,
		"10MB":   10 << 20,
		"1GB":    1 << 30,
		"2gb":    2 << 30,
		" 3 mb ": 3 << 20,
	} {
		got, err := parseMemorySize(input)
		require.NoError(t, err, input)
		require.Equal(t, want, got, input)
	}
	for input, want := range map[string]string{
		"1TB":           "invalid memory size '1TB'",
		"-1":            "invalid memory size '-1'",
		"17179869184GB": "memory size '17179869184GB' overflows",
	} {
		_, err := parseMemorySize(input)
		require.EqualError(t, err, want, input)
	}
}

func TestMounts(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600))
	imports := "from pathlib import Path\nimport os\n"

	t.Run("read-only by default", func(t *testing.T) {
		out, errOut := runREPL(t, imports+"os.getcwd()\nPath('/data/a.txt').read_text()\nPath('/data/b.txt').write_text('x')\n", "-m", dir+"::/data")
		require.Equal(t, "/data\nhello\n", out)
		require.Contains(t, errOut, "error: Traceback")
		require.NoFileExists(t, filepath.Join(dir, "b.txt"))
	})

	t.Run("read-write", func(t *testing.T) {
		out, _ := runREPL(t, imports+"Path('/data/rw.txt').write_text('abc')\n", "-m", dir+"::/data::rw")
		require.Equal(t, "3\n", out)
		content, err := os.ReadFile(filepath.Join(dir, "rw.txt"))
		require.NoError(t, err)
		require.Equal(t, "abc", string(content))
	})

	t.Run("overlay writes last one snippet", func(t *testing.T) {
		out, _ := runREPL(t, imports+"Path('/data/o.txt').write_text('abc')\nPath('/data/o.txt').exists()\n", "-m", dir+"::/data::overlay")
		require.Equal(t, "3\nFalse\n", out)
		require.NoFileExists(t, filepath.Join(dir, "o.txt"))
	})

	t.Run("cwd", func(t *testing.T) {
		require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))
		out, _ := runREPL(t, imports+"os.getcwd()\n", "-m", dir+"::/data", "--cwd", "/data/sub")
		require.Equal(t, "/data/sub\n", out)
		err := run(t.Context(), console{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard}, []string{"--cwd", "relative"})
		require.Error(t, err)
	})
}

func TestResourceLimits(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		out, errOut := runREPL(t, "x = 'a' * 50_000_000\nx = 1\nx\n", "--max-memory", "10MB")
		require.Contains(t, errOut, "MemoryError")
		require.Equal(t, "1\n", out)
	})

	t.Run("recursion", func(t *testing.T) {
		out, errOut := runREPL(t, "def down(n):\n    return down(n + 1)\n\ndown(0)\n2\n", "--max-recursion-depth", "50")
		require.Contains(t, errOut, "RecursionError")
		require.Equal(t, "2\n", out)
	})

	t.Run("duration", func(t *testing.T) {
		_, errOut := runREPL(t, "while True:\n    pass\n\n", "--max-duration", "0.2")
		require.Contains(t, errOut, "TimeoutError")
	})
}

func TestInterruptStopsARunningSnippet(t *testing.T) {
	inR, inW := io.Pipe()
	interrupts := make(chan struct{})
	out, errOut := &syncBuffer{}, &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- run(t.Context(), console{in: inR, out: out, errOut: errOut, interrupts: interrupts}, nil)
	}()

	_, err := io.WriteString(inW, "x = 1\nif True:\n    print('started')\n    while True:\n        pass\n\n")
	require.NoError(t, err)
	waitFor(t, out, func(s string) bool { return strings.Contains(s, "started\n") })
	interrupts <- struct{}{}
	waitFor(t, errOut, func(s string) bool { return strings.Contains(s, restartNotice) })

	_, err = io.WriteString(inW, "x\ny = 2\ny\n")
	require.NoError(t, err)
	waitFor(t, out, func(s string) bool { return strings.HasSuffix(s, "2\n") })
	require.NoError(t, inW.Close())
	require.NoError(t, <-done)
	require.Equal(t, "started\n2\n", out.String())
	require.Contains(t, errOut.String(), "KeyboardInterrupt\n"+restartNotice)
	require.Contains(t, errOut.String(), "NameError")
}

func TestInterruptAtPromptDiscardsPendingInput(t *testing.T) {
	inR, inW := io.Pipe()
	interrupts := make(chan struct{})
	out, errOut := &syncBuffer{}, &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- run(t.Context(), console{in: inR, out: out, errOut: errOut, interactive: true, interrupts: interrupts}, nil)
	}()

	_, err := io.WriteString(inW, "x = [\n")
	require.NoError(t, err)
	waitFor(t, out, func(s string) bool { return s == "❯ … " })
	interrupts <- struct{}{}
	waitFor(t, out, func(s string) bool { return s == "❯ … \n❯ " })
	_, err = io.WriteString(inW, "5\n")
	require.NoError(t, err)
	waitFor(t, out, func(s string) bool { return strings.HasSuffix(s, "5\n❯ ") })
	require.NoError(t, inW.Close())
	require.NoError(t, <-done)
	require.Equal(t, banner, errOut.String())
}
