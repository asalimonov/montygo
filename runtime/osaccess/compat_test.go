package osaccess_test

import (
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/runtime/osaccess"
)

// montyRunner ports the Python MontyRunner. The CPython runner has no Go analogue, so each
// compat test runs only against OSAccess, once per backend.
type montyRunner struct {
	t       *testing.T
	b       montygo.Backend
	files   []*osaccess.MemoryFile
	environ map[string]string
	fs      *osaccess.OSAccess
}

func (r *montyRunner) writeFile(p string, content any) {
	r.files = append(r.files, osaccess.NewMemoryFile(p, content))
	r.fs = nil
}

func (r *montyRunner) setEnviron(environ map[string]string) {
	r.environ = environ
	r.fs = nil
}

func (r *montyRunner) runCode(code string) (any, error) {
	r.t.Helper()
	if r.fs == nil {
		files := make([]osaccess.File, len(r.files))
		for i, f := range r.files {
			files[i] = f
		}
		fs, err := osaccess.New(files, r.environ)
		require.NoError(r.t, err)
		r.fs = fs
	}
	return run(r.t, r.b, "from pathlib import Path\nimport os\n"+code, r.fs.Handler())
}

func (r *montyRunner) mustRunCode(code string) any {
	r.t.Helper()
	v, err := r.runCode(code)
	require.NoError(r.t, err)
	return v
}

func (r *montyRunner) tree() map[string]any {
	result := map[string]any{}
	for _, f := range r.files {
		if f.Deleted() {
			continue
		}
		content, err := f.ReadContent()
		require.NoError(r.t, err)
		parts := pathParts(string(f.Path()))
		node := result
		for _, part := range parts[:len(parts)-1] {
			sub, ok := node[part].(map[string]any)
			if !ok {
				if _, exists := node[part]; exists {
					node = nil
					break
				}
				sub = map[string]any{}
				node[part] = sub
			}
			node = sub
		}
		if node != nil {
			node[parts[len(parts)-1]] = content
		}
	}
	return result
}

func pathParts(p string) []string {
	var parts []string
	if strings.HasPrefix(p, "/") {
		parts = append(parts, "/")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	return parts
}

func compatTest(t *testing.T, name string, fn func(t *testing.T, r *montyRunner)) {
	t.Helper()
	montyTest(t, name, func(t *testing.T, b montygo.Backend) {
		fn(t, &montyRunner{t: t, b: b, environ: map[string]string{}})
	})
}

func TestOSAccessCompat(t *testing.T) {
	compatTest(t, "test_path_exists_file", func(t *testing.T, r *montyRunner) {
		r.writeFile("test/file.txt", "hello")
		require.Equal(t, true, r.mustRunCode("Path('/test/file.txt').exists()"))
	})

	compatTest(t, "test_path_exists_directory", func(t *testing.T, r *montyRunner) {
		r.writeFile("test/subdir/file.txt", "hello")
		require.Equal(t, true, r.mustRunCode("Path('/test/subdir').exists()"))
	})

	compatTest(t, "test_path_exists_missing", func(t *testing.T, r *montyRunner) {
		require.Equal(t, false, r.mustRunCode("Path('/missing/file.txt').exists()"))
	})

	compatTest(t, "test_path_is_file", func(t *testing.T, r *montyRunner) {
		r.writeFile("test/file.txt", "hello")
		require.Equal(t, true, r.mustRunCode("Path('/test/file.txt').is_file()"))
		require.Equal(t, false, r.mustRunCode("Path('/test').is_file()"))
	})

	compatTest(t, "test_path_is_dir", func(t *testing.T, r *montyRunner) {
		r.writeFile("test/file.txt", "hello")
		require.Equal(t, true, r.mustRunCode("Path('/test').is_dir()"))
		require.Equal(t, false, r.mustRunCode("Path('/test/file.txt').is_dir()"))
	})

	compatTest(t, "test_read_text", func(t *testing.T, r *montyRunner) {
		r.writeFile("data/hello.txt", "hello world")
		require.Equal(t, "hello world", r.mustRunCode("Path('/data/hello.txt').read_text()"))
	})

	compatTest(t, "test_read_bytes", func(t *testing.T, r *montyRunner) {
		r.writeFile("data/binary.bin", []byte{0, 1, 2, 3})
		require.Equal(t, []byte{0, 1, 2, 3}, r.mustRunCode("Path('/data/binary.bin').read_bytes()"))
	})

	compatTest(t, "test_read_text_unicode", func(t *testing.T, r *montyRunner) {
		r.writeFile("unicode.txt", "hello \u2603 world")
		require.Equal(t, "hello \u2603 world", r.mustRunCode("Path('/unicode.txt').read_text()"))
	})

	compatTest(t, "test_tree_simple", func(t *testing.T, r *montyRunner) {
		r.writeFile("a.txt", "content a")
		r.writeFile("b.txt", "content b")
		require.Equal(t, map[string]any{"a.txt": "content a", "b.txt": "content b"}, r.tree())
	})

	compatTest(t, "test_tree_nested", func(t *testing.T, r *montyRunner) {
		r.writeFile("dir/subdir/file.txt", "nested content")
		require.Equal(t, map[string]any{"dir": map[string]any{"subdir": map[string]any{"file.txt": "nested content"}}}, r.tree())
	})

	compatTest(t, "test_tree_mixed", func(t *testing.T, r *montyRunner) {
		r.writeFile("root.txt", "root")
		r.writeFile("dir/file.txt", "in dir")
		require.Equal(t, map[string]any{"root.txt": "root", "dir": map[string]any{"file.txt": "in dir"}}, r.tree())
	})

	compatTest(t, "test_stat_size", func(t *testing.T, r *montyRunner) {
		r.writeFile("sized.txt", "hello")
		require.Equal(t, int64(5), r.mustRunCode("Path('/sized.txt').stat().st_size"))
	})

	compatTest(t, "test_stat_size_unicode", func(t *testing.T, r *montyRunner) {
		r.writeFile("unicode.txt", "\u2603")
		require.Equal(t, int64(3), r.mustRunCode("Path('/unicode.txt').stat().st_size"))
	})

	compatTest(t, "test_iterdir", func(t *testing.T, r *montyRunner) {
		r.writeFile("dir/a.txt", "a")
		r.writeFile("dir/b.txt", "b")
		r.writeFile("dir/subdir/c.txt", "c")
		result, ok := r.mustRunCode("list(Path('/dir').iterdir())").([]any)
		require.True(t, ok)
		names := make([]string, len(result))
		for i, p := range result {
			names[i] = path.Base(string(p.(montygo.Path)))
		}
		sort.Strings(names)
		require.Equal(t, []string{"a.txt", "b.txt", "subdir"}, names)
	})

	errorName := func(t *testing.T, r *montyRunner, stmt, exc, want string) {
		t.Helper()
		code := "result = None\ntry:\n    " + stmt + "\nexcept " + exc + " as e:\n    result = type(e).__name__\nresult\n"
		require.Equal(t, want, r.mustRunCode(code))
	}

	compatTest(t, "test_read_text_file_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "Path('/missing.txt').read_text()", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_read_bytes_file_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "Path('/missing.bin').read_bytes()", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_stat_file_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "Path('/missing.txt').stat()", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_iterdir_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "list(Path('/missing_dir').iterdir())", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_read_text_is_directory", func(t *testing.T, r *montyRunner) {
		r.writeFile("mydir/file.txt", "content")
		errorName(t, r, "Path('/mydir').read_text()", "IsADirectoryError", "IsADirectoryError")
	})

	compatTest(t, "test_read_bytes_is_directory", func(t *testing.T, r *montyRunner) {
		r.writeFile("mydir/file.txt", "content")
		errorName(t, r, "Path('/mydir').read_bytes()", "IsADirectoryError", "IsADirectoryError")
	})

	compatTest(t, "test_iterdir_not_a_directory", func(t *testing.T, r *montyRunner) {
		r.writeFile("file.txt", "content")
		errorName(t, r, "list(Path('/file.txt').iterdir())", "NotADirectoryError", "NotADirectoryError")
	})

	compatTest(t, "test_mkdir_file_exists", func(t *testing.T, r *montyRunner) {
		r.writeFile("existing_dir/file.txt", "content")
		errorName(t, r, "Path('/existing_dir').mkdir()", "FileExistsError", "FileExistsError")
	})

	compatTest(t, "test_mkdir_file_at_path", func(t *testing.T, r *montyRunner) {
		r.writeFile("somefile.txt", "content")
		errorName(t, r, "Path('/somefile.txt').mkdir()", "FileExistsError", "FileExistsError")
	})

	compatTest(t, "test_mkdir_exist_ok_no_error", func(t *testing.T, r *montyRunner) {
		r.writeFile("existing_dir/file.txt", "content")
		require.Equal(t, "no error", r.mustRunCode("\nPath('/existing_dir').mkdir(exist_ok=True)\n'no error'\n"))
	})

	compatTest(t, "test_mkdir_parent_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "Path('/no/parent/here').mkdir()", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_mkdir_parents_creates_all", func(t *testing.T, r *montyRunner) {
		require.Equal(t, true, r.mustRunCode("\nPath('/a/b/c/d').mkdir(parents=True)\nPath('/a/b/c/d').is_dir()\n"))
	})

	compatTest(t, "test_unlink_file_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "Path('/missing.txt').unlink()", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_unlink_is_directory", func(t *testing.T, r *montyRunner) {
		r.writeFile("mydir/file.txt", "content")
		result := r.mustRunCode("result = None\ntry:\n    Path('/mydir').unlink()\nexcept OSError as e:\n    result = type(e).__name__\nresult\n")
		require.Contains(t, []any{"IsADirectoryError", "PermissionError"}, result)
	})

	compatTest(t, "test_rmdir_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "Path('/missing_dir').rmdir()", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_rmdir_not_a_directory", func(t *testing.T, r *montyRunner) {
		r.writeFile("file.txt", "content")
		errorName(t, r, "Path('/file.txt').rmdir()", "NotADirectoryError", "NotADirectoryError")
	})

	compatTest(t, "test_rmdir_not_empty", func(t *testing.T, r *montyRunner) {
		r.writeFile("nonempty/file.txt", "content")
		errorName(t, r, "Path('/nonempty').rmdir()", "OSError", "OSError")
	})

	compatTest(t, "test_rename_source_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "Path('/missing.txt').rename(Path('/new.txt'))", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_write_text_new_file", func(t *testing.T, r *montyRunner) {
		result := r.mustRunCode("\ncount = Path('/new_file.txt').write_text('hello world')\n(count, Path('/new_file.txt').read_text())\n")
		require.Equal(t, montygo.Tuple{int64(11), "hello world"}, result)
	})

	compatTest(t, "test_write_text_overwrite", func(t *testing.T, r *montyRunner) {
		r.writeFile("existing.txt", "old content")
		require.Equal(t, "new content", r.mustRunCode("\nPath('/existing.txt').write_text('new content')\nPath('/existing.txt').read_text()\n"))
	})

	compatTest(t, "test_write_bytes_new_file", func(t *testing.T, r *montyRunner) {
		result := r.mustRunCode(`
count = Path('/new_binary.bin').write_bytes(b'\x00\x01\x02')
(count, Path('/new_binary.bin').read_bytes())
`)
		require.Equal(t, montygo.Tuple{int64(3), []byte{0, 1, 2}}, result)
	})

	compatTest(t, "test_write_text_parent_not_found", func(t *testing.T, r *montyRunner) {
		errorName(t, r, "Path('/no/parent/file.txt').write_text('content')", "FileNotFoundError", "FileNotFoundError")
	})

	compatTest(t, "test_write_text_to_directory", func(t *testing.T, r *montyRunner) {
		r.writeFile("mydir/file.txt", "content")
		errorName(t, r, "Path('/mydir').write_text('content')", "IsADirectoryError", "IsADirectoryError")
	})

	compatTest(t, "test_environ_key_access", func(t *testing.T, r *montyRunner) {
		r.setEnviron(map[string]string{"MY_VAR": "my_value"})
		require.Equal(t, "my_value", r.mustRunCode("os.environ['MY_VAR']"))
	})

	compatTest(t, "test_environ_get_method", func(t *testing.T, r *montyRunner) {
		r.setEnviron(map[string]string{"MY_VAR": "my_value"})
		require.Equal(t, "my_value", r.mustRunCode("os.environ.get('MY_VAR')"))
	})

	compatTest(t, "test_environ_get_missing_with_default", func(t *testing.T, r *montyRunner) {
		r.setEnviron(map[string]string{})
		require.Equal(t, "fallback", r.mustRunCode("os.environ.get('MISSING', 'fallback')"))
	})

	compatTest(t, "test_environ_missing_key_raises_keyerror", func(t *testing.T, r *montyRunner) {
		r.setEnviron(map[string]string{})
		result := r.mustRunCode("result = None\ntry:\n    os.environ['NONEXISTENT_KEY']\nexcept KeyError as e:\n    result = str(e)\nresult\n")
		require.Equal(t, "'NONEXISTENT_KEY'", result)
	})
}
