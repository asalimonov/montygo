package mountfs

import (
	"strings"
	"unicode/utf8"

	"github.com/asalimonov/montygo/internal/value"
)

const (
	pathMax       = 4096
	nameMax       = 255
	depthMax      = 64
	errorPathEdge = 20
)

// dirOp maps the mount root ("") to ".", the spelling os.Root accepts.
func dirOp(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func resolveVirtualPath(vpath, mountVirtual string) (string, *mountError) {
	if e := rejectNullBytes(vpath); e != nil {
		return "", e
	}
	normalized := value.NormalizeVirtualPath(vpath)
	rel, ok := stripMountPrefix(normalized, mountVirtual)
	if !ok {
		return "", noMountPoint(vpath)
	}
	if e := rejectDriveOrUNCSegments(rel, normalized); e != nil {
		return "", e
	}
	return rel, nil
}

func stripMountPrefix(normalized, mountVirtual string) (string, bool) {
	if mountVirtual == "/" {
		return strings.TrimPrefix(normalized, "/"), true
	}
	if normalized == mountVirtual {
		return "", true
	}
	rest, ok := strings.CutPrefix(normalized, mountVirtual)
	if !ok {
		return "", false
	}
	return strings.CutPrefix(rest, "/")
}

func rejectDriveOrUNCSegments(rel, normalized string) *mountError {
	if strings.Contains(rel, `\`) {
		return pathEscape(normalized)
	}
	for _, seg := range strings.Split(rel, "/") {
		if isWindowsDrivePrefix(seg) {
			return pathEscape(normalized)
		}
	}
	return nil
}

func isWindowsDrivePrefix(seg string) bool {
	if len(seg) < 2 || seg[1] != ':' {
		return false
	}
	c := seg[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func containsNullByte(path string) bool { return strings.IndexByte(path, 0) >= 0 }

func rejectNullBytes(path string) *mountError {
	if containsNullByte(path) {
		return valueError("embedded null byte")
	}
	return nil
}

// rejectOverlongPath measures the path as sent, before normalization.
func rejectOverlongPath(path string) *mountError {
	if !isOverlong(path) {
		return nil
	}
	shown := path
	if elided, ok := elideMiddle(path); ok {
		shown = elided
	}
	return ioErr(ioInvalidFilename, "File name too long", shown)
}

func isOverlong(path string) bool {
	if len(path) > pathMax {
		return true
	}
	rest := path
	for count := 0; ; count++ {
		if count == depthMax {
			return true
		}
		idx := strings.IndexByte(rest, '/')
		comp := rest
		if idx >= 0 {
			comp = rest[:idx]
		}
		if len(comp) > nameMax {
			return true
		}
		if idx < 0 {
			return false
		}
		rest = rest[idx+1:]
	}
}

func elideMiddle(path string) (string, bool) {
	starts := make([]int, 0, utf8.RuneCountInString(path))
	for i := range path {
		starts = append(starts, i)
	}
	n := len(starts)
	if n < 2*errorPathEdge+1 {
		return "", false
	}
	return path[:starts[errorPathEdge]] + "…" + path[starts[n-errorPathEdge]:], true
}

func formatChildPath(parent, child string) string {
	if strings.HasSuffix(parent, "/") {
		return parent + child
	}
	return parent + "/" + child
}

func joinMountRelative(rel, child string) string {
	if rel == "" || rel == "." {
		return child
	}
	return rel + "/" + child
}
