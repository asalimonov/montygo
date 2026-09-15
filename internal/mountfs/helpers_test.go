package mountfs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

// createFSTestDir builds the tree of tests/fs.rs create_test_dir.
func createFSTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeHostFile(t, dir, "hello.txt", "hello world\n")
	writeHostFile(t, dir, "empty.txt", "")
	writeHostFile(t, dir, "data.bin", "\x00\x01\x02\x03")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir/deep"), 0o755))
	writeHostFile(t, dir, "subdir/nested.txt", "nested content")
	writeHostFile(t, dir, "subdir/deep/file.txt", "deep file")
	writeHostFile(t, dir, "readonly.txt", "readonly content")
	return dir
}

// createSecurityTestDir builds the tree of tests/fs_security.rs create_test_dir.
func createSecurityTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeHostFile(t, dir, "hello.txt", "hello world\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir/deep"), 0o755))
	writeHostFile(t, dir, "subdir/nested.txt", "nested content")
	writeHostFile(t, dir, "subdir/deep/file.txt", "deep file")
	return dir
}

func writeHostFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644))
}

func symlinkT(t *testing.T, target, link string) {
	t.Helper()
	require.NoError(t, os.Symlink(target, link))
}

func openRootT(t *testing.T, virtualPath, hostPath string) *Root {
	t.Helper()
	root, err := OpenRoot(virtualPath, hostPath)
	require.NoError(t, err)
	t.Cleanup(func() { root.Close() })
	return root
}

func u64(v uint64) *uint64 { return &v }

// mountTable builds a one-mount table; writeLimit nil means unlimited.
func mountTable(t *testing.T, virtualPath, hostPath string, mode Mode, writeLimit *uint64, memoryLimit uint64) *Table {
	t.Helper()
	return NewTable([]*Spec{{Root: openRootT(t, virtualPath, hostPath), Mode: mode, WriteBytesLimit: writeLimit, MemoryUsageLimit: memoryLimit}})
}

func mountAtMnt(t *testing.T, hostPath string, mode Mode) *Table {
	t.Helper()
	return mountTable(t, "/mnt", hostPath, mode, nil, DefaultMemoryUsageLimit)
}

func mountAtMntWithLimit(t *testing.T, hostPath string, mode Mode, limit uint64) *Table {
	t.Helper()
	return mountTable(t, "/mnt", hostPath, mode, u64(limit), DefaultMemoryUsageLimit)
}

func mountAtMntWithMemoryLimit(t *testing.T, hostPath string, mode Mode, limit uint64) *Table {
	t.Helper()
	return mountTable(t, "/mnt", hostPath, mode, nil, limit)
}

var allModes = []struct {
	name string
	mode Mode
}{{"ReadWrite", ReadWrite}, {"ReadOnly", ReadOnly}, {"OverlayMemory", Overlay}}

func pathCall(op wire.OsOp, path string) *wire.OsCall { return &wire.OsCall{Op: op, Path: path} }

func writeTextCall(path, text string) *wire.OsCall {
	return &wire.OsCall{Op: wire.OpWriteText, Path: path, Text: text}
}

func appendTextCall(path, text string) *wire.OsCall {
	return &wire.OsCall{Op: wire.OpAppendText, Path: path, Text: text}
}

func writeBytesCall(path string, data []byte) *wire.OsCall {
	return &wire.OsCall{Op: wire.OpWriteBytes, Path: path, Data: data}
}

func appendBytesCall(path string, data []byte) *wire.OsCall {
	return &wire.OsCall{Op: wire.OpAppendBytes, Path: path, Data: data}
}

func mkdirCall(path string, parents, existOK bool) *wire.OsCall {
	return &wire.OsCall{Op: wire.OpMkdir, Path: path, Parents: parents, ExistOK: existOK}
}

func renameCall(src, dst string) *wire.OsCall {
	return &wire.OsCall{Op: wire.OpRename, Path: src, Dst: dst}
}

func openCall(path, mode string) *wire.OsCall {
	return &wire.OsCall{Op: wire.OpOpen, Path: path, Mode: mode}
}

// dispatch is the Rust Option<Result<MontyObject, MountError>>: handled=false is None.
func dispatch(tbl *Table, call *wire.OsCall) (result any, err *mountError, handled bool) {
	return tbl.handle(call)
}

// callOK goes through the public HandleOsCall and requires a handled success.
func callOK(t *testing.T, tbl *Table, call *wire.OsCall) any {
	t.Helper()
	out := tbl.HandleOsCall(context.Background(), call)
	require.True(t, out.Handled, "expected handled: %s %q", call.Name(), call.Path)
	require.Nil(t, out.Exception, "unexpected exception for %s %q: %v", call.Name(), call.Path, excSummary(out.Exception))
	return out.Value
}

// callErr goes through the public HandleOsCall and requires a handled exception.
func callErr(t *testing.T, tbl *Table, call *wire.OsCall) *wire.Exception {
	t.Helper()
	out := tbl.HandleOsCall(context.Background(), call)
	require.True(t, out.Handled, "expected handled: %s %q", call.Name(), call.Path)
	require.NotNil(t, out.Exception, "expected exception for %s %q, got %#v", call.Name(), call.Path, out.Value)
	return out.Exception
}

func requireNotHandled(t *testing.T, tbl *Table, call *wire.OsCall) {
	t.Helper()
	out := tbl.HandleOsCall(context.Background(), call)
	require.False(t, out.Handled, "expected not handled: %s %q, got %#v / %v", call.Name(), call.Path, out.Value, excSummary(out.Exception))
}

func excSummary(e *wire.Exception) string {
	if e == nil {
		return "<nil>"
	}
	return e.Summary()
}

func assertExc(t *testing.T, exc *wire.Exception, excType, msg string) {
	t.Helper()
	require.NotNil(t, exc)
	require.Equal(t, excType, exc.ExcType, "wrong exception type (message %q)", exc.MessageText())
	require.Equal(t, msg, exc.MessageText(), "wrong exception message")
}

// sortedNames extracts final path components from an iterdir result.
func sortedNames(t *testing.T, v any) []string {
	t.Helper()
	items, ok := v.([]any)
	require.True(t, ok, "expected []any from iterdir, got %#v", v)
	names := make([]string, 0, len(items))
	for _, item := range items {
		p, ok := item.(value.Path)
		require.True(t, ok, "expected value.Path in iterdir result, got %#v", item)
		s := string(p)
		names = append(names, s[strings.LastIndexByte(s, '/')+1:])
	}
	sort.Strings(names)
	return names
}

func statValues(t *testing.T, v any) []any {
	t.Helper()
	nt, ok := v.(value.NamedTuple)
	require.True(t, ok, "expected NamedTuple from stat, got %#v", v)
	return nt.Values
}

// requireBlocked mirrors assert_blocked: None, or PathEscape/NoMountPoint/Io/EmbeddedNullByte.
func requireBlocked(t *testing.T, tbl *Table, call *wire.OsCall) {
	t.Helper()
	result, e, handled := dispatch(tbl, call)
	if !handled {
		return
	}
	require.NotNil(t, e, "expected blocked, got Ok(%#v) for %s %q", result, call.Name(), call.Path)
	switch e.kind {
	case errPathEscape, errNoMountPoint, errIO, errValue:
	default:
		t.Fatalf("unexpected error variant for %s %q: %v", call.Name(), call.Path, e)
	}
}

// requireInvisible mirrors assert_invisible: the predicate answers False.
func requireInvisible(t *testing.T, tbl *Table, call *wire.OsCall) {
	t.Helper()
	result, e, handled := dispatch(tbl, call)
	require.True(t, handled, "expected handled for %q", call.Path)
	require.Nil(t, e, "expected Ok(false) for %q, got %v", call.Path, e)
	require.Equal(t, false, result, "expected Ok(false) for %q", call.Path)
}

func TestHelpersSmoke(t *testing.T) {
	dir := createFSTestDir(t)
	for _, m := range allModes {
		tbl := mountAtMnt(t, dir, m.mode)
		require.Equal(t, true, callOK(t, tbl, pathCall(wire.OpExists, "/mnt/hello.txt")))
		require.Equal(t, "hello world\n", callOK(t, tbl, pathCall(wire.OpReadText, "/mnt/hello.txt")))
		require.Equal(t, []string{"data.bin", "empty.txt", "hello.txt", "readonly.txt", "subdir"}, sortedNames(t, callOK(t, tbl, pathCall(wire.OpIterdir, "/mnt"))))
		require.Equal(t, int64(12), statValues(t, callOK(t, tbl, pathCall(wire.OpStat, "/mnt/hello.txt")))[6])
		assertExc(t, callErr(t, tbl, pathCall(wire.OpReadText, "/mnt/nope")), "FileNotFoundError", "[Errno 2] No such file or directory: '/mnt/nope'")
		requireNotHandled(t, tbl, pathCall(wire.OpExists, "/other"))
	}
}
