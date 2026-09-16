package osaccess

import (
	"fmt"
	"unicode/utf8"

	pyrt "github.com/asalimonov/montygo/runtime"
)

// File is a file registered with OSAccess. ReadContent returns a string or []byte.
type File interface {
	Path() pyrt.Path
	SetPath(pyrt.Path)
	Name() string
	Permissions() int64
	Deleted() bool
	ReadContent() (any, error)
	WriteContent(content any) error
	Delete()
}

// MemoryFile is a file whose content lives in memory.
type MemoryFile struct {
	path        pyrt.Path
	name        string
	content     any
	permissions int64
	deleted     bool
}

// NewMemoryFile creates an in-memory file; permissions default to 0o644.
func NewMemoryFile(path string, content any, permissions ...int64) *MemoryFile {
	p := parsePath(path)
	return &MemoryFile{path: pyrt.Path(p.String()), name: p.name(), content: cloneContent(content), permissions: perms(permissions)}
}

func (f *MemoryFile) Path() pyrt.Path           { return f.path }
func (f *MemoryFile) SetPath(p pyrt.Path)       { f.path = p }
func (f *MemoryFile) Name() string              { return f.name }
func (f *MemoryFile) Permissions() int64        { return f.permissions }
func (f *MemoryFile) Deleted() bool             { return f.deleted }
func (f *MemoryFile) ReadContent() (any, error) { return cloneContent(f.content), nil }
func (f *MemoryFile) Delete()                   { f.deleted = true }

func (f *MemoryFile) WriteContent(content any) error {
	f.content = cloneContent(content)
	return nil
}

func (f *MemoryFile) String() string {
	content := "b'...'"
	if _, ok := f.content.(string); ok {
		content = "'...'"
	}
	return fmt.Sprintf("MemoryFile(path=%s, content=%s, permissions=%d)", f.path, content, f.permissions)
}

// CallbackFile delegates reads and writes to host callbacks, which run with full host access.
type CallbackFile struct {
	path        pyrt.Path
	name        string
	read        func(pyrt.Path) (any, error)
	write       func(pyrt.Path, any) error
	permissions int64
	deleted     bool
}

// NewCallbackFile creates a callback-backed file; permissions default to 0o644.
func NewCallbackFile(path string, read func(pyrt.Path) (any, error), write func(pyrt.Path, any) error, permissions ...int64) *CallbackFile {
	p := parsePath(path)
	return &CallbackFile{path: pyrt.Path(p.String()), name: p.name(), read: read, write: write, permissions: perms(permissions)}
}

func (f *CallbackFile) Path() pyrt.Path                { return f.path }
func (f *CallbackFile) SetPath(p pyrt.Path)            { f.path = p }
func (f *CallbackFile) Name() string                   { return f.name }
func (f *CallbackFile) Permissions() int64             { return f.permissions }
func (f *CallbackFile) Deleted() bool                  { return f.deleted }
func (f *CallbackFile) ReadContent() (any, error)      { return f.read(f.path) }
func (f *CallbackFile) WriteContent(content any) error { return f.write(f.path, content) }
func (f *CallbackFile) Delete()                        { f.deleted = true }

func (f *CallbackFile) String() string {
	return fmt.Sprintf("CallbackFile(path=%s, read=%p, write=%p, permissions=%d)", f.path, f.read, f.write, f.permissions)
}

func perms(permissions []int64) int64 {
	if len(permissions) > 0 {
		return permissions[0]
	}
	return 0o644
}

func cloneContent(c any) any {
	if b, ok := c.([]byte); ok {
		return append([]byte{}, b...)
	}
	return c
}

func contentText(c any) (string, error) {
	switch x := c.(type) {
	case string:
		return x, nil
	case []byte:
		return decodeUTF8(x)
	}
	return "", contentTypeError(c)
}

func contentBytes(c any) ([]byte, error) {
	switch x := c.(type) {
	case []byte:
		return x, nil
	case string:
		return []byte(x), nil
	}
	return nil, contentTypeError(c)
}

func contentSize(c any) (int64, error) {
	switch x := c.(type) {
	case []byte:
		return int64(len(x)), nil
	case string:
		return int64(len(x)), nil
	}
	return 0, contentTypeError(c)
}

func contentTypeError(c any) error {
	return pyrt.Raise("TypeError", fmt.Sprintf("file content must be str or bytes, got %T", c))
}

func decodeUTF8(b []byte) (string, error) {
	if utf8.Valid(b) {
		return string(b), nil
	}
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			c := b[i]
			reason := "invalid start byte"
			if c >= 0xc2 && c <= 0xf4 {
				reason = "invalid continuation byte"
				if !utf8.FullRune(b[i:]) {
					reason = "unexpected end of data"
				}
			}
			return "", pyrt.Raise("UnicodeDecodeError", fmt.Sprintf("'utf-8' codec can't decode byte 0x%02x in position %d: %s", c, i, reason))
		}
		i += size
	}
	return string(b), nil
}
