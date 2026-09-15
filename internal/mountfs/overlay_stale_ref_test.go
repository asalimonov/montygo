//go:build unix

package mountfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
)

type miscStaleRef struct {
	tbl *Table
}

// miscNewStaleRef caches a RealFileRef via rename, then swaps its backing file for an outbound symlink.
func miscNewStaleRef(t *testing.T) *miscStaleRef {
	t.Helper()
	mountDir := t.TempDir()
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte(miscSecret), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(mountDir, "inner"), 0o755))
	realFile := filepath.Join(mountDir, "inner/file.txt")
	require.NoError(t, os.WriteFile(realFile, []byte("public"), 0o644))

	tbl := mountAtMnt(t, mountDir, Overlay)
	_, e := miscHandled(t, tbl, renameCall("/mnt/inner/file.txt", "/mnt/moved.txt"))
	require.Nil(t, e, "rename should succeed")

	require.NoError(t, os.Remove(realFile))
	symlinkT(t, secret, realFile)
	return &miscStaleRef{tbl: tbl}
}

func (s *miscStaleRef) requireRejected(t *testing.T, call *wire.OsCall) {
	t.Helper()
	result, e := miscHandled(t, s.tbl, call)
	require.False(t, miscLeaked(result), "HOST FILE DISCLOSURE: dereferencing a stale overlay ref returned the secret")
	require.True(t, e != nil && e.kind == errPathEscape, "expected PathEscape, got %#v / %v", result, e)
}

func TestOverlayStaleRef(t *testing.T) {
	t.Run("stale_ref_is_revalidated_on_read_text", func(t *testing.T) {
		miscNewStaleRef(t).requireRejected(t, pathCall(wire.OpReadText, "/mnt/moved.txt"))
	})

	t.Run("stale_ref_is_revalidated_on_read_bytes", func(t *testing.T) {
		miscNewStaleRef(t).requireRejected(t, pathCall(wire.OpReadBytes, "/mnt/moved.txt"))
	})

	t.Run("stale_ref_is_revalidated_on_stat", func(t *testing.T) {
		miscNewStaleRef(t).requireRejected(t, pathCall(wire.OpStat, "/mnt/moved.txt"))
	})

	t.Run("stale_ref_is_revalidated_on_append", func(t *testing.T) {
		scenario := miscNewStaleRef(t)
		scenario.requireRejected(t, appendTextCall("/mnt/moved.txt", "appended"))
		scenario.requireRejected(t, pathCall(wire.OpReadText, "/mnt/moved.txt"))
	})

	t.Run("mount_root_swap_does_not_redirect_a_cached_ref", func(t *testing.T) {
		base := t.TempDir()
		mountPath := filepath.Join(base, "mount")
		elsewhere := filepath.Join(base, "elsewhere")
		require.NoError(t, os.MkdirAll(filepath.Join(mountPath, "inner"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(elsewhere, "inner"), 0o755))
		writeHostFile(t, mountPath, "inner/file.txt", "public")
		writeHostFile(t, elsewhere, "inner/file.txt", miscSecret)

		tbl := mountAtMnt(t, mountPath, Overlay)
		_, e := miscHandled(t, tbl, renameCall("/mnt/inner/file.txt", "/mnt/moved.txt"))
		require.Nil(t, e, "rename should succeed")

		require.NoError(t, os.Rename(mountPath, filepath.Join(base, "mount_old")))
		symlinkT(t, elsewhere, mountPath)

		result, e := miscReadText(t, tbl, "/mnt/moved.txt")
		require.False(t, miscLeaked(result), "HOST FILE DISCLOSURE: swapping the mount root redirected the read")
		require.Nil(t, e)
		require.Equal(t, "public", result)
	})

	t.Run("renaming_a_symlink_never_resolves_its_target", func(t *testing.T) {
		mountDir := t.TempDir()
		sub := filepath.Join(mountDir, "sub")
		require.NoError(t, os.Mkdir(sub, 0o755))
		writeHostFile(t, sub, "target.txt", "content")
		symlinkT(t, "sub/target.txt", filepath.Join(mountDir, "link.txt"))
		miscChmod(t, sub, 0o000)
		if _, err := os.ReadFile(filepath.Join(sub, "target.txt")); err == nil {
			t.Skip("running as root, mode 0o000 is not enforced")
		}

		tbl := mountAtMnt(t, mountDir, Overlay)
		result, e := miscHandled(t, tbl, renameCall("/mnt/link.txt", "/mnt/moved.txt"))
		require.NoError(t, os.Chmod(sub, 0o755))
		require.True(t, e != nil && e.kind == errPathEscape, "expected the link itself to be refused, got %#v / %v", result, e)
	})

	t.Run("direct_read_through_escaping_symlink_is_rejected", func(t *testing.T) {
		mountDir := t.TempDir()
		outsideDir := t.TempDir()
		secret := filepath.Join(outsideDir, "secret.txt")
		require.NoError(t, os.WriteFile(secret, []byte(miscSecret), 0o644))
		symlinkT(t, secret, filepath.Join(mountDir, "link.txt"))

		tbl := mountAtMnt(t, mountDir, Overlay)
		result, _ := miscReadText(t, tbl, "/mnt/link.txt")
		require.False(t, miscLeaked(result), "control failed: the ordinary resolution path also leaks")
	})

	t.Run("junction_is_classified_as_symlink", func(t *testing.T) {
		t.Skip("Windows-only: junction classification")
	})

	t.Run("renaming_escaping_symlink_into_overlay_is_rejected", func(t *testing.T) {
		mountDir := t.TempDir()
		outsideDir := t.TempDir()
		secret := filepath.Join(outsideDir, "secret.txt")
		require.NoError(t, os.WriteFile(secret, []byte(miscSecret), 0o644))
		symlinkT(t, secret, filepath.Join(mountDir, "link.txt"))

		tbl := mountAtMnt(t, mountDir, Overlay)
		result, e := miscHandled(t, tbl, renameCall("/mnt/link.txt", "/mnt/captured.txt"))
		require.True(t, e != nil && e.kind == errPathEscape, "expected the rename to be refused with PathEscape, got %#v / %v", result, e)
	})

	t.Run("stale_ref_repointed_at_an_in_mount_symlink_is_refused", func(t *testing.T) {
		mountDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(mountDir, "inner"), 0o755))
		realFile := filepath.Join(mountDir, "inner/file.txt")
		require.NoError(t, os.WriteFile(realFile, []byte("public"), 0o644))
		writeHostFile(t, mountDir, "other.txt", "OTHER")

		tbl := mountAtMnt(t, mountDir, Overlay)
		_, e := miscHandled(t, tbl, renameCall("/mnt/inner/file.txt", "/mnt/moved.txt"))
		require.Nil(t, e, "rename should succeed")

		require.NoError(t, os.Remove(realFile))
		symlinkT(t, "../other.txt", realFile)

		result, e := miscReadText(t, tbl, "/mnt/moved.txt")
		require.True(t, e != nil && e.kind == errPathEscape, "a ref must not follow a symlink swapped in behind it, got %#v / %v", result, e)
	})
}
