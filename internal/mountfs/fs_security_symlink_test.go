//go:build unix

package mountfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
)

var sec2HostAbsolutePayloads = []string{
	`/mnt/C:\Windows\System32\drivers\etc\hosts`,
	`/mnt/C:`,
	`/mnt/C:/Windows`,
	`/mnt/\\monty-test.invalid\share\probe`,
	`/mnt/back\slash`,
	`/mnt/sub/../C:\escape`,
	`/mnt/sub/nested\..\..\escape`,
}

func sec2RequireKind(t *testing.T, tbl *Table, call *wire.OsCall, kind errKind) {
	t.Helper()
	result, e, handled := dispatch(tbl, call)
	require.True(t, handled, "expected handled for %s %q", call.Name(), call.Path)
	require.NotNil(t, e, "expected error kind %d for %s %q, got Ok(%#v)", kind, call.Name(), call.Path, result)
	require.Equal(t, kind, e.kind, "wrong error variant for %s %q: %v", call.Name(), call.Path, e)
}

func sec2Names(t *testing.T, v any) []string { return sortedNames(t, v) }

// sec2RefusedCall builds the call assert_refused_before_io issues for an op.
func sec2RefusedCall(op wire.OsOp, path string) *wire.OsCall {
	switch op {
	case wire.OpMkdir:
		return mkdirCall(path, true, false)
	case wire.OpWriteText:
		return writeTextCall(path, "attack")
	case wire.OpWriteBytes:
		return writeBytesCall(path, []byte("attack"))
	}
	return pathCall(op, path)
}

func sec2RequireRefusedBeforeIO(t *testing.T, tbl *Table, op wire.OsOp, path, modeName string) {
	t.Helper()
	call := sec2RefusedCall(op, path)
	result, e, handled := dispatch(tbl, call)
	if handled && e != nil && (e.kind == errPathEscape || e.kind == errReadOnly) {
		return
	}
	t.Fatalf("[%s] expected refusal for %s on %s, got handled=%v result=%#v err=%v", modeName, call.Name(), path, handled, result, e)
}

func sec2OutcomeClass(result any, e *mountError, handled bool) string {
	switch {
	case !handled:
		return "NotHandled"
	case e == nil:
		if b, ok := result.(bool); ok {
			return fmt.Sprintf("Ok(Bool(%v))", b)
		}
		return fmt.Sprintf("Ok(%#v)", result)
	case e.kind == errPathEscape:
		return "PathEscape"
	case e.kind == errNoMountPoint:
		return "NoMountPoint"
	case e.kind == errIO:
		return fmt.Sprintf("Io(%d)", e.io)
	}
	return "Other(" + e.Error() + ")"
}

func sec2WriteRawName(dir, name string, data []byte) error {
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

func TestFSSecuritySymlinks(t *testing.T) {
	t.Run("symlink_to_outside_directory", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		writeHostFile(t, outside, "secret.txt", "secret data")
		symlinkT(t, outside, filepath.Join(dir, "escape_link"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		requireBlocked(t, tbl, pathCall(wire.OpReadText, "/mnt/escape_link/secret.txt"))
		requireInvisible(t, tbl, pathCall(wire.OpExists, "/mnt/escape_link/secret.txt"))
		requireBlocked(t, tbl, pathCall(wire.OpIterdir, "/mnt/escape_link"))
	})

	t.Run("symlink_to_outside_file", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		writeHostFile(t, outside, "secret.txt", "secret")
		symlinkT(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, "link_to_file"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		requireBlocked(t, tbl, pathCall(wire.OpReadText, "/mnt/link_to_file"))
	})

	t.Run("symlink_open_escape", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		writeHostFile(t, outside, "secret.txt", "secret")
		symlinkT(t, outside, filepath.Join(dir, "escape_link"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		requireBlocked(t, tbl, openCall("/mnt/escape_link/secret.txt", "r"))
		requireBlocked(t, tbl, openCall("/mnt/escape_link/new.txt", "w"))
		requireBlocked(t, tbl, openCall("/mnt/escape_link/new.txt", "a"))
	})

	t.Run("symlink_to_parent", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, filepath.Dir(dir), filepath.Join(dir, "parent_link"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		requireBlocked(t, tbl, pathCall(wire.OpIterdir, "/mnt/parent_link"))
	})

	t.Run("relative_symlink_escape", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "../../", filepath.Join(dir, "rel_escape"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		requireBlocked(t, tbl, pathCall(wire.OpIterdir, "/mnt/rel_escape"))
	})

	t.Run("is_symlink_reports_an_outbound_link_that_lives_in_the_mount", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		writeHostFile(t, outside, "secret.txt", "secret")
		symlinkT(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, "escape_link"))

		for _, mode := range []Mode{ReadWrite, Overlay} {
			tbl := mountAtMnt(t, dir, mode)
			require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpIsSymlink, "/mnt/escape_link")), "mode %v", mode)
			requireInvisible(t, tbl, pathCall(wire.OpExists, "/mnt/escape_link"))
			requireInvisible(t, tbl, pathCall(wire.OpIsFile, "/mnt/escape_link"))
		}
	})

	t.Run("symlink_escape_no_info_leak", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		symlinkT(t, outside, filepath.Join(dir, "escape"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		result, e, handled := dispatch(tbl, pathCall(wire.OpReadText, "/mnt/escape/secret"))
		require.True(t, handled)
		require.NotNil(t, e, "expected error, got %#v", result)
		require.NotContains(t, e.Error(), dir, "error message should not contain host path")
	})

	t.Run("symlink_escape_overlay_memory", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		writeHostFile(t, outside, "secret.txt", "secret")
		symlinkT(t, outside, filepath.Join(dir, "escape"))

		tbl := mountAtMnt(t, dir, Overlay)
		requireBlocked(t, tbl, pathCall(wire.OpReadText, "/mnt/escape/secret.txt"))
		requireInvisible(t, tbl, pathCall(wire.OpExists, "/mnt/escape/secret.txt"))
	})

	t.Run("symlink_within_mount_allowed", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "hello.txt", filepath.Join(dir, "internal_link"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/internal_link")))
	})

	t.Run("symlink_to_directory_within_mount_allowed", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "subdir", filepath.Join(dir, "dir_link"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/dir_link/nested.txt")))
		callOK(t, tbl, pathCall(wire.OpIterdir, "/mnt/dir_link"))
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/dir_link/deep/file.txt")))
	})

	t.Run("chained_symlinks_within_mount_allowed", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "hello.txt", filepath.Join(dir, "link1"))
		symlinkT(t, "link1", filepath.Join(dir, "link2"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/link2")))
	})

	t.Run("chained_symlinks_escape_blocked", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		writeHostFile(t, outside, "secret.txt", "secret")
		symlinkT(t, outside, filepath.Join(dir, "link1"))
		symlinkT(t, filepath.Join(dir, "link1"), filepath.Join(dir, "link2"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		requireBlocked(t, tbl, pathCall(wire.OpReadText, "/mnt/link2/secret.txt"))
	})

	t.Run("mkdir_parents_through_symlink_escape_blocked_readwrite", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		symlinkT(t, outside, filepath.Join(dir, "escape"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		result, e, handled := dispatch(tbl, mkdirCall("/mnt/escape/pwned", true, true))
		require.True(t, handled)
		require.NotNil(t, e, "mkdir through symlink escape should be blocked, got %#v", result)
		require.Contains(t, []errKind{errPathEscape, errIO}, e.kind, "unexpected result: %v", e)
		require.NoDirExists(t, filepath.Join(outside, "pwned"), "directory was created outside the mount!")
	})

	t.Run("mkdir_parents_through_symlink_escape_blocked_readonly", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		symlinkT(t, outside, filepath.Join(dir, "escape"))

		tbl := mountAtMnt(t, dir, ReadOnly)
		result, e, handled := dispatch(tbl, mkdirCall("/mnt/escape/pwned", true, true))
		require.True(t, handled)
		require.NotNil(t, e, "mkdir through symlink escape should be blocked, got %#v", result)
		require.Contains(t, []errKind{errPathEscape, errIO, errReadOnly}, e.kind, "unexpected result: %v", e)
		require.NoDirExists(t, filepath.Join(outside, "pwned"), "directory was created outside the mount!")
	})

	t.Run("mkdir_parents_through_nested_symlink_escape_blocked", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		symlinkT(t, outside, filepath.Join(dir, "subdir", "link"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		result, e, handled := dispatch(tbl, mkdirCall("/mnt/subdir/link/deep/dir", true, true))
		require.True(t, handled)
		require.NotNil(t, e, "mkdir through nested symlink escape should be blocked, got %#v", result)
		require.Contains(t, []errKind{errPathEscape, errIO}, e.kind, "unexpected result: %v", e)
		require.NoDirExists(t, filepath.Join(outside, "deep"), "directory was created outside the mount!")
	})

	t.Run("mkdir_parents_within_mount_allowed", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)

		callOK(t, tbl, mkdirCall("/mnt/new/nested/dir", true, true))
		require.DirExists(t, filepath.Join(dir, "new/nested/dir"))
	})

	t.Run("mkdir_parents_through_internal_symlink_allowed", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		symlinkT(t, "subdir", filepath.Join(dir, "internal_link"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		callOK(t, tbl, mkdirCall("/mnt/internal_link/new_child", true, true))
		require.DirExists(t, filepath.Join(dir, "subdir/new_child"))
	})

	t.Run("overlay_symlink_is_refused_at_every_depth_of_a_deep_chain", func(t *testing.T) {
		const deepChain = 40
		names := make([]string, deepChain)
		for i := range names {
			names[i] = fmt.Sprintf("d%d", i)
		}
		chain := strings.Join(names, "/")

		dir := createSecurityTestDir(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, chain), 0o755))
		writeHostFile(t, dir, chain+"/leaf.txt", "deep")
		tbl := mountAtMnt(t, dir, Overlay)
		require.Equal(t, "deep", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/"+chain+"/leaf.txt")))

		for swapped := 0; swapped < deepChain; swapped++ {
			dir := createSecurityTestDir(t)
			parent := filepath.Join(dir, strings.Join(names[:swapped], "/"))
			realDir := filepath.Join(parent, "real", strings.Join(names[swapped+1:], "/"))
			require.NoError(t, os.MkdirAll(realDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(realDir, "leaf.txt"), []byte("deep"), 0o644))
			symlinkT(t, "real", filepath.Join(parent, names[swapped]))

			tbl := mountAtMnt(t, dir, Overlay)
			vpath := "/mnt/" + chain + "/leaf.txt"
			_, e, handled := dispatch(tbl, pathCall(wire.OpReadText, vpath))
			require.True(t, handled)
			require.NotNil(t, e, "a link at depth %d must be refused", swapped)
			require.Equal(t, errPathEscape, e.kind, "a link at depth %d must be refused, got %v", swapped, e)
			require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, vpath)), "a link at depth %d must make the path invisible", swapped)
		}
	})

	t.Run("hard_link_within_mount_allowed", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		require.NoError(t, os.Link(filepath.Join(dir, "hello.txt"), filepath.Join(dir, "hardlink.txt")))

		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hardlink.txt")))
	})

	t.Run("hard_link_from_outside_accessible", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		outsideFile := filepath.Join(outside, "external.txt")
		require.NoError(t, os.WriteFile(outsideFile, []byte("external content"), 0o644))
		require.NoError(t, os.Link(outsideFile, filepath.Join(dir, "hardlink_ext.txt")))

		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "external content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hardlink_ext.txt")))
	})

	t.Run("hard_link_is_not_detected_as_symlink", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		require.NoError(t, os.Link(filepath.Join(dir, "hello.txt"), filepath.Join(dir, "hardlink.txt")))

		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpIsFile, "/mnt/hardlink.txt")))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpIsSymlink, "/mnt/hardlink.txt")))
	})

	t.Run("broken_symlink_write_escape_blocked", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		escapeTarget := filepath.Join(outside, "pwned.txt")
		symlinkT(t, escapeTarget, filepath.Join(dir, "broken_link.txt"))

		_, statErr := os.Stat(filepath.Join(dir, "broken_link.txt"))
		require.Error(t, statErr)
		_, lstatErr := os.Lstat(filepath.Join(dir, "broken_link.txt"))
		require.NoError(t, lstatErr)

		tbl := mountAtMnt(t, dir, ReadWrite)
		requireBlocked(t, tbl, writeTextCall("/mnt/broken_link.txt", "attack"))
		requireBlocked(t, tbl, writeBytesCall("/mnt/broken_link.txt", []byte("attack")))
		require.NoFileExists(t, escapeTarget, "broken symlink write escape: file was created outside the mount!")
	})

	t.Run("broken_symlink_overlay_write_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		escapeTarget := filepath.Join(outside, "pwned.txt")
		symlinkT(t, escapeTarget, filepath.Join(dir, "broken_link.txt"))

		tbl := mountAtMnt(t, dir, Overlay)
		sec2RequireKind(t, tbl, writeTextCall("/mnt/broken_link.txt", "safe"), errPathEscape)
		require.NoFileExists(t, escapeTarget)
	})

	t.Run("iterdir_filters_outbound_symlinks_but_keeps_regular_and_inbound", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		writeHostFile(t, outside, "external.txt", "external")
		symlinkT(t, filepath.Join(outside, "external.txt"), filepath.Join(dir, "escape_link"))
		symlinkT(t, "hello.txt", filepath.Join(dir, "internal_link"))

		tbl := mountAtMnt(t, dir, ReadWrite)
		names := sec2Names(t, callOK(t, tbl, pathCall(wire.OpIterdir, "/mnt")))
		require.NotContains(t, names, "escape_link", "outbound symlink should be filtered from iterdir")
		require.Contains(t, names, "internal_link", "inbound symlink should be kept in iterdir")
		require.Contains(t, names, "hello.txt", "regular files should be present")
	})

	t.Run("iterdir_keeps_inbound_symlink_with_non_utf8_name", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		rawName := "nonutf8\xff_link"
		if err := os.Symlink("hello.txt", filepath.Join(dir, rawName)); err != nil {
			t.Skip("skipped: filesystem rejects non-UTF-8 filenames")
		}

		tbl := mountAtMnt(t, dir, ReadWrite)
		names := sec2Names(t, callOK(t, tbl, pathCall(wire.OpIterdir, "/mnt")))
		require.Contains(t, names, "nonutf8\uFFFD_link", "in-mount symlink with a non-UTF-8 name was dropped: %v", names)
	})

	t.Run("overlay_rename_of_dir_with_non_utf8_named_entry_is_refused", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		if err := sec2WriteRawName(filepath.Join(dir, "subdir"), "nonutf8\xff.txt", []byte("data")); err != nil {
			t.Skip("skipped: filesystem rejects non-UTF-8 filenames")
		}

		tbl := mountAtMnt(t, dir, Overlay)
		_, e, handled := dispatch(tbl, renameCall("/mnt/subdir", "/mnt/moved"))
		require.True(t, handled)
		require.NotNil(t, e, "rename over a non-UTF-8 name must fail with InvalidData")
		require.Equal(t, errIO, e.kind, "rename over a non-UTF-8 name must fail with InvalidData, got %v", e)
		require.Equal(t, ioInvalidData, e.io, "rename over a non-UTF-8 name must fail with InvalidData, got %v", e)

		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/subdir/nested.txt")))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/moved")))
	})

	t.Run("overlay_iterdir_filters_symlinks_like_direct_mode", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		outside := t.TempDir()
		writeHostFile(t, outside, "external.txt", "external")
		symlinkT(t, filepath.Join(outside, "external.txt"), filepath.Join(dir, "escape_link"))
		symlinkT(t, filepath.Join(outside, "missing.txt"), filepath.Join(dir, "broken_link"))
		symlinkT(t, "hello.txt", filepath.Join(dir, "internal_link"))

		direct := mountAtMnt(t, dir, ReadWrite)
		directNames := sec2Names(t, callOK(t, direct, pathCall(wire.OpIterdir, "/mnt")))
		overlay := mountAtMnt(t, dir, Overlay)
		overlayNames := sec2Names(t, callOK(t, overlay, pathCall(wire.OpIterdir, "/mnt")))

		require.Equal(t, directNames, overlayNames)
		require.Contains(t, overlayNames, "internal_link")
		require.NotContains(t, overlayNames, "escape_link")
		require.NotContains(t, overlayNames, "broken_link")
	})

	t.Run("double_slashes", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt//hello.txt")))
	})

	t.Run("dot_components", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/./hello.txt")))
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/./subdir/./nested.txt")))
	})

	t.Run("triple_dots_literal_name", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		result, e, handled := dispatch(tbl, pathCall(wire.OpExists, "/mnt/..."))
		require.True(t, handled, "expected false or Io error for /mnt/...")
		if e != nil {
			require.Equal(t, errIO, e.kind, "expected false or Io error for /mnt/..., got %v", e)
			return
		}
		require.Equal(t, false, result)
	})

	t.Run("mount_relative_virtual_path_rejected", func(t *testing.T) {
		_, err := OpenRoot("relative/path", t.TempDir())
		require.Error(t, err)
		me, ok := err.(*mountError)
		require.True(t, ok, "expected *mountError, got %T", err)
		require.Equal(t, errInvalidMount, me.kind)
	})

	t.Run("mount_nonexistent_host_path", func(t *testing.T) {
		_, err := OpenRoot("/mnt", "/nonexistent/path/that/does/not/exist")
		require.Error(t, err)
		me, ok := err.(*mountError)
		require.True(t, ok, "expected *mountError, got %T", err)
		require.Equal(t, errInvalidMount, me.kind)
	})

	t.Run("mount_file_as_host_path", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "not_a_dir.txt")
		require.NoError(t, os.WriteFile(filePath, []byte("content"), 0o644))

		_, err := OpenRoot("/mnt", filePath)
		require.Error(t, err)
		me, ok := err.(*mountError)
		require.True(t, ok, "expected *mountError, got %T", err)
		require.Equal(t, errInvalidMount, me.kind)
	})

	t.Run("path_escape_error_only_contains_virtual_path", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		_, e, handled := dispatch(tbl, pathCall(wire.OpReadText, "/mnt/C:/evil"))
		require.True(t, handled)
		require.NotNil(t, e, "expected PathEscape")
		require.Equal(t, errPathEscape, e.kind, "expected PathEscape, got %v", e)
		require.Equal(t, "/mnt/C:/evil", e.path)
		require.NotContains(t, e.path, dir, "PathEscape should not contain host path")
	})

	t.Run("null_byte_error_quotes_no_path", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		exc := callErr(t, tbl, pathCall(wire.OpReadText, "/mnt/evil\x00.txt"))
		require.NotNil(t, exc.Message, "exception should have message")
		msg := exc.MessageText()
		require.Equal(t, "embedded null byte", msg)
		require.NotContains(t, msg, "\x00", "the message must not echo the path back")
	})

	t.Run("no_mount_point_returns_none", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		requireNotHandled(t, tbl, pathCall(wire.OpReadText, "/outside/secret.txt"))
	})

	t.Run("error_into_exception_preserves_virtual_path", func(t *testing.T) {
		exc := pathEscape("/mnt/evil").exception()
		require.NotNil(t, exc.Message, "exception should have message")
		msg := exc.MessageText()
		require.Contains(t, msg, "/mnt/evil")
		require.NotContains(t, msg, "/tmp/", "should not contain tmp host paths")
		require.NotContains(t, msg, "/var/", "should not contain var host paths")
	})

	t.Run("empty_table_all_ops_unhandled", func(t *testing.T) {
		tbl := NewTable(nil)
		for _, op := range []wire.OsOp{wire.OpExists, wire.OpIsFile, wire.OpIsDir, wire.OpReadText, wire.OpStat, wire.OpIterdir} {
			requireNotHandled(t, tbl, pathCall(op, "/any/path"))
		}
	})

	t.Run("rename_traversal_src", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		sec2RequireKind(t, tbl, renameCall("/mnt/../etc/passwd", "/mnt/stolen.txt"), errCrossMount)
	})

	t.Run("rename_traversal_dst", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		sec2RequireKind(t, tbl, renameCall("/mnt/hello.txt", "/mnt/../escape.txt"), errCrossMount)
	})

	t.Run("rename_symlink_escape_overlay_read_text", func(t *testing.T) {
		mountDir := t.TempDir()
		outsideDir := t.TempDir()
		const secret = "TOP SECRET CONTENT"
		writeHostFile(t, outsideDir, "secret.txt", secret)
		symlinkT(t, filepath.Join(outsideDir, "secret.txt"), filepath.Join(mountDir, "escape_link"))

		tbl := mountAtMnt(t, mountDir, Overlay)
		_, renameErr, renamed := dispatch(tbl, renameCall("/mnt/escape_link", "/mnt/renamed"))
		if !renamed || renameErr != nil {
			return
		}
		result, e, handled := dispatch(tbl, pathCall(wire.OpReadText, "/mnt/renamed"))
		if !handled || e != nil {
			return
		}
		content, ok := result.(string)
		require.True(t, ok, "unexpected read result: %#v", result)
		require.NotEqual(t, secret, content, "SECURITY: overlay read_text leaked file contents from outside the mount boundary via a renamed symlink")
	})

	t.Run("rename_symlink_escape_overlay_read_bytes", func(t *testing.T) {
		mountDir := t.TempDir()
		outsideDir := t.TempDir()
		secret := []byte("TOP SECRET BYTES")
		require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "secret.bin"), secret, 0o644))
		symlinkT(t, filepath.Join(outsideDir, "secret.bin"), filepath.Join(mountDir, "escape_link"))

		tbl := mountAtMnt(t, mountDir, Overlay)
		_, renameErr, renamed := dispatch(tbl, renameCall("/mnt/escape_link", "/mnt/renamed"))
		if !renamed || renameErr != nil {
			return
		}
		result, e, handled := dispatch(tbl, pathCall(wire.OpReadBytes, "/mnt/renamed"))
		if !handled || e != nil {
			return
		}
		content, ok := result.([]byte)
		require.True(t, ok, "unexpected read result: %#v", result)
		require.NotEqual(t, secret, content, "SECURITY: overlay read_bytes leaked file contents from outside the mount boundary via a renamed symlink")
	})

	t.Run("host_absolute_segment_rejected_in_all_modes", func(t *testing.T) {
		for _, payload := range sec2HostAbsolutePayloads {
			for _, m := range allModes {
				dir := createSecurityTestDir(t)
				tbl := mountAtMnt(t, dir, m.mode)
				sec2RequireRefusedBeforeIO(t, tbl, wire.OpExists, payload, m.name)
			}
		}
	})

	t.Run("host_absolute_segment_rejected_for_every_operation", func(t *testing.T) {
		ops := []wire.OsOp{
			wire.OpExists, wire.OpIsFile, wire.OpIsDir, wire.OpIsSymlink, wire.OpReadText, wire.OpReadBytes,
			wire.OpStat, wire.OpIterdir, wire.OpUnlink, wire.OpMkdir, wire.OpWriteText, wire.OpWriteBytes,
		}
		for _, op := range ops {
			for _, m := range allModes {
				dir := createSecurityTestDir(t)
				tbl := mountAtMnt(t, dir, m.mode)
				sec2RequireRefusedBeforeIO(t, tbl, op, `/mnt/C:\Windows\x`, m.name)
			}
		}
	})

	t.Run("host_absolute_mkdir_parents_is_rejected", func(t *testing.T) {
		for _, m := range allModes {
			dir := createSecurityTestDir(t)
			tbl := mountAtMnt(t, dir, m.mode)
			sec2RequireRefusedBeforeIO(t, tbl, wire.OpMkdir, `/mnt/C:\Temp\pwned`, m.name)
		}
	})

	t.Run("posix_absolute_segment_is_confined_inside_the_mount", func(t *testing.T) {
		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		callOK(t, tbl, mkdirCall("/mnt//etc/monty_probe", true, false))
		require.DirExists(t, filepath.Join(dir, "etc", "monty_probe"), "expected creation inside the mount")
	})

	t.Run("mkdir_parents_creates_nothing_at_the_host_location", func(t *testing.T) {
		outside := t.TempDir()
		target := filepath.Join(outside, "pwned")
		require.NoDirExists(t, target, "precondition: target must not pre-exist")
		payload := "/mnt/" + target

		dir := createSecurityTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		_, e, handled := dispatch(tbl, mkdirCall(payload, true, false))

		require.NoDirExists(t, target, "SANDBOX ESCAPE: mkdir created %s outside the mount", target)
		if handled && e == nil {
			nested := filepath.Join(dir, strings.TrimPrefix(target, "/"))
			require.DirExists(t, nested, "expected the path to be created inside the mount")
		}
	})

	t.Run("host_absolute_exists_is_not_an_oracle", func(t *testing.T) {
		payloads := []string{`/mnt/C:\Windows\System32\ntdll.dll`, `/mnt/C:\Windows\no_such_file_xyz`}
		for _, m := range allModes {
			dir := createSecurityTestDir(t)
			tbl := mountAtMnt(t, dir, m.mode)
			outcomes := make([]string, len(payloads))
			for i, payload := range payloads {
				outcomes[i] = sec2OutcomeClass(dispatch(tbl, pathCall(wire.OpExists, payload)))
			}
			require.Equal(t, outcomes[0], outcomes[1], "[%s] existence oracle: present and absent out-of-mount paths differ", m.name)
		}
	})

	t.Run("exists_still_discriminates_inside_the_mount", func(t *testing.T) {
		for _, m := range allModes {
			dir := createSecurityTestDir(t)
			tbl := mountAtMnt(t, dir, m.mode)
			require.Equal(t, "Ok(Bool(true))", sec2OutcomeClass(dispatch(tbl, pathCall(wire.OpExists, "/mnt/hello.txt"))), "[%s] an in-mount file should be visible", m.name)
			require.Equal(t, "Ok(Bool(false))", sec2OutcomeClass(dispatch(tbl, pathCall(wire.OpExists, "/mnt/no_such_file.txt"))), "[%s] a missing in-mount file should report false", m.name)
		}
	})

	t.Run("host_absolute_segment_is_not_an_overlay_key", func(t *testing.T) {
		for _, payload := range sec2HostAbsolutePayloads {
			dir := createSecurityTestDir(t)
			tbl := mountAtMnt(t, dir, Overlay)
			sec2RequireRefusedBeforeIO(t, tbl, wire.OpWriteText, payload, "OverlayMemory")
			sec2RequireRefusedBeforeIO(t, tbl, wire.OpExists, payload, "OverlayMemory")
			sec2RequireRefusedBeforeIO(t, tbl, wire.OpReadText, payload, "OverlayMemory")
		}
	})

	t.Run("colon_names_that_are_not_drive_prefixes_are_not_rejected_by_the_check", func(t *testing.T) {
		for _, name := range []string{"note:2026.txt", "::double.txt", "ab:cd.txt", "log:12:30.txt"} {
			dir := createSecurityTestDir(t)
			tbl := mountAtMnt(t, dir, ReadWrite)
			path := "/mnt/" + name
			_, e, handled := dispatch(tbl, writeTextCall(path, "ok"))
			require.False(t, handled && e != nil && e.kind == errPathEscape, "the boundary check should not reject %s, got %v", name, e)
			if handled && e == nil {
				result, readErr, readHandled := dispatch(tbl, pathCall(wire.OpReadText, path))
				require.True(t, readHandled && readErr == nil && result == "ok", "reading %s should round-trip, got %#v / %v", name, result, readErr)
			}
		}
	})

	t.Run("single_letter_colon_names_are_refused_on_all_hosts", func(t *testing.T) {
		for _, name := range []string{"a:b.txt", "C:.txt", "z:"} {
			dir := createSecurityTestDir(t)
			tbl := mountAtMnt(t, dir, ReadWrite)
			sec2RequireRefusedBeforeIO(t, tbl, wire.OpWriteText, "/mnt/"+name, "ReadWrite")
		}
	})
}
