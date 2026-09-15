package monty_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func ospCheckRelativePathResults(t *testing.T, b monty.Backend) {
	t.Helper()
	var calls []any
	result, err := run(t, b, `from pathlib import Path
([str(p) for p in Path('.').iterdir()],
 [str(p) for p in Path('sub/..').iterdir()],
 open('./file.txt').name,
 Path('./file.txt').open().name,
 str(open(b'./file.txt').name))`, runOptions{FeedOptions: monty.FeedOptions{
		Cwd: "/data",
		OS: func(_ context.Context, name string, args []any, _ monty.Kwargs) (any, error) {
			calls = append(calls, []any{name, args})
			switch name {
			case "Path.iterdir":
				return []any{"/data/file.txt"}, nil
			case "open":
				h, err := monty.NewFileHandle(fmt.Sprint(args[0]), "r", 0)
				return h, err
			}
			return nil, fmt.Errorf("unexpected OS call: %s", name)
		},
	}})
	require.NoError(t, err)
	require.Equal(t, monty.Tuple{[]any{"file.txt"}, []any{"sub/../file.txt"}, "./file.txt", "file.txt", "b'./file.txt'"}, result)
	openArgs := []any{monty.Path("/data/file.txt"), "r"}
	require.Equal(t, []any{
		[]any{"Path.iterdir", []any{monty.Path("/data")}},
		[]any{"Path.iterdir", []any{monty.Path("/data")}},
		[]any{"open", openArgs},
		[]any{"open", openArgs},
		[]any{"open", openArgs},
	}, calls)
}

func ospCheckOsPathValidation(t *testing.T, b monty.Backend) {
	t.Helper()
	var calls []any
	result, err := run(t, b, `import os
from pathlib import Path
errors = []
for operation in [
    lambda: open(path),
    lambda: Path(path).read_text(),
    lambda: os.chdir(path),
    lambda: os.rename(path, 'dst'),
    lambda: os.rename('src', path),
]:
    try:
        operation()
    except ValueError as e:
        errors.append(str(e))
p = Path(path)
(errors, p.exists(), p.is_file(), p.is_dir(), p.is_symlink(), os.getcwd())`, runOptions{FeedOptions: monty.FeedOptions{
		Cwd:    "/data",
		Inputs: map[string]any{"path": "bad\x00/../x"},
		OS: func(_ context.Context, name string, args []any, kw monty.Kwargs) (any, error) {
			calls = append(calls, []any{name, args, kw})
			return true, nil
		},
	}})
	require.NoError(t, err)
	require.Equal(t, monty.Tuple{
		[]any{
			"embedded null byte",
			"embedded null byte",
			"chdir: embedded null character in path",
			"rename: embedded null character in src",
			"rename: embedded null character in dst",
		},
		false,
		false,
		false,
		false,
		"/data",
	}, result)
	require.Empty(t, calls)
	for _, tc := range []struct{ code, expected string }{
		{"import os\nos.listdir()", "PermissionError: Permission denied: '/'"},
		{"open('./x')", "PermissionError: Permission denied: '/x'"},
		{"open('')", "PermissionError: Permission denied: ''"},
		{`open('bad\0/../x')`, "ValueError: embedded null byte"},
	} {
		_, err := run(t, b, tc.code, runOptions{FeedOptions: monty.FeedOptions{Cwd: "/"}})
		require.Error(t, err, tc.code)
		require.Equal(t, tc.expected, err.Error(), tc.code)
	}
}
