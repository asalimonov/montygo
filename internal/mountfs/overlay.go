package mountfs

import (
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

type realTarget uint8

const (
	targetDir realTarget = iota
	targetFile
	targetSymlink
	targetAbsent
)

// classifyTarget uses one lstat, so a refused symlink leaks nothing about its target.
func classifyTarget(dir *os.Root, rel, vpath string) (realTarget, *mountError) {
	fi, err := dir.Lstat(rel)
	switch {
	case err != nil && !isPathEscape(err) && ioKindOf(err) == ioNotFound:
		return targetAbsent, nil
	case err != nil:
		return 0, mapIO(err, vpath)
	case fi.Mode()&fs.ModeSymlink != 0:
		return targetSymlink, nil
	case fi.IsDir():
		return targetDir, nil
	}
	return targetFile, nil
}

// componentWalk classifies one component per step, descending a descriptor as it goes.
type componentWalk struct {
	root      *os.Root
	current   *os.Root
	exhausted bool
}

func (w *componentWalk) here() *os.Root {
	if w.current != nil {
		return w.current
	}
	return w.root
}

func (w *componentWalk) step(component, vpath string) (realTarget, *mountError) {
	if w.exhausted {
		return targetAbsent, nil
	}
	target, e := classifyTarget(w.here(), component, vpath)
	if e != nil {
		return 0, e
	}
	if target != targetDir {
		w.exhausted = true
		return target, nil
	}
	next, err := w.here().OpenRoot(component)
	if err != nil {
		if !isPathEscape(err) && ioKindOf(err) == ioNotFound {
			w.exhausted = true
			return targetAbsent, nil
		}
		return 0, mapIO(err, vpath)
	}
	w.close()
	w.current = next
	return target, nil
}

func (w *componentWalk) close() {
	if w.current != nil {
		_ = w.current.Close()
		w.current = nil
	}
}

func rejectSymlinkChain(dir *os.Root, rel, vpath string) *mountError {
	if rel == "" || rel == "." {
		return nil
	}
	walk := &componentWalk{root: dir}
	defer walk.close()
	for _, component := range strings.Split(rel, "/") {
		target, e := walk.step(component, vpath)
		if e != nil {
			return e
		}
		switch target {
		case targetSymlink:
			return pathEscape(vpath)
		case targetAbsent, targetFile:
			return nil
		}
	}
	return nil
}

func directoryPrefix(rel string) string {
	if rel == "" {
		return ""
	}
	return rel + "/"
}

func (m *mount) resolveReal(vpath string) (string, *mountError) {
	rel, e := resolveVirtualPath(vpath, m.root.virtualPath)
	if e != nil {
		return "", e
	}
	if e := rejectSymlinkChain(m.root.dir, dirOp(rel), vpath); e != nil {
		return "", e
	}
	return rel, nil
}

func (m *mount) checkedRefPath(ref *overlayEntry, vpath string) (string, *mountError) {
	if e := rejectSymlinkChain(m.root.dir, ref.relative, vpath); e != nil {
		return "", e
	}
	return ref.relative, nil
}

func (m *mount) availableMemory() (memoryBudget, *mountError) {
	usage := m.overlay.memoryUsage()
	if usage > m.memLimit {
		return memoryBudget{}, memoryLimitExceeded(m.memLimit)
	}
	return memoryBudget{available: m.memLimit - usage, limit: m.memLimit}, nil
}

type lookupFailure uint8

const (
	lookupMissing lookupFailure = iota
	lookupPropagate
)

// resolveRealPathState returns the dir-op spelling when the real path is present.
func (m *mount) resolveRealPathState(vpath string, onFailure lookupFailure) (string, bool, *mountError) {
	target, e := m.resolveReal(vpath)
	if e != nil {
		if onFailure == lookupMissing {
			return "", false, nil
		}
		return "", false, e
	}
	rel := dirOp(target)
	_, err := m.root.dir.Stat(rel)
	switch {
	case err == nil:
		return rel, true, nil
	case !isPathEscape(err) && ioKindOf(err) == ioNotFound, onFailure == lookupMissing:
		return "", false, nil
	}
	return "", false, mapIO(err, vpath)
}

func (m *mount) overlayExecute(call *wire.OsCall, mode string) (any, *mountError) {
	path := call.Path
	switch call.Op {
	case wire.OpResolve, wire.OpAbsolute:
		return value.Path(value.NormalizeVirtualPath(path)), nil
	case wire.OpWriteText:
		return m.overlayWrite(path, []byte(call.Text), int64(utf8.RuneCountInString(call.Text)))
	case wire.OpWriteBytes:
		return m.overlayWrite(path, append([]byte{}, call.Data...), int64(len(call.Data)))
	case wire.OpAppendText:
		if e := m.overlayAppend(path, []byte(call.Text)); e != nil {
			return nil, e
		}
		return int64(utf8.RuneCountInString(call.Text)), nil
	case wire.OpAppendBytes:
		if e := m.overlayAppend(path, call.Data); e != nil {
			return nil, e
		}
		return int64(len(call.Data)), nil
	case wire.OpRename:
		return nil, m.overlayRename(path, call.Dst)
	case wire.OpOpen:
		return m.overlayOpen(path, mode)
	}

	rel, e := resolveVirtualPath(path, m.root.virtualPath)
	if e != nil {
		return nil, e
	}
	switch call.Op {
	case wire.OpExists:
		return m.overlayPredicate(rel, path, func(k entryKind) bool { return k != entDeleted }, hostExists)
	case wire.OpIsFile:
		return m.overlayPredicate(rel, path, func(k entryKind) bool { return k == entFile || k == entRealFileRef }, hostIsFile)
	case wire.OpIsDir:
		return m.overlayPredicate(rel, path, func(k entryKind) bool { return k == entDirectory }, hostIsDir)
	case wire.OpIsSymlink:
		return m.overlayIsSymlink(rel, path), nil
	case wire.OpReadText:
		content, e := m.overlayRead(rel, path)
		if e != nil {
			return nil, e
		}
		return bytesToUTF8(content)
	case wire.OpReadBytes:
		return m.overlayRead(rel, path)
	case wire.OpMkdir:
		return nil, m.overlayMkdir(rel, call.Parents, call.ExistOK, path)
	case wire.OpUnlink:
		return nil, m.overlayUnlink(rel, path)
	case wire.OpRmdir:
		return nil, m.overlayRmdir(rel, path)
	case wire.OpIterdir:
		return m.overlayIterdir(rel, path)
	case wire.OpStat:
		return m.overlayStat(rel, path)
	}
	return nil, nil
}

func (m *mount) overlayPredicate(rel, vpath string, byKind func(entryKind) bool, host func(*os.Root, string) bool) (any, *mountError) {
	if entry := m.overlay.get(rel); entry != nil {
		return byKind(entry.kind), nil
	}
	real, present, e := m.resolveRealPathState(vpath, lookupMissing)
	if e != nil {
		return nil, e
	}
	return present && host(m.root.dir, real), nil
}

func (m *mount) overlayIsSymlink(rel, vpath string) bool {
	if m.overlay.get(rel) != nil {
		return false
	}
	target, e := resolveVirtualPath(vpath, m.root.virtualPath)
	if e != nil {
		return false
	}
	op := dirOp(target)
	parent := ""
	if i := strings.LastIndexByte(op, '/'); i >= 0 {
		parent = op[:i]
	}
	return rejectSymlinkChain(m.root.dir, parent, vpath) == nil && hostIsSymlink(m.root.dir, op)
}

func (m *mount) overlayRead(rel, vpath string) ([]byte, *mountError) {
	entry := m.overlay.get(rel)
	if entry == nil {
		target, e := m.resolveReal(vpath)
		if e != nil {
			return nil, e
		}
		budget, e := m.availableMemory()
		if e != nil {
			return nil, e
		}
		return readFileLimited(m.root.dir, dirOp(target), vpath, budget)
	}
	switch entry.kind {
	case entFile:
		budget, e := m.availableMemory()
		if e != nil {
			return nil, e
		}
		if e := budget.check(uint64(len(entry.content))); e != nil {
			return nil, e
		}
		return append([]byte{}, entry.content...), nil
	case entRealFileRef:
		ref, e := m.checkedRefPath(entry, vpath)
		if e != nil {
			return nil, e
		}
		budget, e := m.availableMemory()
		if e != nil {
			return nil, e
		}
		return readFileLimited(m.root.dir, ref, vpath, budget)
	case entDirectory:
		return nil, isADirectory(vpath)
	}
	return nil, notFound(vpath)
}

func (m *mount) overlayWrite(vpath string, data []byte, result int64) (any, *mountError) {
	n := uint64(len(data))
	if e := m.checkWriteLimit(n); e != nil {
		return nil, e
	}
	rel, e := resolveVirtualPath(vpath, m.root.virtualPath)
	if e != nil {
		return nil, e
	}
	if e := m.ensureParentExists(rel, vpath); e != nil {
		return nil, e
	}
	if e := m.rejectDirectoryTarget(rel, vpath); e != nil {
		return nil, e
	}
	if e := m.overlay.checkFileReplacement(rel, n, m.memLimit); e != nil {
		return nil, e
	}
	if e := m.overlay.insert(rel, fileEntry(data, currentTimestamp()), m.memLimit); e != nil {
		return nil, e
	}
	m.commitWriteBytes(n)
	return result, nil
}

func (m *mount) overlayAppend(vpath string, data []byte) *mountError {
	rel, e := resolveVirtualPath(vpath, m.root.virtualPath)
	if e != nil {
		return e
	}
	if e := m.ensureParentExists(rel, vpath); e != nil {
		return e
	}
	if e := m.rejectDirectoryTarget(rel, vpath); e != nil {
		return e
	}
	current := m.overlay.get(rel)
	targetIsOverlayFile := current != nil && current.kind == entFile
	existingLen, e := m.existingFileLen(rel, vpath)
	if e != nil {
		return e
	}
	n := uint64(len(data))
	charged := n
	if m.writeLimit != nil && !targetIsOverlayFile {
		charged = satAdd(existingLen, n)
	}
	if e := m.checkWriteLimit(charged); e != nil {
		return e
	}
	appended, e := m.overlay.appendFile(rel, data, currentTimestamp(), m.memLimit)
	if e != nil {
		return e
	}
	if !appended {
		finalLen := satAdd(existingLen, n)
		if e := m.overlay.checkFileReplacement(rel, finalLen, m.memLimit); e != nil {
			return e
		}
		budget, e := m.availableMemory()
		if e != nil {
			return e
		}
		if e := budget.check(finalLen); e != nil {
			return e
		}
		shrunk, e := budget.shrink(n)
		if e != nil {
			return e
		}
		content, e := m.existingFileBytes(rel, vpath, shrunk)
		if e != nil {
			return e
		}
		content = append(content, data...)
		if e := m.overlay.insert(rel, fileEntry(content, currentTimestamp()), m.memLimit); e != nil {
			return e
		}
	}
	m.commitWriteBytes(charged)
	return nil
}

func (m *mount) existingFileLen(rel, vpath string) (uint64, *mountError) {
	entry := m.overlay.get(rel)
	if entry == nil {
		real, present, e := m.resolveRealPathState(vpath, lookupPropagate)
		if e != nil || !present {
			return 0, e
		}
		return m.hostFileLen(real, vpath)
	}
	switch entry.kind {
	case entFile:
		return uint64(len(entry.content)), nil
	case entRealFileRef:
		ref, e := m.checkedRefPath(entry, vpath)
		if e != nil {
			return 0, e
		}
		return m.hostFileLen(ref, vpath)
	case entDirectory:
		return 0, isADirectory(vpath)
	}
	return 0, nil
}

func (m *mount) hostFileLen(rel, vpath string) (uint64, *mountError) {
	fi, err := m.root.dir.Stat(rel)
	if err != nil {
		return 0, mapIO(err, vpath)
	}
	return uint64(max(fi.Size(), 0)), nil
}

func (m *mount) existingFileBytes(rel, vpath string, budget memoryBudget) ([]byte, *mountError) {
	entry := m.overlay.get(rel)
	if entry == nil {
		real, present, e := m.resolveRealPathState(vpath, lookupPropagate)
		if e != nil || !present {
			return nil, e
		}
		return readFileLimited(m.root.dir, real, vpath, budget)
	}
	switch entry.kind {
	case entFile:
		if e := budget.check(uint64(len(entry.content))); e != nil {
			return nil, e
		}
		return append([]byte{}, entry.content...), nil
	case entRealFileRef:
		ref, e := m.checkedRefPath(entry, vpath)
		if e != nil {
			return nil, e
		}
		return readFileLimited(m.root.dir, ref, vpath, budget)
	case entDirectory:
		return nil, isADirectory(vpath)
	}
	return nil, nil
}

// rejectDirectoryTarget refuses writes over directories and over any symlink.
func (m *mount) rejectDirectoryTarget(rel, vpath string) *mountError {
	if entry := m.overlay.get(rel); entry != nil {
		if entry.kind == entDirectory {
			return isADirectory(vpath)
		}
		return nil
	}
	target, e := m.resolveReal(vpath)
	if e != nil {
		return e
	}
	kind, e := classifyTarget(m.root.dir, dirOp(target), vpath)
	if e != nil {
		return e
	}
	switch kind {
	case targetDir:
		return isADirectory(vpath)
	case targetSymlink:
		return pathEscape(vpath)
	}
	return nil
}

func (m *mount) ensureParentExists(rel, vpath string) *mountError {
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return nil
	}
	walk := &componentWalk{root: m.root.dir}
	defer walk.close()
	current := ""
	for _, component := range strings.Split(rel[:i], "/") {
		if current != "" {
			current += "/"
		}
		current += component
		real, e := walk.step(component, vpath)
		if e != nil {
			return e
		}
		if entry := m.overlay.get(current); entry != nil {
			if entry.kind != entDirectory {
				return notFound(vpath)
			}
			continue
		}
		switch real {
		case targetDir:
		case targetSymlink:
			return pathEscape(vpath)
		default:
			return notFound(vpath)
		}
	}
	return nil
}

func (m *mount) overlayMkdir(rel string, parents, existOK bool, vpath string) *mountError {
	if entry := m.overlay.get(rel); entry != nil {
		switch entry.kind {
		case entDirectory:
			if existOK {
				return nil
			}
			return fileExists(vpath)
		case entFile, entRealFileRef:
			return fileExists(vpath)
		}
	} else {
		target, e := m.resolveReal(vpath)
		if e != nil {
			return e
		}
		kind, e := classifyTarget(m.root.dir, dirOp(target), vpath)
		if e != nil {
			return e
		}
		switch kind {
		case targetDir:
			if existOK {
				return nil
			}
			return fileExists(vpath)
		case targetFile:
			return fileExists(vpath)
		case targetSymlink:
			return pathEscape(vpath)
		}
	}
	var e *mountError
	if parents {
		e = m.createOverlayParents(rel)
	} else {
		e = m.ensureParentExists(rel, vpath)
	}
	if e != nil {
		return e
	}
	return m.overlay.insert(rel, directoryEntry(currentTimestamp()), m.memLimit)
}

func (m *mount) createOverlayParents(rel string) *mountError {
	walk := &componentWalk{root: m.root.dir}
	defer walk.close()
	current := ""
	for _, component := range strings.Split(rel, "/") {
		if current != "" {
			current += "/"
		}
		current += component
		currentVpath := formatChildPath(m.root.virtualPath, current)
		real, e := walk.step(component, currentVpath)
		if e != nil {
			return e
		}
		if entry := m.overlay.get(current); entry != nil {
			switch entry.kind {
			case entFile, entRealFileRef:
				return notADirectory(currentVpath)
			case entDeleted:
				if e := m.overlay.insert(current, directoryEntry(currentTimestamp()), m.memLimit); e != nil {
					return e
				}
			}
			continue
		}
		switch real {
		case targetDir:
			continue
		case targetSymlink:
			return pathEscape(currentVpath)
		case targetFile:
			return notADirectory(currentVpath)
		}
		if e := m.overlay.insert(current, directoryEntry(currentTimestamp()), m.memLimit); e != nil {
			return e
		}
	}
	return nil
}

func (m *mount) overlayUnlink(rel, vpath string) *mountError {
	if entry := m.overlay.get(rel); entry != nil {
		switch entry.kind {
		case entFile, entRealFileRef:
			return m.overlay.insert(rel, deletedEntry(), m.memLimit)
		case entDirectory:
			return isADirectory(vpath)
		}
		return notFound(vpath)
	}
	if e := m.ensureParentExists(rel, vpath); e != nil {
		return e
	}
	target, e := m.resolveReal(vpath)
	if e != nil {
		return e
	}
	kind, e := classifyTarget(m.root.dir, dirOp(target), vpath)
	if e != nil {
		return e
	}
	switch kind {
	case targetFile:
		return m.overlay.insert(rel, deletedEntry(), m.memLimit)
	case targetDir:
		return isADirectory(vpath)
	case targetSymlink:
		return pathEscape(vpath)
	}
	return notFound(vpath)
}

func (m *mount) overlayRmdir(rel, vpath string) *mountError {
	if rel == "" {
		return pathEscape(vpath)
	}
	if entry := m.overlay.get(rel); entry != nil {
		switch entry.kind {
		case entDirectory:
			if m.overlayDirectoryHasChildren(rel) {
				return directoryNotEmpty(vpath)
			}
			return m.overlay.insert(rel, deletedEntry(), m.memLimit)
		case entFile, entRealFileRef:
			return notADirectory(vpath)
		}
		return notFound(vpath)
	}
	if e := m.ensureParentExists(rel, vpath); e != nil {
		return e
	}
	target, e := m.resolveReal(vpath)
	if e != nil {
		return e
	}
	op := dirOp(target)
	if !hostIsDir(m.root.dir, op) {
		if hostExists(m.root.dir, op) {
			return notADirectory(vpath)
		}
		return notFound(vpath)
	}
	visible, e := m.realDirectoryHasVisibleChildren(rel, op, vpath)
	if e != nil {
		return e
	}
	if visible || m.overlayDirectoryHasChildren(rel) {
		return directoryNotEmpty(vpath)
	}
	return m.overlay.insert(rel, deletedEntry(), m.memLimit)
}

func (m *mount) overlayDirectoryHasChildren(rel string) bool {
	for _, key := range m.overlay.prefixKeys(directoryPrefix(rel)) {
		if key != rel && m.overlay.get(key).kind != entDeleted {
			return true
		}
	}
	return false
}

func (m *mount) realDirectoryHasVisibleChildren(rel, op, vpath string) (bool, *mountError) {
	f, err := openDir(m.root.dir, op)
	if err != nil {
		return false, mapIO(err, vpath)
	}
	defer func() { _ = f.Close() }()
	prefix := directoryPrefix(rel)
	visible := false
	for {
		batch, err := f.ReadDir(readDirBatch)
		for _, entry := range batch {
			child := m.overlay.get(prefix + lossyUTF8(entry.Name()))
			if child == nil || child.kind != entDeleted {
				visible = true
				return visible, nil
			}
		}
		if err != nil || len(batch) == 0 {
			return visible, nil
		}
	}
}

func (m *mount) overlayStat(rel, vpath string) (any, *mountError) {
	entry := m.overlay.get(rel)
	if entry == nil {
		target, e := m.resolveReal(vpath)
		if e != nil {
			return nil, e
		}
		return hostStat(m.root.dir, dirOp(target), vpath)
	}
	switch entry.kind {
	case entFile:
		return fileStat(int64(len(entry.content)), entry.mtime), nil
	case entRealFileRef:
		if _, e := m.checkedRefPath(entry, vpath); e != nil {
			return nil, e
		}
		return fileStat(entry.size, entry.mtime), nil
	case entDirectory:
		return dirStat(entry.mtime), nil
	}
	return nil, notFound(vpath)
}

func (m *mount) overlayIterdir(rel, vpath string) (any, *mountError) {
	hostDir, merge := "", false
	if entry := m.overlay.get(rel); entry != nil {
		switch entry.kind {
		case entFile, entRealFileRef:
			return nil, notADirectory(vpath)
		case entDeleted:
			return nil, notFound(vpath)
		}
	} else {
		target, e := m.resolveReal(vpath)
		if e != nil {
			return nil, e
		}
		op := dirOp(target)
		switch {
		case hostIsDir(m.root.dir, op):
			hostDir, merge = op, true
		case !hostExists(m.root.dir, op):
			return nil, notFound(vpath)
		default:
			return nil, notADirectory(vpath)
		}
	}

	prefix := directoryPrefix(rel)
	seen := map[string]struct{}{}
	entries := []any{}
	budget, e := m.availableMemory()
	if e != nil {
		return nil, e
	}
	var usage uint64
	for _, key := range m.overlay.prefixKeys(prefix) {
		rest := key[len(prefix):]
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		usage = satAdd(satAdd(usage, satMul(uint64(len(rest)), 2)), listingEntryMemoryUsage)
		if e := budget.check(usage); e != nil {
			return nil, e
		}
		seen[rest] = struct{}{}
		if m.overlay.get(key).kind != entDeleted {
			child := formatChildPath(vpath, rest)
			usage = satAdd(satAdd(usage, uint64(len(child))), listingEntryMemoryUsage)
			if e := budget.check(usage); e != nil {
				return nil, e
			}
			entries = append(entries, value.Path(child))
		}
	}

	if merge {
		remaining, e := budget.shrink(usage)
		if e != nil {
			return nil, e
		}
		names, e := hostListVisibleDirEntryNames(m.root.dir, hostDir, vpath, remaining.halved())
		if e != nil {
			return nil, e
		}
		for _, name := range names {
			usage = satAdd(satAdd(usage, uint64(len(name))), listingEntryMemoryUsage)
		}
		for _, name := range names {
			if _, ok := seen[name]; ok {
				continue
			}
			child := formatChildPath(vpath, name)
			usage = satAdd(satAdd(usage, uint64(len(child))), listingEntryMemoryUsage)
			if e := budget.check(usage); e != nil {
				return nil, e
			}
			entries = append(entries, value.Path(child))
		}
	}
	return entries, nil
}

type plannedMove struct {
	oldKey string
	newKey string
	entry  *overlayEntry
}

// overlayRename preflights every check before mutating the overlay.
func (m *mount) overlayRename(srcV, dstV string) *mountError {
	st := m.overlay
	srcRel, e := resolveVirtualPath(srcV, m.root.virtualPath)
	if e != nil {
		return e
	}
	dstRel, e := resolveVirtualPath(dstV, m.root.virtualPath)
	if e != nil {
		return e
	}
	if srcRel == "" {
		return pathEscape(srcV)
	}
	if dstRel == "" {
		return pathEscape(dstV)
	}
	if e := m.ensureParentExists(dstRel, dstV); e != nil {
		return e
	}
	if st.get(dstRel) == nil {
		if _, e := m.resolveReal(dstV); e != nil {
			return e
		}
	}
	if e := m.ensureParentExists(srcRel, srcV); e != nil {
		return e
	}
	if entry := st.get(srcRel); entry != nil && entry.kind == entDeleted {
		return notFound(srcV)
	}

	var srcIsDir bool
	if entry := st.get(srcRel); entry != nil {
		srcIsDir = entry.kind == entDirectory
	} else {
		target, e := m.resolveReal(srcV)
		if e != nil {
			return e
		}
		srcIsDir = hostIsDir(m.root.dir, dirOp(target))
	}
	if e := m.rejectRenameTypeMismatch(dstRel, srcIsDir, dstV); e != nil {
		return e
	}
	if srcIsDir {
		if e := m.rejectRenameOntoNonemptyDir(dstRel, dstV); e != nil {
			return e
		}
		if strings.HasPrefix(dstRel, srcRel+"/") {
			return ioErr(ioInvalidInput, "Invalid argument", srcV)
		}
	}

	sourceIsOverlay := st.get(srcRel) != nil
	var realSource *overlayEntry
	if !sourceIsOverlay {
		target, e := m.resolveReal(srcV)
		if e != nil {
			return e
		}
		op := dirOp(target)
		kind, e := classifyTarget(m.root.dir, op, srcV)
		if e != nil {
			return e
		}
		switch kind {
		case targetFile:
			realSource = fileRefFromRelative(m.root.dir, op)
			if realSource == nil {
				return notFound(srcV)
			}
		case targetDir:
			realSource = directoryEntry(hostDirMtime(m.root.dir, op))
		case targetSymlink:
			return pathEscape(srcV)
		default:
			return notFound(srcV)
		}
	}
	source := st.get(srcRel)
	if source == nil {
		source = realSource
	}

	var overlayMoves, realMoves []plannedMove
	if srcIsDir {
		srcPrefix, dstPrefix := srcRel+"/", dstRel+"/"
		childKeys := st.prefixKeys(srcPrefix)
		var planUsage uint64
		for _, key := range childKeys {
			suffix := uint64(len(key)) - uint64(len(srcPrefix))
			planUsage = satAdd(planUsage, satMul(uint64(len(key)), 2))
			planUsage = satAdd(planUsage, satAdd(uint64(len(dstPrefix)), suffix))
			planUsage = satAdd(planUsage, entryMemoryUsage)
		}
		available, e := m.availableMemory()
		if e != nil {
			return e
		}
		remaining, e := available.shrink(planUsage)
		if e != nil {
			return e
		}
		handled := make(map[string]struct{}, len(childKeys))
		for _, key := range childKeys {
			handled[key] = struct{}{}
			overlayMoves = append(overlayMoves, plannedMove{oldKey: key, newKey: dstPrefix + key[len(srcPrefix):]})
		}
		if target, e := m.resolveReal(srcV); e == nil && hostIsDir(m.root.dir, dirOp(target)) {
			children, e := m.collectRealDescendants(dirOp(target), srcPrefix, handled, srcV, remaining)
			if e != nil {
				if e.kind == errMemoryLimit || e.kind == errPathEscape || (e.kind == errIO && e.io == ioInvalidData) {
					return e
				}
				children = nil
			}
			for _, child := range children {
				child.newKey = dstPrefix + strings.TrimPrefix(child.oldKey, srcPrefix)
				realMoves = append(realMoves, child)
			}
		}
	}

	deleted := deletedEntry()
	batch := []replacement{{srcRel, deleted}, {dstRel, source}}
	for _, move := range overlayMoves {
		batch = append(batch, replacement{move.oldKey, deleted}, replacement{move.newKey, st.get(move.oldKey)})
	}
	for _, move := range realMoves {
		batch = append(batch, replacement{move.oldKey, deleted}, replacement{move.newKey, move.entry})
	}
	if e := st.checkReplacements(batch, m.memLimit); e != nil {
		return e
	}

	entry := realSource
	if sourceIsOverlay {
		entry = st.remove(srcRel)
	}
	descendants := make([]plannedMove, 0, len(overlayMoves)+len(realMoves))
	for _, move := range overlayMoves {
		move.entry = st.remove(move.oldKey)
		descendants = append(descendants, move)
	}
	descendants = append(descendants, realMoves...)

	st.insertUnchecked(srcRel, deletedEntry())
	st.insertUnchecked(dstRel, entry)
	for _, move := range descendants {
		st.insertUnchecked(move.oldKey, deletedEntry())
		st.insertUnchecked(move.newKey, move.entry)
	}
	return nil
}

func (m *mount) rejectRenameTypeMismatch(dstRel string, srcIsDir bool, dstV string) *mountError {
	known, dstIsDir := false, false
	if entry := m.overlay.get(dstRel); entry != nil {
		switch entry.kind {
		case entDirectory:
			known, dstIsDir = true, true
		case entFile, entRealFileRef:
			known = true
		}
	} else if target, e := resolveVirtualPath(dstV, m.root.virtualPath); e == nil {
		op := dirOp(target)
		switch {
		case hostIsDir(m.root.dir, op):
			known, dstIsDir = true, true
		case hostExists(m.root.dir, op):
			known = true
		}
	}
	switch {
	case known && dstIsDir && !srcIsDir:
		return isADirectory(dstV)
	case known && !dstIsDir && srcIsDir:
		return notADirectory(dstV)
	}
	return nil
}

func (m *mount) rejectRenameOntoNonemptyDir(dstRel, dstV string) *mountError {
	if entry := m.overlay.get(dstRel); entry != nil {
		if entry.kind != entDirectory {
			return nil
		}
	} else if target, e := resolveVirtualPath(dstV, m.root.virtualPath); e != nil || !hostIsDir(m.root.dir, dirOp(target)) {
		return nil
	}
	if m.overlayDirectoryHasChildren(dstRel) {
		return directoryNotEmpty(dstV)
	}
	if target, e := resolveVirtualPath(dstV, m.root.virtualPath); e == nil && hostIsDir(m.root.dir, dirOp(target)) {
		visible, e := m.realDirectoryHasVisibleChildren(dstRel, dirOp(target), dstV)
		if e != nil {
			return e
		}
		if visible {
			return directoryNotEmpty(dstV)
		}
	}
	return nil
}

// collectRealDescendants captures a real tree for a rename, charging each entry to budget.
func (m *mount) collectRealDescendants(rootRel, prefix string, handled map[string]struct{}, vpath string, budget memoryBudget) ([]plannedMove, *mountError) {
	type pending struct{ rel, prefix string }
	var result []plannedMove
	dirs := []pending{{rootRel, prefix}}
	usage := satAdd(uint64(len(rootRel)), uint64(len(prefix)))
	for len(dirs) > 0 {
		current := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		f, err := openDir(m.root.dir, current.rel)
		if err != nil {
			return nil, mapIO(err, vpath)
		}
		e := eachDirEntry(f, vpath, func(entry fs.DirEntry) (bool, *mountError) {
			name := entry.Name()
			if !utf8.ValidString(name) {
				return true, ioErr(ioInvalidData, "directory contains an entry with a non-UTF-8 name", vpath)
			}
			key := current.prefix + name
			if _, ok := handled[key]; ok || m.overlay.get(key) != nil {
				return false, nil
			}
			typ := entry.Type()
			if typ&fs.ModeSymlink != 0 {
				return true, pathEscape(vpath)
			}
			childRel := joinMountRelative(current.rel, name)
			switch {
			case typ.IsRegular():
				ref := fileRefFromRelative(m.root.dir, childRel)
				if ref == nil {
					return false, nil
				}
				usage = satAdd(satAdd(satAdd(usage, uint64(len(key))), uint64(len(ref.relative))), realDescendantMemoryUsage)
				if e := budget.check(usage); e != nil {
					return true, e
				}
				result = append(result, plannedMove{oldKey: key, entry: ref})
			case typ.IsDir():
				usage = satAdd(satAdd(satAdd(usage, satMul(uint64(len(key)), 2)), uint64(len(childRel))), realDescendantMemoryUsage)
				if e := budget.check(usage); e != nil {
					return true, e
				}
				result = append(result, plannedMove{oldKey: key, entry: directoryEntry(hostDirMtime(m.root.dir, childRel))})
				dirs = append(dirs, pending{childRel, key + "/"})
			}
			return false, nil
		})
		_ = f.Close()
		if e != nil {
			return nil, e
		}
	}
	return result, nil
}

func (m *mount) overlayOpen(path, mode string) (any, *mountError) {
	switch mode[0] {
	case 'r':
		rel, e := resolveVirtualPath(path, m.root.virtualPath)
		if e != nil {
			return nil, e
		}
		if entry := m.overlay.get(rel); entry != nil {
			switch entry.kind {
			case entDirectory:
				return nil, isADirectory(path)
			case entDeleted:
				return nil, notFound(path)
			}
		} else {
			real, present, e := m.resolveRealPathState(path, lookupPropagate)
			switch {
			case e != nil:
				return nil, e
			case !present:
				return nil, notFound(path)
			case hostIsDir(m.root.dir, real):
				return nil, isADirectory(path)
			}
		}
	case 'w':
		if _, e := m.overlayWrite(path, []byte{}, 0); e != nil {
			return nil, e
		}
	case 'a':
		if e := m.ensureAppendTargetExists(path); e != nil {
			return nil, e
		}
	}
	return fileHandle(path, mode), nil
}

// ensureAppendTargetExists creates a missing target without pulling real content.
func (m *mount) ensureAppendTargetExists(vpath string) *mountError {
	rel, e := resolveVirtualPath(vpath, m.root.virtualPath)
	if e != nil {
		return e
	}
	if entry := m.overlay.get(rel); entry != nil {
		switch entry.kind {
		case entFile, entRealFileRef:
			return nil
		case entDirectory:
			return isADirectory(vpath)
		}
		if e := m.ensureParentExists(rel, vpath); e != nil {
			return e
		}
		return m.overlay.insert(rel, fileEntry(nil, currentTimestamp()), m.memLimit)
	}
	target, e := m.resolveReal(vpath)
	if e != nil {
		return e
	}
	kind, e := classifyTarget(m.root.dir, dirOp(target), vpath)
	if e != nil {
		return e
	}
	switch kind {
	case targetDir:
		return isADirectory(vpath)
	case targetSymlink:
		return pathEscape(vpath)
	case targetFile:
		return m.ensureParentExists(rel, vpath)
	}
	if e := m.ensureParentExists(rel, vpath); e != nil {
		return e
	}
	return m.overlay.insert(rel, fileEntry(nil, currentTimestamp()), m.memLimit)
}
