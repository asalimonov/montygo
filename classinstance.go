package montygo

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/asalimonov/montygo/internal/value"
)

// AttrPolicy says which names a wrapper exposes. The zero value exposes none.
type AttrPolicy struct {
	set   bool
	all   bool
	names []string
}

// All exposes every public name.
func All() AttrPolicy { return AttrPolicy{set: true, all: true} }

// Expose exposes exactly the methods of the interface T under their sandbox
// names, so widening the surface means editing the interface. T MUST be an
// interface type.
func Expose[T any]() AttrPolicy {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Interface {
		panic(fmt.Sprintf("montygo.Expose: %s is not an interface type", t))
	}
	names := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		names = append(names, SandboxName(t.Method(i).Name))
	}
	return Names(names...)
}

// Names exposes exactly the given sandbox names.
func Names(names ...string) AttrPolicy { return AttrPolicy{set: true, names: names} }

// Allows reports whether name is exposed.
func (p AttrPolicy) Allows(name string) bool {
	if !p.set {
		return false
	}
	if p.all {
		return name != "" && name[0] != '_'
	}
	for _, n := range p.names {
		if n == name {
			return true
		}
	}
	return false
}

// IsAll reports whether the policy is All.
func (p AttrPolicy) IsAll() bool { return p.set && p.all }

// AttrProvider serves attributes of a host object instead of reflection.
type AttrProvider interface {
	GetAttr(name string) (any, error)
}

// AttrLister enumerates the attributes an All policy sends eagerly.
type AttrLister interface {
	AttrNames() []string
}

// MethodProvider serves method calls of a host object instead of reflection.
type MethodProvider interface {
	CallMethod(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error)
}

// ErrAttrNotExposed is returned by providers for an absent attribute or method.
var ErrAttrNotExposed = errors.New("attribute not exposed")

type attrError struct{ msg string }

func (e *attrError) Error() string        { return e.msg }
func (e *attrError) Is(target error) bool { return target == ErrAttrNotExposed }

// ClassInstanceOptions configure a ClassInstance.
type ClassInstanceOptions struct {
	EagerAttrs     AttrPolicy
	LazyAttrs      AttrPolicy
	AllowedMethods AttrPolicy
	// Name overrides the class name the sandbox sees (set it on ClassType when passing one).
	Name string
	// ConvertValue transforms each value crossing into the sandbox.
	ConvertValue func(name string, value any) (any, error)
	// ID pins the instance identity (a canonical uuid).
	ID        string
	ClassType *ClassType
}

// ClassInstance exposes a host object to the sandbox under a policy. When the
// sandbox returns it, the host receives the original object.
type ClassInstance struct {
	instance  any
	opts      ClassInstanceOptions
	id        string
	classType *ClassType
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func normalizeID(kind, id string) (string, error) {
	if !uuidPattern.MatchString(id) {
		return "", &ValueError{Message: fmt.Sprintf("%s id must be a canonical uuid string, got %q", kind, id)}
	}
	return strings.ToLower(id), nil
}

func classOf(instance any) reflect.Type {
	t := reflect.TypeOf(instance)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// NewClassInstance wraps a host object.
func NewClassInstance(instance any, opts ClassInstanceOptions) (*ClassInstance, error) {
	if instance == nil {
		return nil, &ValueError{Message: "ClassInstance expects an object instance"}
	}
	c := &ClassInstance{instance: instance, opts: opts}
	if opts.ID != "" {
		id, err := normalizeID("ClassInstance", opts.ID)
		if err != nil {
			return nil, err
		}
		c.id = id
	} else {
		c.id = newUUID()
	}
	goType := classOf(instance)
	if opts.ClassType != nil {
		if opts.ClassType.goType != goType {
			return nil, &ValueError{Message: "classType does not match the instance's class"}
		}
		if opts.Name != "" {
			return nil, &ValueError{Message: "pass name on the ClassType wrapper, not alongside classType"}
		}
		c.classType = opts.ClassType
	} else {
		ct, err := newClassType(goType, ClassTypeOptions{Name: opts.Name})
		if err != nil {
			return nil, err
		}
		c.classType = ct
	}
	return c, nil
}

// MustClassInstance is NewClassInstance that panics on error.
func MustClassInstance(instance any, opts ClassInstanceOptions) *ClassInstance {
	c, err := NewClassInstance(instance, opts)
	if err != nil {
		panic(err)
	}
	return c
}

// ID returns the instance identity.
func (c *ClassInstance) ID() string { return c.id }

// Instance returns the wrapped host object.
func (c *ClassInstance) Instance() any { return c.instance }

// ClassType returns the wrapper of the instance's class.
func (c *ClassInstance) ClassType() *ClassType { return c.classType }

// Name returns the class name the sandbox sees.
func (c *ClassInstance) Name() string { return c.classType.Name() }

func (c *ClassInstance) wrapperID() string   { return c.id }
func (c *ClassInstance) wrapperName() string { return c.Name() }
func (c *ClassInstance) identity() any       { return c.instance }

func (c *ClassInstance) attrErr(name string) error {
	return &attrError{msg: fmt.Sprintf("'%s' object has no attribute '%s'", c.Name(), name)}
}

func (c *ClassInstance) convert(name string, v any) (any, error) {
	if c.opts.ConvertValue != nil {
		return c.opts.ConvertValue(name, v)
	}
	return v, nil
}

func (c *ClassInstance) eagerAttrs() ([]value.Pair, error) {
	p := c.opts.EagerAttrs
	if !p.set {
		return nil, nil
	}
	var names []string
	if p.all {
		if lister, ok := c.instance.(AttrLister); ok {
			for _, n := range lister.AttrNames() {
				if n != "" && n[0] != '_' {
					names = append(names, n)
				}
			}
		} else {
			for _, n := range memberIndex(reflect.TypeOf(c.instance)).fieldNames {
				if n[0] != '_' {
					names = append(names, n)
				}
			}
		}
	} else {
		names = p.names
	}
	out := make([]value.Pair, 0, len(names))
	for _, name := range names {
		v, found, err := c.readAttr(name)
		if err != nil && !errors.Is(err, ErrAttrNotExposed) {
			return nil, err
		}
		if !found {
			v = nil
		}
		cv, err := c.convert(name, v)
		if err != nil {
			return nil, err
		}
		out = append(out, value.Pair{Key: name, Value: cv})
	}
	return out, nil
}

func (c *ClassInstance) readAttr(name string) (any, bool, error) {
	if provider, ok := c.instance.(AttrProvider); ok {
		v, err := provider.GetAttr(name)
		if err != nil {
			return nil, false, err
		}
		return v, true, nil
	}
	fv, ok := fieldByName(reflect.ValueOf(c.instance), name)
	if !ok {
		return nil, false, nil
	}
	return fv.Interface(), true, nil
}

func (c *ClassInstance) lazyAttr(name string) (any, error) {
	if !c.opts.LazyAttrs.Allows(name) {
		return nil, c.attrErr(name)
	}
	v, found, err := c.readAttr(name)
	if err != nil {
		if errors.Is(err, ErrAttrNotExposed) {
			return nil, c.attrErr(name)
		}
		return nil, err
	}
	if !found {
		return nil, c.attrErr(name)
	}
	return c.convert(name, v)
}

func (c *ClassInstance) callMethod(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error) {
	if name == "__call__" || !c.opts.AllowedMethods.Allows(name) {
		return nil, c.attrErr(name)
	}
	var result any
	var err error
	if provider, ok := c.instance.(MethodProvider); ok {
		result, err = provider.CallMethod(ctx, name, args, kwargs)
		if errors.Is(err, ErrAttrNotExposed) {
			return nil, c.attrErr(name)
		}
	} else {
		fn, ok := methodByName(reflect.ValueOf(c.instance), name, !c.opts.AllowedMethods.all)
		if !ok {
			return nil, c.attrErr(name)
		}
		result, err = callReflect(ctx, fn, args, kwargs)
	}
	if err != nil {
		return nil, err
	}
	if fut, ok := result.(*Future); ok {
		return fut.then(func(v any) (any, error) { return c.convert(name, v) }), nil
	}
	return c.convert(name, result)
}

func callReflect(ctx context.Context, fn reflect.Value, args []any, kwargs Kwargs) (any, error) {
	f, err := Func(fn.Interface())
	if err != nil {
		return nil, err
	}
	return f.Call(ctx, args, kwargs)
}

// ClassTypeOptions configure a ClassType.
type ClassTypeOptions struct {
	// EagerAttrs, LazyAttrs and AllowedMethods apply to Statics.
	EagerAttrs     AttrPolicy
	LazyAttrs      AttrPolicy
	AllowedMethods AttrPolicy
	// Statics are class constants and static methods by sandbox name.
	Statics      map[string]any
	Name         string
	ConvertValue func(name string, value any) (any, error)
	ID           string
	// Init lets sandbox code instantiate the class.
	Init bool
	// Constructor builds instances; nil fills exported fields from the arguments.
	Constructor            any
	InstanceEagerAttrs     AttrPolicy
	InstanceLazyAttrs      AttrPolicy
	InstanceAllowedMethods AttrPolicy
	// InstanceWrapper customizes how constructed instances are exposed.
	InstanceWrapper func(classType *ClassType, instance any) (*ClassInstance, error)
}

// ClassType exposes a host class to the sandbox.
type ClassType struct {
	goType reflect.Type
	opts   ClassTypeOptions
	id     string
}

var classIDs sync.Map

// NewClassType wraps the class T.
func NewClassType[T any](opts ClassTypeOptions) (*ClassType, error) {
	return newClassType(classOf(reflect.New(reflect.TypeFor[T]()).Elem().Interface()), opts)
}

// NewClassTypeOf wraps the class t.
func NewClassTypeOf(t reflect.Type, opts ClassTypeOptions) (*ClassType, error) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return newClassType(t, opts)
}

// MustClassType is NewClassType that panics on error.
func MustClassType[T any](opts ClassTypeOptions) *ClassType {
	c, err := NewClassType[T](opts)
	if err != nil {
		panic(err)
	}
	return c
}

func newClassType(t reflect.Type, opts ClassTypeOptions) (*ClassType, error) {
	if t == nil {
		return nil, &ValueError{Message: "ClassType expects a class (constructor function)"}
	}
	c := &ClassType{goType: t, opts: opts}
	if opts.ID != "" {
		id, err := normalizeID("ClassType", opts.ID)
		if err != nil {
			return nil, err
		}
		c.id = id
	} else {
		id, _ := classIDs.LoadOrStore(t, newUUID())
		c.id = id.(string)
	}
	return c, nil
}

// ID returns the class identity.
func (c *ClassType) ID() string { return c.id }

// GoType returns the wrapped Go type.
func (c *ClassType) GoType() reflect.Type { return c.goType }

// Name returns the class name the sandbox sees.
func (c *ClassType) Name() string {
	if c.opts.Name != "" {
		return c.opts.Name
	}
	if n := c.goType.Name(); n != "" {
		return n
	}
	return "object"
}

func (c *ClassType) wrapperID() string   { return c.id }
func (c *ClassType) wrapperName() string { return c.Name() }
func (c *ClassType) identity() any       { return c.goType }

func (c *ClassType) attrErr(name string) error {
	return &attrError{msg: fmt.Sprintf("type object '%s' has no attribute '%s'", c.Name(), name)}
}

func (c *ClassType) convert(name string, v any) (any, error) {
	if c.opts.ConvertValue != nil {
		return c.opts.ConvertValue(name, v)
	}
	return v, nil
}

func isCallable(v any) bool {
	if _, ok := v.(Function); ok {
		return true
	}
	return v != nil && reflect.TypeOf(v).Kind() == reflect.Func
}

func (c *ClassType) eagerAttrs() ([]value.Pair, error) {
	p := c.opts.EagerAttrs
	if !p.set {
		return nil, nil
	}
	var names []string
	if p.all {
		for name, v := range c.opts.Statics {
			if name != "" && name[0] != '_' && !isCallable(v) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
	} else {
		names = p.names
	}
	out := make([]value.Pair, 0, len(names))
	for _, name := range names {
		cv, err := c.convert(name, c.opts.Statics[name])
		if err != nil {
			return nil, err
		}
		out = append(out, value.Pair{Key: name, Value: cv})
	}
	return out, nil
}

func (c *ClassType) lazyAttr(name string) (any, error) {
	v, ok := c.opts.Statics[name]
	if !c.opts.LazyAttrs.Allows(name) || !ok {
		return nil, c.attrErr(name)
	}
	return c.convert(name, v)
}

func (c *ClassType) callMethod(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error) {
	if name == "__call__" {
		return c.Construct(ctx, args, kwargs)
	}
	if !c.opts.AllowedMethods.Allows(name) {
		return nil, c.attrErr(name)
	}
	entry, ok := c.opts.Statics[name]
	if !ok || (c.opts.AllowedMethods.all && !isCallable(entry)) {
		return nil, c.attrErr(name)
	}
	fn, err := Func(entry)
	if err != nil {
		return nil, typeErr("'%s' object is not callable", hostTypeName(entry))
	}
	result, err := fn.Call(ctx, args, kwargs)
	if err != nil {
		return nil, err
	}
	if fut, ok := result.(*Future); ok {
		return fut.then(func(v any) (any, error) { return c.convert(name, v) }), nil
	}
	return c.convert(name, result)
}

// Construct builds an instance for the sandbox, honouring the Init policy.
func (c *ClassType) Construct(ctx context.Context, args []any, kwargs Kwargs) (*ClassInstance, error) {
	if !c.opts.Init {
		return nil, typeErr("cannot instantiate host class '%s'", c.Name())
	}
	var instance any
	if c.opts.Constructor != nil {
		fn, err := Func(c.opts.Constructor)
		if err != nil {
			return nil, err
		}
		instance, err = fn.Call(ctx, args, kwargs)
		if err != nil {
			return nil, err
		}
	} else {
		built, err := c.fillFields(args, kwargs)
		if err != nil {
			return nil, err
		}
		instance = built
	}
	if c.opts.InstanceWrapper != nil {
		return c.opts.InstanceWrapper(c, instance)
	}
	return c.instanceWrapper(instance)
}

func (c *ClassType) instanceWrapper(instance any) (*ClassInstance, error) {
	opts := ClassInstanceOptions{
		EagerAttrs:     c.opts.InstanceEagerAttrs,
		LazyAttrs:      c.opts.InstanceLazyAttrs,
		AllowedMethods: c.opts.InstanceAllowedMethods,
		ConvertValue:   c.opts.ConvertValue,
	}
	if classOf(instance) == c.goType {
		opts.ClassType = c
	}
	return NewClassInstance(instance, opts)
}

func (c *ClassType) fillFields(args []any, kwargs Kwargs) (any, error) {
	if c.goType.Kind() != reflect.Struct {
		return nil, typeErr("cannot instantiate host class '%s' without a Constructor", c.Name())
	}
	ptr := reflect.New(c.goType)
	idx := memberIndex(ptr.Type())
	if len(args) > len(idx.fieldNames) {
		return nil, typeErr("%s() takes %d positional arguments but %d were given", c.Name(), len(idx.fieldNames), len(args))
	}
	for i, arg := range args {
		if err := setField(ptr.Elem(), idx.fields[idx.fieldNames[i]], arg); err != nil {
			return nil, typeErr("%s() argument '%s': %v", c.Name(), idx.fieldNames[i], err)
		}
	}
	for name, arg := range kwargs {
		fi, ok := idx.fields[name]
		if !ok {
			return nil, typeErr("%s() got an unexpected keyword argument '%s'", c.Name(), name)
		}
		if err := setField(ptr.Elem(), fi, arg); err != nil {
			return nil, typeErr("%s() argument '%s': %v", c.Name(), name, err)
		}
	}
	return ptr.Interface(), nil
}

func setField(structValue reflect.Value, index []int, arg any) error {
	f := structValue.FieldByIndex(index)
	v, err := assign(f.Type(), arg)
	if err != nil {
		return err
	}
	f.Set(v)
	return nil
}

// ClassProxy is a read-only stand-in for an instance the host has no original for.
type ClassProxy struct {
	Name        string
	ID          string
	IsDataclass bool
	Attributes  *Dict
	typ         value.Type
}

func (p *ClassProxy) String() string {
	return fmt.Sprintf("MontyClassProxy(name=%s, id=%s, attributes=%s)", value.StringRepr(p.Name), value.StringRepr(p.ID), value.Repr(p.Attributes))
}

type wrapper interface {
	wrapperID() string
	wrapperName() string
	identity() any
	eagerAttrs() ([]value.Pair, error)
	lazyAttr(name string) (any, error)
	callMethod(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error)
}

type instanceStore struct {
	mu    sync.Mutex
	m     map[string]wrapper
	limit uint64
	peak  int
}

func newInstanceStore(limit uint64) *instanceStore {
	return &instanceStore{m: map[string]wrapper{}, limit: limit}
}

func (s *instanceStore) stats() (count, peak int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m), s.peak
}

func sameObject(a, b any) bool {
	if ta, ok := a.(reflect.Type); ok {
		tb, ok := b.(reflect.Type)
		return ok && ta == tb
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() {
		return false
	}
	switch va.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return va.Pointer() == vb.Pointer()
	}
	if va.Type().Comparable() {
		return a == b
	}
	return reflect.DeepEqual(a, b)
}

func (s *instanceStore) put(w wrapper, onlyIfAbsent bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.m[w.wrapperID()]; ok {
		if !sameObject(existing.identity(), w.identity()) {
			return &ConversionError{Message: fmt.Sprintf("wrapper id '%s' already identifies a different object in this session", w.wrapperID())}
		}
		if onlyIfAbsent {
			return nil
		}
		s.m[w.wrapperID()] = w
		return nil
	}
	if s.limit != Unlimited && uint64(len(s.m)) >= s.limit {
		return &ResourceError{Resource: "host object", Limit: s.limit}
	}
	s.m[w.wrapperID()] = w
	if len(s.m) > s.peak {
		s.peak = len(s.m)
	}
	return nil
}

func (s *instanceStore) get(id string) (wrapper, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.m[id]
	return w, ok
}

type typeMembers struct {
	fieldNames []string
	fields     map[string][]int
	methods    map[string]string
}

var memberCache sync.Map

func memberIndex(t reflect.Type) *typeMembers {
	if cached, ok := memberCache.Load(t); ok {
		return cached.(*typeMembers)
	}
	m := &typeMembers{fields: map[string][]int{}, methods: map[string]string{}}
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() == reflect.Struct {
		for _, f := range reflect.VisibleFields(st) {
			if !f.IsExported() || f.Anonymous {
				continue
			}
			name := f.Tag.Get("monty")
			if name == "-" {
				continue
			}
			if name == "" {
				name = SandboxName(f.Name)
			}
			if _, dup := m.fields[name]; dup {
				continue
			}
			m.fields[name] = f.Index
			m.fieldNames = append(m.fieldNames, name)
		}
	}
	for _, mt := range []reflect.Type{t, reflect.PointerTo(st)} {
		for i := 0; i < mt.NumMethod(); i++ {
			meth := mt.Method(i)
			if _, dup := m.methods[SandboxName(meth.Name)]; !dup {
				m.methods[SandboxName(meth.Name)] = meth.Name
			}
		}
	}
	memberCache.Store(t, m)
	return m
}

// SandboxName converts a Go identifier to the snake_case name the sandbox uses.
func SandboxName(goName string) string {
	runes := []rune(goName)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fieldByName(rv reflect.Value, name string) (reflect.Value, bool) {
	idx := memberIndex(rv.Type())
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	if index, ok := idx.fields[name]; ok {
		f, err := rv.FieldByIndexErr(index)
		return f, err == nil
	}
	return reflect.Value{}, false
}

func methodByName(rv reflect.Value, name string, allowFuncFields bool) (reflect.Value, bool) {
	idx := memberIndex(rv.Type())
	if goName, ok := idx.methods[name]; ok {
		if m := rv.MethodByName(goName); m.IsValid() {
			return m, true
		}
		if rv.Kind() != reflect.Pointer {
			ptr := reflect.New(rv.Type())
			ptr.Elem().Set(rv)
			if m := ptr.MethodByName(goName); m.IsValid() {
				return m, true
			}
		}
	}
	if allowFuncFields {
		if f, ok := fieldByName(rv, name); ok && f.Kind() == reflect.Func && !f.IsNil() {
			return f, true
		}
	}
	return reflect.Value{}, false
}
