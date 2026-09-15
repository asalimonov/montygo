package mountfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

// DefaultMemoryUsageLimit is the default aggregate memory budget of one mount.
const DefaultMemoryUsageLimit uint64 = 100_000_000

// Root is a host directory opened once and bound to a normalized virtual path.
type Root struct {
	virtualPath string
	hostPath    string
	dir         *os.Root
	closed      atomic.Bool
}

// OpenRoot opens hostPath before resolving its name, so the descriptor is the boundary.
func OpenRoot(virtualPath, hostPath string) (*Root, error) {
	if !strings.HasPrefix(virtualPath, "/") {
		return nil, invalidMount(fmt.Sprintf("virtual path must be absolute, got: '%s'", virtualPath))
	}
	dir, err := os.OpenRoot(hostPath)
	if err != nil {
		return nil, invalidMount(fmt.Sprintf("cannot open host path '%s': %s", hostPath, ioDisplay(err)))
	}
	canonical, err := canonicalize(hostPath)
	if err != nil {
		_ = dir.Close()
		return nil, invalidMount(fmt.Sprintf("cannot resolve host path '%s': %s", hostPath, ioDisplay(err)))
	}
	return &Root{virtualPath: value.NormalizeVirtualPath(virtualPath), hostPath: canonical, dir: dir}, nil
}

func canonicalize(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// VirtualPath returns the normalized virtual prefix.
func (r *Root) VirtualPath() string { return r.virtualPath }

// HostPath returns the canonical host directory; diagnostics only.
func (r *Root) HostPath() string { return r.hostPath }

// Close releases the directory descriptor.
func (r *Root) Close() error {
	if r.closed.Swap(true) {
		return nil
	}
	return r.dir.Close()
}

// Closed reports whether Close was called.
func (r *Root) Closed() bool { return r.closed.Load() }

// Spec configures one mount of a Table.
type Spec struct {
	Root             *Root
	Mode             Mode
	WriteBytesLimit  *uint64
	MemoryUsageLimit uint64
}

// Outcome is the result of Table.HandleOsCall.
type Outcome struct {
	Handled   bool
	Value     any
	Exception *wire.Exception
}

type mount struct {
	root       *Root
	mode       Mode
	writeUsed  uint64
	writeLimit *uint64
	memLimit   uint64
	overlay    *overlayState
}

// Table routes OS calls to mounts, longest virtual prefix first.
type Table struct {
	mu        sync.Mutex
	mounts    []*mount
	firstPath string
	hasFirst  bool
}

// NewTable builds a table without filesystem I/O.
func NewTable(specs []*Spec) *Table {
	t := &Table{}
	for i, spec := range specs {
		if i == 0 {
			t.firstPath, t.hasFirst = spec.Root.VirtualPath(), true
		}
		m := &mount{root: spec.Root, mode: spec.Mode, memLimit: spec.MemoryUsageLimit}
		if spec.WriteBytesLimit != nil {
			limit := *spec.WriteBytesLimit
			m.writeLimit = &limit
		}
		if spec.Mode == Overlay {
			m.overlay = newOverlayState()
		}
		t.push(m)
	}
	return t
}

func (t *Table) push(m *mount) {
	n := len(m.root.virtualPath)
	at := 0
	for at < len(t.mounts) && len(t.mounts[at].root.virtualPath) > n {
		at++
	}
	t.mounts = append(t.mounts, nil)
	copy(t.mounts[at+1:], t.mounts[at:])
	t.mounts[at] = m
}

// Len returns the number of mounts.
func (t *Table) Len() int { return len(t.mounts) }

// FirstVirtualPath returns the virtual path of the first spec as given.
func (t *Table) FirstVirtualPath() (string, bool) { return t.firstPath, t.hasFirst }

// HandleOsCall services a filesystem call covered by a mount.
func (t *Table) HandleOsCall(_ context.Context, call *wire.OsCall) Outcome {
	result, e, handled := t.handle(call)
	switch {
	case !handled:
		return Outcome{}
	case e != nil:
		return Outcome{Handled: true, Exception: e.exception()}
	}
	return Outcome{Handled: true, Value: result}
}

func (t *Table) handle(call *wire.OsCall) (any, *mountError, bool) {
	if call == nil || !call.IsFS() {
		return nil, nil, false
	}
	primary := call.Path
	rejection := rejectOverlongPath(primary)
	if rejection == nil && containsNullByte(primary) {
		rejection = valueError(call.NullMessage(false))
	}
	if rejection != nil {
		if call.IsExistenceCheck() {
			return false, nil, true
		}
		return nil, rejection, true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	index, e, handled := t.route(primary, call)
	if !handled {
		return nil, nil, false
	}
	if e != nil {
		return nil, e, true
	}
	result, e := t.mounts[index].execute(call)
	return result, e, true
}

func (t *Table) route(primary string, call *wire.OsCall) (int, *mountError, bool) {
	src := t.findMount(primary)
	if call.Op != wire.OpRename {
		return src, nil, src >= 0
	}
	if e := rejectOverlongPath(call.Dst); e != nil {
		return 0, e, true
	}
	if containsNullByte(call.Dst) {
		return 0, valueError(call.NullMessage(true)), true
	}
	dst := t.findMount(call.Dst)
	switch {
	case src < 0 && dst < 0:
		return 0, nil, false
	case src >= 0 && src == dst:
		return src, nil, true
	}
	return 0, &mountError{kind: errCrossMount, path: primary, dst: call.Dst}, true
}

func (t *Table) findMount(vpath string) int {
	normalized := value.NormalizeVirtualPath(vpath)
	for i, m := range t.mounts {
		if pathMatchesMount(normalized, m.root.virtualPath) {
			return i
		}
	}
	return -1
}

func pathMatchesMount(normalized, mountVirtual string) bool {
	if mountVirtual == "/" || normalized == mountVirtual {
		return true
	}
	return strings.HasPrefix(normalized, mountVirtual) && len(normalized) > len(mountVirtual) && normalized[len(mountVirtual)] == '/'
}

func (m *mount) execute(call *wire.OsCall) (any, *mountError) {
	mode := ""
	if call.Op == wire.OpOpen {
		canonical, err := value.CanonicalFileMode(call.Mode)
		if err != nil {
			return nil, valueError(err.Error())
		}
		mode = canonical
	}
	if m.mode == ReadOnly && isWriteCall(call, mode) {
		return nil, &mountError{kind: errReadOnly, path: call.Path}
	}
	if m.mode == Overlay {
		return m.overlayExecute(call, mode)
	}
	return m.directExecute(call, mode)
}

func isWriteCall(call *wire.OsCall, mode string) bool {
	if call.Op == wire.OpOpen {
		return value.ModeCreates(mode)
	}
	return call.IsWrite()
}

func (m *mount) checkWriteLimit(n uint64) *mountError {
	if m.writeLimit != nil && satAdd(m.writeUsed, n) > *m.writeLimit {
		return writeLimitExceeded(*m.writeLimit)
	}
	return nil
}

func (m *mount) commitWriteBytes(n uint64) {
	if m.writeLimit != nil {
		m.writeUsed = satAdd(m.writeUsed, n)
	}
}

func fileHandle(path, mode string) *value.FileHandle {
	return &value.FileHandle{Path: value.NormalizeVirtualPath(path), Mode: mode}
}
