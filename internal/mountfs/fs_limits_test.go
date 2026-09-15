//go:build unix

package mountfs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
)

func fsbRepeat(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }

func TestFSLimits(t *testing.T) {
	t.Run("rename_cross_mount_error", func(t *testing.T) {
		dir1 := createFSTestDir(t)
		dir2 := createFSTestDir(t)
		tbl := NewTable([]*Spec{
			{Root: openRootT(t, "/mnt1", dir1), Mode: ReadWrite, MemoryUsageLimit: DefaultMemoryUsageLimit},
			{Root: openRootT(t, "/mnt2", dir2), Mode: ReadWrite, MemoryUsageLimit: DefaultMemoryUsageLimit},
		})
		exc := callErr(t, tbl, renameCall("/mnt1/hello.txt", "/mnt2/hello.txt"))
		assertExc(t, exc, "OSError", "[Errno 18] Invalid cross-device link: '/mnt1/hello.txt' -> '/mnt2/hello.txt'")
	})

	t.Run("no_mount_point_returns_none", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		requireNotHandled(t, tbl, pathCall(wire.OpExists, "/unmounted/file.txt"))
	})

	t.Run("empty_mount_table", func(t *testing.T) {
		tbl := NewTable(nil)
		require.Equal(t, 0, tbl.Len())
	})

	t.Run("mount_table_len", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := NewTable([]*Spec{
			{Root: openRootT(t, "/a", dir), Mode: ReadWrite, MemoryUsageLimit: DefaultMemoryUsageLimit},
			{Root: openRootT(t, "/b", dir), Mode: ReadOnly, MemoryUsageLimit: DefaultMemoryUsageLimit},
		})
		require.Equal(t, 2, tbl.Len())
		require.NotEqual(t, 0, tbl.Len())
	})

	t.Run("mount_sorting_specific_wins", func(t *testing.T) {
		dir := createFSTestDir(t)
		sub := t.TempDir()
		writeHostFile(t, sub, "specific.txt", "from specific mount")
		tbl := NewTable([]*Spec{
			{Root: openRootT(t, "/data", dir), Mode: ReadWrite, MemoryUsageLimit: DefaultMemoryUsageLimit},
			{Root: openRootT(t, "/data/sub", sub), Mode: ReadWrite, MemoryUsageLimit: DefaultMemoryUsageLimit},
		})
		require.Equal(t, "from specific mount", callOK(t, tbl, pathCall(wire.OpReadText, "/data/sub/specific.txt")))
	})

	t.Run("non_filesystem_ops_not_handled", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		requireNotHandled(t, tbl, &wire.OsCall{Op: wire.OpGetenv, Key: "PATH"})
	})

	t.Run("mount_prefix_no_partial_match", func(t *testing.T) {
		tbl := mountTable(t, "/data", createFSTestDir(t), ReadWrite, nil, DefaultMemoryUsageLimit)
		requireNotHandled(t, tbl, pathCall(wire.OpExists, "/data2/file.txt"))
	})

	t.Run("path_with_spaces", func(t *testing.T) {
		dir := t.TempDir()
		writeHostFile(t, dir, "hello world.txt", "spaces")
		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "spaces", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hello world.txt")))
	})

	t.Run("path_with_unicode", func(t *testing.T) {
		dir := t.TempDir()
		writeHostFile(t, dir, "文件.txt", "unicode")
		tbl := mountAtMnt(t, dir, ReadWrite)
		require.Equal(t, "unicode", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/文件.txt")))
	})

	t.Run("windows_style_paths_do_not_match_mounts", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		requireNotHandled(t, tbl, pathCall(wire.OpExists, `\mnt\hello.txt`))
		requireNotHandled(t, tbl, pathCall(wire.OpReadText, `C:\mnt\hello.txt`))
		requireNotHandled(t, tbl, pathCall(wire.OpResolve, `/mnt\hello.txt`))
	})

	t.Run("windows_style_write_paths_do_not_touch_host_mount", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMnt(t, dir, ReadWrite)
		requireNotHandled(t, tbl, writeTextCall(`\mnt\created.txt`, "should not be written"))
		_, err := os.Stat(filepath.Join(dir, "created.txt"))
		require.True(t, os.IsNotExist(err), "the host mount should not be modified for an unhandled windows-style path")
	})

	t.Run("mount_memory_usage_limit_defaults_to_100_mb", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadOnly)
		require.Equal(t, uint64(100_000_000), DefaultMemoryUsageLimit)
		require.Equal(t, DefaultMemoryUsageLimit, tbl.mounts[0].memLimit)
		require.Nil(t, tbl.mounts[0].overlay)
	})

	t.Run("direct_reads_accept_exact_limit_and_reject_one_byte_over", func(t *testing.T) {
		dir := createFSTestDir(t)
		writeHostFile(t, dir, "exact.txt", "12345")
		writeHostFile(t, dir, "large.txt", "123456")
		tbl := mountAtMntWithMemoryLimit(t, dir, ReadOnly, 5)
		require.Equal(t, []byte("12345"), callOK(t, tbl, pathCall(wire.OpReadBytes, "/mnt/exact.txt")))
		assertExc(t, callErr(t, tbl, pathCall(wire.OpReadBytes, "/mnt/large.txt")), "MemoryError", "mount memory usage limit of 5 bytes exceeded")
		assertExc(t, callErr(t, tbl, pathCall(wire.OpReadText, "/mnt/large.txt")), "MemoryError", "mount memory usage limit of 5 bytes exceeded")
	})

	t.Run("directory_results_obey_mount_memory_budget", func(t *testing.T) {
		dir := createFSTestDir(t)
		writeHostFile(t, dir, "one", "")
		writeHostFile(t, dir, "two", "")
		tbl := mountAtMntWithMemoryLimit(t, dir, ReadOnly, 100)
		assertExc(t, callErr(t, tbl, pathCall(wire.OpIterdir, "/mnt")), "MemoryError", "mount memory usage limit of 100 bytes exceeded")
	})

	t.Run("overlay_retained_data_and_reads_share_one_memory_budget", func(t *testing.T) {
		dir := createFSTestDir(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "large.bin"), fsbRepeat('x', 800), 0o644))
		tbl := mountAtMntWithMemoryLimit(t, dir, Overlay, 1_000)
		callOK(t, tbl, writeBytesCall("/mnt/overlay.bin", fsbRepeat('a', 500)))
		assertExc(t, callErr(t, tbl, pathCall(wire.OpReadBytes, "/mnt/overlay.bin")), "MemoryError", "mount memory usage limit of 1 KB exceeded")
		assertExc(t, callErr(t, tbl, appendBytesCall("/mnt/large.bin", []byte("7"))), "MemoryError", "mount memory usage limit of 1 KB exceeded")
		got, err := os.ReadFile(filepath.Join(dir, "large.bin"))
		require.NoError(t, err)
		require.Equal(t, fsbRepeat('x', 800), got)
	})

	t.Run("separate_overlay_files_share_memory_budget", func(t *testing.T) {
		tbl := mountAtMntWithMemoryLimit(t, createFSTestDir(t), Overlay, 1_000)
		callOK(t, tbl, writeBytesCall("/mnt/one.bin", fsbRepeat('a', 300)))
		assertExc(t, callErr(t, tbl, writeBytesCall("/mnt/two.bin", fsbRepeat('b', 300))), "MemoryError", "mount memory usage limit of 1 KB exceeded")
	})

	t.Run("overwriting_an_overlay_file_reuses_its_budget", func(t *testing.T) {
		tbl := mountAtMntWithMemoryLimit(t, createFSTestDir(t), Overlay, 2_000)
		callOK(t, tbl, writeBytesCall("/mnt/f.bin", fsbRepeat('a', 1_200)))
		assertExc(t, callErr(t, tbl, writeBytesCall("/mnt/g.bin", fsbRepeat('b', 1_200))), "MemoryError", "mount memory usage limit of 2 KB exceeded")
		callOK(t, tbl, writeTextCall("/mnt/f.bin", strings.Repeat("a", 1_400)))
		assertExc(t, callErr(t, tbl, writeBytesCall("/mnt/h.bin", fsbRepeat('c', 1_400))), "MemoryError", "mount memory usage limit of 2 KB exceeded")
	})

	t.Run("in_place_append_obeys_memory_budget", func(t *testing.T) {
		tbl := mountAtMntWithMemoryLimit(t, createFSTestDir(t), Overlay, 3_000)
		callOK(t, tbl, writeBytesCall("/mnt/a.bin", fsbRepeat('a', 400)))
		callOK(t, tbl, appendBytesCall("/mnt/a.bin", fsbRepeat('b', 200)))
		expected := append(fsbRepeat('a', 400), fsbRepeat('b', 200)...)
		require.Equal(t, expected, callOK(t, tbl, pathCall(wire.OpReadBytes, "/mnt/a.bin")))
		assertExc(t, callErr(t, tbl, appendBytesCall("/mnt/a.bin", fsbRepeat('c', 3_000))), "MemoryError", "mount memory usage limit of 3 KB exceeded")
		require.Equal(t, expected, callOK(t, tbl, pathCall(wire.OpReadBytes, "/mnt/a.bin")))
	})

	t.Run("deleting_an_overlay_file_frees_budget", func(t *testing.T) {
		tbl := mountAtMntWithMemoryLimit(t, createFSTestDir(t), Overlay, 1_600)
		callOK(t, tbl, writeBytesCall("/mnt/big.bin", fsbRepeat('a', 600)))
		assertExc(t, callErr(t, tbl, writeBytesCall("/mnt/b2.bin", fsbRepeat('b', 600))), "MemoryError", "mount memory usage limit of 1.6 KB exceeded")
		callOK(t, tbl, pathCall(wire.OpUnlink, "/mnt/big.bin"))
		callOK(t, tbl, writeBytesCall("/mnt/b2.bin", fsbRepeat('b', 600)))
	})

	t.Run("tombstones_are_charged_to_the_memory_budget", func(t *testing.T) {
		tbl := mountAtMntWithMemoryLimit(t, createFSTestDir(t), Overlay, 1_000)
		callOK(t, tbl, writeBytesCall("/mnt/x.bin", fsbRepeat('a', 600)))
		assertExc(t, callErr(t, tbl, pathCall(wire.OpUnlink, "/mnt/hello.txt")), "MemoryError", "mount memory usage limit of 1 KB exceeded")
	})

	t.Run("overlay_iterdir_obeys_memory_budget", func(t *testing.T) {
		tbl := mountAtMntWithMemoryLimit(t, t.TempDir(), Overlay, 4_500)
		for i := 0; i < 10; i++ {
			callOK(t, tbl, writeBytesCall(fmt.Sprintf("/mnt/f%d", i), []byte("x")))
		}
		assertExc(t, callErr(t, tbl, pathCall(wire.OpIterdir, "/mnt")), "MemoryError", "mount memory usage limit of 4.5 KB exceeded")
	})

	t.Run("fall_through_reads_share_budget_with_retained_data", func(t *testing.T) {
		dir := createFSTestDir(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "large.bin"), fsbRepeat('x', 800), 0o644))
		tbl := mountAtMntWithMemoryLimit(t, dir, Overlay, 1_000)
		require.Equal(t, fsbRepeat('x', 800), callOK(t, tbl, pathCall(wire.OpReadBytes, "/mnt/large.bin")))
		callOK(t, tbl, writeBytesCall("/mnt/keep.bin", fsbRepeat('k', 100)))
		assertExc(t, callErr(t, tbl, pathCall(wire.OpReadBytes, "/mnt/large.bin")), "MemoryError", "mount memory usage limit of 1 KB exceeded")
	})

	t.Run("overlay_directory_rename_obeys_memory_budget", func(t *testing.T) {
		tbl := mountAtMntWithMemoryLimit(t, t.TempDir(), Overlay, 10_000)
		callOK(t, tbl, mkdirCall("/mnt/d", false, false))
		for i := 0; i < 8; i++ {
			callOK(t, tbl, writeBytesCall(fmt.Sprintf("/mnt/d/f%d", i), []byte("x")))
		}
		callOK(t, tbl, renameCall("/mnt/d", "/mnt/e"))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/d")))
		require.Equal(t, []byte("x"), callOK(t, tbl, pathCall(wire.OpReadBytes, "/mnt/e/f0")))

		tbl = mountAtMntWithMemoryLimit(t, t.TempDir(), Overlay, 5_000)
		callOK(t, tbl, mkdirCall("/mnt/d", false, false))
		for i := 0; i < 8; i++ {
			callOK(t, tbl, writeBytesCall(fmt.Sprintf("/mnt/d/f%d", i), []byte("x")))
		}
		assertExc(t, callErr(t, tbl, renameCall("/mnt/d", "/mnt/e")), "MemoryError", "mount memory usage limit of 5 KB exceeded")
		require.Equal(t, []byte("x"), callOK(t, tbl, pathCall(wire.OpReadBytes, "/mnt/d/f0")))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/e")))
	})

	t.Run("real_directory_rename_capture_obeys_memory_budget", func(t *testing.T) {
		dir := createFSTestDir(t)
		tbl := mountAtMntWithMemoryLimit(t, dir, Overlay, 1_500)
		assertExc(t, callErr(t, tbl, renameCall("/mnt/subdir", "/mnt/moved")), "MemoryError", "mount memory usage limit of 1.5 KB exceeded")
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/subdir/nested.txt")))
		fi, err := os.Stat(filepath.Join(dir, "subdir/nested.txt"))
		require.NoError(t, err)
		require.True(t, fi.Mode().IsRegular())
	})

	t.Run("rw_write_text_within_limit", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), ReadWrite, 100)
		callOK(t, tbl, writeTextCall("/mnt/a.txt", "hello"))
		require.Equal(t, "hello", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/a.txt")))
	})

	t.Run("rw_write_text_exceeds_limit", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), ReadWrite, 10)
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/a.txt", strings.Repeat("a]", 10))), "OSError", "disk write limit of 10 bytes exceeded")
	})

	t.Run("rw_write_bytes_exceeds_limit", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), ReadWrite, 5)
		assertExc(t, callErr(t, tbl, writeBytesCall("/mnt/a.bin", make([]byte, 10))), "OSError", "disk write limit of 5 bytes exceeded")
	})

	t.Run("rw_cumulative_writes_exceed_limit", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), ReadWrite, 15)
		callOK(t, tbl, writeTextCall("/mnt/a.txt", "0123456789"))
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/b.txt", "0123456789")), "OSError", "disk write limit of 15 bytes exceeded")
	})

	t.Run("ovl_write_text_exceeds_limit", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), Overlay, 10)
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/a.txt", strings.Repeat("a]", 10))), "OSError", "disk write limit of 10 bytes exceeded")
	})

	t.Run("ovl_write_bytes_exceeds_limit", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), Overlay, 5)
		assertExc(t, callErr(t, tbl, writeBytesCall("/mnt/a.bin", make([]byte, 10))), "OSError", "disk write limit of 5 bytes exceeded")
	})

	t.Run("ovl_cumulative_writes_exceed_limit", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), Overlay, 15)
		callOK(t, tbl, writeTextCall("/mnt/a.txt", "0123456789"))
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/b.txt", "0123456789")), "OSError", "disk write limit of 15 bytes exceeded")
	})

	t.Run("ovl_append_existing_real_file_counts_existing_bytes_toward_limit", func(t *testing.T) {
		dir := createFSTestDir(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "large.bin"), make([]byte, 10), 0o644))
		tbl := mountAtMntWithLimit(t, dir, Overlay, 5)
		assertExc(t, callErr(t, tbl, appendBytesCall("/mnt/large.bin", []byte{1})), "OSError", "disk write limit of 5 bytes exceeded")
		require.Equal(t, make([]byte, 10), callOK(t, tbl, pathCall(wire.OpReadBytes, "/mnt/large.bin")),
			"failed append should leave the real backing file visible and unchanged")
		callOK(t, tbl, writeBytesCall("/mnt/quota_ok.bin", fsbRepeat(1, 5)))
	})

	t.Run("write_limit_pretty_format_kb", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), ReadWrite, 5_000)
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/a.txt", strings.Repeat("x", 5_001))), "OSError", "disk write limit of 5 KB exceeded")
	})

	t.Run("write_limit_pretty_format_mb", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), Overlay, 1_500_000)
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/a.txt", strings.Repeat("x", 1_500_001))), "OSError", "disk write limit of 1.5 MB exceeded")
	})

	t.Run("write_exactly_at_limit_succeeds", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), ReadWrite, 10)
		callOK(t, tbl, writeTextCall("/mnt/a.txt", "0123456789"))
	})

	t.Run("write_one_over_limit_fails", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), ReadWrite, 10)
		assertExc(t, callErr(t, tbl, writeTextCall("/mnt/a.txt", "01234567890")), "OSError", "disk write limit of 10 bytes exceeded")
	})

	t.Run("no_limit_allows_large_writes", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), ReadWrite)
		callOK(t, tbl, writeTextCall("/mnt/big.txt", strings.Repeat("x", 100_000)))
	})

	t.Run("rw_unlink_symlink_removes_link_not_target", func(t *testing.T) {
		dir := createFSTestDir(t)
		symlinkT(t, filepath.Join(dir, "hello.txt"), filepath.Join(dir, "link.txt"))
		tbl := mountAtMnt(t, dir, ReadWrite)
		callOK(t, tbl, pathCall(wire.OpUnlink, "/mnt/link.txt"))
		_, err := os.Lstat(filepath.Join(dir, "link.txt"))
		require.Error(t, err)
		got, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
		require.NoError(t, err)
		require.Equal(t, "hello world\n", string(got))
	})

	t.Run("rw_rename_symlink_renames_link_not_target", func(t *testing.T) {
		dir := createFSTestDir(t)
		symlinkT(t, filepath.Join(dir, "hello.txt"), filepath.Join(dir, "link.txt"))
		tbl := mountAtMnt(t, dir, ReadWrite)
		callOK(t, tbl, renameCall("/mnt/link.txt", "/mnt/moved_link.txt"))
		_, err := os.Lstat(filepath.Join(dir, "link.txt"))
		require.Error(t, err)
		fi, err := os.Lstat(filepath.Join(dir, "moved_link.txt"))
		require.NoError(t, err)
		require.True(t, fi.Mode()&os.ModeSymlink != 0)
		got, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
		require.NoError(t, err)
		require.Equal(t, "hello world\n", string(got))
	})

	t.Run("rw_failed_write_does_not_consume_quota", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), ReadWrite, 10)
		callErr(t, tbl, writeTextCall("/mnt/no_such_dir/file.txt", "12345"))
		callOK(t, tbl, writeTextCall("/mnt/quota_ok.txt", "0123456789"))
	})

	t.Run("ovl_failed_write_does_not_consume_quota", func(t *testing.T) {
		tbl := mountAtMntWithLimit(t, createFSTestDir(t), Overlay, 10)
		callErr(t, tbl, writeTextCall("/mnt/no_such_dir/file.txt", "12345"))
		callOK(t, tbl, writeTextCall("/mnt/quota_ok.txt", "0123456789"))
	})

	t.Run("ovl_rename_directory_preserves_descendants", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, renameCall("/mnt/subdir", "/mnt/renamed_dir"))
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/renamed_dir/nested.txt")))
		require.Equal(t, "deep file", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/renamed_dir/deep/file.txt")))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/subdir/nested.txt")))
	})

	t.Run("ovl_mem_rename_file_onto_directory", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		require.Equal(t, "IsADirectoryError", callErr(t, tbl, renameCall("/mnt/hello.txt", "/mnt/subdir")).ExcType)
	})

	t.Run("ovl_mem_rename_directory_onto_file", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		require.Equal(t, "NotADirectoryError", callErr(t, tbl, renameCall("/mnt/subdir", "/mnt/hello.txt")).ExcType)
	})

	t.Run("ovl_mem_rename_directory_into_own_subdir", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		exc := callErr(t, tbl, renameCall("/mnt/subdir", "/mnt/subdir/deep/moved"))
		require.Equal(t, "OSError", exc.ExcType)
		require.Contains(t, exc.MessageText(), "Invalid argument")
	})

	t.Run("ovl_mem_rename_overlay_file_onto_overlay_dir", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, writeTextCall("/mnt/src.txt", "content"))
		callOK(t, tbl, mkdirCall("/mnt/dst_dir", false, false))
		require.Equal(t, "IsADirectoryError", callErr(t, tbl, renameCall("/mnt/src.txt", "/mnt/dst_dir")).ExcType)
	})

	t.Run("ovl_mem_rename_of_a_symlink_is_refused", func(t *testing.T) {
		dir := createFSTestDir(t)
		symlinkT(t, "hello.txt", filepath.Join(dir, "link.txt"))
		tbl := mountAtMnt(t, dir, Overlay)
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpIsSymlink, "/mnt/link.txt")))
		assertExc(t, callErr(t, tbl, renameCall("/mnt/link.txt", "/mnt/moved_link.txt")), "PermissionError", "[Errno 13] Permission denied: '/mnt/link.txt'")
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/moved_link.txt")))
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hello.txt")))
	})

	t.Run("ovl_mem_read_through_a_symlink_cannot_go_stale", func(t *testing.T) {
		dir := createFSTestDir(t)
		symlinkT(t, "hello.txt", filepath.Join(dir, "link.txt"))
		tbl := mountAtMnt(t, dir, Overlay)
		callOK(t, tbl, writeTextCall("/mnt/hello.txt", "OVERWRITTEN"))
		assertExc(t, callErr(t, tbl, pathCall(wire.OpReadText, "/mnt/link.txt")), "PermissionError", "[Errno 13] Permission denied: '/mnt/link.txt'")
		assertExc(t, callErr(t, tbl, pathCall(wire.OpStat, "/mnt/link.txt")), "PermissionError", "[Errno 13] Permission denied: '/mnt/link.txt'")
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/link.txt")))
		require.Equal(t, "OVERWRITTEN", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hello.txt")))
	})

	t.Run("ovl_mem_every_operation_on_a_symlink_is_refused", func(t *testing.T) {
		dir := createFSTestDir(t)
		symlinkT(t, "hello.txt", filepath.Join(dir, "link.txt"))
		symlinkT(t, "subdir", filepath.Join(dir, "link_dir"))
		tbl := mountAtMnt(t, dir, Overlay)
		calls := []*wire.OsCall{
			pathCall(wire.OpReadText, "/mnt/link.txt"),
			pathCall(wire.OpReadBytes, "/mnt/link.txt"),
			writeTextCall("/mnt/link.txt", "x"),
			writeBytesCall("/mnt/link.txt", []byte("x")),
			appendBytesCall("/mnt/link.txt", []byte("x")),
			pathCall(wire.OpStat, "/mnt/link.txt"),
			pathCall(wire.OpUnlink, "/mnt/link.txt"),
			pathCall(wire.OpRmdir, "/mnt/link_dir"),
			mkdirCall("/mnt/link_dir", false, false),
			mkdirCall("/mnt/link_dir", false, true),
			mkdirCall("/mnt/link_dir", true, true),
			pathCall(wire.OpIterdir, "/mnt/link_dir"),
			renameCall("/mnt/link.txt", "/mnt/moved.txt"),
			renameCall("/mnt/hello.txt", "/mnt/link.txt"),
			pathCall(wire.OpReadText, "/mnt/link_dir/nested.txt"),
		}
		for _, c := range calls {
			exc := callErr(t, tbl, c)
			require.Equal(t, "PermissionError", exc.ExcType, "%s %q -> %q must be refused", c.Name(), c.Path, c.Dst)
		}
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hello.txt")))
		require.Equal(t, []string{"deep", "nested.txt"}, sortedNames(t, callOK(t, tbl, pathCall(wire.OpIterdir, "/mnt/subdir"))))
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpIsSymlink, "/mnt/link_dir")))
	})

	t.Run("ovl_mem_read_below_a_symlinked_directory_is_refused", func(t *testing.T) {
		dir := createFSTestDir(t)
		symlinkT(t, "subdir", filepath.Join(dir, "link_dir"))
		tbl := mountAtMnt(t, dir, Overlay)
		for _, c := range []*wire.OsCall{
			pathCall(wire.OpReadText, "/mnt/link_dir/nested.txt"),
			pathCall(wire.OpStat, "/mnt/link_dir/nested.txt"),
			pathCall(wire.OpIterdir, "/mnt/link_dir"),
		} {
			require.Equal(t, "PermissionError", callErr(t, tbl, c).ExcType, "%s %q", c.Name(), c.Path)
		}
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/subdir/nested.txt")))
	})

	t.Run("ovl_mem_rmdir_real_dir_with_overlay_children", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, pathCall(wire.OpUnlink, "/mnt/subdir/nested.txt"))
		callOK(t, tbl, pathCall(wire.OpUnlink, "/mnt/subdir/deep/file.txt"))
		callOK(t, tbl, pathCall(wire.OpRmdir, "/mnt/subdir/deep"))
		callOK(t, tbl, writeTextCall("/mnt/subdir/overlay_only.txt", "overlay"))
		exc := callErr(t, tbl, pathCall(wire.OpRmdir, "/mnt/subdir"))
		require.Equal(t, "OSError", exc.ExcType)
		require.Contains(t, exc.MessageText(), "Directory not empty")
		require.Equal(t, "overlay", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/subdir/overlay_only.txt")))
	})

	t.Run("mkdir_on_symlink_to_dir_follows_for_exist_ok", func(t *testing.T) {
		for _, parents := range []bool{false, true} {
			dir := createFSTestDir(t)
			symlinkT(t, "subdir", filepath.Join(dir, "link_dir"))
			tbl := mountAtMnt(t, dir, ReadWrite)
			require.Nil(t, callOK(t, tbl, mkdirCall("/mnt/link_dir", parents, true)), "exist_ok on a symlink to a directory must succeed (parents=%v)", parents)
			assertExc(t, callErr(t, tbl, mkdirCall("/mnt/link_dir", parents, false)), "FileExistsError", "[Errno 17] File exists: '/mnt/link_dir'")
		}
	})

	t.Run("mkdir_exist_ok_still_refuses_non_directory_symlinks", func(t *testing.T) {
		for _, parents := range []bool{false, true} {
			dir := createFSTestDir(t)
			symlinkT(t, "nowhere", filepath.Join(dir, "dangling"))
			symlinkT(t, "hello.txt", filepath.Join(dir, "link.txt"))
			tbl := mountAtMnt(t, dir, ReadWrite)
			for _, name := range []string{"dangling", "link.txt"} {
				exc := callErr(t, tbl, mkdirCall("/mnt/"+name, parents, true))
				assertExc(t, exc, "FileExistsError", fmt.Sprintf("[Errno 17] File exists: '/mnt/%s'", name))
			}
		}
	})

	t.Run("ovl_mem_rename_onto_tombstoned_dir_succeeds", func(t *testing.T) {
		dir := createFSTestDir(t)
		require.NoError(t, os.Mkdir(filepath.Join(dir, "target"), 0o755))
		tbl := mountAtMnt(t, dir, Overlay)
		callOK(t, tbl, pathCall(wire.OpRmdir, "/mnt/target"))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/target")))
		callOK(t, tbl, renameCall("/mnt/hello.txt", "/mnt/target"))
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/target")))
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpIsFile, "/mnt/target")))
		require.Equal(t, false, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/hello.txt")))
	})

	t.Run("ovl_mem_rename_dir_onto_tombstoned_file_succeeds", func(t *testing.T) {
		tbl := mountAtMnt(t, createFSTestDir(t), Overlay)
		callOK(t, tbl, pathCall(wire.OpUnlink, "/mnt/hello.txt"))
		callOK(t, tbl, renameCall("/mnt/subdir", "/mnt/hello.txt"))
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpIsDir, "/mnt/hello.txt")))
		require.Equal(t, "nested content", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hello.txt/nested.txt")))
	})

	t.Run("on_no_handler_includes_errno", func(t *testing.T) {
		t.Skip("OsFunctionCall::on_no_handler is the host default for unhandled calls, not part of mountfs")
	})
}
