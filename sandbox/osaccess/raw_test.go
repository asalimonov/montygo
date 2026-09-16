package osaccess_test

import (
	"context"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/sandbox/osaccess"
)

type testOS struct {
	osaccess.Base
	files       map[string][]byte
	directories map[string]bool
}

func newTestOS() *testOS {
	return &testOS{files: map[string][]byte{}, directories: map[string]bool{"/": true}}
}

func (o *testOS) ensureParentExists(p string) {
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	for i := 1; i < len(parts); i++ {
		parent := strings.Join(parts[:i], "/")
		if parent == "" {
			parent = "/"
		}
		o.directories[parent] = true
	}
}

func (o *testOS) PathExists(path sandbox.Path) (bool, error) {
	_, file := o.files[string(path)]
	return file || o.directories[string(path)], nil
}

func (o *testOS) PathIsFile(path sandbox.Path) (bool, error) {
	_, ok := o.files[string(path)]
	return ok, nil
}

func (o *testOS) PathIsDir(path sandbox.Path) (bool, error) { return o.directories[string(path)], nil }

func (o *testOS) PathIsSymlink(sandbox.Path) (bool, error) { return false, nil }

func (o *testOS) PathReadText(path sandbox.Path) (string, error) {
	data, err := o.PathReadBytes(path)
	return string(data), err
}

func (o *testOS) PathReadBytes(path sandbox.Path) ([]byte, error) {
	data, ok := o.files[string(path)]
	if !ok {
		return nil, monterr.Raise("FileNotFoundError", "No such file: "+string(path))
	}
	return data, nil
}

func (o *testOS) PathWriteText(path sandbox.Path, data string) (int, error) {
	o.ensureParentExists(string(path))
	o.files[string(path)] = []byte(data)
	return utf8.RuneCountInString(data), nil
}

func (o *testOS) PathWriteBytes(path sandbox.Path, data []byte) (int, error) {
	o.ensureParentExists(string(path))
	o.files[string(path)] = data
	return len(data), nil
}

func (o *testOS) PathAppendText(path sandbox.Path, data string) (int, error) {
	o.ensureParentExists(string(path))
	o.files[string(path)] = append(o.files[string(path)], data...)
	return utf8.RuneCountInString(data), nil
}

func (o *testOS) PathAppendBytes(path sandbox.Path, data []byte) (int, error) {
	o.ensureParentExists(string(path))
	o.files[string(path)] = append(o.files[string(path)], data...)
	return len(data), nil
}

func (o *testOS) PathMkdir(path sandbox.Path, parents, existOK bool) error {
	p := string(path)
	if o.directories[p] {
		if !existOK {
			return monterr.Raise("FileExistsError", "Directory exists: "+p)
		}
		return nil
	}
	if parents {
		o.ensureParentExists(p)
	}
	o.directories[p] = true
	return nil
}

func (o *testOS) PathUnlink(path sandbox.Path) error {
	if _, ok := o.files[string(path)]; !ok {
		return monterr.Raise("FileNotFoundError", "No such file: "+string(path))
	}
	delete(o.files, string(path))
	return nil
}

func (o *testOS) PathRmdir(path sandbox.Path) error {
	p := string(path)
	if !o.directories[p] {
		return monterr.Raise("FileNotFoundError", "No such directory: "+p)
	}
	for f := range o.files {
		if strings.HasPrefix(f, p+"/") {
			return monterr.Raise("OSError", "Directory not empty: "+p)
		}
	}
	for d := range o.directories {
		if d != p && strings.HasPrefix(d, p+"/") {
			return monterr.Raise("OSError", "Directory not empty: "+p)
		}
	}
	delete(o.directories, p)
	return nil
}

func (o *testOS) PathIterdir(path sandbox.Path) ([]sandbox.Path, error) {
	p := string(path)
	if !o.directories[p] {
		return nil, monterr.Raise("FileNotFoundError", "No such directory: "+p)
	}
	prefix := strings.TrimRight(p, "/") + "/"
	seen := map[string]bool{}
	var names []string
	collect := func(entry string) {
		if !strings.HasPrefix(entry, prefix) || entry == p {
			return
		}
		child := strings.Split(entry[len(prefix):], "/")[0]
		if child != "" && !seen[child] {
			seen[child] = true
			names = append(names, prefix+child)
		}
	}
	for f := range o.files {
		collect(f)
	}
	for d := range o.directories {
		collect(d)
	}
	sort.Strings(names)
	out := make([]sandbox.Path, len(names))
	for i, n := range names {
		out[i] = sandbox.Path(n)
	}
	return out, nil
}

func (o *testOS) PathStat(path sandbox.Path) (osaccess.StatResult, error) {
	p := string(path)
	zero := 0.0
	if data, ok := o.files[p]; ok {
		return osaccess.FileStat(int64(len(data)), 0o644, &zero), nil
	}
	if o.directories[p] {
		return osaccess.DirStat(0o755, &zero), nil
	}
	return osaccess.StatResult{}, monterr.Raise("FileNotFoundError", "No such file or directory: "+p)
}

func (o *testOS) PathRename(path, target sandbox.Path) error {
	p, t := string(path), string(target)
	if data, ok := o.files[p]; ok {
		o.ensureParentExists(t)
		delete(o.files, p)
		o.files[t] = data
		return nil
	}
	if o.directories[p] {
		o.ensureParentExists(t)
		delete(o.directories, p)
		o.directories[t] = true
		prefix := strings.TrimRight(p, "/") + "/"
		for f, data := range o.files {
			if strings.HasPrefix(f, prefix) {
				delete(o.files, f)
				o.files[t+f[len(p):]] = data
			}
		}
		return nil
	}
	return monterr.Raise("FileNotFoundError", "No such file or directory: "+p)
}

func (o *testOS) PathResolve(path sandbox.Path) (string, error) {
	var parts []string
	for _, part := range strings.Split(string(path), "/") {
		switch {
		case part == "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		case part != "" && part != ".":
			parts = append(parts, part)
		}
	}
	return "/" + strings.Join(parts, "/"), nil
}

func (o *testOS) PathAbsolute(path sandbox.Path) (string, error) {
	if strings.HasPrefix(string(path), "/") {
		return string(path), nil
	}
	return "/" + string(path), nil
}

var testEnv = map[string]string{"TEST_VAR": "test_value", "HOME": "/test/home"}

func (o *testOS) Getenv(key string, def any) (any, error) {
	if v, ok := testEnv[key]; ok {
		return v, nil
	}
	return def, nil
}

func (o *testOS) GetEnviron() (map[string]string, error) {
	return map[string]string{"TEST_VAR": "test_value", "HOME": "/test/home"}, nil
}

func (o *testOS) DateToday() (sandbox.Date, error) {
	return sandbox.Date{Year: 2024, Month: 1, Day: 15}, nil
}

func (o *testOS) DatetimeNow(tz *sandbox.TimeZone) (sandbox.DateTime, error) {
	dt := sandbox.DateTime{Year: 2024, Month: 1, Day: 15, Hour: 10, Minute: 30, Second: 5, Microsecond: 123456}
	if tz != nil {
		off := tz.OffsetSeconds
		dt.OffsetSeconds = &off
		dt.TimezoneName = tz.Name
	}
	return dt, nil
}

type partialOS struct{ *testOS }

func (partialOS) PathExists(sandbox.Path) (bool, error) { return false, osaccess.ErrNotImplemented }

type pathRecordingOS struct {
	*testOS
	readArgs []string
}

func (o *pathRecordingOS) PathOpen(path sandbox.Path, mode string) (*sandbox.FileHandle, error) {
	if _, ok := o.files[string(path)]; strings.HasPrefix(mode, "r") && !ok {
		return nil, monterr.Raise("FileNotFoundError", "No such file: "+string(path))
	}
	return sandbox.NewFileHandle(string(path), mode, 0)
}

func (o *pathRecordingOS) PathReadText(path sandbox.Path) (string, error) {
	o.readArgs = append(o.readArgs, string(path))
	return o.testOS.PathReadText(path)
}

func TestOSAccessRaw(t *testing.T) {
	montyTest(t, "test_abstract_filesystem_exists", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.files["/test.txt"] = []byte("hello")
		require.Equal(t, true, mustRun(t, b, `from pathlib import Path; Path("/test.txt").exists()`, osaccess.Handler(fs)))
	})

	montyTest(t, "test_abstract_filesystem_exists_missing", func(t *testing.T, b backend) {
		require.Equal(t, false, mustRun(t, b, `from pathlib import Path; Path("/missing.txt").exists()`, osaccess.Handler(newTestOS())))
	})

	// Python compares type name and repr; Go compares the sandbox.Date value.
	montyTest(t, "test_abstract_os_date_today", func(t *testing.T, b backend) {
		result := mustRun(t, b, `from datetime import date; date.today()`, osaccess.Handler(newTestOS()))
		require.Equal(t, sandbox.Date{Year: 2024, Month: 1, Day: 15}, result)
	})

	// Python compares repr "datetime.datetime(2024, 1, 15, 10, 30, 5, 123456, tzinfo=datetime.timezone.utc)"; Go compares fields.
	montyTest(t, "test_abstract_os_datetime_now_with_timezone", func(t *testing.T, b backend) {
		result := mustRun(t, b, `from datetime import datetime, timezone; datetime.now(timezone.utc)`, osaccess.Handler(newTestOS()))
		dt, ok := result.(sandbox.DateTime)
		require.True(t, ok, "got %T", result)
		require.NotNil(t, dt.OffsetSeconds)
		require.Equal(t, int32(0), *dt.OffsetSeconds)
		dt.OffsetSeconds = nil
		dt.TimezoneName = nil
		require.Equal(t, sandbox.DateTime{Year: 2024, Month: 1, Day: 15, Hour: 10, Minute: 30, Second: 5, Microsecond: 123456}, dt)
	})

	t.Run("test_abstract_os_dispatch", func(t *testing.T) {
		fs := newTestOS()
		fs.files["/test.txt"] = []byte("hello")
		result, err := osaccess.Dispatch(context.Background(), fs, "Path.read_text", []any{sandbox.Path("/test.txt")}, host.Kwargs{})
		require.NoError(t, err)
		require.Equal(t, "hello", result)
	})

	t.Run("test_abstract_os_dispatch_not_handled", func(t *testing.T) {
		fs := partialOS{newTestOS()}
		result, err := osaccess.Handler(fs)(context.Background(), "Path.exists", []any{sandbox.Path("/tmp")}, host.Kwargs{})
		require.NoError(t, err)
		require.Same(t, host.NotHandled, result)
	})

	montyTest(t, "test_abstract_os_dispatch_not_handled_falls_back_in_run", func(t *testing.T, b backend) {
		fs := newTestOS()
		handler := func(ctx context.Context, name string, args []any, kwargs host.Kwargs) (any, error) {
			if name == "Path.exists" {
				return host.NotHandled, nil
			}
			return osaccess.Dispatch(ctx, fs, name, args, kwargs)
		}
		code := `
from pathlib import Path
message = None
try:
    Path('/tmp').exists()
except PermissionError as exc:
    message = str(exc)
message
`
		require.Equal(t, "Permission denied: '/tmp'", mustRun(t, b, code, handler))
	})

	montyTest(t, "test_abstract_filesystem_is_file", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.files["/file.txt"] = []byte("content")
		fs.directories["/mydir"] = true
		code := `
from pathlib import Path
(Path('/file.txt').is_file(), Path('/mydir').is_file())
`
		require.Equal(t, sandbox.Tuple{true, false}, mustRun(t, b, code, osaccess.Handler(fs)))
	})

	montyTest(t, "test_abstract_filesystem_is_dir", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.files["/file.txt"] = []byte("content")
		fs.directories["/mydir"] = true
		code := `
from pathlib import Path
(Path('/file.txt').is_dir(), Path('/mydir').is_dir())
`
		require.Equal(t, sandbox.Tuple{false, true}, mustRun(t, b, code, osaccess.Handler(fs)))
	})

	montyTest(t, "test_abstract_filesystem_read_text", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.files["/hello.txt"] = []byte("Hello, World!")
		require.Equal(t, "Hello, World!", mustRun(t, b, `from pathlib import Path; Path("/hello.txt").read_text()`, osaccess.Handler(fs)))
	})

	// Python round-trips exception() to a FileNotFoundError instance; Go checks the exception info.
	montyTest(t, "test_abstract_filesystem_read_text_missing", func(t *testing.T, b backend) {
		_, err := run(t, b, `from pathlib import Path; Path("/missing.txt").read_text()`, osaccess.Handler(newTestOS()))
		rte := requireRuntimeError(t, err, "FileNotFoundError: No such file: /missing.txt")
		info := rte.Exception()
		require.Equal(t, "FileNotFoundError", info.TypeName)
		require.True(t, monterr.IsSubclass(info.TypeName, "OSError"))
	})

	montyTest(t, "test_abstract_filesystem_read_bytes", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.files["/data.bin"] = []byte{0, 1, 2, 3}
		require.Equal(t, []byte{0, 1, 2, 3}, mustRun(t, b, `from pathlib import Path; Path("/data.bin").read_bytes()`, osaccess.Handler(fs)))
	})

	// Python records type(path) is PurePosixPath; Go records the raw handler argument type (sandbox.Path, never a handle).
	montyTest(t, "test_abstract_filesystem_open_passes_path_to_read_handler", func(t *testing.T, b backend) {
		fs := &pathRecordingOS{testOS: newTestOS()}
		fs.files["/hello.txt"] = []byte("hi")
		var rawArgs []any
		handler := func(ctx context.Context, name string, args []any, kwargs host.Kwargs) (any, error) {
			if name == "Path.read_text" {
				rawArgs = append(rawArgs, args[0])
			}
			return osaccess.Dispatch(ctx, fs, name, args, kwargs)
		}
		code := `
f = open('/hello.txt')
data = f.read()
f.close()
data
`
		require.Equal(t, "hi", mustRun(t, b, code, handler))
		require.Equal(t, []string{"/hello.txt"}, fs.readArgs)
		require.Equal(t, []any{sandbox.Path("/hello.txt")}, rawArgs)

		fs.readArgs, rawArgs = nil, nil
		require.Equal(t, "hi", mustRun(t, b, `from pathlib import Path; Path("/hello.txt").read_text()`, handler))
		require.Equal(t, []string{"/hello.txt"}, fs.readArgs)
		require.Equal(t, []any{sandbox.Path("/hello.txt")}, rawArgs)
	})

	montyTest(t, "test_abstract_filesystem_stat_file", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.files["/file.txt"] = []byte("hello world")
		code := `
from pathlib import Path
s = Path('/file.txt').stat()
(s.st_size, s.st_mode)
`
		require.Equal(t, sandbox.Tuple{int64(11), int64(0o100644)}, mustRun(t, b, code, osaccess.Handler(fs)))
	})

	montyTest(t, "test_abstract_filesystem_stat_directory", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.directories["/mydir"] = true
		code := `
from pathlib import Path
s = Path('/mydir').stat()
s.st_mode
`
		require.Equal(t, int64(0o040755), mustRun(t, b, code, osaccess.Handler(fs)))
	})

	montyTest(t, "test_abstract_filesystem_stat_missing", func(t *testing.T, b backend) {
		_, err := run(t, b, "from pathlib import Path\nPath(\"/missing\").stat()", osaccess.Handler(newTestOS()))
		rte := requireRuntimeError(t, err, "FileNotFoundError: No such file or directory: /missing")
		require.Equal(t, `Traceback (most recent call last):
  File "<python-input-0>", line 2, in <module>
    Path("/missing").stat()
    ~~~~~~~~~~~~~~~~~~~~~~~
FileNotFoundError: No such file or directory: /missing`, rte.Display(monterr.DisplayTraceback))
	})

	montyTest(t, "test_abstract_filesystem_iterdir", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.directories["/mydir"] = true
		fs.files["/mydir/a.txt"] = []byte("a")
		fs.files["/mydir/b.txt"] = []byte("b")
		fs.directories["/mydir/subdir"] = true
		code := `
from pathlib import Path
list(Path('/mydir').iterdir())
`
		result, ok := mustRun(t, b, code, osaccess.Handler(fs)).([]any)
		require.True(t, ok)
		require.Len(t, result, 3)
		names := make([]string, len(result))
		for i, p := range result {
			names[i] = string(p.(sandbox.Path))
		}
		sort.Strings(names)
		require.Equal(t, []string{"/mydir/a.txt", "/mydir/b.txt", "/mydir/subdir"}, names)
	})

	montyTest(t, "test_abstract_filesystem_iterdir_empty", func(t *testing.T, b backend) {
		fs := newTestOS()
		fs.directories["/empty"] = true
		code := `
from pathlib import Path
list(Path('/empty').iterdir())
`
		require.Equal(t, []any{}, mustRun(t, b, code, osaccess.Handler(fs)))
	})

	montyTest(t, "test_abstract_filesystem_resolve", func(t *testing.T, b backend) {
		code := `
from pathlib import Path
str(Path('/foo/bar/../baz').resolve())
`
		require.Equal(t, "/foo/baz", mustRun(t, b, code, osaccess.Handler(newTestOS())))
	})

	montyTest(t, "test_abstract_filesystem_absolute", func(t *testing.T, b backend) {
		code := `
from pathlib import Path
str(Path('/already/absolute').absolute())
`
		require.Equal(t, "/already/absolute", mustRun(t, b, code, osaccess.Handler(newTestOS())))
	})

	montyTest(t, "test_abstract_filesystem_getenv", func(t *testing.T, b backend) {
		code := `
import os
os.getenv('TEST_VAR')
`
		require.Equal(t, "test_value", mustRun(t, b, code, osaccess.Handler(newTestOS())))
	})

	montyTest(t, "test_abstract_filesystem_getenv_missing", func(t *testing.T, b backend) {
		code := `
import os
os.getenv('NONEXISTENT')
`
		require.Nil(t, mustRun(t, b, code, osaccess.Handler(newTestOS())))
	})

	montyTest(t, "test_abstract_filesystem_getenv_default", func(t *testing.T, b backend) {
		code := `
import os
os.getenv('NONEXISTENT', 'my_default')
`
		require.Equal(t, "my_default", mustRun(t, b, code, osaccess.Handler(newTestOS())))
	})

	// Python asserts type(result) is PurePosixPath; Go asserts sandbox.Path.
	montyTest(t, "test_path_monty_to_py", func(t *testing.T, b backend) {
		result := mustRun(t, b, `from pathlib import Path; Path("/foo/bar/thing.txt")`, nil)
		require.IsType(t, sandbox.Path(""), result)
		require.Equal(t, sandbox.Path("/foo/bar/thing.txt"), result)
	})

	// Python passes a PurePosixPath input; Go passes sandbox.Path.
	montyTest(t, "test_path_py_to_monty", func(t *testing.T, b backend) {
		result, err := runFeed(t, b, nil, `f"type={type(p)} {p=}"`, &montygo.FeedOptions{Inputs: map[string]any{"p": sandbox.Path("/foo/bar/thing.txt")}})
		require.NoError(t, err)
		require.Equal(t, "type=<class 'PosixPath'> p=PosixPath('/foo/bar/thing.txt')", result)
	})

	t.Run("go_dispatch_unknown_name_and_base_not_handled", func(t *testing.T) {
		ctx := context.Background()
		result, err := osaccess.Dispatch(ctx, newTestOS(), "os.unknown", nil, nil)
		require.NoError(t, err)
		require.Same(t, host.NotHandled, result)
		result, err = osaccess.Handler(osaccess.Base{})(ctx, "open", []any{sandbox.Path("/x"), "r"}, nil)
		require.NoError(t, err)
		require.Same(t, host.NotHandled, result)
	})
}

func TestStatHelpers(t *testing.T) {
	// Python indexes the tuple; Go reads the NamedTuple values at the same indexes.
	t.Run("test_file_stat_helper", func(t *testing.T) {
		mtime := 1700000000.0
		stat := osaccess.FileStat(1024, 0o644, &mtime).NamedTuple()
		require.Len(t, stat.Values, 10)
		require.Equal(t, int64(0o100644), stat.Values[0])
		require.Equal(t, int64(1024), stat.Values[6])
		require.Equal(t, 1700000000.0, stat.Values[8])
	})

	t.Run("test_dir_stat_helper", func(t *testing.T) {
		mtime := 1700000000.0
		stat := osaccess.DirStat(0o755, &mtime).NamedTuple()
		require.Len(t, stat.Values, 10)
		require.Equal(t, int64(0o040755), stat.Values[0])
		require.Equal(t, int64(4096), stat.Values[6])
		require.Equal(t, 1700000000.0, stat.Values[8])
	})

	t.Run("go_stat_defaults_and_named_tuple", func(t *testing.T) {
		require.Equal(t, int64(0o100644), osaccess.FileStat(1, 0, nil).StMode)
		require.Equal(t, int64(0o100755), osaccess.FileStat(1, 0o100755, nil).StMode)
		dir := osaccess.DirStat(0, nil)
		require.Equal(t, osaccess.StatResult{StMode: 0o040755, StNlink: 2, StSize: 4096, StAtime: dir.StAtime, StMtime: dir.StAtime, StCtime: dir.StAtime}, dir)
		nt := dir.NamedTuple()
		require.Equal(t, "StatResult", nt.TypeName)
		require.Equal(t, []string{"st_mode", "st_ino", "st_dev", "st_nlink", "st_uid", "st_gid", "st_size", "st_atime", "st_mtime", "st_ctime"}, nt.FieldNames)
	})
}
