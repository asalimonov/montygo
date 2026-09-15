package mountfs

import (
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"syscall"
	"time"

	"github.com/asalimonov/montygo/internal/value"
)

const listingEntryMemoryUsage uint64 = 128

type memoryBudget struct {
	available uint64
	limit     uint64
}

func fullBudget(limit uint64) memoryBudget { return memoryBudget{available: limit, limit: limit} }

func (b memoryBudget) check(n uint64) *mountError {
	if n > b.available {
		return memoryLimitExceeded(b.limit)
	}
	return nil
}

func (b memoryBudget) shrink(n uint64) (memoryBudget, *mountError) {
	if n > b.available {
		return b, memoryLimitExceeded(b.limit)
	}
	return memoryBudget{available: b.available - n, limit: b.limit}, nil
}

func (b memoryBudget) halved() memoryBudget {
	return memoryBudget{available: b.available / 2, limit: b.limit}
}

func satAdd(a, b uint64) uint64 {
	if s := a + b; s >= a {
		return s
	}
	return math.MaxUint64
}

func satMul(a, b uint64) uint64 {
	if a != 0 && b > math.MaxUint64/a {
		return math.MaxUint64
	}
	return a * b
}

func statResult(mode, nlink, size int64, mtime float64) value.NamedTuple {
	return value.NamedTuple{
		TypeName:   "StatResult",
		FieldNames: []string{"st_mode", "st_ino", "st_dev", "st_nlink", "st_uid", "st_gid", "st_size", "st_atime", "st_mtime", "st_ctime"},
		Values:     []any{mode, int64(0), int64(0), nlink, int64(0), int64(0), size, mtime, mtime, mtime},
	}
}

func fileStat(size int64, mtime float64) value.NamedTuple {
	return statResult(0o100644, 1, size, mtime)
}

func dirStat(mtime float64) value.NamedTuple { return statResult(0o040755, 2, 4096, mtime) }

func unixSecs(t time.Time) float64 {
	if t.Before(time.Unix(0, 0)) {
		return 0
	}
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}

func currentTimestamp() float64 { return unixSecs(time.Now()) }

func mtimeSecs(fi fs.FileInfo) float64 { return unixSecs(fi.ModTime()) }

func hostDirMtime(dir *os.Root, rel string) float64 {
	if fi, err := dir.Stat(rel); err == nil {
		return mtimeSecs(fi)
	}
	return currentTimestamp()
}

func hostExists(dir *os.Root, rel string) bool {
	_, err := dir.Stat(rel)
	return err == nil
}

func hostIsDir(dir *os.Root, rel string) bool {
	fi, err := dir.Stat(rel)
	return err == nil && fi.IsDir()
}

func hostIsFile(dir *os.Root, rel string) bool {
	fi, err := dir.Stat(rel)
	return err == nil && fi.Mode().IsRegular()
}

func hostIsSymlink(dir *os.Root, rel string) bool {
	fi, err := dir.Lstat(rel)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// rejectNonRegular is a raceable path check for error quality; openRegular decides.
func rejectNonRegular(dir *os.Root, rel, vpath string) *mountError {
	fi, err := dir.Stat(rel)
	switch {
	case err != nil:
		return nil
	case fi.IsDir():
		return isADirectory(vpath)
	case !fi.Mode().IsRegular():
		return permissionDenied(vpath)
	}
	return nil
}

func openRegular(dir *os.Root, rel, vpath string, flag int, perm fs.FileMode) (*os.File, *mountError) {
	f, err := dir.OpenFile(rel, flag|oNonblock, perm)
	if err != nil {
		return nil, mapIO(err, vpath)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, mapIO(err, vpath)
	}
	if fi.Mode().IsRegular() {
		return f, nil
	}
	_ = f.Close()
	if fi.IsDir() {
		return nil, isADirectory(vpath)
	}
	return nil, permissionDenied(vpath)
}

// readFileLimited reads at most budget+1 bytes so enforcement never trusts metadata.
func readFileLimited(dir *os.Root, rel, vpath string, budget memoryBudget) ([]byte, *mountError) {
	if e := rejectNonRegular(dir, rel, vpath); e != nil {
		return nil, e
	}
	f, e := openRegular(dir, rel, vpath, os.O_RDONLY, 0)
	if e != nil {
		return nil, e
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, mapIO(err, vpath)
	}
	size := uint64(max(fi.Size(), 0))
	if e := budget.check(size); e != nil {
		return nil, e
	}
	limit := satAdd(budget.available, 1)
	content, err := readAtMost(f, make([]byte, 0, min(size+1, limit)), limit)
	if err != nil {
		return nil, mapIO(err, vpath)
	}
	if e := budget.check(uint64(len(content))); e != nil {
		return nil, e
	}
	return content, nil
}

func readAtMost(r io.Reader, buf []byte, limit uint64) ([]byte, error) {
	for uint64(len(buf)) < limit {
		if len(buf) == cap(buf) {
			grow := uint64(max(cap(buf), 512))
			grow = min(grow, limit-uint64(len(buf)))
			buf = append(buf, make([]byte, grow)...)[:len(buf)]
		}
		room := min(uint64(cap(buf)-len(buf)), limit-uint64(len(buf)))
		n, err := r.Read(buf[len(buf) : len(buf)+int(room)])
		buf = buf[:len(buf)+n]
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
	return buf, nil
}

func writeBytesToFile(dir *os.Root, rel string, content []byte, vpath string) *mountError {
	return writeWithFlags(dir, rel, content, vpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
}

func appendBytesToFile(dir *os.Root, rel string, content []byte, vpath string) *mountError {
	return writeWithFlags(dir, rel, content, vpath, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
}

func writeWithFlags(dir *os.Root, rel string, content []byte, vpath string, flag int) *mountError {
	if e := rejectNonRegular(dir, rel, vpath); e != nil {
		return e
	}
	f, e := openRegular(dir, rel, vpath, flag, 0o666)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(content); err != nil {
		return mapIO(err, vpath)
	}
	return nil
}

func hostMkdir(dir *os.Root, rel string, parents, existOK bool, vpath string) *mountError {
	var err error
	if parents {
		if fi, statErr := dir.Stat(rel); statErr == nil {
			if fi.IsDir() && existOK {
				return nil
			}
			return fileExists(vpath)
		}
		err = createDirAll(dir, rel)
	} else {
		err = dir.Mkdir(rel, 0o777)
	}
	if err == nil {
		return nil
	}
	if !isPathEscape(err) && ioKindOf(err) == ioAlreadyExists && existOK && hostIsDir(dir, rel) {
		return nil
	}
	return mapIO(err, vpath)
}

// createDirAll follows std's DirBuilder::create_dir_all step for step.
func createDirAll(dir *os.Root, p string) error {
	if p == "" {
		return nil
	}
	err := dir.Mkdir(p, 0o777)
	if err == nil {
		return nil
	}
	if isPathEscape(err) || ioKindOf(err) != ioNotFound {
		if hostIsDir(dir, p) {
			return nil
		}
		return err
	}
	parent := ""
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			parent = p[:i]
			break
		}
	}
	if err := createDirAll(dir, parent); err != nil {
		return err
	}
	if err := dir.Mkdir(p, 0o777); err != nil && !hostIsDir(dir, p) {
		return err
	}
	return nil
}

// hostUnlink removes a file or the link itself, never a directory.
func hostUnlink(dir *os.Root, rel, vpath string) *mountError {
	fi, err := dir.Lstat(rel)
	if err != nil {
		return mapIO(err, vpath)
	}
	if fi.IsDir() {
		return mapIO(&os.PathError{Op: "unlinkat", Path: rel, Err: unlinkDirErrno}, vpath)
	}
	if err := dir.Remove(rel); err != nil {
		return mapIO(err, vpath)
	}
	return nil
}

// hostRmdir removes an empty directory, never a file or link.
func hostRmdir(dir *os.Root, rel, vpath string) *mountError {
	fi, err := dir.Lstat(rel)
	if err != nil {
		return mapIO(err, vpath)
	}
	if !fi.IsDir() {
		return mapIO(&os.PathError{Op: "unlinkat", Path: rel, Err: syscall.ENOTDIR}, vpath)
	}
	if err := dir.Remove(rel); err != nil {
		return mapIO(err, vpath)
	}
	return nil
}

func hostStat(dir *os.Root, rel, vpath string) (any, *mountError) {
	fi, err := dir.Stat(rel)
	if err != nil {
		return nil, mapIO(err, vpath)
	}
	if fi.IsDir() {
		return dirStat(mtimeSecs(fi)), nil
	}
	return fileStat(fi.Size(), mtimeSecs(fi)), nil
}

func openDir(dir *os.Root, rel string) (*os.File, error) {
	return dir.OpenFile(rel, os.O_RDONLY|oDirectory, 0)
}

const readDirBatch = 256

// eachDirEntry streams entries in host order until fn stops or fails.
func eachDirEntry(f *os.File, vpath string, fn func(fs.DirEntry) (bool, *mountError)) *mountError {
	for {
		batch, err := f.ReadDir(readDirBatch)
		for _, entry := range batch {
			stop, e := fn(entry)
			if e != nil || stop {
				return e
			}
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			return nil
		}
		if err != nil {
			return mapIO(err, vpath)
		}
	}
}

func hostIterdir(dir *os.Root, rel, vpath string, budget memoryBudget) (any, *mountError) {
	names, e := hostListVisibleDirEntryNames(dir, rel, vpath, budget.halved())
	if e != nil {
		return nil, e
	}
	var usage uint64
	for _, name := range names {
		usage = satAdd(satAdd(usage, uint64(len(name))), listingEntryMemoryUsage)
	}
	result := make([]any, 0, len(names))
	for _, name := range names {
		path := formatChildPath(vpath, name)
		usage = satAdd(satAdd(usage, uint64(len(path))), listingEntryMemoryUsage)
		if e := budget.check(usage); e != nil {
			return nil, e
		}
		result = append(result, value.Path(path))
	}
	return result, nil
}

// hostListVisibleDirEntryNames hides symlinks that do not resolve inside the mount.
func hostListVisibleDirEntryNames(dir *os.Root, rel, vpath string, budget memoryBudget) ([]string, *mountError) {
	if _, err := dir.Stat(rel); err != nil {
		return nil, mapIO(err, vpath)
	}
	f, err := openDir(dir, rel)
	if err != nil {
		return nil, mapIO(err, vpath)
	}
	defer func() { _ = f.Close() }()
	var names []string
	var usage uint64
	e := eachDirEntry(f, vpath, func(entry fs.DirEntry) (bool, *mountError) {
		if entry.Type()&fs.ModeSymlink != 0 {
			if _, err := dir.Stat(joinMountRelative(rel, entry.Name())); err != nil {
				return false, nil
			}
		}
		name := lossyUTF8(entry.Name())
		usage = satAdd(satAdd(usage, uint64(len(name))), listingEntryMemoryUsage)
		if e := budget.check(usage); e != nil {
			return true, e
		}
		names = append(names, name)
		return false, nil
	})
	if e != nil {
		return nil, e
	}
	return names, nil
}
