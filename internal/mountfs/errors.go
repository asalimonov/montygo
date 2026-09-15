package mountfs

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"syscall"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
)

type errKind uint8

const (
	errNoMountPoint errKind = iota
	errPathEscape
	errValue
	errReadOnly
	errCrossMount
	errIO
	errInvalidUTF8
	errInvalidMount
	errWriteLimit
	errMemoryLimit
)

type ioKind uint8

const (
	ioOther ioKind = iota
	ioNotFound
	ioAlreadyExists
	ioPermissionDenied
	ioIsADirectory
	ioNotADirectory
	ioDirectoryNotEmpty
	ioInvalidFilename
	ioInvalidInput
	ioInvalidData
)

// mountError mirrors monty-fs MountError; exception() is its sandbox rendering.
type mountError struct {
	kind  errKind
	path  string
	dst   string
	msg   string
	io    ioKind
	limit uint64
	utf8  *utf8Failure
}

type utf8Failure struct {
	start     int
	end       int
	firstByte byte
	reason    string
	object    []byte
	hasObject bool
}

// maxUnicodeObjectLen is UnicodeErrorData::MAX_OBJECT_LEN.
const maxUnicodeObjectLen = 64 * 1024

func pathEscape(vpath string) *mountError { return &mountError{kind: errPathEscape, path: vpath} }

func noMountPoint(vpath string) *mountError { return &mountError{kind: errNoMountPoint, path: vpath} }

func valueError(msg string) *mountError { return &mountError{kind: errValue, msg: msg} }

func invalidMount(msg string) *mountError { return &mountError{kind: errInvalidMount, msg: msg} }

func memoryLimitExceeded(limit uint64) *mountError {
	return &mountError{kind: errMemoryLimit, limit: limit}
}

func writeLimitExceeded(limit uint64) *mountError {
	return &mountError{kind: errWriteLimit, limit: limit}
}

func ioErr(kind ioKind, msg, vpath string) *mountError {
	return &mountError{kind: errIO, io: kind, msg: msg, path: vpath}
}

func notFound(vpath string) *mountError { return ioErr(ioNotFound, "No such file or directory", vpath) }

func isADirectory(vpath string) *mountError { return ioErr(ioIsADirectory, "Is a directory", vpath) }

func notADirectory(vpath string) *mountError { return ioErr(ioNotADirectory, "Not a directory", vpath) }

func fileExists(vpath string) *mountError { return ioErr(ioAlreadyExists, "File exists", vpath) }

func permissionDenied(vpath string) *mountError {
	return ioErr(ioPermissionDenied, "Permission denied", vpath)
}

func directoryNotEmpty(vpath string) *mountError {
	return ioErr(ioDirectoryNotEmpty, "Directory not empty", vpath)
}

func (e *mountError) Error() string {
	switch e.kind {
	case errNoMountPoint:
		return "no mount point for path: " + e.path
	case errPathEscape:
		return "path escape detected: " + e.path
	case errValue, errInvalidMount:
		return e.msg
	case errReadOnly:
		return "read-only mount: " + e.path
	case errCrossMount:
		return "cross-mount rename: " + e.path + " -> " + e.dst
	case errIO:
		return "I/O error on " + e.path + ": " + e.msg
	case errInvalidUTF8:
		return fmt.Sprintf("invalid UTF-8 byte 0x%02x at position %d", e.utf8.firstByte, e.utf8.start)
	case errWriteLimit:
		return "disk write limit of " + formatBytesPretty(e.limit) + " exceeded"
	case errMemoryLimit:
		return "mount memory usage limit of " + formatBytesPretty(e.limit) + " exceeded"
	}
	return "mount error"
}

func (e *mountError) exception() *wire.Exception {
	repr := value.StringRepr
	switch e.kind {
	case errNoMountPoint, errPathEscape:
		return wire.NewException("PermissionError", "[Errno 13] Permission denied: "+repr(e.path))
	case errValue:
		return wire.NewException("ValueError", e.msg)
	case errReadOnly:
		return wire.NewException("PermissionError", "[Errno 30] Read-only file system: "+repr(e.path))
	case errCrossMount:
		return wire.NewException("OSError", "[Errno 18] Invalid cross-device link: "+repr(e.path)+" -> "+repr(e.dst))
	case errIO:
		switch e.io {
		case ioNotFound:
			return wire.NewException("FileNotFoundError", "[Errno 2] No such file or directory: "+repr(e.path))
		case ioAlreadyExists:
			return wire.NewException("FileExistsError", "[Errno 17] File exists: "+repr(e.path))
		case ioPermissionDenied:
			return wire.NewException("PermissionError", "[Errno 13] Permission denied: "+repr(e.path))
		case ioIsADirectory:
			return wire.NewException("IsADirectoryError", "[Errno 21] Is a directory: "+repr(e.path))
		case ioNotADirectory:
			return wire.NewException("NotADirectoryError", "[Errno 20] Not a directory: "+repr(e.path))
		case ioDirectoryNotEmpty:
			return wire.NewException("OSError", "[Errno 39] Directory not empty: "+repr(e.path))
		case ioInvalidFilename:
			return wire.NewException("OSError", "[Errno 36] File name too long: "+repr(e.path))
		}
		return wire.NewException("OSError", e.msg+": "+repr(e.path))
	case errInvalidUTF8:
		u := e.utf8
		exc := wire.NewException("UnicodeDecodeError", unicodeDecodeErrorMsg("utf-8", u.firstByte, u.start, u.end, u.reason))
		if u.hasObject {
			exc.Data = &wire.ExcData{Unicode: &wire.UnicodeErrorData{
				Encoding:    "utf-8",
				ObjectBytes: append([]byte{}, u.object...),
				Start:       uint64(u.start),
				End:         uint64(u.end),
				Reason:      u.reason,
			}}
		}
		return exc
	case errInvalidMount:
		return wire.NewException("TypeError", e.msg)
	case errWriteLimit:
		return wire.NewException("OSError", "disk write limit of "+formatBytesPretty(e.limit)+" exceeded")
	case errMemoryLimit:
		return wire.NewException("MemoryError", "mount memory usage limit of "+formatBytesPretty(e.limit)+" exceeded")
	}
	return wire.NewException("OSError", e.Error())
}

func unicodeDecodeErrorMsg(codec string, firstByte byte, start, end int, reason string) string {
	if end-start == 1 {
		return fmt.Sprintf("'%s' codec can't decode byte 0x%02x in position %d: %s", codec, firstByte, start, reason)
	}
	return fmt.Sprintf("'%s' codec can't decode bytes in position %d-%d: %s", codec, start, end-1, reason)
}

func formatBytesPretty(bytes uint64) string {
	const (
		kb = 1_000
		mb = 1_000_000
		gb = 1_000_000_000
		tb = 1_000_000_000_000
	)
	if bytes < kb {
		return strconv.FormatUint(bytes, 10) + " bytes"
	}
	var v float64
	var unit string
	switch {
	case bytes < mb:
		v, unit = float64(bytes)/kb, "KB"
	case bytes < gb:
		v, unit = float64(bytes)/mb, "MB"
	case bytes < tb:
		v, unit = float64(bytes)/gb, "GB"
	default:
		v, unit = float64(bytes)/tb, "TB"
	}
	if math.Mod(math.Round(v*10), 10) < float64Epsilon {
		return strconv.FormatFloat(v, 'f', 0, 64) + " " + unit
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + unit
}

const float64Epsilon = 2.220446049250313e-16

const pathEscapesText = "path escapes from parent"

// isPathEscape detects os.Root's confinement refusal, which carries no errno.
func isPathEscape(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err.Error() == pathEscapesText {
			return true
		}
	}
	return false
}

func ioKindOf(err error) ioKind {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ioOther
	}
	switch errno {
	case syscall.ENOENT:
		return ioNotFound
	case syscall.EEXIST:
		return ioAlreadyExists
	case syscall.EACCES, syscall.EPERM:
		return ioPermissionDenied
	case syscall.EISDIR:
		return ioIsADirectory
	case syscall.ENOTDIR:
		return ioNotADirectory
	case syscall.ENOTEMPTY:
		return ioDirectoryNotEmpty
	case syscall.ENAMETOOLONG:
		return ioInvalidFilename
	case syscall.EINVAL:
		return ioInvalidInput
	}
	return ioOther
}

// ioDisplay renders an OS error the way Rust's io::Error Display does.
func ioDisplay(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		text := errno.Error()
		if text != "" && text[0] >= 'a' && text[0] <= 'z' {
			text = string(text[0]-'a'+'A') + text[1:]
		}
		return text + " (os error " + strconv.Itoa(int(errno)) + ")"
	}
	for inner := errors.Unwrap(err); inner != nil; inner = errors.Unwrap(inner) {
		err = inner
	}
	return err.Error()
}

func mapIO(err error, vpath string) *mountError {
	if isPathEscape(err) {
		return pathEscape(vpath)
	}
	return &mountError{kind: errIO, io: ioKindOf(err), msg: ioDisplay(err), path: vpath}
}
