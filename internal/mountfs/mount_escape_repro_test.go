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

func miscRequireTooLong(t *testing.T, e *mountError) {
	t.Helper()
	require.NotNil(t, e, "expected the overlong path to be rejected")
	require.Equal(t, errIO, e.kind, "expected the overlong path to be rejected, got %v", e)
	require.Equal(t, ioInvalidFilename, e.io)
	require.Equal(t, "File name too long", e.msg)
}

func miscRequireCrossMount(t *testing.T, tbl *Table, call *wire.OsCall) {
	t.Helper()
	result, e, handled := dispatch(tbl, call)
	require.True(t, handled && e != nil && e.kind == errCrossMount, "expected CrossMountRename, got %#v / %v", result, e)
}

func TestMountEscapeRepro(t *testing.T) {
	t.Run("mount_root_stays_pinned_across_rebuilds", func(t *testing.T) {
		base := t.TempDir()
		outside := t.TempDir()
		shared := filepath.Join(base, "shared")
		require.NoError(t, os.Mkdir(shared, 0o755))
		writeHostFile(t, shared, "inside.txt", "in-mount")
		writeHostFile(t, outside, "secret.txt", "HOST SECRET")
		symlinkT(t, outside, filepath.Join(base, "prepared-link"))

		childRoot := openRootT(t, "/child", shared)
		childHostPath := childRoot.HostPath()

		writable := mountTable(t, "/parent", base, ReadWrite, nil, DefaultMemoryUsageLimit)
		_, e := miscHandled(t, writable, renameCall("/parent/shared", "/parent/old-shared"))
		require.Nil(t, e, "staging the swap failed: %v", e)
		_, e = miscHandled(t, writable, renameCall("/parent/prepared-link", "/parent/shared"))
		require.Nil(t, e)

		rebuilt := NewTable([]*Spec{{Root: childRoot, Mode: ReadOnly, MemoryUsageLimit: DefaultMemoryUsageLimit}})
		inside, e := miscReadText(t, rebuilt, "/child/inside.txt")
		require.Nil(t, e)
		require.Equal(t, "in-mount", inside)
		other, e := miscReadText(t, rebuilt, "/child/secret.txt")
		require.True(t, e != nil && e.kind == errIO && e.io == ioNotFound, "expected the redirected read to miss, got %#v / %v", other, e)

		fromPath := mountTable(t, "/child", childHostPath, ReadOnly, nil, DefaultMemoryUsageLimit)
		leaked, e := miscReadText(t, fromPath, "/child/secret.txt")
		require.Nil(t, e)
		require.Equal(t, "HOST SECRET", leaked)
	})

	t.Run("rename_with_one_side_out_of_mount_is_refused", func(t *testing.T) {
		host := t.TempDir()
		writeHostFile(t, host, "source.txt", "public")
		other := t.TempDir()

		tbl := NewTable([]*Spec{
			{Root: openRootT(t, "/data", host), Mode: ReadOnly, MemoryUsageLimit: DefaultMemoryUsageLimit},
			{Root: openRootT(t, "/other", other), Mode: ReadOnly, MemoryUsageLimit: DefaultMemoryUsageLimit},
		})

		miscRequireCrossMount(t, tbl, renameCall("/data/source.txt", "/other/result.txt"))
		miscRequireCrossMount(t, tbl, renameCall("/data/source.txt", "/outside/result.txt"))
		miscRequireCrossMount(t, tbl, renameCall("/outside/source.txt", "/data/result.txt"))
		require.FileExists(t, filepath.Join(host, "source.txt"))

		call := renameCall("/outside/source.txt", "/elsewhere/result.txt")
		requireNotHandled(t, tbl, call)
		require.Equal(t, "/outside/source.txt", call.Path)
	})

	t.Run("overlong_path_rejected_even_when_it_normalizes_short", func(t *testing.T) {
		host := t.TempDir()
		writeHostFile(t, host, "hello.txt", "hello")

		longAndDeep := "/mnt/" + strings.Repeat("a/", 5_000) + "hello.txt"
		longButCollapsing := "/mnt/" + strings.Repeat("a/", 5_000) + strings.Repeat("../", 5_000) + "hello.txt"
		require.True(t, len(longAndDeep) > 4096 && len(longButCollapsing) > 4096)

		for _, mode := range []Mode{ReadOnly, Overlay} {
			tbl := mountAtMnt(t, host, mode)
			_, e := miscReadText(t, tbl, longAndDeep)
			miscRequireTooLong(t, e)
			_, e = miscReadText(t, tbl, longButCollapsing)
			miscRequireTooLong(t, e)
			read, e := miscReadText(t, tbl, "/mnt/a/../hello.txt")
			require.Nil(t, e)
			require.Equal(t, "hello", read)
		}
	})

	t.Run("overlong_path_is_refused_before_it_is_normalized", func(t *testing.T) {
		host := t.TempDir()
		tbl := mountAtMnt(t, host, ReadOnly)

		long := "/mnt/" + strings.Repeat("a/", 2_000_000) + "x"
		require.Greater(t, len(long), 4_000_000)

		exc := callErr(t, tbl, pathCall(wire.OpReadText, long))
		require.Equal(t, "[Errno 36] File name too long: '/mnt/a/a/a/a/a/a/a/a…/a/a/a/a/a/a/a/a/a/x'", exc.MessageText())

		unmounted := "/nowhere/" + strings.Repeat("a/", 2_000_000) + "x"
		_, e := miscReadText(t, tbl, unmounted)
		miscRequireTooLong(t, e)

		_, e = miscHandled(t, tbl, renameCall("/mnt/hello.txt", long))
		miscRequireTooLong(t, e)

		requireNotHandled(t, tbl, pathCall(wire.OpExists, "/nowhere/short.txt"))
	})

	t.Run("overlong_path_with_multibyte_characters_is_elided_safely", func(t *testing.T) {
		host := t.TempDir()
		tbl := mountAtMnt(t, host, ReadOnly)

		long := "/mnt/" + strings.Repeat("🦀", 2_000)
		exc := callErr(t, tbl, pathCall(wire.OpReadText, long))
		require.Equal(t, "[Errno 36] File name too long: '/mnt/🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀…🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀🦀'", exc.MessageText())
	})

	t.Run("overlong_path_predicates_answer_false", func(t *testing.T) {
		host := t.TempDir()
		tbl := mountAtMnt(t, host, ReadOnly)

		plain := "/mnt/" + strings.Repeat("a/", 5_000) + "x"
		padded := "/mnt/" + strings.Repeat("a/", 5_000) + strings.Repeat("../", 5_000) + "x"
		component := "/mnt/" + strings.Repeat("b", 5_000)

		for _, path := range []string{plain, padded, component} {
			for _, op := range []wire.OsOp{wire.OpExists, wire.OpIsFile, wire.OpIsDir, wire.OpIsSymlink} {
				result, e := miscHandled(t, tbl, pathCall(op, path))
				require.Nil(t, e, "%s on an overlong path must answer False, as CPython does", op.Name())
				require.Equal(t, false, result, "%s on an overlong path must answer False, as CPython does", op.Name())
			}
		}
	})
}
