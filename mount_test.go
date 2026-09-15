package monty_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

type mntTestDir struct {
	t   *testing.T
	dir string
}

func mntCreateTestDir(t *testing.T) *mntTestDir {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data.bin"), []byte{0x00, 0x01, 0x02}, 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "subdir", "nested.txt"), []byte("nested content"), 0o644))
	return &mntTestDir{t: t, dir: dir}
}

func (d *mntTestDir) open(opts monty.MountDirOptions) (*monty.MountDir, error) {
	opts.HostPath = d.dir
	m, err := monty.NewMountDir(opts)
	if err == nil {
		d.t.Cleanup(func() { _ = m.Close() })
	}
	return m, err
}

func (d *mntTestDir) mount(opts monty.MountDirOptions) *monty.MountDir {
	d.t.Helper()
	m, err := d.open(opts)
	require.NoError(d.t, err)
	return m
}

func mntOpts(mounts ...*monty.MountDir) runOptions {
	return runOptions{FeedOptions: monty.FeedOptions{Mount: mounts}}
}

func mntFeed(mounts ...*monty.MountDir) *monty.FeedOptions {
	return &monty.FeedOptions{Mount: mounts}
}

func mntU64(v uint64) *uint64 { return &v }

func mntPathArg(v any) string {
	switch x := v.(type) {
	case monty.Path:
		return string(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func mntRequireRuntimeError(t *testing.T, err error, message string) {
	t.Helper()
	var re *monty.RuntimeError
	require.ErrorAs(t, err, &re)
	require.Equal(t, message, err.Error())
}

func mntRequireValueError(t *testing.T, err error) string {
	t.Helper()
	var ve *monty.ValueError
	require.ErrorAs(t, err, &ve)
	return ve.Error()
}

func mntExportedFields(v any) []string {
	rt := reflect.TypeOf(v)
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	var names []string
	for i := range rt.NumField() {
		if rt.Field(i).IsExported() {
			names = append(names, rt.Field(i).Name)
		}
	}
	return names
}

type mntCall struct {
	name string
	args []any
}

func mntWriteFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func mntReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func mntExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestMount(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("browser wasm reports mounts as unsupported", func(t *testing.T) {
			t.Skip("Go wasm backend services mounts host-side")
		})

		t.Run("a mount follows its directory across feeds, not its path", func(t *testing.T) {
			base := t.TempDir()
			outside := t.TempDir()
			shared := filepath.Join(base, "shared")
			require.NoError(t, os.Mkdir(shared, 0o755))
			mntWriteFile(t, filepath.Join(shared, "inside.txt"), "in-mount")
			mntWriteFile(t, filepath.Join(outside, "secret.txt"), "HOST SECRET")
			if err := os.Symlink(outside, filepath.Join(base, "prepared-link")); err != nil {
				t.Skipf("host forbids symlink creation: %v", err)
			}

			child, err := monty.NewMountDir(monty.MountDirOptions{HostPath: shared, VirtualPath: "/child", Mode: monty.MountReadOnly})
			require.NoError(t, err)
			defer child.Close()
			parent, err := monty.NewMountDir(monty.MountDirOptions{HostPath: base, VirtualPath: "/parent", Mode: monty.MountReadWrite})
			require.NoError(t, err)
			defer parent.Close()
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)

			_, err = s.FeedRun(ctx, `from pathlib import Path
Path('/parent/shared').rename('/parent/old-shared')
Path('/parent/prepared-link').rename('/parent/shared')`, mntFeed(parent))
			if err != nil && runtime.GOOS == "windows" {
				t.Skip("Windows refuses to rename a directory a mount holds open")
			}
			require.NoError(t, err)

			result, err := s.FeedRun(ctx, `from pathlib import Path
f"{Path('/child/inside.txt').read_text()}:{Path('/child/secret.txt').exists()}"`, mntFeed(child))
			require.NoError(t, err)
			require.Equal(t, "in-mount:False", result)
		})

		t.Run("cwd defaults to the root without mounts", func(t *testing.T) {
			require.Equal(t, monty.Tuple{"/", "/main.py"}, mustRun(t, b, "import os\n(os.getcwd(), __file__)", runOptions{}))
			require.Equal(t, "/work", mustRun(t, b, "import os\nos.getcwd()", runOptions{FeedOptions: monty.FeedOptions{Cwd: "/work/"}}))
		})

		t.Run("NUL paths never reach callbacks and no-handler errors use clean paths", func(t *testing.T) {
			ospCheckOsPathValidation(t, b)
		})

		t.Run("filesystem results preserve relative paths", func(t *testing.T) {
			ospCheckRelativePathResults(t, b)
		})

		for _, cwd := range []string{"/", "/data"} {
			t.Run(fmt.Sprintf("os callbacks receive normalized paths with cwd %s", cwd), func(t *testing.T) {
				var calls []mntCall
				result, err := run(t, b, `import os
from pathlib import Path
Path('sub/../file.txt').exists()
Path('/other//sub/../file.txt').exists()
os.listdir()
os.rename('./sub/../src', '../dst')
open('./sub//../file.txt').read()`, runOptions{FeedOptions: monty.FeedOptions{
					Cwd: cwd,
					OS: func(_ context.Context, name string, args []any, _ monty.Kwargs) (any, error) {
						calls = append(calls, mntCall{name, args})
						switch name {
						case "Path.iterdir":
							return []any{}, nil
						case "open":
							h, err := monty.NewFileHandle(mntPathArg(args[0]), "r", 0)
							return h, err
						case "Path.read_text":
							return "hello", nil
						}
						return true, nil
					},
				}})
				require.NoError(t, err)
				require.Equal(t, "hello", result)
				prefix := cwd
				if cwd == "/" {
					prefix = ""
				}
				require.Equal(t, []mntCall{
					{"Path.exists", []any{monty.Path(prefix + "/file.txt")}},
					{"Path.exists", []any{monty.Path("/other/file.txt")}},
					{"Path.iterdir", []any{monty.Path(cwd)}},
					{"Path.rename", []any{monty.Path(prefix + "/src"), monty.Path("/dst")}},
					{"open", []any{monty.Path(prefix + "/file.txt"), "r"}},
					{"Path.read_text", []any{monty.Path(prefix + "/file.txt")}},
				}, calls)
			})
		}

		t.Run("cwd defaults to the first mount and persists across feeds", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			feed := func(code string, opts *monty.FeedOptions) any {
				t.Helper()
				v, err := s.FeedRun(ctx, code, opts)
				require.NoError(t, err)
				return v
			}
			require.Equal(t, monty.Tuple{"/data", "/data/main.py", "hello world"},
				feed("import os\n(os.getcwd(), __file__, open('hello.txt').read())", mntFeed(md)))
			require.Equal(t, "/data/subdir", feed("os.chdir('subdir')\nos.getcwd()", mntFeed(md)))
			require.Equal(t, "/data/subdir", feed("os.getcwd()", mntFeed(md)))
			require.Equal(t, "/data/subdir", feed("os.getcwd()", nil))
			require.Equal(t, "nested content", feed("open('nested.txt').read()", &monty.FeedOptions{Mount: []*monty.MountDir{md}, Cwd: "/data/subdir"}))
			require.Equal(t, "/data", feed("os.getcwd()", &monty.FeedOptions{Mount: []*monty.MountDir{md}, Cwd: "/data"}))
		})

		t.Run("an explicit cwd persists across feeds", func(t *testing.T) {
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			v, err := s.FeedRun(ctx, "import os\nos.getcwd()", &monty.FeedOptions{Cwd: "/work"})
			require.NoError(t, err)
			require.Equal(t, "/work", v)
			v, err = s.FeedRun(ctx, "os.getcwd()", nil)
			require.NoError(t, err)
			require.Equal(t, "/work", v)
			v, err = s.FeedRun(ctx, "os.getcwd()", &monty.FeedOptions{Cwd: "/"})
			require.NoError(t, err)
			require.Equal(t, "/", v)
		})

		t.Run("an invalid cwd is refused", func(t *testing.T) {
			for _, c := range []struct{ cwd, message string }{
				{"data", `cwd must be an absolute POSIX path: "data"`},
				{"/data\x00", `cwd must not contain NUL bytes: "/data\0"`},
			} {
				_, err := run(t, b, "1", runOptions{FeedOptions: monty.FeedOptions{Cwd: c.cwd}})
				mntRequireRuntimeError(t, err, "ValueError: "+c.message)
			}
			// TS rejects cwd '' but Go's zero Cwd means "not set", so it keeps the default.
			require.Equal(t, "/", mustRun(t, b, "import os\nos.getcwd()", runOptions{FeedOptions: monty.FeedOptions{Cwd: ""}}))

			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			v, err := s.FeedRun(ctx, "import os\nos.getcwd()", &monty.FeedOptions{Cwd: "/work//"})
			require.NoError(t, err)
			require.Equal(t, "/work", v)
			v, err = s.FeedRun(ctx, "os.getcwd()", &monty.FeedOptions{Cwd: "///"})
			require.NoError(t, err)
			require.Equal(t, "/", v)
		})

		t.Run("MountDir repr", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			require.Equal(t, fmt.Sprintf("MountDir(host_path='%s', virtual_path='/data', mode='read-only')", d.dir), md.String())
		})

		t.Run("MountDir invalid mode", func(t *testing.T) {
			d := mntCreateTestDir(t)
			_, err := d.open(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountMode("invalid")})
			require.Equal(t, "invalid mount mode: 'invalid'. Expected 'read-only', 'read-write' or 'overlay'", mntRequireValueError(t, err))
		})

		t.Run("MountDir attributes", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			require.Equal(t, "/data", md.VirtualPath)
			require.Equal(t, d.dir, md.HostPath)
			require.Equal(t, monty.MountReadOnly, md.Mode)
			require.Equal(t, uint64(100_000_000), md.MemoryUsageLimit)

			limited := d.mount(monty.MountDirOptions{VirtualPath: "/limited", MemoryUsageLimit: mntU64(1234)})
			require.Equal(t, uint64(1234), limited.MemoryUsageLimit)

			fields := mntExportedFields(md)
			sort.Strings(fields)
			require.Equal(t, []string{"HostPath", "MemoryUsageLimit", "Mode", "VirtualPath", "WriteBytesLimit"}, fields)
		})

		t.Run("MountDir nonexistent host path", func(t *testing.T) {
			_, err := monty.NewMountDir(monty.MountDirOptions{HostPath: "/nonexistent/path/that/does/not/exist", VirtualPath: "/data"})
			message := mntRequireValueError(t, err)
			require.True(t, strings.HasPrefix(message, "cannot open host path '/nonexistent/path/that/does/not/exist':"), message)
		})

		t.Run("MountDir non-absolute virtual path", func(t *testing.T) {
			d := mntCreateTestDir(t)
			_, err := d.open(monty.MountDirOptions{VirtualPath: "relative"})
			require.Equal(t, "virtual path must be absolute, got: 'relative'", mntRequireValueError(t, err))
		})

		t.Run("closing a mount releases it and later feeds are refused", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			require.Equal(t, "hello world", mustRun(t, b, "open('/data/hello.txt').read()", mntOpts(md)))

			require.NoError(t, md.Close())
			require.NoError(t, md.Close())
			_, err := run(t, b, "open('/data/hello.txt').read()", mntOpts(md))
			var oe *monty.OptionError
			require.ErrorAs(t, err, &oe)
			require.Equal(t, "mount is closed: create a new MountDir", oe.Error())
			require.Equal(t, "/data", md.VirtualPath)

			var disposed *monty.MountDir
			func() {
				scoped := d.mount(monty.MountDirOptions{VirtualPath: "/scoped", Mode: monty.MountReadOnly})
				defer scoped.Close()
				require.Equal(t, "hello world", mustRun(t, b, "open('/scoped/hello.txt').read()", mntOpts(scoped)))
				disposed = scoped
			}()
			_, err = run(t, b, "open('/scoped/hello.txt').read()", mntOpts(disposed))
			require.Error(t, err)
		})

		t.Run("MountDir default mode is overlay", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data"})
			require.Equal(t, monty.MountOverlay, md.Mode)
		})

		t.Run("MountDir write_bytes_limit", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", WriteBytesLimit: mntU64(1024)})
			require.NotNil(t, md.WriteBytesLimit)
			require.Equal(t, uint64(1024), *md.WriteBytesLimit)

			md2 := d.mount(monty.MountDirOptions{VirtualPath: "/data"})
			require.Nil(t, md2.WriteBytesLimit)
		})

		t.Run("MontyFileHandle exposes canonical file properties", func(t *testing.T) {
			handle, err := monty.NewFileHandle("/data/message.bin", "br", 12)
			require.NoError(t, err)
			require.Equal(t, []string{"Path", "Mode", "Position"}, mntExportedFields(handle))
			require.Equal(t, "/data/message.bin", handle.Path)
			require.Equal(t, "rb", handle.Mode)
			require.Equal(t, uint64(12), handle.Position)
			require.True(t, handle.Binary())
			require.True(t, handle.Readable())
			require.False(t, handle.Writable())
			// Object.isFrozen has no Go counterpart: handles are plain values.
		})

		t.Run("os callback file handles support text and binary reads", func(t *testing.T) {
			var calls []mntCall
			osCallback := func(_ context.Context, name string, args []any, _ monty.Kwargs) (any, error) {
				calls = append(calls, mntCall{name, args})
				path := mntPathArg(args[0])
				switch name {
				case "open":
					h, err := monty.NewFileHandle(path, args[1].(string), 0)
					return h, err
				case "Path.read_text":
					return "hello", nil
				case "Path.read_bytes":
					return []byte{0, 1, 2}, nil
				}
				return nil, fmt.Errorf("unexpected OS call: %s", name)
			}
			opts := runOptions{FeedOptions: monty.FeedOptions{OS: osCallback}}

			require.Equal(t, "hello", mustRun(t, b, "open('/data/message.txt').read()", opts))
			require.Equal(t, []byte{0, 1, 2}, mustRun(t, b, "open('/data/data.bin', 'rb').read()", opts))
			require.Equal(t, []mntCall{
				{"open", []any{monty.Path("/data/message.txt"), "r"}},
				{"Path.read_text", []any{monty.Path("/data/message.txt")}},
				{"open", []any{monty.Path("/data/data.bin"), "rb"}},
				{"Path.read_bytes", []any{monty.Path("/data/data.bin")}},
			}, calls)
		})

		t.Run("file handles round-trip with canonical modes and positions", func(t *testing.T) {
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			v, err := s.FeedRun(ctx, "open('/data/message.txt')", &monty.FeedOptions{
				OS: func(_ context.Context, _ string, args []any, _ monty.Kwargs) (any, error) {
					h, err := monty.NewFileHandle(mntPathArg(args[0]), "tr", 42)
					return h, err
				},
			})
			require.NoError(t, err)
			require.IsType(t, &monty.FileHandle{}, v)
			returned := v.(*monty.FileHandle)
			want, err := monty.NewFileHandle("/data/message.txt", "r", 42)
			require.NoError(t, err)
			require.Equal(t, want, returned)
			require.False(t, returned.Binary())
			require.True(t, returned.Readable())
			require.False(t, returned.Writable())

			returnedAgain, err := s.FeedRun(ctx, "open('/data/message.txt')", &monty.FeedOptions{
				OS: func(context.Context, string, []any, monty.Kwargs) (any, error) { return returned, nil },
			})
			require.NoError(t, err)
			require.Equal(t, returned, returnedAgain)

			defaultPosition, err := s.FeedRun(ctx, "open('/data/default.txt')", &monty.FeedOptions{
				OS: func(_ context.Context, _ string, args []any, _ monty.Kwargs) (any, error) {
					h, err := monty.NewFileHandle(mntPathArg(args[0]), "r", 0)
					return h, err
				},
			})
			require.NoError(t, err)
			wantDefault, err := monty.NewFileHandle("/data/default.txt", "r", 0)
			require.NoError(t, err)
			require.Equal(t, wantDefault, defaultPosition)
		})

		t.Run("MontyFileHandle rejects invalid arguments", func(t *testing.T) {
			// Non-string path/mode and negative, fractional, infinite or string
			// positions are unrepresentable in Go's static types.
			for _, c := range []struct {
				mode     string
				position uint64
				expected string
			}{
				{"q", 0, "invalid mode: 'q'"},
				{"b", 0, "Must have exactly one of create/read/write/append mode and at most one plus"},
				{"r", 1 << 53, "MontyFileHandle position must be a non-negative safe integer"},
			} {
				_, err := monty.NewFileHandle("/x", c.mode, c.position)
				require.Equal(t, c.expected, mntRequireValueError(t, err))
			}
		})

		t.Run("read_text via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			require.Equal(t, "hello world", mustRun(t, b, "from pathlib import Path; Path('/data/hello.txt').read_text()", mntOpts(md)))
		})

		t.Run("read_bytes via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			require.Equal(t, []byte{0x00, 0x01, 0x02}, mustRun(t, b, "from pathlib import Path; Path('/data/data.bin').read_bytes()", mntOpts(md)))
		})

		t.Run("path exists via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			code := `
from pathlib import Path
exists_file = Path('/data/hello.txt').exists()
exists_dir = Path('/data/subdir').exists()
exists_missing = Path('/data/nope.txt').exists()
[exists_file, exists_dir, exists_missing]
`
			require.Equal(t, []any{true, true, false}, mustRun(t, b, code, mntOpts(md)))
		})

		t.Run("is_file and is_dir via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			code := `
from pathlib import Path
[Path('/data/hello.txt').is_file(), Path('/data/hello.txt').is_dir(),
 Path('/data/subdir').is_file(), Path('/data/subdir').is_dir()]
`
			require.Equal(t, []any{true, false, false, true}, mustRun(t, b, code, mntOpts(md)))
		})

		t.Run("iterdir via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			code := `
from pathlib import Path
sorted([p.name for p in Path('/data').iterdir()])
`
			require.Equal(t, []any{"data.bin", "hello.txt", "subdir"}, mustRun(t, b, code, mntOpts(md)))
		})

		t.Run("stat via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			code := `
from pathlib import Path
s = Path('/data/hello.txt').stat()
s.st_size
`
			require.Equal(t, int64(11), mustRun(t, b, code, mntOpts(md)))
		})

		t.Run("read nested file via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			require.Equal(t, "nested content", mustRun(t, b, "from pathlib import Path; Path('/data/subdir/nested.txt').read_text()", mntOpts(md)))
		})

		t.Run("write blocked on read-only mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			_, err := run(t, b, "from pathlib import Path; Path('/data/new.txt').write_text('x')", mntOpts(md))
			mntRequireRuntimeError(t, err, "PermissionError: [Errno 30] Read-only file system: '/data/new.txt'")
		})

		t.Run("write succeeds on read-write mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadWrite})
			code := `
from pathlib import Path
Path('/data/new.txt').write_text('written by monty')
Path('/data/new.txt').read_text()
`
			require.Equal(t, "written by monty", mustRun(t, b, code, mntOpts(md)))
			require.Equal(t, "written by monty", mntReadFile(t, filepath.Join(d.dir, "new.txt")))
		})

		t.Run("overlay write does not modify host", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			code := `
from pathlib import Path
Path('/data/overlay_file.txt').write_text('overlay content')
Path('/data/overlay_file.txt').read_text()
`
			require.Equal(t, "overlay content", mustRun(t, b, code, mntOpts(md)))
			require.False(t, mntExists(filepath.Join(d.dir, "overlay_file.txt")))
		})

		t.Run("overlay read falls through to host", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			require.Equal(t, "hello world", mustRun(t, b, "from pathlib import Path; Path('/data/hello.txt').read_text()", mntOpts(md)))
		})

		t.Run("overlay writes do not persist across runs", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			mustRun(t, b, "from pathlib import Path; Path('/data/persistent.txt').write_text('run1')", mntOpts(md))
			_, err := run(t, b, "from pathlib import Path; Path('/data/persistent.txt').read_text()", mntOpts(md))
			mntRequireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/data/persistent.txt'")
		})

		t.Run("overlay memory usage limit is aggregate", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay, MemoryUsageLimit: mntU64(1000)})
			code := `
from pathlib import Path
p = Path('/data/retained.bin')
p.write_bytes(b'a' * 500)
p.read_bytes()
`
			_, err := run(t, b, code, mntOpts(md))
			mntRequireRuntimeError(t, err, "MemoryError: mount memory usage limit of 1 KB exceeded")
		})

		t.Run("mkdir and rmdir via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			code := `
from pathlib import Path
Path('/data/newdir').mkdir()
exists = Path('/data/newdir').is_dir()
Path('/data/newdir').rmdir()
after = Path('/data/newdir').exists()
[exists, after]
`
			require.Equal(t, []any{true, false}, mustRun(t, b, code, mntOpts(md)))
		})

		t.Run("unlink via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			code := `
from pathlib import Path
Path('/data/hello.txt').unlink()
Path('/data/hello.txt').exists()
`
			require.Equal(t, false, mustRun(t, b, code, mntOpts(md)))
			require.True(t, mntExists(filepath.Join(d.dir, "hello.txt")))
		})

		t.Run("rename via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			code := `
from pathlib import Path
Path('/data/hello.txt').rename('/data/renamed.txt')
[Path('/data/hello.txt').exists(), Path('/data/renamed.txt').read_text()]
`
			require.Equal(t, []any{false, "hello world"}, mustRun(t, b, code, mntOpts(md)))
		})

		t.Run("resolve via mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			require.Equal(t, "/data/hello.txt", mustRun(t, b, "from pathlib import Path; str(Path('/data/subdir/../hello.txt').resolve())", mntOpts(md)))
		})

		t.Run("path traversal blocked", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			_, err := run(t, b, "from pathlib import Path; Path('/data/../../etc/passwd').read_text()", mntOpts(md))
			mntRequireRuntimeError(t, err, "PermissionError: Permission denied: '/etc/passwd'")
		})

		t.Run("unmounted path denied", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			_, err := run(t, b, "from pathlib import Path; Path('/other/file.txt').exists()", mntOpts(md))
			mntRequireRuntimeError(t, err, "PermissionError: Permission denied: '/other/file.txt'")
		})

		t.Run("non-filesystem os call without fallback", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			_, err := run(t, b, "import os; os.getenv('PATH')", mntOpts(md))
			mntRequireRuntimeError(t, err, "RuntimeError: 'os.getenv' is not supported in this environment")
		})

		t.Run("multiple mounts with different modes", func(t *testing.T) {
			d := mntCreateTestDir(t)
			dir2 := t.TempDir()
			mntWriteFile(t, filepath.Join(dir2, "file2.txt"), "from mount2")
			writable, err := monty.NewMountDir(monty.MountDirOptions{HostPath: dir2, VirtualPath: "/rw", Mode: monty.MountReadWrite})
			require.NoError(t, err)
			defer writable.Close()
			mounts := []*monty.MountDir{d.mount(monty.MountDirOptions{VirtualPath: "/ro", Mode: monty.MountReadOnly}), writable}
			code := `
from pathlib import Path
a = Path('/ro/hello.txt').read_text()
b = Path('/rw/file2.txt').read_text()
[a, b]
`
			require.Equal(t, []any{"hello world", "from mount2"}, mustRun(t, b, code, mntOpts(mounts...)))
		})

		t.Run("mount works with external functions", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			code := `
from pathlib import Path
content = Path('/data/hello.txt').read_text()
result = get_prefix()
result + content
`
			opts := mntOpts(md)
			opts.ExternalLookup = map[string]any{
				"get_prefix": monty.FunctionFunc(func(context.Context, []any, monty.Kwargs) (any, error) { return "PREFIX: ", nil }),
			}
			require.Equal(t, "PREFIX: hello world", mustRun(t, b, code, opts))
		})

		t.Run("session feed with mount read", func(t *testing.T) {
			d := mntCreateTestDir(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			_, err := s.FeedRun(ctx, "from pathlib import Path", mntFeed(md))
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "Path('/data/hello.txt').read_text()", mntFeed(md))
			require.NoError(t, err)
			require.Equal(t, "hello world", v)
		})

		t.Run("session overlay write is discarded between feeds", func(t *testing.T) {
			d := mntCreateTestDir(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			_, err := s.FeedRun(ctx, "from pathlib import Path", mntFeed(md))
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "Path('/data/new.txt').write_text('from repl')", mntFeed(md))
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "Path('/data/new.txt').read_text()", mntFeed(md))
			mntRequireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/data/new.txt'")
			require.False(t, mntExists(filepath.Join(d.dir, "new.txt")))
		})

		t.Run("session overlay overwrite reverts between feeds", func(t *testing.T) {
			d := mntCreateTestDir(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			_, err := s.FeedRun(ctx, "from pathlib import Path", mntFeed(md))
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "Path('/data/hello.txt').write_text('version1')", mntFeed(md))
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "Path('/data/hello.txt').read_text()", mntFeed(md))
			require.NoError(t, err)
			require.Equal(t, "hello world", v)
			require.Equal(t, "hello world", mntReadFile(t, filepath.Join(d.dir, "hello.txt")))
		})

		t.Run("session overlay delete reverts between feeds", func(t *testing.T) {
			d := mntCreateTestDir(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			_, err := s.FeedRun(ctx, "from pathlib import Path", mntFeed(md))
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "Path('/data/hello.txt').unlink()", mntFeed(md))
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "Path('/data/hello.txt').exists()", mntFeed(md))
			require.NoError(t, err)
			require.Equal(t, true, v)
			require.True(t, mntExists(filepath.Join(d.dir, "hello.txt")))
		})

		t.Run("overlay mkdir and nested write within one feed", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			code := `
from pathlib import Path
Path('/data/mydir').mkdir()
Path('/data/mydir/file.txt').write_text('nested')
Path('/data/mydir/file.txt').read_text()
`
			require.Equal(t, "nested", mustRun(t, b, code, mntOpts(md)))
			require.False(t, mntExists(filepath.Join(d.dir, "mydir")))
		})

		t.Run("overlay iterdir sees overlay files", func(t *testing.T) {
			d := mntCreateTestDir(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountOverlay})
			code := `
from pathlib import Path
Path('/data/extra.txt').write_text('extra')
sorted([p.name for p in Path('/data').iterdir()])
`
			require.Equal(t, []any{"data.bin", "extra.txt", "hello.txt", "subdir"}, mustRun(t, b, code, mntOpts(md)))
		})

		t.Run("session read-write mount writes to host", func(t *testing.T) {
			d := mntCreateTestDir(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadWrite})
			_, err := s.FeedRun(ctx, "from pathlib import Path", mntFeed(md))
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "Path('/data/rw_file.txt').write_text('written')", mntFeed(md))
			require.NoError(t, err)
			v, err := s.FeedRun(ctx, "Path('/data/rw_file.txt').read_text()", mntFeed(md))
			require.NoError(t, err)
			require.Equal(t, "written", v)
			require.Equal(t, "written", mntReadFile(t, filepath.Join(d.dir, "rw_file.txt")))
		})

		t.Run("session read-only mount blocks write", func(t *testing.T) {
			d := mntCreateTestDir(t)
			s := newSession(t, b, monty.CheckoutOptions{})
			ctx := testCtx(t)
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			_, err := s.FeedRun(ctx, "from pathlib import Path", mntFeed(md))
			require.NoError(t, err)
			_, err = s.FeedRun(ctx, "Path('/data/nope.txt').write_text('x')", mntFeed(md))
			mntRequireRuntimeError(t, err, "PermissionError: [Errno 30] Read-only file system: '/data/nope.txt'")
		})

		t.Run("relative symlink inside a mount is followed", func(t *testing.T) {
			d := mntCreateTestDir(t)
			if err := os.Symlink("hello.txt", filepath.Join(d.dir, "rel_link.txt")); err != nil {
				t.Skipf("host forbids symlink creation: %v", err)
			}
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})
			require.Equal(t, "hello world", mustRun(t, b, "from pathlib import Path; Path('/data/rel_link.txt').read_text()", mntOpts(md)))
		})

		t.Run("absolute symlink target is refused inside a mount", func(t *testing.T) {
			d := mntCreateTestDir(t)
			if err := os.Symlink(filepath.Join(d.dir, "hello.txt"), filepath.Join(d.dir, "abs_link.txt")); err != nil {
				t.Skipf("host forbids symlink creation: %v", err)
			}
			md := d.mount(monty.MountDirOptions{VirtualPath: "/data", Mode: monty.MountReadOnly})

			_, err := run(t, b, "from pathlib import Path; Path('/data/abs_link.txt').read_text()", mntOpts(md))
			mntRequireRuntimeError(t, err, "PermissionError: [Errno 13] Permission denied: '/data/abs_link.txt'")

			seen := mustRun(t, b, "from pathlib import Path; p = Path('/data/abs_link.txt'); (p.exists(), p.is_file())", mntOpts(md))
			require.Equal(t, monty.Tuple{false, false}, seen)
		})
	})
}
