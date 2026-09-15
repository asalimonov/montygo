//go:build unix

package mountfs

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
)

const miscSecret = "SECRET-HOST-CONTENT"

func miscSoakEnabled(t *testing.T) {
	t.Helper()
	if v := os.Getenv("MONTY_FS_SOAK"); v == "" || v == "0" {
		t.Skip("race soak: set MONTY_FS_SOAK=1 to run it")
	}
}

// miscHandled mirrors the Rust dispatch helper that panics on NotHandled.
func miscHandled(t *testing.T, tbl *Table, call *wire.OsCall) (any, *mountError) {
	t.Helper()
	result, e, handled := dispatch(tbl, call)
	require.True(t, handled, "mount table returned NotHandled: %s %q", call.Name(), call.Path)
	return result, e
}

func miscReadText(t *testing.T, tbl *Table, path string) (any, *mountError) {
	t.Helper()
	return miscHandled(t, tbl, pathCall(wire.OpReadText, path))
}

func miscMountRW(t *testing.T, host string) *Table {
	t.Helper()
	return mountAtMnt(t, host, ReadWrite)
}

func miscLeaked(result any) bool {
	switch v := result.(type) {
	case string:
		return strings.Contains(v, miscSecret)
	case []byte:
		return strings.Contains(string(v), miscSecret)
	}
	return false
}

func miscIsPermissionIO(e *mountError) bool {
	return e != nil && e.kind == errIO && e.io == ioPermissionDenied
}

// miscChmod sets mode bits and restores 0o755 before t.TempDir removes the tree.
func miscChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(path, mode))
	t.Cleanup(func() { os.Chmod(path, 0o755) })
}

func miscMkfifo(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, syscall.Mkfifo(path, 0o644))
}

func TestMountConfinement(t *testing.T) {
	t.Run("mount_root_swapped_for_symlink_still_reads_original", func(t *testing.T) {
		base := t.TempDir()
		mountPath := filepath.Join(base, "mount")
		elsewhere := filepath.Join(base, "elsewhere")
		require.NoError(t, os.Mkdir(mountPath, 0o755))
		require.NoError(t, os.Mkdir(elsewhere, 0o755))
		writeHostFile(t, mountPath, "file.txt", "public")
		writeHostFile(t, elsewhere, "file.txt", miscSecret)

		tbl := miscMountRW(t, mountPath)
		require.NoError(t, os.Rename(mountPath, filepath.Join(base, "moved")))
		symlinkT(t, elsewhere, mountPath)

		result, e := miscReadText(t, tbl, "/mnt/file.txt")
		require.False(t, miscLeaked(result), "HOST FILE DISCLOSURE: read followed the swapped mount root")
		require.Nil(t, e, "expected the original directory to still back the mount, got %v", e)
		require.Equal(t, "public", result)
	})

	t.Run("mount_survives_host_directory_rename", func(t *testing.T) {
		base := t.TempDir()
		mountPath := filepath.Join(base, "mount")
		require.NoError(t, os.Mkdir(mountPath, 0o755))
		writeHostFile(t, mountPath, "file.txt", "public")

		tbl := miscMountRW(t, mountPath)
		require.NoError(t, os.Rename(mountPath, filepath.Join(base, "renamed")))

		result, e := miscReadText(t, tbl, "/mnt/file.txt")
		require.Nil(t, e, "expected the mount to follow its directory through a rename, got %v", e)
		require.Equal(t, "public", result)
	})

	t.Run("mounted_directory_cannot_be_renamed_on_windows", func(t *testing.T) {
		t.Skip("Windows-only: ERROR_SHARING_VIOLATION on renaming a mounted directory")
	})

	t.Run("read_through_swapped_intermediate_directory_is_rejected", func(t *testing.T) {
		mountDir := t.TempDir()
		outsideDir := t.TempDir()
		writeHostFile(t, outsideDir, "secret.txt", miscSecret)
		data := filepath.Join(mountDir, "data")
		require.NoError(t, os.Mkdir(data, 0o755))
		writeHostFile(t, data, "file.txt", "public")

		tbl := miscMountRW(t, mountDir)
		require.NoError(t, os.RemoveAll(data))
		symlinkT(t, outsideDir, data)

		result, _ := miscReadText(t, tbl, "/mnt/data/secret.txt")
		require.False(t, miscLeaked(result), "HOST FILE DISCLOSURE: read traversed an outbound symlink")
	})

	t.Run("concurrent_rename_cannot_redirect_a_read", func(t *testing.T) {
		miscSoakEnabled(t)
		mountDir := t.TempDir()
		outsideDir := t.TempDir()
		secret := filepath.Join(outsideDir, "secret.txt")
		require.NoError(t, os.WriteFile(secret, []byte(miscSecret), 0o644))
		data := filepath.Join(mountDir, "data")
		require.NoError(t, os.Mkdir(data, 0o755))
		target := filepath.Join(data, "file.txt")
		require.NoError(t, os.WriteFile(target, []byte("public"), 0o644))
		decoy := filepath.Join(mountDir, "decoy")
		symlinkT(t, secret, decoy)

		tbl := miscMountRW(t, mountDir)
		var stop atomic.Bool
		var swaps atomic.Uint32
		done := make(chan struct{})
		staging := filepath.Join(mountDir, "staging")
		go func() {
			defer close(done)
			for !stop.Load() {
				if _, err := os.Lstat(target); err != nil {
					_ = os.Rename(staging, target)
				} else if _, err := os.Lstat(decoy); err != nil {
					_ = os.Rename(target, decoy)
					_ = os.Rename(staging, target)
				}
				if os.Rename(target, staging) == nil &&
					os.Rename(decoy, target) == nil &&
					os.Rename(target, decoy) == nil &&
					os.Rename(staging, target) == nil {
					swaps.Add(1)
				}
			}
		}()

		deadline := time.Now().Add(3 * time.Second)
		leaks, reads := 0, 0
		for time.Now().Before(deadline) {
			result, e := miscReadText(t, tbl, "/mnt/data/file.txt")
			if s, ok := result.(string); ok && e == nil {
				reads++
				if strings.Contains(s, miscSecret) {
					leaks++
				}
			}
		}
		stop.Store(true)
		<-done

		require.Greater(t, reads, 0, "no read ever succeeded — the scenario did not run")
		require.Greater(t, swaps.Load(), uint32(0), "no swap ever completed — the scenario did not run")
		require.Equal(t, 0, leaks, "HOST FILE DISCLOSURE: a racing read returned host content")
	})

	t.Run("write_through_swapped_directory_cannot_escape", func(t *testing.T) {
		mountDir := t.TempDir()
		outsideDir := t.TempDir()
		outsideFile := filepath.Join(outsideDir, "target.txt")
		require.NoError(t, os.WriteFile(outsideFile, []byte(miscSecret), 0o644))
		data := filepath.Join(mountDir, "data")
		require.NoError(t, os.Mkdir(data, 0o755))

		tbl := miscMountRW(t, mountDir)
		require.NoError(t, os.RemoveAll(data))
		symlinkT(t, outsideDir, data)

		dispatch(tbl, writeTextCall("/mnt/data/target.txt", "clobbered"))
		content, err := os.ReadFile(outsideFile)
		require.NoError(t, err)
		require.Equal(t, miscSecret, string(content), "the out-of-mount file was modified")

		entries, err := os.ReadDir(outsideDir)
		require.NoError(t, err)
		var created []string
		for _, entry := range entries {
			if p := filepath.Join(outsideDir, entry.Name()); p != outsideFile {
				created = append(created, p)
			}
		}
		require.Empty(t, created, "files were created outside the mount")
	})

	t.Run("relative_symlink_target_is_followed_inside_the_mount", func(t *testing.T) {
		mountDir := t.TempDir()
		writeHostFile(t, mountDir, "hello.txt", "in-mount")
		symlinkT(t, "hello.txt", filepath.Join(mountDir, "link.txt"))

		tbl := miscMountRW(t, mountDir)
		result, e := miscReadText(t, tbl, "/mnt/link.txt")
		require.Nil(t, e)
		require.Equal(t, "in-mount", result)
	})

	t.Run("absolute_symlink_target_is_refused_even_inside_the_mount", func(t *testing.T) {
		mountDir := t.TempDir()
		writeHostFile(t, mountDir, "hello.txt", "in-mount")
		symlinkT(t, filepath.Join(mountDir, "hello.txt"), filepath.Join(mountDir, "abs.txt"))

		tbl := miscMountRW(t, mountDir)
		_, e := miscReadText(t, tbl, "/mnt/abs.txt")
		require.NotNil(t, e, "absolute in-mount symlink should be refused")
		require.Equal(t, errPathEscape, e.kind, "absolute in-mount symlink should be refused, got %v", e)

		for _, op := range []wire.OsOp{wire.OpExists, wire.OpIsFile, wire.OpIsDir} {
			result, e := miscHandled(t, tbl, pathCall(op, "/mnt/abs.txt"))
			require.Nil(t, e)
			require.Equal(t, false, result)
		}

		result, e := miscHandled(t, tbl, pathCall(wire.OpIsSymlink, "/mnt/abs.txt"))
		require.Nil(t, e)
		require.Equal(t, true, result)
	})

	t.Run("genuine_permission_error_is_not_reported_as_an_escape", func(t *testing.T) {
		mountDir := t.TempDir()
		unreadable := filepath.Join(mountDir, "locked.txt")
		require.NoError(t, os.WriteFile(unreadable, []byte("in-mount"), 0o644))
		miscChmod(t, unreadable, 0o000)
		if _, err := os.ReadFile(unreadable); err == nil {
			t.Skip("running as root, mode 0o000 is not enforced")
		}

		tbl := miscMountRW(t, mountDir)
		_, e := miscReadText(t, tbl, "/mnt/locked.txt")
		require.True(t, miscIsPermissionIO(e), "expected a plain IO permission error, got %v", e)
	})

	t.Run("overlay_permission_errors_match_direct_mode", func(t *testing.T) {
		probe := t.TempDir()
		probeSub := filepath.Join(probe, "sub")
		require.NoError(t, os.Mkdir(probeSub, 0o755))
		writeHostFile(t, probeSub, "f.txt", "content")
		miscChmod(t, probeSub, 0o000)
		if _, err := os.ReadFile(filepath.Join(probeSub, "f.txt")); err == nil {
			t.Skip("running as root, mode 0o000 is not enforced")
		}

		for _, mode := range []Mode{ReadWrite, Overlay} {
			dir := t.TempDir()
			sub := filepath.Join(dir, "sub")
			require.NoError(t, os.Mkdir(sub, 0o755))
			writeHostFile(t, sub, "f.txt", "content")
			miscChmod(t, sub, 0o000)

			tbl := mountAtMnt(t, dir, mode)
			for _, call := range []*wire.OsCall{
				pathCall(wire.OpIterdir, "/mnt/sub"),
				pathCall(wire.OpReadText, "/mnt/sub/f.txt"),
				openCall("/mnt/sub/f.txt", "r"),
				appendTextCall("/mnt/sub/f.txt", "x"),
				pathCall(wire.OpStat, "/mnt/sub/f.txt"),
			} {
				result, e := miscHandled(t, tbl, call)
				require.True(t, miscIsPermissionIO(e), "%s %s: expected a permission error, got %#v / %v", mode, call.Name(), result, e)
			}

			for _, op := range []wire.OsOp{wire.OpExists, wire.OpIsFile, wire.OpIsDir} {
				result, e := miscHandled(t, tbl, pathCall(op, "/mnt/sub/f.txt"))
				require.Nil(t, e, "%s %s: predicates must answer False, not raise", mode, op.Name())
				require.Equal(t, false, result, "%s %s: predicates must answer False, not raise", mode, op.Name())
			}

			require.NoError(t, os.Chmod(sub, 0o755))
		}
	})

	t.Run("denied_write_is_not_reported_as_an_escape", func(t *testing.T) {
		mountDir := t.TempDir()
		locked := filepath.Join(mountDir, "locked.txt")
		require.NoError(t, os.WriteFile(locked, []byte("content"), 0o644))
		require.NoError(t, os.Chmod(locked, 0o444))
		if f, err := os.OpenFile(locked, os.O_WRONLY, 0); err == nil {
			f.Close()
			require.NoError(t, os.Chmod(locked, 0o644))
			t.Skip("host permits writing a read-only file")
		}

		tbl := miscMountRW(t, mountDir)
		_, e := miscHandled(t, tbl, writeTextCall("/mnt/locked.txt", "x"))
		require.NoError(t, os.Chmod(locked, 0o644))
		require.True(t, miscIsPermissionIO(e), "expected a plain IO permission error, got %v", e)
	})

	t.Run("mount_construction_rejects_at_the_open_first", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "not-a-directory.txt")
		require.NoError(t, os.WriteFile(file, []byte("content"), 0o644))

		for _, host := range []string{file, filepath.Join(dir, "absent")} {
			root, err := OpenRoot("/mnt", host)
			require.Nil(t, root)
			me, ok := err.(*mountError)
			require.True(t, ok, "expected InvalidMount for %s, got %v", host, err)
			require.Equal(t, errInvalidMount, me.kind, "expected InvalidMount for %s, got %v", host, err)
			require.True(t, strings.HasPrefix(me.msg, "cannot open host path"), "a pre-check answered before the open: %s", me.msg)
		}
	})

	t.Run("search_only_directory_mountability", func(t *testing.T) {
		base := t.TempDir()
		target := filepath.Join(base, "searchonly")
		require.NoError(t, os.Mkdir(target, 0o755))
		require.NoError(t, os.Chmod(target, 0o111))

		root, err := OpenRoot("/mnt", target)
		require.NoError(t, os.Chmod(target, 0o755))
		if err == nil {
			root.Close()
			return
		}
		me, ok := err.(*mountError)
		require.True(t, ok && me.kind == errInvalidMount && strings.Contains(me.msg, "cannot open host path"),
			"expected InvalidMount for a search-only directory, got %v", err)
	})

	t.Run("deleting_the_mount_root_is_refused", func(t *testing.T) {
		mountDir := t.TempDir()
		tbl := miscMountRW(t, mountDir)

		result, e := miscHandled(t, tbl, pathCall(wire.OpRmdir, "/mnt"))
		require.NotNil(t, e, "rmdir of the mount root must fail, got %#v", result)
		require.DirExists(t, mountDir, "host directory must survive rmdir")

		result, e = miscHandled(t, tbl, pathCall(wire.OpUnlink, "/mnt"))
		require.NotNil(t, e, "unlink of the mount root must fail, got %#v", result)
		require.DirExists(t, mountDir, "host directory must survive unlink")
	})

	t.Run("overlay_rmdir_of_the_mount_root_leaves_the_host_directory", func(t *testing.T) {
		mountDir := t.TempDir()
		tbl := mountAtMnt(t, mountDir, Overlay)

		result, e := miscHandled(t, tbl, pathCall(wire.OpRmdir, "/mnt"))
		require.NotNil(t, e, "overlay rmdir of the mount root must fail, got %#v", result)
		require.DirExists(t, mountDir, "host directory must survive overlay rmdir")
	})

	t.Run("mkdir_parents_under_an_escaping_symlink_cannot_reach_the_host", func(t *testing.T) {
		mountDir := t.TempDir()
		outside := t.TempDir()
		writeHostFile(t, outside, "secret.txt", miscSecret)
		symlinkT(t, outside, filepath.Join(mountDir, "evil"))

		direct := miscMountRW(t, mountDir)
		_, e := miscHandled(t, direct, mkdirCall("/mnt/evil/foo", true, false))
		require.True(t, e != nil && e.kind == errPathEscape, "direct mkdir through an escaping symlink must be refused, got %v", e)
		require.NoDirExists(t, filepath.Join(outside, "foo"), "directory created outside the mount")

		overlay := mountAtMnt(t, mountDir, Overlay)
		_, e = miscHandled(t, overlay, mkdirCall("/mnt/evil/foo", true, false))
		require.True(t, e != nil && e.kind == errPathEscape, "overlay mkdir through an escaping symlink must be refused, got %v", e)
		_, e = miscHandled(t, overlay, writeTextCall("/mnt/evil/foo/x.txt", "data"))
		require.True(t, e != nil && e.kind == errPathEscape, "overlay write under an escaping symlink must be refused, got %v", e)
		require.NoDirExists(t, filepath.Join(outside, "foo"), "directory created outside the mount")
		require.NoFileExists(t, filepath.Join(outside, "x.txt"), "file created outside the mount")

		result, e := miscReadText(t, overlay, "/mnt/evil/secret.txt")
		require.False(t, miscLeaked(result), "HOST FILE DISCLOSURE: read followed the escaping symlink")
		require.NotNil(t, e, "outside read must be refused, got %#v", result)
	})

	t.Run("fifo_in_the_mount_is_refused_promptly", func(t *testing.T) {
		mountDir := t.TempDir()
		miscMkfifo(t, filepath.Join(mountDir, "pipe"))

		tbl := miscMountRW(t, mountDir)
		outcome := make(chan *mountError, 1)
		go func() {
			_, e, _ := dispatch(tbl, pathCall(wire.OpReadText, "/mnt/pipe"))
			outcome <- e
		}()
		select {
		case e := <-outcome:
			require.True(t, miscIsPermissionIO(e), "expected a refusal, got %v", e)
		case <-time.After(10 * time.Second):
			t.Fatal("reading a FIFO blocked the servicing thread")
		}
	})

	t.Run("fifo_raced_into_place_between_check_and_open_is_refused", func(t *testing.T) {
		miscSoakEnabled(t)
		mountDir := t.TempDir()
		target := filepath.Join(mountDir, "file.txt")
		regular := filepath.Join(mountDir, "regular")
		fifo := filepath.Join(mountDir, "fifo")
		require.NoError(t, os.WriteFile(target, []byte("public"), 0o644))
		require.NoError(t, os.WriteFile(regular, []byte("public"), 0o644))
		miscMkfifo(t, fifo)

		var stop atomic.Bool
		swapperDone := make(chan struct{})
		go func() {
			defer close(swapperDone)
			for !stop.Load() {
				if _, err := os.Stat(target); err != nil {
					if os.Rename(regular, target) != nil {
						_ = os.Rename(fifo, target)
					}
				}
				if os.Rename(target, regular) == nil {
					_ = os.Rename(fifo, target)
				}
				if os.Rename(target, fifo) == nil {
					_ = os.Rename(regular, target)
				}
			}
		}()

		tbl := miscMountRW(t, mountDir)
		unexpected := make(chan string, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				result, e, _ := dispatch(tbl, pathCall(wire.OpReadText, "/mnt/file.txt"))
				if e == nil && result != "public" {
					select {
					case unexpected <- "unexpected value":
					default:
					}
				}
			}
		}()

		var ok bool
		select {
		case <-finished:
			ok = true
		case <-time.After(30 * time.Second):
		}
		stop.Store(true)
		<-swapperDone
		require.True(t, ok, "a read blocked on the raced-in FIFO")
		select {
		case msg := <-unexpected:
			t.Fatal(msg)
		default:
		}
	})
}
