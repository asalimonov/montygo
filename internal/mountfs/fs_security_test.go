//go:build unix

package mountfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
)

func sec1NullByteError(t *testing.T, tbl *Table, call *wire.OsCall, expected string) {
	t.Helper()
	_, e, handled := dispatch(tbl, call)
	require.True(t, handled, "a null byte is refused with or without a mount")
	require.NotNil(t, e, "expected a ValueError")
	exc := e.exception()
	require.Equal(t, "ValueError", exc.ExcType)
	require.Equal(t, expected, exc.MessageText())
}

func sec1RequirePathEscape(t *testing.T, tbl *Table, call *wire.OsCall, context string) {
	t.Helper()
	result, e, handled := dispatch(tbl, call)
	require.True(t, handled, context)
	require.NotNil(t, e, "%s: got Ok(%#v)", context, result)
	require.Equal(t, errPathEscape, e.kind, "%s: got %v", context, e)
}

func sec1ReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func sec1Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestFSSecurity(t *testing.T) {
	t.Run("traversal_dotdot_from_root", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, pathCall(wire.OpReadText, "/mnt/../etc/passwd"))
			requireBlocked(t, tbl, pathCall(wire.OpExists, "/mnt/../etc/passwd"))
		}
	})

	t.Run("traversal_dotdot_from_subdir", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, pathCall(wire.OpReadText, "/mnt/subdir/../../etc/passwd"))
		}
	})

	t.Run("traversal_many_dotdots", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, pathCall(wire.OpReadText, "/mnt/a/../../../../../../../etc/passwd"))
		}
	})

	t.Run("traversal_dotdot_write_text", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, writeTextCall("/mnt/../escape.txt", "attack"))
		}
	})

	t.Run("traversal_dotdot_write_bytes", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, writeBytesCall("/mnt/../escape.bin", []byte("attack")))
		}
	})

	t.Run("traversal_dotdot_open", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, openCall("/mnt/../open_escape.txt", "w"))
			requireBlocked(t, tbl, openCall("/mnt/../open_escape.txt", "a"))
			requireBlocked(t, tbl, openCall("/mnt/../../etc/passwd", "r"))
		}
	})

	t.Run("traversal_dotdot_mkdir", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, mkdirCall("/mnt/../escape_dir", false, false))
		}
	})

	t.Run("traversal_dotdot_unlink", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, pathCall(wire.OpUnlink, "/mnt/../some_file"))
		}
	})

	t.Run("traversal_dotdot_stat", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, pathCall(wire.OpStat, "/mnt/../etc/passwd"))
		}
	})

	t.Run("traversal_dotdot_iterdir", func(t *testing.T) {
		for _, m := range allModes {
			tbl := mountAtMnt(t, createSecurityTestDir(t), m.mode)
			requireBlocked(t, tbl, pathCall(wire.OpIterdir, "/mnt/.."))
		}
	})

	t.Run("valid_dotdot_within_mount", func(t *testing.T) {
		tbl := mountAtMnt(t, createSecurityTestDir(t), ReadWrite)
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/subdir/../hello.txt")))
	})

	t.Run("null_byte_position_does_not_matter", func(t *testing.T) {
		tbl := mountAtMnt(t, createSecurityTestDir(t), ReadWrite)
		for _, path := range []string{
			"/mnt/hello\x00.txt",
			"/mnt/\x00hello.txt",
			"/mnt/hello.txt\x00",
			"/mnt/sub\x00dir/nested.txt",
		} {
			sec1NullByteError(t, tbl, pathCall(wire.OpReadText, path), "embedded null byte")
		}
	})

	t.Run("null_byte_messages_match_cpython_per_operation", func(t *testing.T) {
		tbl := mountAtMnt(t, createSecurityTestDir(t), ReadWrite)
		bad := "/mnt/evil\x00.txt"
		for _, c := range []struct {
			op       wire.OsOp
			expected string
		}{
			{wire.OpReadText, "embedded null byte"},
			{wire.OpReadBytes, "embedded null byte"},
			{wire.OpStat, "stat: embedded null character in path"},
			{wire.OpIterdir, "scandir: embedded null character in path"},
			{wire.OpUnlink, "unlink: embedded null character in path"},
		} {
			sec1NullByteError(t, tbl, pathCall(c.op, bad), c.expected)
		}
		sec1NullByteError(t, tbl, pathCall(wire.OpRmdir, bad), "rmdir: embedded null character in path")
		sec1NullByteError(t, tbl, mkdirCall(bad, false, false), "mkdir: embedded null character in path")
		sec1NullByteError(t, tbl, renameCall(bad, "/mnt/ok.txt"), "rename: embedded null character in src")
		sec1NullByteError(t, tbl, renameCall("/mnt/hello.txt", bad), "rename: embedded null character in dst")
	})

	t.Run("null_byte_messages_for_resolve_and_absolute", func(t *testing.T) {
		tbl := mountAtMnt(t, createSecurityTestDir(t), ReadWrite)
		bad := "/mnt/evil\x00.txt"
		sec1NullByteError(t, tbl, pathCall(wire.OpResolve, bad), "lstat: embedded null character in path")
		sec1NullByteError(t, tbl, pathCall(wire.OpAbsolute, bad), "embedded null byte")
	})

	t.Run("overlong_path_outranks_a_null_byte", func(t *testing.T) {
		tbl := mountAtMnt(t, createSecurityTestDir(t), ReadWrite)
		bad := "/mnt/" + strings.Repeat("a", 256) + "\x00.txt"
		for _, call := range []*wire.OsCall{
			pathCall(wire.OpStat, bad),
			renameCall("/mnt/hello.txt", bad),
		} {
			_, e, handled := dispatch(tbl, call)
			require.True(t, handled, "handled without routing")
			require.NotNil(t, e, "expected an OSError")
			exc := e.exception()
			require.Equal(t, "OSError", exc.ExcType)
			require.Equal(t, `[Errno 36] File name too long: '/mnt/aaaaaaaaaaaaaaa…aaaaaaaaaaaaaaa\x00.txt'`, exc.MessageText())
		}
	})

	t.Run("too_many_components_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		atLimit := "/mnt/" + strings.Join(sec1Repeat("d", 62), "/")
		overLimit := "/mnt/" + strings.Join(sec1Repeat("d", 63), "/")
		for _, m := range allModes {
			tbl := mountAtMnt(t, dir, m.mode)
			require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, atLimit)))
			require.Equal(t, "FileNotFoundError", callErr(t, tbl, pathCall(wire.OpStat, atLimit)).ExcType)

			exc := callErr(t, tbl, pathCall(wire.OpStat, overLimit))
			require.Equal(t, "OSError", exc.ExcType)
			require.True(t, strings.HasPrefix(exc.MessageText(), "[Errno 36] File name too long"), "got %q", exc.MessageText())
			require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, overLimit)))
		}

		empty := NewTable(nil)
		_, e, handled := dispatch(empty, renameCall("/nowhere/a.txt", overLimit))
		require.True(t, handled, "a too-deep destination is refused without a mount")
		require.NotNil(t, e)
		require.Equal(t, errIO, e.kind, "got %v", e)
	})

	t.Run("null_byte_predicates_answer_false", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		for _, m := range allModes {
			tbl := mountAtMnt(t, dir, m.mode)
			for _, op := range []wire.OsOp{wire.OpExists, wire.OpIsFile, wire.OpIsDir, wire.OpIsSymlink} {
				requireInvisible(t, tbl, pathCall(op, "/mnt/hello\x00.txt"))
			}
		}
	})

	t.Run("null_byte_is_refused_without_a_mount", func(t *testing.T) {
		tbl := NewTable(nil)
		sec1NullByteError(t, tbl, pathCall(wire.OpReadText, "/nowhere/evil\x00.txt"), "embedded null byte")
		requireInvisible(t, tbl, pathCall(wire.OpExists, "/nowhere/evil\x00.txt"))
	})

	t.Run("null_byte_write_ops_in_every_mode", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		for _, m := range allModes {
			tbl := mountAtMnt(t, dir, m.mode)
			requireBlocked(t, tbl, writeTextCall("/mnt/evil\x00.txt", "attack"))
			requireBlocked(t, tbl, writeBytesCall("/mnt/evil\x00.bin", []byte("attack")))
			requireBlocked(t, tbl, mkdirCall("/mnt/evil\x00dir", false, false))
			requireBlocked(t, tbl, openCall("/mnt/evil\x00.txt", "w"))
			requireBlocked(t, tbl, openCall("/mnt/evil\x00.txt", "a"))

			requireInvisible(t, tbl, pathCall(wire.OpExists, "/mnt/evil\x00.txt"))
			require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hello.txt")),
				"%s: an unrelated read must still work", m.name)
		}
	})

	t.Run("overlay_write_over_inbound_symlink_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, filepath.Join(dir, "hello.txt"), filepath.Join(dir, "link.txt"))
		tbl := mountAtMnt(t, dir, Overlay)
		sec1RequirePathEscape(t, tbl, writeTextCall("/mnt/link.txt", "aliased"),
			"overlay write over an in-mount symlink must be refused")
		require.Equal(t, "hello world\n", sec1ReadFile(t, filepath.Join(dir, "hello.txt")))
	})

	t.Run("overlay_write_over_dangling_symlink_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, filepath.Join(dir, "missing.txt"), filepath.Join(dir, "dangle.txt"))
		tbl := mountAtMnt(t, dir, Overlay)
		sec1RequirePathEscape(t, tbl, writeTextCall("/mnt/dangle.txt", "ghost"),
			"overlay write over a dangling symlink must be refused")
		require.False(t, sec1Exists(filepath.Join(dir, "missing.txt")), "link target must not be created")
	})

	t.Run("overlay_rename_of_dir_containing_a_symlink_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "nested.txt", filepath.Join(dir, "subdir/link.txt"))
		tbl := mountAtMnt(t, dir, Overlay)
		sec1RequirePathEscape(t, tbl, renameCall("/mnt/subdir", "/mnt/moved"),
			"renaming a directory containing a symlink must be refused")
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/subdir")))
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/subdir/nested.txt")))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/moved")))
	})

	t.Run("overlay_delete_through_a_symlink_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "subdir", filepath.Join(dir, "link_dir"))
		symlinkT(t, "hello.txt", filepath.Join(dir, "link.txt"))
		tbl := mountAtMnt(t, dir, Overlay)
		for _, path := range []string{"/mnt/link_dir/nested.txt", "/mnt/link.txt"} {
			sec1RequirePathEscape(t, tbl, pathCall(wire.OpUnlink, path), "unlink of "+path+" must be refused")
		}
		sec1RequirePathEscape(t, tbl, pathCall(wire.OpRmdir, "/mnt/link_dir"), "rmdir of a symlink must be refused")
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/subdir/nested.txt")))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/link_dir/nested.txt")))
	})

	t.Run("overlay_rename_onto_a_symlink_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "hello.txt", filepath.Join(dir, "link.txt"))
		writeHostFile(t, dir, "src.txt", "moved")
		tbl := mountAtMnt(t, dir, Overlay)
		sec1RequirePathEscape(t, tbl, renameCall("/mnt/src.txt", "/mnt/link.txt"),
			"renaming onto a symlink must be refused")
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hello.txt")))
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/src.txt")))
	})

	t.Run("overlay_rename_out_of_a_symlinked_directory_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "subdir", filepath.Join(dir, "link_dir"))
		tbl := mountAtMnt(t, dir, Overlay)
		sec1RequirePathEscape(t, tbl, renameCall("/mnt/link_dir/nested.txt", "/mnt/moved.txt"),
			"renaming out of a symlinked directory must be refused")
		sec1RequirePathEscape(t, tbl, pathCall(wire.OpUnlink, "/mnt/link_dir/nested.txt"),
			"unlink must agree with rename")
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/subdir/nested.txt")))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/moved.txt")))
	})

	t.Run("overlay_rename_of_a_symlink_to_a_dir_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "subdir", filepath.Join(dir, "link_dir"))
		tbl := mountAtMnt(t, dir, Overlay)
		sec1RequirePathEscape(t, tbl, renameCall("/mnt/link_dir", "/mnt/moved"),
			"renaming a symlink to a directory must be refused")
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/moved")))
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpIsDir, "/mnt/subdir")))
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/subdir/nested.txt")))
	})

	t.Run("overlay_append_open_below_inbound_symlink_dir_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "subdir", filepath.Join(dir, "link_dir"))
		tbl := mountAtMnt(t, dir, Overlay)
		requireBlocked(t, tbl, openCall("/mnt/link_dir/nested.txt", "a"))
		require.Equal(t, "nested content", sec1ReadFile(t, filepath.Join(dir, "subdir/nested.txt")))
	})

	t.Run("overlay_write_through_inbound_symlink_dir_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "subdir", filepath.Join(dir, "link_dir"))
		tbl := mountAtMnt(t, dir, Overlay)
		sec1RequirePathEscape(t, tbl, mkdirCall("/mnt/link_dir/new", true, false),
			"overlay mkdir through an in-mount symlink dir must be refused")
		sec1RequirePathEscape(t, tbl, writeTextCall("/mnt/link_dir/deep/x.txt", "aliased"),
			"overlay write below an in-mount symlink dir must be refused")
		require.False(t, sec1Exists(filepath.Join(dir, "subdir/deep/x.txt")), "host must be untouched")
	})
}

func sec1Repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}
