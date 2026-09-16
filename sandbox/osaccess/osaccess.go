package osaccess

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

// OSAccess is an in-memory virtual filesystem and environment for sandboxed code.
// Files backed by MemoryFile never touch the host filesystem.
type OSAccess struct {
	Files   []File
	Environ map[string]string

	mu   sync.Mutex
	tree *dir
}

// Option configures New.
type Option func(*options)

type options struct {
	rootDir string
}

// WithRootDir rebases relative file paths onto dir, which must be absolute. The default is "/".
func WithRootDir(dir string) Option {
	return func(o *options) { o.rootDir = dir }
}

// New builds the tree from files. Relative file paths are rebased onto the root directory.
func New(files []File, environ map[string]string, opts ...Option) (*OSAccess, error) {
	cfg := options{rootDir: "/"}
	for _, opt := range opts {
		opt(&cfg)
	}
	o := &OSAccess{Files: append([]File{}, files...), Environ: environ, tree: newDir()}
	if len(environ) == 0 {
		o.Environ = map[string]string{}
	}
	o.tree.set("/", newDir())
	root := parsePath(cfg.rootDir)
	if !root.isAbs() {
		return nil, monterr.Raise("AssertionError", "Root directory must be absolute, got "+root.String())
	}
	for _, f := range o.Files {
		p := parsePath(string(f.Path()))
		if !p.isAbs() {
			p = root.join(p)
			f.SetPath(sandbox.Path(p.String()))
		}
		parts := p.allParts()
		sub := o.tree
		for _, part := range parts[:len(parts)-1] {
			entry := sub.setdefault(part)
			d, ok := entry.(*dir)
			if !ok {
				return nil, monterr.Raise("ValueError", fmt.Sprintf("Cannot put file %s within sub-directory of file %s", describe(f), describe(entry)))
			}
			sub = d
		}
		sub.set(parts[len(parts)-1], f)
	}
	return o, nil
}

// Handler returns the host.OSHandler backed by o.
func (o *OSAccess) Handler() host.OSHandler { return Handler(o) }

func (o *OSAccess) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	files := make([]string, len(o.Files))
	for i, f := range o.Files {
		files[i] = describe(f)
	}
	return fmt.Sprintf("OSAccess(files=[%s], environ=%s)", strings.Join(files, ", "), sandbox.Repr(environDict(o.Environ)))
}

func (o *OSAccess) PathExists(path sandbox.Path) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.entry(parsePath(string(path))) != nil, nil
}

func (o *OSAccess) PathIsFile(path sandbox.Path) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.entry(parsePath(string(path))).(File)
	return ok, nil
}

func (o *OSAccess) PathIsDir(path sandbox.Path) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.entry(parsePath(string(path))).(*dir)
	return ok, nil
}

func (o *OSAccess) PathIsSymlink(sandbox.Path) (bool, error) { return false, nil }

// PathOpen validates mode before any side effect, then applies the open-time effect of r, w or a.
func (o *OSAccess) PathOpen(path sandbox.Path, mode string) (*sandbox.FileHandle, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := parsePath(string(path))
	h, err := sandbox.NewFileHandle(p.String(), mode, 0)
	if err != nil {
		return nil, monterr.Raise("ValueError", err.Error())
	}
	var empty any = ""
	if h.Binary() {
		empty = []byte{}
	}
	switch h.Mode[0] {
	case 'r':
		switch o.entry(p).(type) {
		case nil:
			return nil, errNotFound(p)
		case *dir:
			return nil, errIsDir(p)
		}
	case 'w':
		if err := o.writeFile(p, empty); err != nil {
			return nil, err
		}
	default:
		switch o.entry(p).(type) {
		case nil:
			if err := o.writeFile(p, empty); err != nil {
				return nil, err
			}
		case *dir:
			return nil, errIsDir(p)
		}
	}
	return h, nil
}

func (o *OSAccess) PathReadText(path sandbox.Path) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f, err := o.file(parsePath(string(path)))
	if err != nil {
		return "", err
	}
	content, err := f.ReadContent()
	if err != nil {
		return "", err
	}
	return contentText(content)
}

func (o *OSAccess) PathReadBytes(path sandbox.Path) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f, err := o.file(parsePath(string(path)))
	if err != nil {
		return nil, err
	}
	content, err := f.ReadContent()
	if err != nil {
		return nil, err
	}
	return contentBytes(content)
}

// PathWriteText returns the number of characters written.
func (o *OSAccess) PathWriteText(path sandbox.Path, data string) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.writeFile(parsePath(string(path)), data); err != nil {
		return 0, err
	}
	return utf8.RuneCountInString(data), nil
}

func (o *OSAccess) PathWriteBytes(path sandbox.Path, data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.writeFile(parsePath(string(path)), append([]byte{}, data...)); err != nil {
		return 0, err
	}
	return len(data), nil
}

// PathAppendText keeps text storage text; bytes storage is decoded first. It returns the character count.
func (o *OSAccess) PathAppendText(path sandbox.Path, data string) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.appendData(parsePath(string(path)), data); err != nil {
		return 0, err
	}
	return utf8.RuneCountInString(data), nil
}

func (o *OSAccess) PathAppendBytes(path sandbox.Path, data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.appendData(parsePath(string(path)), append([]byte{}, data...)); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (o *OSAccess) PathMkdir(path sandbox.Path, parents, existOK bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := parsePath(string(path))
	switch o.entry(p).(type) {
	case File:
		return errExists(p)
	case *dir:
		if existOK {
			return nil
		}
		return errExists(p)
	}
	switch parent := o.entry(p.parent()).(type) {
	case *dir:
		parent.set(p.name(), newDir())
		return nil
	case File:
		return errNotDir(p)
	}
	if !parents {
		return errNotFound(p)
	}
	sub := o.tree
	for _, part := range p.allParts() {
		d, ok := sub.setdefault(part).(*dir)
		if !ok {
			return errNotDir(p)
		}
		sub = d
	}
	return nil
}

func (o *OSAccess) PathUnlink(path sandbox.Path) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := parsePath(string(path))
	f, err := o.file(p)
	if err != nil {
		return err
	}
	f.Delete()
	parent, err := o.parentDir(p)
	if err != nil {
		return err
	}
	return parent.del(f.Name())
}

func (o *OSAccess) PathRmdir(path sandbox.Path) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := parsePath(string(path))
	d, err := o.dir(p)
	if err != nil {
		return err
	}
	if d.len() > 0 {
		return monterr.Raise("OSError", "[Errno 39] Directory not empty: "+sandbox.Repr(p.String()))
	}
	parent, err := o.parentDir(p)
	if err != nil {
		return err
	}
	return parent.del(p.name())
}

// PathIterdir returns full child paths in insertion order.
func (o *OSAccess) PathIterdir(path sandbox.Path) ([]sandbox.Path, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := parsePath(string(path))
	d, err := o.dir(p)
	if err != nil {
		return nil, err
	}
	out := make([]sandbox.Path, 0, d.len())
	for _, name := range d.names {
		out = append(out, sandbox.Path(p.join(parsePath(name)).String()))
	}
	return out, nil
}

func (o *OSAccess) PathStat(path sandbox.Path) (StatResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := parsePath(string(path))
	entry, err := o.entryExists(p)
	if err != nil {
		return StatResult{}, err
	}
	f, ok := entry.(File)
	if !ok {
		return DirStat(0, nil), nil
	}
	content, err := f.ReadContent()
	if err != nil {
		return StatResult{}, err
	}
	size, err := contentSize(content)
	if err != nil {
		return StatResult{}, err
	}
	return fileStat(size, f.Permissions(), nil), nil
}

// PathRename moves a file or directory. A moved file keeps its Path; files inside a moved directory are rebased.
func (o *OSAccess) PathRename(path, target sandbox.Path) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	src, dst := parsePath(string(path)), parsePath(string(target))
	pair := sandbox.Repr(src.String()) + " -> " + sandbox.Repr(dst.String())
	srcEntry := o.entry(src)
	if srcEntry == nil {
		return monterr.Raise("FileNotFoundError", "[Errno 2] No such file or directory: "+pair)
	}
	parent, err := o.parentDir(src)
	if err != nil {
		return err
	}
	targetParent, ok := o.entry(dst.parent()).(*dir)
	if !ok {
		return monterr.Raise("FileNotFoundError", "[Errno 2] No such file or directory: "+pair)
	}
	targetEntry := o.entry(dst)
	if f, isFile := srcEntry.(File); isFile {
		switch t := targetEntry.(type) {
		case *dir:
			return monterr.Raise("IsADirectoryError", "[Errno 21] Is a directory: "+pair)
		case File:
			t.Delete()
		}
		if err := parent.del(parsePath(string(f.Path())).name()); err != nil {
			return err
		}
		targetParent.set(dst.name(), f)
		return nil
	}
	srcDir := srcEntry.(*dir)
	switch t := targetEntry.(type) {
	case File:
		return monterr.Raise("NotADirectoryError", "[Errno 20] Not a directory: "+pair)
	case *dir:
		if t.len() > 0 {
			return monterr.Raise("OSError", "[Errno 66] Directory not empty: "+pair)
		}
	}
	if srcDir.contains(targetParent) {
		return monterr.Raise("OSError", "[Errno 22] Invalid argument: "+pair)
	}
	if err := parent.del(src.name()); err != nil {
		return err
	}
	targetParent.set(dst.name(), srcDir)
	return rebasePaths(srcDir, src, dst)
}

func (o *OSAccess) PathResolve(path sandbox.Path) (string, error) { return o.PathAbsolute(path) }

// PathAbsolute treats "/" as the working directory.
func (o *OSAccess) PathAbsolute(path sandbox.Path) (string, error) {
	p := parsePath(string(path))
	if p.isAbs() {
		return p.String(), nil
	}
	return parsePath("/").join(p).String(), nil
}

func (o *OSAccess) Getenv(key string, def any) (any, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if v, ok := o.Environ[key]; ok {
		return v, nil
	}
	return def, nil
}

// GetEnviron returns Environ itself, not a copy.
func (o *OSAccess) GetEnviron() (map[string]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Environ, nil
}

func (o *OSAccess) DateToday() (sandbox.Date, error) { return Base{}.DateToday() }

func (o *OSAccess) DatetimeNow(tz *sandbox.TimeZone) (sandbox.DateTime, error) {
	return Base{}.DatetimeNow(tz)
}

func (o *OSAccess) entry(p purePath) any {
	parts := p.allParts()
	if len(parts) == 0 {
		return nil
	}
	d := o.tree
	for _, part := range parts[:len(parts)-1] {
		next, ok := d.entries[part].(*dir)
		if !ok {
			return nil
		}
		d = next
	}
	return d.entries[parts[len(parts)-1]]
}

func (o *OSAccess) entryExists(p purePath) (any, error) {
	entry := o.entry(p)
	if entry == nil {
		return nil, errNotFound(p)
	}
	return entry, nil
}

func (o *OSAccess) file(p purePath) (File, error) {
	entry, err := o.entryExists(p)
	if err != nil {
		return nil, err
	}
	f, ok := entry.(File)
	if !ok {
		return nil, errIsDir(p)
	}
	return f, nil
}

func (o *OSAccess) dir(p purePath) (*dir, error) {
	entry, err := o.entryExists(p)
	if err != nil {
		return nil, err
	}
	d, ok := entry.(*dir)
	if !ok {
		return nil, errNotDir(p)
	}
	return d, nil
}

func (o *OSAccess) parentDir(p purePath) (*dir, error) {
	entry := o.entry(p.parent())
	d, ok := entry.(*dir)
	if !ok {
		return nil, monterr.Raise("AssertionError", "Expected parent of a file to always be a directory, got "+describe(entry))
	}
	return d, nil
}

func (o *OSAccess) writeFile(p purePath, data any) error {
	switch entry := o.entry(p).(type) {
	case File:
		return entry.WriteContent(data)
	case *dir:
		return errIsDir(p)
	}
	parent, ok := o.entry(p.parent()).(*dir)
	if !ok {
		return errNotFound(p)
	}
	f := NewMemoryFile(p.String(), data)
	parent.set(p.name(), f)
	o.Files = append(o.Files, f)
	return nil
}

func (o *OSAccess) appendData(p purePath, data any) error {
	switch entry := o.entry(p).(type) {
	case File:
		content, err := entry.ReadContent()
		if err != nil {
			return err
		}
		if text, ok := data.(string); ok {
			existing, err := contentText(content)
			if err != nil {
				return err
			}
			return entry.WriteContent(existing + text)
		}
		existing, err := contentBytes(content)
		if err != nil {
			return err
		}
		return entry.WriteContent(append(append([]byte{}, existing...), data.([]byte)...))
	case *dir:
		return errIsDir(p)
	}
	return o.writeFile(p, data)
}

func rebasePaths(d *dir, oldPrefix, newPrefix purePath) error {
	for _, name := range d.names {
		switch entry := d.entries[name].(type) {
		case File:
			current := parsePath(string(entry.Path()))
			rel, ok := current.relativeTo(oldPrefix)
			if !ok {
				return monterr.Raise("ValueError", fmt.Sprintf("%s is not in the subpath of %s", sandbox.Repr(current.String()), sandbox.Repr(oldPrefix.String())))
			}
			entry.SetPath(sandbox.Path(newPrefix.join(rel).String()))
		case *dir:
			if err := rebasePaths(entry, oldPrefix, newPrefix); err != nil {
				return err
			}
		}
	}
	return nil
}

func errNotFound(p purePath) error {
	return monterr.Raise("FileNotFoundError", "[Errno 2] No such file or directory: "+sandbox.Repr(p.String()))
}

func errIsDir(p purePath) error {
	return monterr.Raise("IsADirectoryError", "[Errno 21] Is a directory: "+sandbox.Repr(p.String()))
}

func errNotDir(p purePath) error {
	return monterr.Raise("NotADirectoryError", "[Errno 20] Not a directory: "+sandbox.Repr(p.String()))
}

func errExists(p purePath) error {
	return monterr.Raise("FileExistsError", "[Errno 17] File exists: "+sandbox.Repr(p.String()))
}

func describe(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case *dir:
		return x.String()
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

type dir struct {
	names   []string
	entries map[string]any
}

func newDir() *dir { return &dir{entries: map[string]any{}} }

func (d *dir) len() int { return len(d.names) }

func (d *dir) set(name string, v any) {
	if _, ok := d.entries[name]; !ok {
		d.names = append(d.names, name)
	}
	d.entries[name] = v
}

func (d *dir) setdefault(name string) any {
	if v, ok := d.entries[name]; ok {
		return v
	}
	sub := newDir()
	d.set(name, sub)
	return sub
}

func (d *dir) del(name string) error {
	if _, ok := d.entries[name]; !ok {
		return monterr.Raise("KeyError", sandbox.Repr(name))
	}
	delete(d.entries, name)
	for i, n := range d.names {
		if n == name {
			d.names = append(d.names[:i], d.names[i+1:]...)
			break
		}
	}
	return nil
}

func (d *dir) contains(other *dir) bool {
	if d == other {
		return true
	}
	for _, v := range d.entries {
		if sub, ok := v.(*dir); ok && sub.contains(other) {
			return true
		}
	}
	return false
}

func (d *dir) String() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, name := range d.names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(sandbox.Repr(name) + ": " + describe(d.entries[name]))
	}
	b.WriteByte('}')
	return b.String()
}

type purePath struct {
	root  string
	parts []string
}

func parsePath(s string) purePath {
	var p purePath
	if strings.HasPrefix(s, "/") {
		p.root = "/"
		if strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "///") {
			p.root = "//"
		}
	}
	for _, seg := range strings.Split(s, "/") {
		if seg != "" && seg != "." {
			p.parts = append(p.parts, seg)
		}
	}
	return p
}

func (p purePath) String() string {
	s := p.root + strings.Join(p.parts, "/")
	if s == "" {
		return "."
	}
	return s
}

func (p purePath) isAbs() bool { return p.root != "" }

func (p purePath) allParts() []string {
	if p.root == "" {
		return p.parts
	}
	return append([]string{p.root}, p.parts...)
}

func (p purePath) name() string {
	if len(p.parts) == 0 {
		return ""
	}
	return p.parts[len(p.parts)-1]
}

func (p purePath) parent() purePath {
	if len(p.parts) == 0 {
		return p
	}
	return purePath{root: p.root, parts: p.parts[:len(p.parts)-1]}
}

func (p purePath) join(other purePath) purePath {
	if other.isAbs() {
		return other
	}
	parts := append(append([]string{}, p.parts...), other.parts...)
	return purePath{root: p.root, parts: parts}
}

func (p purePath) relativeTo(prefix purePath) (purePath, bool) {
	all, pre := p.allParts(), prefix.allParts()
	if len(pre) > len(all) {
		return purePath{}, false
	}
	for i := range pre {
		if all[i] != pre[i] {
			return purePath{}, false
		}
	}
	return purePath{parts: append([]string{}, all[len(pre):]...)}, true
}
