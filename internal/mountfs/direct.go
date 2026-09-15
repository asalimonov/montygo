package mountfs

import (
	"unicode/utf8"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

func (m *mount) directExecute(call *wire.OsCall, mode string) (any, *mountError) {
	dir := m.root.dir
	path := call.Path
	boolQuery := func(query func(rel string) bool) (any, *mountError) {
		rel, e := resolveVirtualPath(path, m.root.virtualPath)
		if e != nil {
			return nil, e
		}
		return query(dirOp(rel)), nil
	}
	switch call.Op {
	case wire.OpExists:
		return boolQuery(func(rel string) bool { return hostExists(dir, rel) })
	case wire.OpIsFile:
		return boolQuery(func(rel string) bool { return hostIsFile(dir, rel) })
	case wire.OpIsDir:
		return boolQuery(func(rel string) bool { return hostIsDir(dir, rel) })
	case wire.OpIsSymlink:
		return boolQuery(func(rel string) bool { return hostIsSymlink(dir, rel) })
	case wire.OpResolve, wire.OpAbsolute:
		return value.Path(value.NormalizeVirtualPath(path)), nil
	case wire.OpWriteText:
		return m.directWrite(path, []byte(call.Text), int64(utf8.RuneCountInString(call.Text)), false)
	case wire.OpWriteBytes:
		return m.directWrite(path, call.Data, int64(len(call.Data)), false)
	case wire.OpAppendText:
		return m.directWrite(path, []byte(call.Text), int64(utf8.RuneCountInString(call.Text)), true)
	case wire.OpAppendBytes:
		return m.directWrite(path, call.Data, int64(len(call.Data)), true)
	case wire.OpRename:
		return m.directRename(path, call.Dst)
	case wire.OpOpen:
		return m.directOpen(path, mode)
	}

	rel, e := resolveVirtualPath(path, m.root.virtualPath)
	if e != nil {
		return nil, e
	}
	switch call.Op {
	case wire.OpReadText:
		content, e := readFileLimited(dir, dirOp(rel), path, fullBudget(m.memLimit))
		if e != nil {
			return nil, e
		}
		return bytesToUTF8(content)
	case wire.OpReadBytes:
		return readFileLimited(dir, dirOp(rel), path, fullBudget(m.memLimit))
	case wire.OpMkdir:
		return nil, hostMkdir(dir, dirOp(rel), call.Parents, call.ExistOK, path)
	case wire.OpUnlink:
		return nil, hostUnlink(dir, dirOp(rel), path)
	case wire.OpRmdir:
		if rel == "" {
			return nil, pathEscape(path)
		}
		return nil, hostRmdir(dir, rel, path)
	case wire.OpIterdir:
		return hostIterdir(dir, dirOp(rel), path, fullBudget(m.memLimit))
	case wire.OpStat:
		return hostStat(dir, dirOp(rel), path)
	}
	return nil, nil
}

func (m *mount) directWrite(path string, data []byte, result int64, appendMode bool) (any, *mountError) {
	n := uint64(len(data))
	if e := m.checkWriteLimit(n); e != nil {
		return nil, e
	}
	rel, e := resolveVirtualPath(path, m.root.virtualPath)
	if e != nil {
		return nil, e
	}
	if appendMode {
		e = appendBytesToFile(m.root.dir, dirOp(rel), data, path)
	} else {
		e = writeBytesToFile(m.root.dir, dirOp(rel), data, path)
	}
	if e != nil {
		return nil, e
	}
	m.commitWriteBytes(n)
	return result, nil
}

func (m *mount) directOpen(path, mode string) (any, *mountError) {
	rel, e := resolveVirtualPath(path, m.root.virtualPath)
	if e != nil {
		return nil, e
	}
	dir, op := m.root.dir, dirOp(rel)
	switch mode[0] {
	case 'r':
		if _, err := dir.Stat(op); err != nil {
			return nil, mapIO(err, path)
		}
		if e := rejectNonRegular(dir, op, path); e != nil {
			return nil, e
		}
	case 'w':
		if e := m.checkWriteLimit(0); e != nil {
			return nil, e
		}
		if e := writeBytesToFile(dir, op, nil, path); e != nil {
			return nil, e
		}
		m.commitWriteBytes(0)
	case 'a':
		if e := appendBytesToFile(dir, op, nil, path); e != nil {
			return nil, e
		}
	}
	return fileHandle(path, mode), nil
}

func (m *mount) directRename(src, dst string) (any, *mountError) {
	srcRel, e := resolveVirtualPath(src, m.root.virtualPath)
	if e != nil {
		return nil, e
	}
	dstRel, e := resolveVirtualPath(dst, m.root.virtualPath)
	if e != nil {
		return nil, e
	}
	if srcRel == "" {
		return nil, pathEscape(src)
	}
	if dstRel == "" {
		return nil, pathEscape(dst)
	}
	if err := m.root.dir.Rename(srcRel, dstRel); err != nil {
		return nil, mapIO(err, src)
	}
	return nil, nil
}
