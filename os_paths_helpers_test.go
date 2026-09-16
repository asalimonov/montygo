package montygo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

func ospCheckRelativePathResults(t *testing.T, b backend) {
	t.Helper()
	var calls []any
	result, err := run(t, b, `from pathlib import Path
([str(p) for p in Path('.').iterdir()],
 [str(p) for p in Path('sub/..').iterdir()],
 open('./file.txt').name,
 Path('./file.txt').open().name,
 str(open(b'./file.txt').name))`, runOptions{Runtime: mustRuntime(montygo.RuntimeOptions{OS: func(_ context.Context, name string, args []any, _ host.Kwargs) (any, error) {
		calls = append(calls, []any{name, args})
		switch name {
		case "Path.iterdir":
			return []any{"/data/file.txt"}, nil
		case "open":
			h, err := sandbox.NewFileHandle(fmt.Sprint(args[0]), "r", 0)
			return h, err
		}
		return nil, fmt.Errorf("unexpected OS call: %s", name)
	}}), FeedOptions: montygo.FeedOptions{Cwd: "/data"}})
	require.NoError(t, err)
	require.Equal(t, sandbox.Tuple{[]any{"file.txt"}, []any{"sub/../file.txt"}, "./file.txt", "file.txt", "b'./file.txt'"}, result)
	openArgs := []any{sandbox.Path("/data/file.txt"), "r"}
	require.Equal(t, []any{
		[]any{"Path.iterdir", []any{sandbox.Path("/data")}},
		[]any{"Path.iterdir", []any{sandbox.Path("/data")}},
		[]any{"open", openArgs},
		[]any{"open", openArgs},
		[]any{"open", openArgs},
	}, calls)
}

func ospCheckOsPathValidation(t *testing.T, b backend) {
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
(errors, p.exists(), p.is_file(), p.is_dir(), p.is_symlink(), os.getcwd())`, runOptions{Runtime: mustRuntime(montygo.RuntimeOptions{OS: func(_ context.Context, name string, args []any, kw host.Kwargs) (any, error) {
		calls = append(calls, []any{name, args, kw})
		return true, nil
	}}), FeedOptions: montygo.FeedOptions{Cwd: "/data", Inputs: map[string]any{"path": "bad\x00/../x"}}})
	require.NoError(t, err)
	require.Equal(t, sandbox.Tuple{
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
		_, err := run(t, b, tc.code, runOptions{FeedOptions: montygo.FeedOptions{Cwd: "/"}})
		require.Error(t, err, tc.code)
		require.Equal(t, tc.expected, err.Error(), tc.code)
	}
}
