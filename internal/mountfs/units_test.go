package mountfs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wire"
)

func TestParseMode(t *testing.T) {
	for _, m := range []Mode{ReadOnly, ReadWrite, Overlay} {
		parsed, err := ParseMode(m.String())
		require.NoError(t, err)
		require.Equal(t, m, parsed)
	}
	_, err := ParseMode("bogus")
	require.EqualError(t, err, "Invalid mode 'bogus', expected 'read-only', 'read-write', or 'overlay'")
}

func TestFormatBytesPretty(t *testing.T) {
	for in, want := range map[uint64]string{
		0: "0 bytes", 999: "999 bytes", 1_000: "1 KB", 1_500: "1.5 KB", 1_600: "1.6 KB",
		4_500: "4.5 KB", 1_950: "2 KB", 1_500_000: "1.5 MB", 100_000_000: "100 MB",
		2_000_000_000: "2 GB", 3_000_000_000_000: "3 TB",
	} {
		require.Equal(t, want, formatBytesPretty(in), "bytes=%d", in)
	}
}

func TestUnicodeDecodeError(t *testing.T) {
	cases := []struct {
		in         string
		msg        string
		start, end uint64
	}{
		{"a\xffb", "'utf-8' codec can't decode byte 0xff in position 1: invalid start byte", 1, 2},
		{"a\xe2(b", "'utf-8' codec can't decode byte 0xe2 in position 1: invalid continuation byte", 1, 2},
		{"ab\xe2\x82", "'utf-8' codec can't decode bytes in position 2-3: unexpected end of data", 2, 4},
		{"\xed\xa0\x80", "'utf-8' codec can't decode byte 0xed in position 0: invalid continuation byte", 0, 1},
		{"\xf0\x9f\x98x", "'utf-8' codec can't decode bytes in position 0-2: invalid continuation byte", 0, 3},
		{"\xc0\x80", "'utf-8' codec can't decode byte 0xc0 in position 0: invalid start byte", 0, 1},
	}
	for _, c := range cases {
		_, e := bytesToUTF8([]byte(c.in))
		require.NotNil(t, e, "%q", c.in)
		exc := e.exception()
		require.Equal(t, "UnicodeDecodeError", exc.ExcType)
		require.Equal(t, c.msg, exc.MessageText())
		require.Equal(t, &wire.UnicodeErrorData{Encoding: "utf-8", ObjectBytes: []byte(c.in), Start: c.start, End: c.end, Reason: exc.Data.Unicode.Reason}, exc.Data.Unicode)
	}
	_, e := bytesToUTF8(append([]byte(strings.Repeat("a", maxUnicodeObjectLen)), 0xff))
	require.Nil(t, e.exception().Data)
	require.Equal(t, "a��b", lossyUTF8("a\xff\xfeb"))
	require.Equal(t, "�", lossyUTF8("\xe2\x82"))
}

func TestPathPolicy(t *testing.T) {
	require.Nil(t, rejectOverlongPath("/mnt/"+strings.Repeat("a", 255)))
	e := rejectOverlongPath("/mnt/" + strings.Repeat("a", 256))
	require.Equal(t, "[Errno 36] File name too long: '/mnt/aaaaaaaaaaaaaaa…aaaaaaaaaaaaaaaaaaaa'", e.exception().MessageText())
	require.Nil(t, rejectOverlongPath(strings.Repeat("/a", 63)))
	require.NotNil(t, rejectOverlongPath(strings.Repeat("/a", 64)))
	_, ok := elideMiddle(strings.Repeat("é", 40))
	require.False(t, ok)
	elided, ok := elideMiddle(strings.Repeat("é", 20) + "x" + strings.Repeat("ü", 20))
	require.True(t, ok)
	require.Equal(t, strings.Repeat("é", 20)+"…"+strings.Repeat("ü", 20), elided)

	for _, c := range []struct{ path, mount, rel string }{
		{"/mnt/a/b", "/mnt", "a/b"}, {"/mnt", "/mnt", ""}, {"/a", "/", "a"}, {"/", "/", ""},
	} {
		rel, ok := stripMountPrefix(c.path, c.mount)
		require.True(t, ok)
		require.Equal(t, c.rel, rel)
	}
	_, ok = stripMountPrefix("/mnt2/a", "/mnt")
	require.False(t, ok)
	require.NotNil(t, rejectDriveOrUNCSegments(`a\b`, "/mnt/a"))
	require.NotNil(t, rejectDriveOrUNCSegments("x/C:", "/mnt/x/C:"))
	require.Nil(t, rejectDriveOrUNCSegments("::double.txt/note:2026.txt", "/mnt"))
}

func TestNewTableOrdering(t *testing.T) {
	dirA, dirB, dirC := t.TempDir(), t.TempDir(), t.TempDir()
	writeHostFile(t, dirA, "f", "a")
	writeHostFile(t, dirB, "f", "b")
	writeHostFile(t, dirC, "f", "c")
	a, b, c := openRootT(t, "/data/", dirA), openRootT(t, "/data/sub", dirB), openRootT(t, "/data", dirC)
	require.Equal(t, "/data", a.VirtualPath())
	tbl := NewTable([]*Spec{
		{Root: a, Mode: ReadOnly, MemoryUsageLimit: DefaultMemoryUsageLimit},
		{Root: b, Mode: ReadOnly, MemoryUsageLimit: DefaultMemoryUsageLimit},
		{Root: c, Mode: ReadOnly, MemoryUsageLimit: DefaultMemoryUsageLimit},
	})
	require.Equal(t, 3, tbl.Len())
	first, ok := tbl.FirstVirtualPath()
	require.True(t, ok)
	require.Equal(t, "/data", first)
	require.Equal(t, "b", callOK(t, tbl, pathCall(wire.OpReadText, "/data/sub/f")))
	require.Equal(t, "c", callOK(t, tbl, pathCall(wire.OpReadText, "/data/f")))
	_, ok = NewTable(nil).FirstVirtualPath()
	require.False(t, ok)

	require.NoError(t, a.Close())
	require.True(t, a.Closed())
	require.NoError(t, a.Close())
}
