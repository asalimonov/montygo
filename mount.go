package montygo

import (
	"context"
	"fmt"

	"github.com/asalimonov/montygo/internal/mountfs"
	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wire"
)

// MountMode selects how a mounted directory may be changed.
type MountMode string

const (
	MountReadOnly  MountMode = "read-only"
	MountReadWrite MountMode = "read-write"
	// MountOverlay keeps writes in memory and discards them when the feed ends.
	MountOverlay MountMode = "overlay"
)

// DefaultMountMemoryUsageLimit is a mount's default memory budget (100 MB).
const DefaultMountMemoryUsageLimit uint64 = mountfs.DefaultMemoryUsageLimit

// MountDirOptions configure a mount.
type MountDirOptions struct {
	HostPath    string
	VirtualPath string
	// Mode defaults to MountOverlay.
	Mode MountMode
	// WriteBytesLimit caps bytes written through the mount per feed; nil is unlimited.
	WriteBytesLimit *uint64
	// MemoryUsageLimit caps overlay data and transient results; nil means 100 MB.
	MemoryUsageLimit *uint64
}

// MountDir mounts a host directory into the sandbox at a virtual path. The
// host directory is opened once, so the mount follows that directory.
type MountDir struct {
	HostPath         string
	VirtualPath      string
	Mode             MountMode
	WriteBytesLimit  *uint64
	MemoryUsageLimit uint64
	root             *mountfs.Root
	mode             mountfs.Mode
}

// NewMountDir validates the options and opens the host directory.
func NewMountDir(opts MountDirOptions) (*MountDir, error) {
	mode := opts.Mode
	if mode == "" {
		mode = MountOverlay
	}
	parsed, err := mountfs.ParseMode(string(mode))
	if err != nil {
		return nil, &ValueError{Message: fmt.Sprintf("invalid mount mode: '%s'. Expected 'read-only', 'read-write' or 'overlay'", mode)}
	}
	limit := DefaultMountMemoryUsageLimit
	if opts.MemoryUsageLimit != nil {
		limit = *opts.MemoryUsageLimit
	}
	root, err := mountfs.OpenRoot(opts.VirtualPath, opts.HostPath)
	if err != nil {
		return nil, &ValueError{Message: err.Error()}
	}
	return &MountDir{
		HostPath:         opts.HostPath,
		VirtualPath:      opts.VirtualPath,
		Mode:             mode,
		WriteBytesLimit:  opts.WriteBytesLimit,
		MemoryUsageLimit: limit,
		root:             root,
		mode:             parsed,
	}, nil
}

// Close releases the host directory; later feeds using the mount fail.
func (m *MountDir) Close() error { return m.root.Close() }

func (m *MountDir) String() string {
	return fmt.Sprintf("MountDir(host_path='%s', virtual_path='%s', mode='%s')", m.HostPath, m.VirtualPath, m.Mode)
}

type mountTable struct{ t *mountfs.Table }

func (m mountTable) HandleOsCall(ctx context.Context, call *wire.OsCall) (bool, any, *wire.Exception) {
	out := m.t.HandleOsCall(ctx, call)
	return out.Handled, out.Value, out.Exception
}

func buildMounts(mounts []*MountDir) (pool.MountTable, string, error) {
	if len(mounts) == 0 {
		return nil, "", nil
	}
	specs := make([]*mountfs.Spec, 0, len(mounts))
	for _, m := range mounts {
		if m == nil {
			continue
		}
		if m.root.Closed() {
			return nil, "", &OptionError{Message: "mount is closed: create a new MountDir"}
		}
		specs = append(specs, &mountfs.Spec{Root: m.root, Mode: m.mode, WriteBytesLimit: m.WriteBytesLimit, MemoryUsageLimit: m.MemoryUsageLimit})
	}
	if len(specs) == 0 {
		return nil, "", nil
	}
	table := mountfs.NewTable(specs)
	first, _ := table.FirstVirtualPath()
	return mountTable{t: table}, first, nil
}
