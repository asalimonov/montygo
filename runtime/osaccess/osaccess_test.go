package osaccess_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/runtime/osaccess"
)

type P = montygo.Path

type customFile struct {
	path        montygo.Path
	name        string
	permissions int64
	deleted     bool
	content     string
}

func newCustomFile(path, content string) *customFile {
	return &customFile{path: montygo.Path(path), name: path[strings.LastIndex(path, "/")+1:], permissions: 0o644, content: content}
}

func (f *customFile) Path() montygo.Path        { return f.path }
func (f *customFile) SetPath(p montygo.Path)    { f.path = p }
func (f *customFile) Name() string              { return f.name }
func (f *customFile) Permissions() int64        { return f.permissions }
func (f *customFile) Deleted() bool             { return f.deleted }
func (f *customFile) ReadContent() (any, error) { return f.content, nil }
func (f *customFile) Delete()                   { f.deleted = true }

func (f *customFile) WriteContent(content any) error {
	switch c := content.(type) {
	case string:
		f.content = c
	case []byte:
		f.content = string(c)
	}
	return nil
}

func TestOSAccess(t *testing.T) {
	testInit(t)
	testExistence(t)
	testReading(t)
	testWriting(t)
	testOpen(t)
	testMkdirRmdir(t)
	testIterdirUnlinkStat(t)
	testRenameResolve(t)
	testEnviron(t)
	testFiles(t)
	testEdgeCases(t)
}

func testInit(t *testing.T) {
	// Python reads PurePosixPath.as_posix(); Go reads File.Path().
	t.Run("test_non_absolute_path", func(t *testing.T) {
		osa, err := osaccess.New([]osaccess.File{mem("relative/path.txt", "test")}, nil)
		require.NoError(t, err)
		require.Equal(t, P("/relative/path.txt"), osa.Files[0].Path())

		osa, err = osaccess.New([]osaccess.File{mem("relative/path.txt", "test")}, nil, osaccess.WithRootDir("/foo/bar"))
		require.NoError(t, err)
		require.Equal(t, P("/foo/bar/relative/path.txt"), osa.Files[0].Path())
	})

	t.Run("test_file_nested_within_file_rejected", func(t *testing.T) {
		_, err := osaccess.New([]osaccess.File{mem("/test/file.txt", "outer"), mem("/test/file.txt/nested.txt", "inner")}, nil)
		requireRaised(t, err, "ValueError",
			"Cannot put file MemoryFile(path=/test/file.txt/nested.txt, content='...', permissions=420) "+
				"within sub-directory of file MemoryFile(path=/test/file.txt, content='...', permissions=420)")
	})

	montyTest(t, "test_empty_initialization", func(t *testing.T, b montygo.Backend) {
		require.Equal(t, false, mustRun(t, b, `from pathlib import Path; Path("/any/path").exists()`, newFS(t).Handler()))
	})

	montyTest(t, "test_environ_parameter", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"MY_VAR": "my_value"})
		require.Equal(t, "my_value", mustRun(t, b, "import os; os.getenv('MY_VAR')", fs.Handler()))
	})

	// Python checks isinstance and tzinfo; Go checks the offset pointer.
	t.Run("test_time_methods_direct_api", func(t *testing.T) {
		fs := newFS(t)
		today, err := fs.DateToday()
		require.NoError(t, err)
		require.NotZero(t, today.Year)
		naive, err := fs.DatetimeNow(nil)
		require.NoError(t, err)
		require.Nil(t, naive.OffsetSeconds)
		aware, err := fs.DatetimeNow(&montygo.TimeZone{OffsetSeconds: 0})
		require.NoError(t, err)
		require.NotNil(t, aware.OffsetSeconds)
		require.Equal(t, int32(0), *aware.OffsetSeconds)
	})
}

func testExistence(t *testing.T) {
	montyTest(t, "test_path_exists_file", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		require.Equal(t, true, mustRun(t, b, `from pathlib import Path; Path("/test/file.txt").exists()`, fs.Handler()))
	})

	montyTest(t, "test_path_exists_directory", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		require.Equal(t, true, mustRun(t, b, `from pathlib import Path; Path("/test/subdir").exists()`, fs.Handler()))
	})

	montyTest(t, "test_path_exists_nested", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/a/b/c/d/file.txt", "deep"))
		code := `
from pathlib import Path
(Path('/a').exists(), Path('/a/b').exists(), Path('/a/b/c').exists(), Path('/a/b/c/d').exists())
`
		require.Equal(t, montygo.Tuple{true, true, true, true}, mustRun(t, b, code, fs.Handler()))
	})

	montyTest(t, "test_path_exists_missing", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		require.Equal(t, false, mustRun(t, b, `from pathlib import Path; Path("/other/path").exists()`, fs.Handler()))
	})

	montyTest(t, "test_path_is_file_for_file", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		require.Equal(t, true, mustRun(t, b, `from pathlib import Path; Path("/test/file.txt").is_file()`, fs.Handler()))
	})

	montyTest(t, "test_path_is_file_for_directory", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		require.Equal(t, false, mustRun(t, b, `from pathlib import Path; Path("/test/subdir").is_file()`, fs.Handler()))
	})

	montyTest(t, "test_path_is_file_missing", func(t *testing.T, b montygo.Backend) {
		require.Equal(t, false, mustRun(t, b, `from pathlib import Path; Path("/missing").is_file()`, newFS(t).Handler()))
	})

	montyTest(t, "test_path_is_dir_for_directory", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		require.Equal(t, true, mustRun(t, b, `from pathlib import Path; Path("/test/subdir").is_dir()`, fs.Handler()))
	})

	montyTest(t, "test_path_is_dir_for_file", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		require.Equal(t, false, mustRun(t, b, `from pathlib import Path; Path("/test/file.txt").is_dir()`, fs.Handler()))
	})

	montyTest(t, "test_path_is_dir_missing", func(t *testing.T, b montygo.Backend) {
		require.Equal(t, false, mustRun(t, b, `from pathlib import Path; Path("/missing").is_dir()`, newFS(t).Handler()))
	})

	montyTest(t, "test_path_is_symlink_always_false", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		code := `
from pathlib import Path
(Path('/test/file.txt').is_symlink(), Path('/test').is_symlink(), Path('/missing').is_symlink())
`
		require.Equal(t, montygo.Tuple{false, false, false}, mustRun(t, b, code, fs.Handler()))
	})
}

func testReading(t *testing.T) {
	montyTest(t, "test_read_text_string_content", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello world"))
		require.Equal(t, "hello world", mustRun(t, b, `from pathlib import Path; Path("/test/file.txt").read_text()`, fs.Handler()))
	})

	montyTest(t, "test_read_text_bytes_content_decoded", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", []byte("bytes content")))
		require.Equal(t, "bytes content", mustRun(t, b, `from pathlib import Path; Path("/test/file.txt").read_text()`, fs.Handler()))
	})

	montyTest(t, "test_read_bytes_bytes_content", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.bin", []byte{0, 1, 2, 3}))
		require.Equal(t, []byte{0, 1, 2, 3}, mustRun(t, b, `from pathlib import Path; Path("/test/file.bin").read_bytes()`, fs.Handler()))
	})

	montyTest(t, "test_read_bytes_string_content_encoded", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		require.Equal(t, []byte("hello"), mustRun(t, b, `from pathlib import Path; Path("/test/file.txt").read_bytes()`, fs.Handler()))
	})

	montyTest(t, "test_read_text_file_not_found", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, `from pathlib import Path; Path("/missing.txt").read_text()`, newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/missing.txt'")
	})

	montyTest(t, "test_read_bytes_file_not_found", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, `from pathlib import Path; Path("/missing.bin").read_bytes()`, newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/missing.bin'")
	})

	montyTest(t, "test_read_text_is_a_directory", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		_, err := run(t, b, `from pathlib import Path; Path("/test/subdir").read_text()`, fs.Handler())
		requireRuntimeError(t, err, "IsADirectoryError: [Errno 21] Is a directory: '/test/subdir'")
	})

	montyTest(t, "test_read_bytes_is_a_directory", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		_, err := run(t, b, `from pathlib import Path; Path("/test/subdir").read_bytes()`, fs.Handler())
		requireRuntimeError(t, err, "IsADirectoryError: [Errno 21] Is a directory: '/test/subdir'")
	})
}

func testWriting(t *testing.T) {
	montyTest(t, "test_write_text_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/existing.txt", "existing"))
		result := mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/new.txt').write_text('new content')\n", fs.Handler())
		require.Equal(t, int64(11), result)
		require.True(t, must[bool](t)(fs.PathExists("/test/new.txt")))
		require.Equal(t, "new content", must[string](t)(fs.PathReadText("/test/new.txt")))
	})

	montyTest(t, "test_write_text_overwrite_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "original"))
		mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/file.txt').write_text('updated')\n", fs.Handler())
		require.Equal(t, "updated", must[string](t)(fs.PathReadText("/test/file.txt")))
	})

	montyTest(t, "test_write_bytes_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/existing.txt", "existing"))
		result := mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/new.bin').write_bytes(b'binary data')\n", fs.Handler())
		require.Equal(t, int64(11), result)
		require.Equal(t, []byte("binary data"), must[[]byte](t)(fs.PathReadBytes("/test/new.bin")))
	})

	montyTest(t, "test_write_text_parent_not_exists_via_monty", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, "from pathlib import Path; Path('/no/parent/file.txt').write_text('test')", newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/no/parent/file.txt'")
	})

	montyTest(t, "test_write_text_to_directory_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		_, err := run(t, b, "from pathlib import Path; Path('/test/subdir').write_text('test')", fs.Handler())
		requireRuntimeError(t, err, "IsADirectoryError: [Errno 21] Is a directory: '/test/subdir'")
	})

	t.Run("test_write_text_new_file_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/existing.txt", "existing"))
		must[int](t)(fs.PathWriteText("/test/new.txt", "new content"))
		require.True(t, must[bool](t)(fs.PathExists("/test/new.txt")))
		require.Equal(t, "new content", must[string](t)(fs.PathReadText("/test/new.txt")))
	})

	t.Run("test_write_text_overwrite_existing_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "original"))
		must[int](t)(fs.PathWriteText("/test/file.txt", "updated"))
		require.Equal(t, "updated", must[string](t)(fs.PathReadText("/test/file.txt")))
	})

	t.Run("test_write_bytes_new_file_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/existing.txt", "existing"))
		must[int](t)(fs.PathWriteBytes("/test/new.bin", []byte("binary data")))
		require.Equal(t, []byte("binary data"), must[[]byte](t)(fs.PathReadBytes("/test/new.bin")))
	})

	t.Run("test_write_bytes_overwrite_existing_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.bin", []byte("original")))
		must[int](t)(fs.PathWriteBytes("/test/file.bin", []byte("updated")))
		require.Equal(t, []byte("updated"), must[[]byte](t)(fs.PathReadBytes("/test/file.bin")))
	})

	t.Run("test_write_text_parent_not_exists_direct", func(t *testing.T) {
		_, err := newFS(t).PathWriteText("/no/parent/file.txt", "test")
		requireRaised(t, err, "FileNotFoundError", "[Errno 2] No such file or directory: '/no/parent/file.txt'")
	})

	t.Run("test_write_text_to_directory_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		_, err := fs.PathWriteText("/test/subdir", "test")
		requireRaised(t, err, "IsADirectoryError", "[Errno 21] Is a directory: '/test/subdir'")
	})

	t.Run("test_append_text_non_ascii_returns_char_count", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "start "))
		require.Equal(t, 3, must[int](t)(fs.PathAppendText("/test/file.txt", "αβγ")))
		require.Equal(t, "start αβγ", must[string](t)(fs.PathReadText("/test/file.txt")))
	})

	t.Run("test_append_bytes_returns_byte_count", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.bin", []byte("start ")))
		require.Equal(t, 6, must[int](t)(fs.PathAppendBytes("/test/file.bin", []byte("αβγ"))))
		require.Equal(t, []byte("start αβγ"), must[[]byte](t)(fs.PathReadBytes("/test/file.bin")))
	})
}

func testOpen(t *testing.T) {
	montyTest(t, "test_open_read_text", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/hello.txt", "hello world"))
		require.Equal(t, "hello world", mustRun(t, b, "\nf = open('/data/hello.txt')\ndata = f.read()\nf.close()\ndata\n", fs.Handler()))
	})

	montyTest(t, "test_open_read_bytes", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/blob.bin", []byte{0, 1, 2}))
		require.Equal(t, []byte{0, 1, 2}, mustRun(t, b, "\nf = open('/data/blob.bin', 'rb')\ndata = f.read()\nf.close()\ndata\n", fs.Handler()))
	})

	montyTest(t, "test_open_missing_file_raises_file_not_found", func(t *testing.T, b montygo.Backend) {
		code := `
try:
    open('/data/missing.txt')
    result = 'no error'
except FileNotFoundError as e:
    result = str(e)
result
`
		require.Equal(t, "[Errno 2] No such file or directory: '/data/missing.txt'", mustRun(t, b, code, newFS(t).Handler()))
	})

	montyTest(t, "test_open_directory_raises_is_a_directory", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/inner/file.txt", "x"))
		code := `
try:
    open('/data/inner')
    result = 'no error'
except IsADirectoryError as e:
    result = str(e)
result
`
		require.Equal(t, "[Errno 21] Is a directory: '/data/inner'", mustRun(t, b, code, fs.Handler()))
	})

	montyTest(t, "test_open_write_truncates_existing", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/file.txt", "previous"))
		mustRun(t, b, "\nopen('/data/file.txt', 'w').close()\n", fs.Handler())
		require.Equal(t, "", must[string](t)(fs.PathReadText("/data/file.txt")))
	})

	montyTest(t, "test_open_write_creates_missing", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t)
		mustRun(t, b, "open('/created.txt', 'w').close()", fs.Handler())
		require.True(t, must[bool](t)(fs.PathExists("/created.txt")))
		require.Equal(t, "", must[string](t)(fs.PathReadText("/created.txt")))
	})

	montyTest(t, "test_open_write_then_write_data", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/file.txt", "old"))
		result := mustRun(t, b, "\nf = open('/data/file.txt', 'w')\nn = f.write('new content')\nf.close()\nn\n", fs.Handler())
		require.Equal(t, int64(11), result)
		require.Equal(t, "new content", must[string](t)(fs.PathReadText("/data/file.txt")))
	})

	montyTest(t, "test_open_append_preserves_existing", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/log.txt", "keep me"))
		mustRun(t, b, "\nf = open('/data/log.txt', 'a')\nf.write('!')\nf.close()\n", fs.Handler())
		require.Equal(t, "keep me!", must[string](t)(fs.PathReadText("/data/log.txt")))
	})

	montyTest(t, "test_open_append_creates_missing", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t)
		mustRun(t, b, "\nf = open('/fresh.txt', 'a')\nf.write('seed')\nf.close()\n", fs.Handler())
		require.Equal(t, "seed", must[string](t)(fs.PathReadText("/fresh.txt")))
	})

	montyTest(t, "test_open_append_text_non_ascii_returns_char_count", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/file.txt", "start "))
		result := mustRun(t, b, "\nf = open('/data/file.txt', 'a')\nn = f.write('αβγ')\nf.close()\nn\n", fs.Handler())
		require.Equal(t, int64(3), result)
		require.Equal(t, "start αβγ", must[string](t)(fs.PathReadText("/data/file.txt")))
	})

	montyTest(t, "test_open_binary_write_returns_byte_count", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t)
		code := `
f = open('/out.bin', 'wb')
n = f.write(b'\x10\x11\x12')
f.close()
n
`
		require.Equal(t, int64(3), mustRun(t, b, code, fs.Handler()))
		require.Equal(t, []byte{0x10, 0x11, 0x12}, must[[]byte](t)(fs.PathReadBytes("/out.bin")))
	})

	montyTest(t, "test_open_write_to_read_only_raises", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/file.txt", "x"))
		code := `
f = open('/data/file.txt', 'r')
try:
    f.write('y')
    result = 'no error'
except OSError as e:
    result = str(e)
result
`
		require.Equal(t, "not writable", mustRun(t, b, code, fs.Handler()))
	})

	montyTest(t, "test_open_read_from_write_only_raises", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/file.txt", "x"))
		code := `
f = open('/data/file.txt', 'w')
try:
    f.read()
    result = 'no error'
except OSError as e:
    result = str(e)
result
`
		require.Equal(t, "not readable", mustRun(t, b, code, fs.Handler()))
	})

	montyTest(t, "test_open_keyword_args", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/data/hello.txt", "hi"))
		code := "\nf = open(file='/data/hello.txt', mode='r', encoding='utf-8')\ndata = f.read()\nf.close()\ndata\n"
		require.Equal(t, "hi", mustRun(t, b, code, fs.Handler()))
	})

	// Python checks isinstance(handle, MontyFileHandle); Go returns *montygo.FileHandle by type.
	t.Run("test_path_open_returns_monty_file_handle", func(t *testing.T) {
		fs := newFS(t, mem("/data/file.txt", "x"))
		handle := must[*montygo.FileHandle](t)(fs.PathOpen("/data/file.txt", "r"))
		require.Equal(t, "/data/file.txt", handle.Path)
		require.Equal(t, "r", handle.Mode)
		require.Equal(t, []bool{false, true, false}, []bool{handle.Binary(), handle.Readable(), handle.Writable()})
	})

	t.Run("test_path_open_normalizes_mode", func(t *testing.T) {
		fs := newFS(t, mem("/data/file.txt", "x"))
		require.Equal(t, "r", must[*montygo.FileHandle](t)(fs.PathOpen("/data/file.txt", "rt")).Mode)
	})

	t.Run("test_path_open_rejects_plus_modes", func(t *testing.T) {
		fs := newFS(t, mem("/data/file.txt", "x"))
		for _, mode := range []string{"r+", "rb+", "r+b", "w+", "wb+", "a+", "ab+"} {
			_, err := fs.PathOpen("/data/file.txt", mode)
			requireRaised(t, err, "ValueError", "update modes ('+') are not yet supported")
		}
	})

	t.Run("test_path_open_w_truncates_via_direct_api", func(t *testing.T) {
		fs := newFS(t, mem("/data/file.txt", "previous"))
		must[*montygo.FileHandle](t)(fs.PathOpen("/data/file.txt", "w"))
		require.Equal(t, "", must[string](t)(fs.PathReadText("/data/file.txt")))
	})

	t.Run("test_path_open_r_missing_raises", func(t *testing.T) {
		_, err := newFS(t).PathOpen("/missing.txt", "r")
		requireRaised(t, err, "FileNotFoundError", "[Errno 2] No such file or directory: '/missing.txt'")
	})

	t.Run("test_path_open_invalid_mode_does_not_truncate", func(t *testing.T) {
		fs := newFS(t, mem("/data/file.txt", "precious"))
		for _, mode := range []string{"wxyz", "axyz", "w!", "a?"} {
			_, err := fs.PathOpen("/data/file.txt", mode)
			requireExcType(t, err, "ValueError")
			require.Equal(t, "precious", must[string](t)(fs.PathReadText("/data/file.txt")), "mode %q touched the file before validation", mode)
		}
	})
}

func testMkdirRmdir(t *testing.T) {
	montyTest(t, "test_mkdir_basic_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/newdir').mkdir()\n", fs.Handler())
		require.True(t, must[bool](t)(fs.PathIsDir("/test/newdir")))
	})

	montyTest(t, "test_mkdir_with_parents_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t)
		mustRun(t, b, "\nfrom pathlib import Path\nPath('/a/b/c/d').mkdir(parents=True)\n", fs.Handler())
		for _, p := range []P{"/a", "/a/b", "/a/b/c", "/a/b/c/d"} {
			require.True(t, must[bool](t)(fs.PathIsDir(p)), p)
		}
	})

	montyTest(t, "test_mkdir_exist_ok_true_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/subdir').mkdir(exist_ok=True)\n", fs.Handler())
		require.True(t, must[bool](t)(fs.PathIsDir("/test/subdir")))
	})

	montyTest(t, "test_mkdir_exist_ok_false_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		_, err := run(t, b, "from pathlib import Path; Path('/test/subdir').mkdir()", fs.Handler())
		requireRuntimeError(t, err, "FileExistsError: [Errno 17] File exists: '/test/subdir'")
	})

	montyTest(t, "test_mkdir_parent_not_exists_via_monty", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, "from pathlib import Path; Path('/no/parent/dir').mkdir()", newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/no/parent/dir'")
	})

	t.Run("test_mkdir_basic_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		require.NoError(t, fs.PathMkdir("/test/newdir", false, false))
		require.True(t, must[bool](t)(fs.PathIsDir("/test/newdir")))
	})

	t.Run("test_mkdir_with_parents_direct", func(t *testing.T) {
		fs := newFS(t)
		require.NoError(t, fs.PathMkdir("/a/b/c/d", true, false))
		for _, p := range []P{"/a", "/a/b", "/a/b/c", "/a/b/c/d"} {
			require.True(t, must[bool](t)(fs.PathIsDir(p)), p)
		}
	})

	t.Run("test_mkdir_exist_ok_true_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		require.NoError(t, fs.PathMkdir("/test/subdir", false, true))
		require.True(t, must[bool](t)(fs.PathIsDir("/test/subdir")))
	})

	t.Run("test_mkdir_exist_ok_false_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		requireRaised(t, fs.PathMkdir("/test/subdir", false, false), "FileExistsError", "[Errno 17] File exists: '/test/subdir'")
	})

	t.Run("test_mkdir_file_exists_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		requireRaised(t, fs.PathMkdir("/test/file.txt", false, false), "FileExistsError", "[Errno 17] File exists: '/test/file.txt'")
	})

	t.Run("test_mkdir_parent_not_exists_direct", func(t *testing.T) {
		requireRaised(t, newFS(t).PathMkdir("/no/parent/dir", false, false), "FileNotFoundError", "[Errno 2] No such file or directory: '/no/parent/dir'")
	})

	t.Run("test_mkdir_parent_is_file_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		requireRaised(t, fs.PathMkdir("/test/file.txt/subdir", true, false), "NotADirectoryError", "[Errno 20] Not a directory: '/test/file.txt/subdir'")
	})

	montyTest(t, "test_rmdir_empty_directory_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		require.NoError(t, fs.PathMkdir("/test/newdir", false, false))
		mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/newdir').rmdir()\n", fs.Handler())
		require.False(t, must[bool](t)(fs.PathExists("/test/newdir")))
	})

	montyTest(t, "test_rmdir_non_empty_directory_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		_, err := run(t, b, "from pathlib import Path; Path('/test/subdir').rmdir()", fs.Handler())
		requireRuntimeError(t, err, "OSError: [Errno 39] Directory not empty: '/test/subdir'")
	})

	montyTest(t, "test_rmdir_not_found_via_monty", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, "from pathlib import Path; Path('/missing').rmdir()", newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/missing'")
	})

	montyTest(t, "test_rmdir_file_not_directory_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		_, err := run(t, b, "from pathlib import Path; Path('/test/file.txt').rmdir()", fs.Handler())
		requireRuntimeError(t, err, "NotADirectoryError: [Errno 20] Not a directory: '/test/file.txt'")
	})

	t.Run("test_rmdir_empty_directory_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		require.NoError(t, fs.PathMkdir("/test/newdir", false, false))
		require.NoError(t, fs.PathRmdir("/test/newdir"))
		require.False(t, must[bool](t)(fs.PathExists("/test/newdir")))
	})

	t.Run("test_rmdir_non_empty_directory_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		requireRaised(t, fs.PathRmdir("/test/subdir"), "OSError", "[Errno 39] Directory not empty: '/test/subdir'")
	})

	t.Run("test_rmdir_file_not_directory_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		requireRaised(t, fs.PathRmdir("/test/file.txt"), "NotADirectoryError", "[Errno 20] Not a directory: '/test/file.txt'")
	})

	t.Run("test_rmdir_not_found_direct", func(t *testing.T) {
		requireRaised(t, newFS(t).PathRmdir("/missing"), "FileNotFoundError", "[Errno 2] No such file or directory: '/missing'")
	})
}

func testIterdirUnlinkStat(t *testing.T) {
	montyTest(t, "test_iterdir_list_contents", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/a.txt", "a"), mem("/test/b.txt", "b"), mem("/test/subdir/c.txt", "c"))
		result := mustRun(t, b, "\nfrom pathlib import Path\n[str(p) for p in Path('/test').iterdir()]\n", fs.Handler())
		require.Equal(t, []any{"/test/a.txt", "/test/b.txt", "/test/subdir"}, result)
	})

	t.Run("test_iterdir_empty_directory_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		require.NoError(t, fs.PathMkdir("/test/empty", false, false))
		require.Empty(t, must[[]montygo.Path](t)(fs.PathIterdir("/test/empty")))
	})

	t.Run("test_iterdir_not_a_directory_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		_, err := fs.PathIterdir("/test/file.txt")
		requireRaised(t, err, "NotADirectoryError", "[Errno 20] Not a directory: '/test/file.txt'")
	})

	montyTest(t, "test_iterdir_not_found", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, "from pathlib import Path; list(Path('/missing').iterdir())", newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/missing'")
	})

	montyTest(t, "test_unlink_file_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/file.txt').unlink()\n", fs.Handler())
		require.False(t, must[bool](t)(fs.PathExists("/test/file.txt")))
	})

	montyTest(t, "test_unlink_file_not_found_via_monty", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, "from pathlib import Path; Path('/missing.txt').unlink()", newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/missing.txt'")
	})

	montyTest(t, "test_unlink_is_directory_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		_, err := run(t, b, "from pathlib import Path; Path('/test/subdir').unlink()", fs.Handler())
		requireRuntimeError(t, err, "IsADirectoryError: [Errno 21] Is a directory: '/test/subdir'")
	})

	t.Run("test_unlink_file_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		require.NoError(t, fs.PathUnlink("/test/file.txt"))
		require.False(t, must[bool](t)(fs.PathExists("/test/file.txt")))
	})

	t.Run("test_unlink_file_not_found_direct", func(t *testing.T) {
		requireRaised(t, newFS(t).PathUnlink("/missing.txt"), "FileNotFoundError", "[Errno 2] No such file or directory: '/missing.txt'")
	})

	t.Run("test_unlink_is_directory_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		requireRaised(t, fs.PathUnlink("/test/subdir"), "IsADirectoryError", "[Errno 21] Is a directory: '/test/subdir'")
	})

	montyTest(t, "test_stat_file", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello world"))
		result := mustRun(t, b, "\nfrom pathlib import Path\ns = Path('/test/file.txt').stat()\n(s.st_size, s.st_mode & 0o777)\n", fs.Handler())
		require.Equal(t, montygo.Tuple{int64(11), int64(0o644)}, result)
	})

	montyTest(t, "test_stat_file_custom_permissions", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello", 0o755))
		result := mustRun(t, b, "\nfrom pathlib import Path\ns = Path('/test/file.txt').stat()\ns.st_mode & 0o777\n", fs.Handler())
		require.Equal(t, int64(0o755), result)
	})

	montyTest(t, "test_stat_directory", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/subdir/file.txt", "hello"))
		result := mustRun(t, b, "\nfrom pathlib import Path\ns = Path('/test/subdir').stat()\ns.st_mode\n", fs.Handler())
		require.Equal(t, int64(0o040755), result)
	})

	montyTest(t, "test_stat_file_not_found", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, "from pathlib import Path; Path('/missing').stat()", newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/missing'")
	})

	montyTest(t, "test_stat_bytes_content_size", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.bin", []byte{0, 1, 2, 3, 4}))
		require.Equal(t, int64(5), mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/file.bin').stat().st_size\n", fs.Handler()))
	})

	montyTest(t, "test_stat_unicode_size", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "☃"))
		require.Equal(t, int64(3), mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/file.txt').stat().st_size\n", fs.Handler()))
	})
}

func testRenameResolve(t *testing.T) {
	montyTest(t, "test_rename_file_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/old.txt", "content"))
		mustRun(t, b, "\nfrom pathlib import Path\nPath('/test/old.txt').rename(Path('/test/new.txt'))\n", fs.Handler())
		require.False(t, must[bool](t)(fs.PathExists("/test/old.txt")))
		require.True(t, must[bool](t)(fs.PathExists("/test/new.txt")))
		require.Equal(t, "content", must[string](t)(fs.PathReadText("/test/new.txt")))
	})

	montyTest(t, "test_rename_source_not_found_via_monty", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, "from pathlib import Path; Path('/missing.txt').rename(Path('/new.txt'))", newFS(t).Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/missing.txt' -> '/new.txt'")
	})

	montyTest(t, "test_rename_target_parent_not_found_via_monty", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "content"))
		_, err := run(t, b, "from pathlib import Path; Path('/test/file.txt').rename(Path('/no/parent/file.txt'))", fs.Handler())
		requireRuntimeError(t, err, "FileNotFoundError: [Errno 2] No such file or directory: '/test/file.txt' -> '/no/parent/file.txt'")
	})

	t.Run("test_rename_file_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/old.txt", "content"))
		require.NoError(t, fs.PathRename("/test/old.txt", "/test/new.txt"))
		require.False(t, must[bool](t)(fs.PathExists("/test/old.txt")))
		require.True(t, must[bool](t)(fs.PathExists("/test/new.txt")))
		require.Equal(t, "content", must[string](t)(fs.PathReadText("/test/new.txt")))
	})

	t.Run("test_rename_source_not_found_direct", func(t *testing.T) {
		requireRaised(t, newFS(t).PathRename("/missing.txt", "/new.txt"), "FileNotFoundError", "[Errno 2] No such file or directory: '/missing.txt' -> '/new.txt'")
	})

	t.Run("test_rename_target_parent_not_found_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "content"))
		requireRaised(t, fs.PathRename("/test/file.txt", "/no/parent/file.txt"), "FileNotFoundError",
			"[Errno 2] No such file or directory: '/test/file.txt' -> '/no/parent/file.txt'")
	})

	t.Run("test_rename_directory_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/olddir/file.txt", "content"))
		require.NoError(t, fs.PathMkdir("/test/newdir", false, false))
		require.NoError(t, fs.PathRename("/test/newdir", "/test/renamed"))
		require.True(t, must[bool](t)(fs.PathIsDir("/test/renamed")))
	})

	t.Run("test_rename_directory_non_empty_target_direct", func(t *testing.T) {
		fs := newFS(t, mem("/test/src/a.txt", "a"), mem("/test/dst/b.txt", "b"))
		requireRaised(t, fs.PathRename("/test/src", "/test/dst"), "OSError", "[Errno 66] Directory not empty: '/test/src' -> '/test/dst'")
	})

	t.Run("test_rename_directory_updates_file_paths_direct", func(t *testing.T) {
		file1 := mem("/old/dir/file1.txt", "one")
		file2 := mem("/old/dir/subdir/file2.txt", "two")
		fs := newFS(t, file1, file2)
		require.NoError(t, fs.PathMkdir("/new", false, false))
		require.NoError(t, fs.PathRename("/old/dir", "/new/location"))
		require.Equal(t, "one", must[string](t)(fs.PathReadText("/new/location/file1.txt")))
		require.Equal(t, "two", must[string](t)(fs.PathReadText("/new/location/subdir/file2.txt")))
		require.Equal(t, P("/new/location/file1.txt"), file1.Path())
		require.Equal(t, P("/new/location/subdir/file2.txt"), file2.Path())
		require.False(t, must[bool](t)(fs.PathExists("/old/dir")))
		require.False(t, must[bool](t)(fs.PathExists("/old/dir/file1.txt")))
	})

	// Python recurses until RecursionError after corrupting the tree; Go rejects the move up front.
	t.Run("go_rename_directory_into_itself_rejected", func(t *testing.T) {
		fs := newFS(t, mem("/a/file.txt", "x"))
		requireRaised(t, fs.PathRename("/a", "/a/b"), "OSError", "[Errno 22] Invalid argument: '/a' -> '/a/b'")
		require.Equal(t, "x", must[string](t)(fs.PathReadText("/a/file.txt")))
	})

	montyTest(t, "test_path_resolve_absolute", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/test/file.txt", "hello"))
		require.Equal(t, "/test/file.txt", mustRun(t, b, "\nfrom pathlib import Path\nstr(Path('/test/file.txt').resolve())\n", fs.Handler()))
	})

	montyTest(t, "test_path_absolute_already_absolute", func(t *testing.T, b montygo.Backend) {
		require.Equal(t, "/already/absolute", mustRun(t, b, "\nfrom pathlib import Path\nstr(Path('/already/absolute').absolute())\n", newFS(t).Handler()))
	})

	montyTest(t, "test_path_absolute_relative", func(t *testing.T, b montygo.Backend) {
		require.Equal(t, "/relative/path", mustRun(t, b, "\nfrom pathlib import Path\nstr(Path('relative/path').absolute())\n", newFS(t).Handler()))
	})

	montyTest(t, "test_path_resolve_same_as_absolute", func(t *testing.T, b montygo.Backend) {
		code := "\nfrom pathlib import Path\nstr(Path('relative').resolve()) == str(Path('relative').absolute())\n"
		require.Equal(t, true, mustRun(t, b, code, newFS(t).Handler()))
	})
}

func testEnviron(t *testing.T) {
	montyTest(t, "test_getenv_existing_key", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"MY_VAR": "my_value"})
		require.Equal(t, "my_value", mustRun(t, b, "import os; os.getenv('MY_VAR')", fs.Handler()))
	})

	montyTest(t, "test_getenv_missing_key", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"OTHER": "value"})
		require.Nil(t, mustRun(t, b, "import os; os.getenv('MISSING')", fs.Handler()))
	})

	montyTest(t, "test_getenv_missing_with_default", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{})
		require.Equal(t, "default_value", mustRun(t, b, "import os; os.getenv('MISSING', 'default_value')", fs.Handler()))
	})

	montyTest(t, "test_getenv_multiple_vars", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"VAR1": "value1", "VAR2": "value2", "VAR3": "value3"})
		result := mustRun(t, b, "\nimport os\n(os.getenv('VAR1'), os.getenv('VAR2'), os.getenv('VAR3'))\n", fs.Handler())
		require.Equal(t, montygo.Tuple{"value1", "value2", "value3"}, result)
	})

	montyTest(t, "test_get_environ_returns_dict", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"HOME": "/home/user", "USER": "testuser"})
		requireStringDict(t, map[string]any{"HOME": "/home/user", "USER": "testuser"}, mustRun(t, b, "import os; os.environ", fs.Handler()))
	})

	montyTest(t, "test_get_environ_key_access", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"MY_VAR": "my_value"})
		require.Equal(t, "my_value", mustRun(t, b, "import os; os.environ['MY_VAR']", fs.Handler()))
	})

	montyTest(t, "test_get_environ_key_missing_raises", func(t *testing.T, b montygo.Backend) {
		_, err := run(t, b, "import os; os.environ['MISSING']", newEnvFS(t, map[string]string{}).Handler())
		requireRuntimeError(t, err, "KeyError: MISSING")
	})

	montyTest(t, "test_get_environ_get_method", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"HOME": "/home/user"})
		require.Equal(t, "/home/user", mustRun(t, b, "import os; os.environ.get('HOME')", fs.Handler()))
	})

	montyTest(t, "test_get_environ_get_missing_with_default", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{})
		require.Equal(t, "fallback", mustRun(t, b, "import os; os.environ.get('MISSING', 'fallback')", fs.Handler()))
	})

	montyTest(t, "test_get_environ_len", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"A": "1", "B": "2", "C": "3"})
		require.Equal(t, int64(3), mustRun(t, b, "import os; len(os.environ)", fs.Handler()))
	})

	montyTest(t, "test_get_environ_contains", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"PRESENT": "value"})
		result := mustRun(t, b, "\nimport os\n('PRESENT' in os.environ, 'ABSENT' in os.environ)\n", fs.Handler())
		require.Equal(t, montygo.Tuple{true, false}, result)
	})

	montyTest(t, "test_get_environ_keys", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"X": "1", "Y": "2"})
		require.ElementsMatch(t, []any{"X", "Y"}, mustRun(t, b, "import os; list(os.environ.keys())", fs.Handler()))
	})

	montyTest(t, "test_get_environ_values", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"X": "a", "Y": "b"})
		require.ElementsMatch(t, []any{"a", "b"}, mustRun(t, b, "import os; list(os.environ.values())", fs.Handler()))
	})

	montyTest(t, "test_get_environ_items", func(t *testing.T, b montygo.Backend) {
		fs := newEnvFS(t, map[string]string{"X": "1", "Y": "2"})
		require.ElementsMatch(t, []any{montygo.Tuple{"X", "1"}, montygo.Tuple{"Y", "2"}}, mustRun(t, b, "import os; list(os.environ.items())", fs.Handler()))
	})

	montyTest(t, "test_get_environ_empty", func(t *testing.T, b montygo.Backend) {
		requireStringDict(t, map[string]any{}, mustRun(t, b, "import os; os.environ", newFS(t).Handler()))
	})
}

func requireStringDict(t testing.TB, want map[string]any, got any) {
	t.Helper()
	d, ok := got.(*montygo.Dict)
	require.True(t, ok, "want *montygo.Dict, got %T", got)
	m, ok := d.StringMap()
	require.True(t, ok)
	require.Equal(t, want, m)
}

func testFiles(t *testing.T) {
	t.Run("test_memory_file_string_content", func(t *testing.T) {
		file := osaccess.NewMemoryFile("/test/file.txt", "hello")
		require.Equal(t, "hello", must[any](t)(file.ReadContent()))
		require.Equal(t, P("/test/file.txt"), file.Path())
		require.Equal(t, "file.txt", file.Name())
	})

	t.Run("test_memory_file_bytes_content", func(t *testing.T) {
		file := osaccess.NewMemoryFile("/test/file.bin", []byte{0, 1, 2})
		require.Equal(t, []byte{0, 1, 2}, must[any](t)(file.ReadContent()))
	})

	t.Run("test_memory_file_custom_permissions", func(t *testing.T) {
		require.Equal(t, int64(0o755), osaccess.NewMemoryFile("/test/exec.sh", "#!/bin/bash", 0o755).Permissions())
	})

	t.Run("test_memory_file_write_and_read", func(t *testing.T) {
		file := osaccess.NewMemoryFile("/test/file.txt", "original")
		require.NoError(t, file.WriteContent("updated"))
		require.Equal(t, "updated", must[any](t)(file.ReadContent()))
	})

	t.Run("test_memory_file_delete", func(t *testing.T) {
		file := osaccess.NewMemoryFile("/test/file.txt", "content")
		require.False(t, file.Deleted())
		file.Delete()
		require.True(t, file.Deleted())
	})

	// Python repr() maps to String().
	t.Run("test_memory_file_repr", func(t *testing.T) {
		require.Equal(t, "MemoryFile(path=/test/file.txt, content='...', permissions=420)", osaccess.NewMemoryFile("/test/file.txt", "content").String())
	})

	t.Run("test_memory_file_bytes_repr", func(t *testing.T) {
		require.Equal(t, "MemoryFile(path=/test/file.bin, content=b'...', permissions=420)", osaccess.NewMemoryFile("/test/file.bin", []byte{0}).String())
	})

	montyTest(t, "test_callback_file_read", func(t *testing.T, b montygo.Backend) {
		var readCalls []montygo.Path
		read := func(p montygo.Path) (any, error) {
			readCalls = append(readCalls, p)
			return "content from " + string(p), nil
		}
		write := func(montygo.Path, any) error { return nil }
		fs := newFS(t, osaccess.NewCallbackFile("/test/file.txt", read, write))
		result := mustRun(t, b, `from pathlib import Path; Path("/test/file.txt").read_text()`, fs.Handler())
		require.Equal(t, "content from /test/file.txt", result)
		require.Len(t, readCalls, 1)
	})

	t.Run("test_callback_file_write_direct", func(t *testing.T) {
		type call struct {
			path    montygo.Path
			content any
		}
		var written []call
		read := func(montygo.Path) (any, error) { return "", nil }
		write := func(p montygo.Path, content any) error {
			written = append(written, call{p, content})
			return nil
		}
		fs := newFS(t, osaccess.NewCallbackFile("/test/file.txt", read, write))
		must[int](t)(fs.PathWriteText("/test/file.txt", "new content"))
		require.Len(t, written, 1)
		require.Equal(t, "new content", written[0].content)
	})

	t.Run("test_callback_file_custom_permissions", func(t *testing.T) {
		file := osaccess.NewCallbackFile("/test/file.txt", func(montygo.Path) (any, error) { return "", nil }, func(montygo.Path, any) error { return nil }, 0o700)
		require.Equal(t, int64(0o700), file.Permissions())
	})

	t.Run("test_callback_file_repr", func(t *testing.T) {
		file := osaccess.NewCallbackFile("/test/file.txt", func(montygo.Path) (any, error) { return "", nil }, func(montygo.Path, any) error { return nil })
		require.Contains(t, file.String(), "CallbackFile(path=/test/file.txt")
	})

	montyTest(t, "test_custom_abstract_file", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, newCustomFile("/test/custom.txt", "custom content"))
		require.Equal(t, "custom content", mustRun(t, b, `from pathlib import Path; Path("/test/custom.txt").read_text()`, fs.Handler()))
	})

	montyTest(t, "test_custom_abstract_file_mixed_with_memory_file", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, newCustomFile("/test/custom.txt", "from custom"), mem("/test/memory.txt", "from memory"))
		code := "\nfrom pathlib import Path\n(Path('/test/custom.txt').read_text(), Path('/test/memory.txt').read_text())\n"
		require.Equal(t, montygo.Tuple{"from custom", "from memory"}, mustRun(t, b, code, fs.Handler()))
	})

	t.Run("test_os_access_direct_api", func(t *testing.T) {
		fs := newFS(t, mem("/test/file.txt", "hello"), mem("/test/subdir/nested.txt", "nested"))

		require.True(t, must[bool](t)(fs.PathExists("/test/file.txt")))
		require.False(t, must[bool](t)(fs.PathExists("/missing")))

		require.True(t, must[bool](t)(fs.PathIsFile("/test/file.txt")))
		require.False(t, must[bool](t)(fs.PathIsDir("/test/file.txt")))
		require.True(t, must[bool](t)(fs.PathIsDir("/test/subdir")))
		require.False(t, must[bool](t)(fs.PathIsFile("/test/subdir")))

		require.Equal(t, "hello", must[string](t)(fs.PathReadText("/test/file.txt")))
		require.Equal(t, []byte("hello"), must[[]byte](t)(fs.PathReadBytes("/test/file.txt")))

		require.Equal(t, int64(5), must[osaccess.StatResult](t)(fs.PathStat("/test/file.txt")).StSize)

		contents := must[[]montygo.Path](t)(fs.PathIterdir("/test"))
		sort.Slice(contents, func(i, j int) bool { return contents[i] < contents[j] })
		require.Equal(t, []montygo.Path{"/test/file.txt", "/test/subdir"}, contents)

		require.Equal(t, "/relative", must[string](t)(fs.PathAbsolute("relative")))
		require.Equal(t, "/absolute", must[string](t)(fs.PathAbsolute("/absolute")))
	})
}

func testEdgeCases(t *testing.T) {
	montyTest(t, "test_root_directory", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/file.txt", "root file"))
		code := "\nfrom pathlib import Path\n(Path('/').is_dir(), sorted([str(p) for p in Path('/').iterdir()]))\n"
		require.Equal(t, montygo.Tuple{true, []any{"/file.txt"}}, mustRun(t, b, code, fs.Handler()))
	})

	montyTest(t, "test_empty_file", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/empty.txt", ""))
		code := "\nfrom pathlib import Path\n(Path('/empty.txt').read_text(), Path('/empty.txt').stat().st_size)\n"
		require.Equal(t, montygo.Tuple{"", int64(0)}, mustRun(t, b, code, fs.Handler()))
	})

	montyTest(t, "test_large_nested_path", func(t *testing.T, b montygo.Backend) {
		fs := newFS(t, mem("/a/b/c/d/e/f/g/h/i/j/file.txt", "deep"))
		require.Equal(t, "deep", mustRun(t, b, "\nfrom pathlib import Path\nPath('/a/b/c/d/e/f/g/h/i/j/file.txt').read_text()\n", fs.Handler()))
	})

	montyTest(t, "test_special_characters_in_content", func(t *testing.T, b montygo.Backend) {
		content := "line1\nline2\ttab\r\nwindows"
		fs := newFS(t, mem("/special.txt", content))
		require.Equal(t, content, mustRun(t, b, `from pathlib import Path; Path("/special.txt").read_text()`, fs.Handler()))
	})
}
