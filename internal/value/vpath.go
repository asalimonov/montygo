package value

import (
	"fmt"
	"strings"
)

// NormalizeVirtualPath lexically normalizes a virtual POSIX path: "." and
// empty segments are dropped, ".." never climbs above the root, and the
// result is always absolute.
func NormalizeVirtualPath(path string) string {
	if isNormalizedVirtualPath(path) {
		return path
	}
	var out strings.Builder
	for _, seg := range strings.Split(path, "/") {
		switch seg {
		case "", ".":
		case "..":
			s := out.String()
			i := strings.LastIndexByte(s, '/')
			if i < 0 {
				i = 0
			}
			out.Reset()
			out.WriteString(s[:i])
		default:
			out.WriteByte('/')
			out.WriteString(seg)
		}
	}
	if out.Len() == 0 {
		return "/"
	}
	return out.String()
}

func isNormalizedVirtualPath(path string) bool {
	if path == "/" {
		return true
	}
	if !strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	for _, seg := range strings.Split(path[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// ValidateCwd checks a sandbox working directory and trims trailing slashes.
func ValidateCwd(cwd string) (string, error) {
	if strings.ContainsRune(cwd, 0) {
		return "", fmt.Errorf("cwd must not contain NUL bytes: %s", RustDebugString(cwd))
	}
	if !strings.HasPrefix(cwd, "/") {
		return "", fmt.Errorf("cwd must be an absolute POSIX path: %s", RustDebugString(cwd))
	}
	trimmed := strings.TrimRight(cwd, "/")
	if trimmed == "" {
		return "/", nil
	}
	return trimmed, nil
}
