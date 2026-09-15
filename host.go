package montygo

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Host is a validated set of functions and objects a session exposes to the
// sandbox. Registration validates signatures, so a bad host function fails
// when it is registered rather than when the sandbox calls it.
type Host struct {
	mu      sync.Mutex
	funcs   map[string]hostFunc
	objects map[string]*ClassInstance
}

type hostFunc struct {
	fn             Function
	sig            reflect.Type
	parameterNames []string
}

// HostFuncOptions supplies names for reflected parameters in generated stubs.
// Names do not enable keyword binding for positional Go parameters.
type HostFuncOptions struct {
	// ParameterNames excludes context.Context and Kwargs, but includes a variadic
	// parameter. Nil keeps generated names; a non-nil slice must match exactly.
	ParameterNames []string
}

// NewHost returns an empty host registry.
func NewHost() *Host {
	return &Host{funcs: map[string]hostFunc{}, objects: map[string]*ClassInstance{}}
}

func validHostName(name string) error {
	if name == "" {
		return &ValueError{Message: "host name must not be empty"}
	}
	switch name {
	case "False", "None", "True", "and", "as", "assert", "async", "await",
		"break", "class", "continue", "def", "del", "elif", "else", "except",
		"finally", "for", "from", "global", "if", "import", "in", "is",
		"lambda", "nonlocal", "not", "or", "pass", "raise", "return", "try",
		"while", "with", "yield":
		return &ValueError{Message: fmt.Sprintf("host name %q is a Python keyword", name)}
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return &ValueError{Message: fmt.Sprintf("host name %q is not a Python identifier", name)}
		}
	}
	return nil
}

// Func registers fn under name; the signature is validated now.
func (h *Host) Func(name string, fn any, opts ...HostFuncOptions) error {
	if len(opts) > 1 {
		return &ValueError{Message: "Host.Func accepts at most one options value"}
	}
	if err := validHostName(name); err != nil {
		return err
	}
	f, err := Func(fn)
	if err != nil {
		return err
	}
	var sig reflect.Type
	if _, direct := fn.(Function); !direct {
		if t := reflect.TypeOf(fn); t != nil && t.Kind() == reflect.Func {
			sig = t
		}
	}
	var names []string
	if len(opts) == 1 {
		names = opts[0].ParameterNames
	}
	if err := validateParameterNames(sig, names); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.taken(name) {
		return &ValueError{Message: fmt.Sprintf("host name %q is already registered", name)}
	}
	h.funcs[name] = hostFunc{fn: f, sig: sig, parameterNames: append([]string(nil), names...)}
	return nil
}

// Object registers v as a host object under name. Set opts.ID to a fixed uuid
// when sessions must be restored from dumps on other checkouts (see Restorable).
func (h *Host) Object(name string, v any, opts ClassInstanceOptions) error {
	if err := validHostName(name); err != nil {
		return err
	}
	ci, err := NewClassInstance(v, opts)
	if err != nil {
		return err
	}
	if err := validHostName(ci.Name()); err != nil {
		return fmt.Errorf("host class: %w", err)
	}
	for _, m := range ci.classType.stubMethods(ci.opts.AllowedMethods) {
		if err := validHostName(m.name); err != nil {
			return fmt.Errorf("host method: %w", err)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.taken(name) {
		return &ValueError{Message: fmt.Sprintf("host name %q is already registered", name)}
	}
	h.objects[name] = ci
	return nil
}

func (h *Host) taken(name string) bool {
	_, f := h.funcs[name]
	_, o := h.objects[name]
	return f || o
}

// Names lists every registered name, sorted.
func (h *Host) Names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.funcs)+len(h.objects))
	for name := range h.funcs {
		names = append(names, name)
	}
	for name := range h.objects {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ErrHostObjectNotRestorable reports an object registered without a pinned ID.
var ErrHostObjectNotRestorable = errors.New("host object has no pinned ID; set ClassInstanceOptions.ID to restore sessions from dumps")

// Restorable reports the first object registered without a pinned ID, or nil.
func (h *Host) Restorable() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range h.sortedObjects() {
		if h.objects[name].opts.ID == "" {
			return fmt.Errorf("%s: %w", name, ErrHostObjectNotRestorable)
		}
	}
	return nil
}

func (h *Host) sortedObjects() []string {
	names := make([]string, 0, len(h.objects))
	for name := range h.objects {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *Host) sortedFuncs() []string {
	names := make([]string, 0, len(h.funcs))
	for name := range h.funcs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *Host) lookupEntry(name string) (any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f, ok := h.funcs[name]; ok {
		return f.fn, true
	}
	o, ok := h.objects[name]
	return o, ok
}

// register puts every object into the session's store under its ID.
func (h *Host) register(store *instanceStore) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range h.sortedObjects() {
		if err := store.put(h.objects[name], true); err != nil {
			return fmt.Errorf("register host object %s: %w", name, err)
		}
	}
	return nil
}

// Stubs renders Python stub declarations for TypeCheckStubs.
func (h *Host) Stubs() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	b.WriteString("from typing import Any, Awaitable\n")
	for _, name := range h.sortedFuncs() {
		b.WriteString("\n")
		f := h.funcs[name]
		writeFuncStub(&b, name, f.sig, "", f.parameterNames)
	}
	for _, name := range h.sortedObjects() {
		ci := h.objects[name]
		fmt.Fprintf(&b, "\nclass %s:\n", ci.Name())
		wrote := false
		for _, m := range ci.classType.stubMethods(ci.opts.AllowedMethods) {
			writeFuncStub(&b, m.name, m.sig, "    ", nil)
			wrote = true
		}
		if !wrote {
			b.WriteString("    ...\n")
		}
		fmt.Fprintf(&b, "\n%s: %s\n", name, ci.Name())
	}
	return b.String()
}

type stubMethod struct {
	name string
	sig  reflect.Type
}

func (c *ClassType) stubMethods(policy AttrPolicy) []stubMethod {
	members := memberIndex(c.goType)
	names := make([]string, 0, len(members.methods))
	for name := range members.methods {
		if policy.Allows(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]stubMethod, 0, len(names))
	for _, name := range names {
		goName := members.methods[name]
		var sig reflect.Type
		if m, ok := reflect.PointerTo(c.goType).MethodByName(goName); ok {
			sig = m.Type
		} else if m, ok := c.goType.MethodByName(goName); ok {
			sig = m.Type
		}
		out = append(out, stubMethod{name: name, sig: sig})
	}
	return out
}

// writeFuncStub renders `def name(params) -> result: ...`; a method signature
// includes its receiver, which becomes `self`.
func writeFuncStub(b *strings.Builder, name string, sig reflect.Type, indent string, names []string) {
	if sig == nil {
		fmt.Fprintf(b, "%sdef %s(*args: Any, **kwargs: Any) -> Any: ...\n", indent, name)
		return
	}
	var params []string
	first, last := 0, sig.NumIn()
	if indent != "" {
		params = append(params, "self")
		first = 1
	}
	if first < last && sig.In(first) == contextType {
		first++
	}
	kwargs := last > first && sig.In(last-1) == kwargsType && !sig.IsVariadic()
	if kwargs {
		last--
	}
	paramName := func(i int, variadic bool) string {
		if names != nil {
			return names[i-first]
		}
		if variadic {
			return "args"
		}
		return fmt.Sprintf("arg%d", i-first)
	}
	fixed := last
	if sig.IsVariadic() {
		fixed--
	}
	for i := first; i < fixed; i++ {
		t := sig.In(i)
		params = append(params, fmt.Sprintf("%s: %s", paramName(i, false), pyType(t)))
	}
	if len(params) > 0 {
		params = append(params, "/")
	}
	if sig.IsVariadic() {
		params = append(params, fmt.Sprintf("*%s: %s", paramName(last-1, true), pyType(sig.In(last-1).Elem())))
	}
	if kwargs {
		params = append(params, "**kwargs: Any")
	}
	fmt.Fprintf(b, "%sdef %s(%s) -> %s: ...\n", indent, name, strings.Join(params, ", "), pyResult(sig))
}

func validateParameterNames(sig reflect.Type, names []string) error {
	if names == nil {
		return nil
	}
	if sig == nil {
		if len(names) == 0 {
			return nil
		}
		return &ValueError{Message: "parameter names require a reflected Go function"}
	}
	first, last := 0, sig.NumIn()
	if first < last && sig.In(first) == contextType {
		first++
	}
	kwargs := last > first && sig.In(last-1) == kwargsType && !sig.IsVariadic()
	if kwargs {
		last--
	}
	if len(names) != last-first {
		return &ValueError{Message: fmt.Sprintf("expected %d parameter names, got %d", last-first, len(names))}
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if err := validHostName(name); err != nil {
			return fmt.Errorf("parameter name: %w", err)
		}
		if seen[name] || (kwargs && name == "kwargs") {
			return &ValueError{Message: fmt.Sprintf("duplicate parameter name %q", name)}
		}
		seen[name] = true
	}
	return nil
}

var (
	futureType = reflect.TypeFor[*Future]()
	bytesType  = reflect.TypeFor[[]byte]()
)

func pyType(t reflect.Type) string {
	switch t {
	case futureType:
		return "Awaitable[Any]"
	case bytesType:
		return "bytes"
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.String:
		return "str"
	case reflect.Bool:
		return "bool"
	case reflect.Slice, reflect.Array:
		return "list[" + pyType(t.Elem()) + "]"
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			return "dict[str, " + pyType(t.Elem()) + "]"
		}
	case reflect.Pointer:
		return pyType(t.Elem())
	}
	return "Any"
}

func pyResult(sig reflect.Type) string {
	switch sig.NumOut() {
	case 0:
		return "None"
	case 1:
		if sig.Out(0) == errorType {
			return "None"
		}
		return pyType(sig.Out(0))
	}
	return pyType(sig.Out(0))
}
