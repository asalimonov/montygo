//go:build unix

package mountfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

func fsaHostExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fsaReadHost(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestFS(t *testing.T) {
	exists := func(p string) *wire.OsCall { return pathCall(wire.OpExists, p) }
	isFile := func(p string) *wire.OsCall { return pathCall(wire.OpIsFile, p) }
	isDir := func(p string) *wire.OsCall { return pathCall(wire.OpIsDir, p) }
	isSymlink := func(p string) *wire.OsCall { return pathCall(wire.OpIsSymlink, p) }
	readText := func(p string) *wire.OsCall { return pathCall(wire.OpReadText, p) }
	readBytes := func(p string) *wire.OsCall { return pathCall(wire.OpReadBytes, p) }
	stat := func(p string) *wire.OsCall { return pathCall(wire.OpStat, p) }
	iterdir := func(p string) *wire.OsCall { return pathCall(wire.OpIterdir, p) }
	unlink := func(p string) *wire.OsCall { return pathCall(wire.OpUnlink, p) }
	rmdir := func(p string) *wire.OsCall { return pathCall(wire.OpRmdir, p) }

	// ReadWrite mode

	t.Run("rw_exists", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/subdir")))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/nonexistent")))
	})

	t.Run("rw_is_file", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, true, callOK(t, tbl, isFile("/mnt/hello.txt")))
		assert.Equal(t, false, callOK(t, tbl, isFile("/mnt/subdir")))
		assert.Equal(t, false, callOK(t, tbl, isFile("/mnt/nonexistent")))
	})

	t.Run("rw_is_dir", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, true, callOK(t, tbl, isDir("/mnt/subdir")))
		assert.Equal(t, false, callOK(t, tbl, isDir("/mnt/hello.txt")))
		assert.Equal(t, true, callOK(t, tbl, isDir("/mnt/subdir/deep")))
	})

	t.Run("rw_is_symlink", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, false, callOK(t, tbl, isSymlink("/mnt/hello.txt")))
	})

	t.Run("rw_is_symlink_true_for_symlink", func(t *testing.T) {
		dir := createFSTestDir(t)
		symlinkT(t, filepath.Join(dir, "hello.txt"), filepath.Join(dir, "link.txt"))
		tbl := mountAtMnt(t, dir, ReadWrite)
		assert.Equal(t, true, callOK(t, tbl, isSymlink("/mnt/link.txt")))
		assert.Equal(t, false, callOK(t, tbl, isSymlink("/mnt/hello.txt")))
		assert.Equal(t, false, callOK(t, tbl, isSymlink("/mnt/nope.txt")))
	})

	t.Run("overlay_is_symlink_true_for_symlink", func(t *testing.T) {
		dir := createFSTestDir(t)
		symlinkT(t, filepath.Join(dir, "hello.txt"), filepath.Join(dir, "link.txt"))
		tbl := mountAtMnt(t, dir, Overlay)
		assert.Equal(t, true, callOK(t, tbl, isSymlink("/mnt/link.txt")))
		assert.Equal(t, false, callOK(t, tbl, isSymlink("/mnt/hello.txt")))
	})

	t.Run("rw_read_text", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, "hello world\n", callOK(t, tbl, readText("/mnt/hello.txt")))
		assert.Equal(t, "", callOK(t, tbl, readText("/mnt/empty.txt")))
		assert.Equal(t, "nested content", callOK(t, tbl, readText("/mnt/subdir/nested.txt")))
		assert.Equal(t, "deep file", callOK(t, tbl, readText("/mnt/subdir/deep/file.txt")))
	})

	t.Run("rw_read_text_invalid_utf8", func(t *testing.T) {
		dir := createFSTestDir(t)
		writeHostFile(t, dir, "bad_start.txt", "a\xffb")
		writeHostFile(t, dir, "bad_continuation.txt", "a\xe2(b")
		writeHostFile(t, dir, "truncated.txt", "ab\xe2\x82")
		tbl := mountAtMnt(t, dir, ReadWrite)

		exc := callErr(t, tbl, readText("/mnt/bad_start.txt"))
		assertExc(t, exc, "UnicodeDecodeError", "'utf-8' codec can't decode byte 0xff in position 1: invalid start byte")
		require.NotNil(t, exc.Data)
		assert.Equal(t, &wire.UnicodeErrorData{
			Encoding:    "utf-8",
			ObjectBytes: []byte("a\xffb"),
			Start:       1,
			End:         2,
			Reason:      "invalid start byte",
		}, exc.Data.Unicode)
		exc = callErr(t, tbl, readText("/mnt/bad_continuation.txt"))
		assertExc(t, exc, "UnicodeDecodeError", "'utf-8' codec can't decode byte 0xe2 in position 1: invalid continuation byte")
		exc = callErr(t, tbl, readText("/mnt/truncated.txt"))
		assertExc(t, exc, "UnicodeDecodeError", "'utf-8' codec can't decode bytes in position 2-3: unexpected end of data")
	})

	t.Run("rw_read_text_not_found", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assertExc(t, callErr(t, tbl, readText("/mnt/nonexistent.txt")), "FileNotFoundError",
			"[Errno 2] No such file or directory: '/mnt/nonexistent.txt'")
	})

	t.Run("rw_read_bytes", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, []byte{0x00, 0x01, 0x02, 0x03}, callOK(t, tbl, readBytes("/mnt/data.bin")))
		assert.Equal(t, []byte{}, callOK(t, tbl, readBytes("/mnt/empty.txt")))
	})

	t.Run("rw_write_text_and_read_back", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		callOK(t, tbl, writeTextCall("/mnt/new_file.txt", "new content"))
		assert.Equal(t, "new content", callOK(t, tbl, readText("/mnt/new_file.txt")))
		assert.Equal(t, "new content", fsaReadHost(t, filepath.Join(dir, "new_file.txt")))
	})

	t.Run("rw_write_bytes_and_read_back", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		callOK(t, tbl, writeBytesCall("/mnt/out.bin", []byte{0xff, 0xfe}))
		assert.Equal(t, []byte{0xff, 0xfe}, callOK(t, tbl, readBytes("/mnt/out.bin")))
	})

	t.Run("rw_overwrite_existing", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		callOK(t, tbl, writeTextCall("/mnt/hello.txt", "overwritten"))
		assert.Equal(t, "overwritten", callOK(t, tbl, readText("/mnt/hello.txt")))
	})

	t.Run("rw_stat_file", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		values := statValues(t, callOK(t, tbl, stat("/mnt/hello.txt")))
		assert.Equal(t, int64(12), values[6], "st_size should be 12")
	})

	t.Run("rw_stat_dir", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		values := statValues(t, callOK(t, tbl, stat("/mnt/subdir")))
		mode, ok := values[0].(int64)
		require.True(t, ok, "st_mode should be Int")
		assert.Equal(t, int64(0o040_000), mode&0o170_000, "should be directory type")
	})

	t.Run("rw_iterdir", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, []string{"data.bin", "empty.txt", "hello.txt", "readonly.txt", "subdir"},
			sortedNames(t, callOK(t, tbl, iterdir("/mnt"))))
	})

	t.Run("rw_iterdir_nested", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, []string{"deep", "nested.txt"}, sortedNames(t, callOK(t, tbl, iterdir("/mnt/subdir"))))
	})

	t.Run("rw_mkdir", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		callOK(t, tbl, mkdirCall("/mnt/new_dir", false, false))
		assert.Equal(t, true, callOK(t, tbl, isDir("/mnt/new_dir")))
		fi, err := os.Stat(filepath.Join(dir, "new_dir"))
		require.NoError(t, err)
		assert.True(t, fi.IsDir())
	})

	t.Run("rw_mkdir_parents", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		callOK(t, tbl, mkdirCall("/mnt/a/b/c", true, false))
		assert.Equal(t, true, callOK(t, tbl, isDir("/mnt/a/b/c")))
	})

	t.Run("rw_mkdir_exist_ok", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		callOK(t, tbl, mkdirCall("/mnt/subdir", false, true))
	})

	t.Run("rw_mkdir_already_exists_error", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assertExc(t, callErr(t, tbl, mkdirCall("/mnt/subdir", false, false)), "FileExistsError",
			"[Errno 17] File exists: '/mnt/subdir'")
	})

	t.Run("rw_unlink", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/hello.txt")))
		callOK(t, tbl, unlink("/mnt/hello.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.False(t, fsaHostExists(filepath.Join(dir, "hello.txt")))
	})

	t.Run("rw_unlink_not_found", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assertExc(t, callErr(t, tbl, unlink("/mnt/nonexistent.txt")), "FileNotFoundError",
			"[Errno 2] No such file or directory: '/mnt/nonexistent.txt'")
	})

	t.Run("rw_rmdir", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		callOK(t, tbl, mkdirCall("/mnt/empty_dir", false, false))
		callOK(t, tbl, rmdir("/mnt/empty_dir"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/empty_dir")))
	})

	t.Run("rw_rename", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		callOK(t, tbl, renameCall("/mnt/hello.txt", "/mnt/renamed.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.Equal(t, "hello world\n", callOK(t, tbl, readText("/mnt/renamed.txt")))
	})

	t.Run("rw_resolve", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, value.Path("/mnt/hello.txt"), callOK(t, tbl, pathCall(wire.OpResolve, "/mnt/subdir/../hello.txt")))
	})

	t.Run("rw_absolute", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		assert.Equal(t, value.Path("/mnt/subdir"), callOK(t, tbl, pathCall(wire.OpAbsolute, "/mnt/./subdir")))
	})

	// ReadOnly mode

	t.Run("ro_reads_work", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadOnly)
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.Equal(t, true, callOK(t, tbl, isFile("/mnt/hello.txt")))
		assert.Equal(t, true, callOK(t, tbl, isDir("/mnt/subdir")))
		assert.Equal(t, "hello world\n", callOK(t, tbl, readText("/mnt/hello.txt")))
		assert.Equal(t, []byte{0x00, 0x01, 0x02, 0x03}, callOK(t, tbl, readBytes("/mnt/data.bin")))
		callOK(t, tbl, stat("/mnt/hello.txt"))
		callOK(t, tbl, iterdir("/mnt"))
	})

	t.Run("ro_write_text_blocked", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadOnly)
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/new.txt", "blocked")), "PermissionError",
			"[Errno 30] Read-only file system: '/mnt/new.txt'")
	})

	t.Run("ro_write_bytes_blocked", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadOnly)
		assertExc(t, callErr(t, tbl, writeBytesCall("/mnt/new.bin", []byte{0x00})), "PermissionError",
			"[Errno 30] Read-only file system: '/mnt/new.bin'")
	})

	t.Run("ro_mkdir_blocked", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadOnly)
		assertExc(t, callErr(t, tbl, mkdirCall("/mnt/newdir", false, false)), "PermissionError",
			"[Errno 30] Read-only file system: '/mnt/newdir'")
	})

	t.Run("ro_unlink_blocked", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadOnly)
		assertExc(t, callErr(t, tbl, unlink("/mnt/hello.txt")), "PermissionError",
			"[Errno 30] Read-only file system: '/mnt/hello.txt'")
	})

	t.Run("ro_rmdir_blocked", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadOnly)
		assertExc(t, callErr(t, tbl, rmdir("/mnt/subdir")), "PermissionError",
			"[Errno 30] Read-only file system: '/mnt/subdir'")
	})

	t.Run("ro_rename_blocked", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadOnly)
		assertExc(t, callErr(t, tbl, renameCall("/mnt/hello.txt", "/mnt/renamed.txt")), "PermissionError",
			"[Errno 30] Read-only file system: '/mnt/hello.txt'")
	})

	// OverlayMemory mode

	t.Run("ovl_mem_reads_fall_through", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.Equal(t, "hello world\n", callOK(t, tbl, readText("/mnt/hello.txt")))
		assert.Equal(t, []byte{0x00, 0x01, 0x02, 0x03}, callOK(t, tbl, readBytes("/mnt/data.bin")))
		assert.Equal(t, true, callOK(t, tbl, isDir("/mnt/subdir")))
	})

	t.Run("ovl_mem_write_readable_back", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeTextCall("/mnt/new_overlay.txt", "overlay content"))
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/new_overlay.txt")))
		assert.Equal(t, "overlay content", callOK(t, tbl, readText("/mnt/new_overlay.txt")))
		assert.Equal(t, true, callOK(t, tbl, isFile("/mnt/new_overlay.txt")))
	})

	t.Run("ovl_mem_write_does_not_modify_host", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, Overlay)
		callOK(t, tbl, writeTextCall("/mnt/hello.txt", "overlay overwrite"))
		assert.Equal(t, "overlay overwrite", callOK(t, tbl, readText("/mnt/hello.txt")))
		assert.Equal(t, "hello world\n", fsaReadHost(t, filepath.Join(dir, "hello.txt")))
	})

	t.Run("ovl_mem_tombstone", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, Overlay)
		callOK(t, tbl, unlink("/mnt/hello.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.True(t, fsaHostExists(filepath.Join(dir, "hello.txt")))
	})

	t.Run("ovl_mem_iterdir_merges", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeTextCall("/mnt/overlay_new.txt", "new"))
		names := sortedNames(t, callOK(t, tbl, iterdir("/mnt")))
		assert.Contains(t, names, "hello.txt", "should contain real files")
		assert.Contains(t, names, "overlay_new.txt", "should contain overlay files")
	})

	t.Run("ovl_mem_iterdir_respects_tombstones", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, unlink("/mnt/hello.txt"))
		names := sortedNames(t, callOK(t, tbl, iterdir("/mnt")))
		assert.NotContains(t, names, "hello.txt", "tombstoned file should be hidden")
	})

	t.Run("ovl_mem_iterdir_missing_directory_errors", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		assert.Equal(t, "FileNotFoundError", callErr(t, tbl, iterdir("/mnt/no_such_dir")).ExcType)
	})

	t.Run("ovl_mem_iterdir_file_errors", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		assert.Equal(t, "NotADirectoryError", callErr(t, tbl, iterdir("/mnt/hello.txt")).ExcType)
	})

	t.Run("ovl_mem_path_component_too_long", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		path := "/mnt/" + strings.Repeat("a", 256)
		assert.Equal(t, false, callOK(t, tbl, exists(path)))
		assertExc(t, callErr(t, tbl, stat(path)), "OSError",
			"[Errno 36] File name too long: '/mnt/aaaaaaaaaaaaaaa…aaaaaaaaaaaaaaaaaaaa'")
		callOK(t, tbl, exists("/mnt/"+strings.Repeat("b", 255)))
	})

	t.Run("ovl_mem_path_total_too_long", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		segment := strings.Repeat("x", 200)
		segments := make([]string, 21)
		for i := range segments {
			segments[i] = segment
		}
		path := "/mnt/" + strings.Join(segments, "/")
		require.Greater(t, len(path), 4096)
		assert.Equal(t, false, callOK(t, tbl, exists(path)))
		assertExc(t, callErr(t, tbl, stat(path)), "OSError",
			"[Errno 36] File name too long: '/mnt/xxxxxxxxxxxxxxx…xxxxxxxxxxxxxxxxxxxx'")
	})

	t.Run("rw_path_component_too_long", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		path := "/mnt/" + strings.Repeat("a", 256)
		assertExc(t, callErr(t, tbl, stat(path)), "OSError",
			"[Errno 36] File name too long: '/mnt/aaaaaaaaaaaaaaa…aaaaaaaaaaaaaaaaaaaa'")
	})

	t.Run("ovl_mem_recreated_directory_shadows_old_real_children", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, unlink("/mnt/subdir/nested.txt"))
		callOK(t, tbl, unlink("/mnt/subdir/deep/file.txt"))
		callOK(t, tbl, rmdir("/mnt/subdir/deep"))
		callOK(t, tbl, rmdir("/mnt/subdir"))
		callOK(t, tbl, mkdirCall("/mnt/subdir", false, false))
		names := sortedNames(t, callOK(t, tbl, iterdir("/mnt/subdir")))
		assert.Empty(t, names, "recreated overlay dir should shadow old real children")
	})

	t.Run("ovl_mem_mkdir", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, Overlay)
		callOK(t, tbl, mkdirCall("/mnt/overlay_dir", false, false))
		assert.Equal(t, true, callOK(t, tbl, isDir("/mnt/overlay_dir")))
		assert.False(t, fsaHostExists(filepath.Join(dir, "overlay_dir")))
	})

	t.Run("ovl_mem_stat_overlay_file", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeTextCall("/mnt/sized.txt", "12345"))
		values := statValues(t, callOK(t, tbl, stat("/mnt/sized.txt")))
		assert.Equal(t, int64(5), values[6], "st_size should be 5")
	})

	t.Run("ovl_mem_rmdir_overlay", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, mkdirCall("/mnt/temp_dir", false, false))
		callOK(t, tbl, rmdir("/mnt/temp_dir"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/temp_dir")))
	})

	t.Run("rename_of_mount_root_is_refused_in_both_modes", func(t *testing.T) {
		for _, mode := range []Mode{ReadWrite, Overlay} {
			tbl := mountAtMnt(t, createFSTestDir(t), mode)
			for _, pair := range [][2]string{{"/mnt", "/mnt/moved"}, {"/mnt/moved", "/mnt"}} {
				assertExc(t, callErr(t, tbl, renameCall(pair[0], pair[1])), "PermissionError",
					"[Errno 13] Permission denied: '/mnt'")
			}
			assert.Equal(t, true, callOK(t, tbl, exists("/mnt/hello.txt")), "mode %s", mode)
		}
	})

	t.Run("rmdir_of_mount_root_is_refused_in_both_modes", func(t *testing.T) {
		for _, mode := range []Mode{ReadWrite, Overlay} {
			tbl := mountAtMnt(t, createFSTestDir(t), mode)
			assertExc(t, callErr(t, tbl, rmdir("/mnt")), "PermissionError", "[Errno 13] Permission denied: '/mnt'")
			assert.Equal(t, true, callOK(t, tbl, exists("/mnt")), "mode %s", mode)
			assert.Equal(t, true, callOK(t, tbl, exists("/mnt/hello.txt")), "mode %s", mode)
		}
	})

	t.Run("rmdir_nonexistent_is_not_found_in_both_modes", func(t *testing.T) {
		for _, mode := range []Mode{ReadWrite, Overlay} {
			tbl := mountAtMnt(t, createFSTestDir(t), mode)
			assertExc(t, callErr(t, tbl, rmdir("/mnt/nonexistent_dir")), "FileNotFoundError",
				"[Errno 2] No such file or directory: '/mnt/nonexistent_dir'")
			assertExc(t, callErr(t, tbl, rmdir("/mnt/hello.txt")), "NotADirectoryError",
				"[Errno 20] Not a directory: '/mnt/hello.txt'")
		}
	})

	t.Run("ovl_mem_rename", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, Overlay)
		callOK(t, tbl, renameCall("/mnt/hello.txt", "/mnt/moved.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.Equal(t, "hello world\n", callOK(t, tbl, readText("/mnt/moved.txt")))
		assert.True(t, fsaHostExists(filepath.Join(dir, "hello.txt")))
	})

	t.Run("ovl_mem_write_bytes", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeBytesCall("/mnt/bin_overlay.dat", []byte{0xAA, 0xBB}))
		assert.Equal(t, []byte{0xAA, 0xBB}, callOK(t, tbl, readBytes("/mnt/bin_overlay.dat")))
	})

	t.Run("ovl_mem_resolve", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		assert.Equal(t, value.Path("/mnt/hello.txt"), callOK(t, tbl, pathCall(wire.OpResolve, "/mnt/subdir/../hello.txt")))
	})

	t.Run("ovl_mem_rename_directory", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, Overlay)
		callOK(t, tbl, renameCall("/mnt/subdir", "/mnt/renamed_dir"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/subdir")))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/subdir/nested.txt")))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/subdir/deep/file.txt")))
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/renamed_dir")))
		assert.Equal(t, "nested content", callOK(t, tbl, readText("/mnt/renamed_dir/nested.txt")))
		assert.Equal(t, "deep file", callOK(t, tbl, readText("/mnt/renamed_dir/deep/file.txt")))
		assert.True(t, fsaHostExists(filepath.Join(dir, "subdir/nested.txt")))
	})

	t.Run("ovl_mem_rename_directory_with_overlay_children", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeTextCall("/mnt/subdir/overlay_file.txt", "overlay content"))
		callOK(t, tbl, renameCall("/mnt/subdir", "/mnt/moved"))
		assert.Equal(t, "overlay content", callOK(t, tbl, readText("/mnt/moved/overlay_file.txt")))
		assert.Equal(t, "nested content", callOK(t, tbl, readText("/mnt/moved/nested.txt")))
	})

	t.Run("ovl_mem_write_missing_parent", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/nonexistent/child.txt", "x")), "FileNotFoundError",
			"[Errno 2] No such file or directory: '/mnt/nonexistent/child.txt'")
		assertExc(t, callErr(t, tbl, writeBytesCall("/mnt/nonexistent/child.bin", []byte{0})), "FileNotFoundError",
			"[Errno 2] No such file or directory: '/mnt/nonexistent/child.bin'")
	})

	t.Run("ovl_mem_write_existing_parent", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeTextCall("/mnt/subdir/new_file.txt", "new content"))
		assert.Equal(t, "new content", callOK(t, tbl, readText("/mnt/subdir/new_file.txt")))
	})

	t.Run("ovl_mem_write_after_mkdir", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, mkdirCall("/mnt/newdir", false, false))
		callOK(t, tbl, writeTextCall("/mnt/newdir/file.txt", "content"))
		assert.Equal(t, "content", callOK(t, tbl, readText("/mnt/newdir/file.txt")))
	})

	// Overlay rename — exhaustive tests

	t.Run("ovl_mem_rename_file_overwrites_existing_file", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, renameCall("/mnt/hello.txt", "/mnt/empty.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.Equal(t, "hello world\n", callOK(t, tbl, readText("/mnt/empty.txt")))
	})

	t.Run("ovl_mem_rename_overlay_file_overwrites_overlay_file", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeTextCall("/mnt/a.txt", "aaa"))
		callOK(t, tbl, writeTextCall("/mnt/b.txt", "bbb"))
		callOK(t, tbl, renameCall("/mnt/a.txt", "/mnt/b.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/a.txt")))
		assert.Equal(t, "aaa", callOK(t, tbl, readText("/mnt/b.txt")))
	})

	t.Run("ovl_mem_rename_to_same_path", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, renameCall("/mnt/hello.txt", "/mnt/hello.txt"))
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.Equal(t, "hello world\n", callOK(t, tbl, readText("/mnt/hello.txt")))
	})

	t.Run("ovl_mem_rename_deleted_file_fails", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, unlink("/mnt/hello.txt"))
		assertExc(t, callErr(t, tbl, renameCall("/mnt/hello.txt", "/mnt/other.txt")), "FileNotFoundError",
			"[Errno 2] No such file or directory: '/mnt/hello.txt'")
	})

	t.Run("ovl_mem_rename_nonexistent_file_fails", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		assertExc(t, callErr(t, tbl, renameCall("/mnt/no_such_file.txt", "/mnt/other.txt")), "FileNotFoundError",
			"[Errno 2] No such file or directory: '/mnt/no_such_file.txt'")
	})

	t.Run("ovl_mem_rename_into_nonexistent_parent_fails", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		assertExc(t, callErr(t, tbl, renameCall("/mnt/hello.txt", "/mnt/no_such_dir/file.txt")), "FileNotFoundError",
			"[Errno 2] No such file or directory: '/mnt/no_such_dir/file.txt'")
	})

	t.Run("ovl_mem_rename_dir_with_tombstoned_entries", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, unlink("/mnt/subdir/nested.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/subdir/nested.txt")))
		callOK(t, tbl, renameCall("/mnt/subdir", "/mnt/moved"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/moved/nested.txt")))
		assert.Equal(t, "deep file", callOK(t, tbl, readText("/mnt/moved/deep/file.txt")))
	})

	t.Run("ovl_mem_rename_deeply_nested_overlay_dirs", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, mkdirCall("/mnt/a", false, false))
		callOK(t, tbl, mkdirCall("/mnt/a/b", false, false))
		callOK(t, tbl, mkdirCall("/mnt/a/b/c", false, false))
		callOK(t, tbl, writeTextCall("/mnt/a/b/c/leaf.txt", "leaf"))
		callOK(t, tbl, renameCall("/mnt/a", "/mnt/x"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/a")))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/a/b/c/leaf.txt")))
		assert.Equal(t, true, callOK(t, tbl, exists("/mnt/x/b/c")))
		assert.Equal(t, "leaf", callOK(t, tbl, readText("/mnt/x/b/c/leaf.txt")))
	})

	t.Run("ovl_mem_rename_then_rename_again", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, renameCall("/mnt/hello.txt", "/mnt/step1.txt"))
		callOK(t, tbl, renameCall("/mnt/step1.txt", "/mnt/step2.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/hello.txt")))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/step1.txt")))
		assert.Equal(t, "hello world\n", callOK(t, tbl, readText("/mnt/step2.txt")))
	})

	t.Run("ovl_mem_rename_overlay_written_file", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeTextCall("/mnt/new_file.txt", "overlay only"))
		callOK(t, tbl, renameCall("/mnt/new_file.txt", "/mnt/renamed_new.txt"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/new_file.txt")))
		assert.Equal(t, "overlay only", callOK(t, tbl, readText("/mnt/renamed_new.txt")))
	})

	t.Run("ovl_mem_rename_dir_iterdir_consistent", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, renameCall("/mnt/subdir", "/mnt/newdir"))
		rootNames := sortedNames(t, callOK(t, tbl, iterdir("/mnt")))
		assert.NotContains(t, rootNames, "subdir", "old name still in listing")
		assert.Contains(t, rootNames, "newdir", "new name missing from listing")
		newNames := sortedNames(t, callOK(t, tbl, iterdir("/mnt/newdir")))
		assert.Contains(t, newNames, "nested.txt")
		assert.Contains(t, newNames, "deep")
	})

	t.Run("ovl_mem_rename_dir_over_empty_overlay_dir", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, mkdirCall("/mnt/target_dir", false, false))
		callOK(t, tbl, writeTextCall("/mnt/subdir/extra.txt", "extra"))
		callOK(t, tbl, renameCall("/mnt/subdir", "/mnt/target_dir"))
		assert.Equal(t, false, callOK(t, tbl, exists("/mnt/subdir")))
		assert.Equal(t, "extra", callOK(t, tbl, readText("/mnt/target_dir/extra.txt")))
		assert.Equal(t, "nested content", callOK(t, tbl, readText("/mnt/target_dir/nested.txt")))
	})
}
