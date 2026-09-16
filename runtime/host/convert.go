package host

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"

	"github.com/asalimonov/montygo/internal/value"
	pyrt "github.com/asalimonov/montygo/runtime"
)

const maxInputDepth = 48

func PrepareValue(v any, store *InstanceStore) (any, error) {
	return prepareInner(v, store, 0)
}

func depthError() error { return &pyrt.ConversionError{Message: "Max input depth exceeded"} }

func prepareInner(v any, store *InstanceStore, depth int) (any, error) {
	if depth > maxInputDepth {
		return nil, depthError()
	}
	walk := func(item any) (any, error) { return prepareInner(item, store, depth+1) }
	switch x := v.(type) {
	case nil, bool, string, int64, float64, value.EllipsisType, value.NotImplementedType, value.Date,
		value.TimeDelta, value.TimeZone, value.Exception, value.BuiltinFunction, value.Path, value.Function:
		return v, nil
	case value.Time:
		if x.TimezoneName != nil && x.OffsetSeconds == nil {
			return nil, &pyrt.ConversionError{Message: "MontyTime timezoneName requires offsetSeconds"}
		}
		return x, nil
	case value.DateTime:
		if x.TimezoneName != nil && x.OffsetSeconds == nil {
			return nil, &pyrt.ConversionError{Message: "MontyDateTime timezoneName requires offsetSeconds"}
		}
		return x, nil
	case []byte:
		return x, nil
	case int:
		return int64(x), nil
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case uint8:
		return int64(x), nil
	case uint16:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint:
		return uintValue(uint64(x)), nil
	case uint64:
		return uintValue(x), nil
	case float32:
		return float64(x), nil
	case *big.Int:
		if x == nil {
			return nil, nil
		}
		return value.IntValue(x), nil
	case *value.FileHandle:
		return x, nil
	case value.FileHandle:
		return &x, nil
	case *ClassType:
		if err := store.put(x, false); err != nil {
			return nil, err
		}
		return classTypeMarker(x, store, depth)
	case *ClassInstance:
		return instanceMarker(x, store, depth)
	case *ClassProxy:
		attrs := &value.Dict{}
		for _, p := range x.Attributes.Pairs() {
			pv, err := walk(p.Value)
			if err != nil {
				return nil, err
			}
			attrs.Append(p.Key, pv)
		}
		typ := x.typ
		typ.Attrs = nil
		return value.Instance{Type: typ, ID: x.ID, Attrs: attrs}, nil
	case value.Instance, *value.Instance:
		return nil, &pyrt.ConversionError{Message: "raw ClassInstance markers are not accepted — wrap the object in ClassInstance(...)"}
	case value.Type:
		if x.Origin != value.OriginBuiltin {
			return nil, &pyrt.ConversionError{Message: "raw Type markers are not accepted — pass the class through ClassType(...)"}
		}
		return x, nil
	case value.Cycle:
		return nil, &pyrt.ConversionError{Message: "Cannot convert cycle marker to Monty value"}
	case value.NamedTuple:
		out := x
		out.Values = make([]any, len(x.Values))
		for i, it := range x.Values {
			pv, err := walk(it)
			if err != nil {
				return nil, err
			}
			out.Values[i] = pv
		}
		return out, nil
	case []any:
		return prepareItems(x, walk)
	case value.Tuple:
		items, err := prepareItems(x, walk)
		return value.Tuple(items), err
	case *value.Dict:
		out := &value.Dict{}
		for _, p := range x.Pairs() {
			k, err := walk(p.Key)
			if err != nil {
				return nil, err
			}
			pv, err := walk(p.Value)
			if err != nil {
				return nil, err
			}
			out.Append(k, pv)
		}
		return out, nil
	case *value.Set:
		out := &value.Set{}
		for _, it := range x.Items() {
			pv, err := walk(it)
			if err != nil {
				return nil, err
			}
			out.Append(pv)
		}
		return out, nil
	case *value.FrozenSet:
		out := &value.FrozenSet{}
		for _, it := range x.Items() {
			pv, err := walk(it)
			if err != nil {
				return nil, err
			}
			out.Append(pv)
		}
		return out, nil
	case Function:
		return value.Function{Name: "<anonymous>"}, nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Func:
		if rv.IsNil() {
			return nil, nil
		}
		return value.Function{Name: funcName(rv)}, nil
	case reflect.Slice:
		if rv.IsNil() {
			return []any{}, nil
		}
		fallthrough
	case reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, rv.Len())
			reflect.Copy(reflect.ValueOf(b), rv)
			return b, nil
		}
		items := make([]any, rv.Len())
		for i := range items {
			pv, err := walk(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			items[i] = pv
		}
		return items, nil
	case reflect.Map:
		keys := rv.MapKeys()
		sortKeys(keys)
		out := &value.Dict{}
		for _, k := range keys {
			pk, err := walk(k.Interface())
			if err != nil {
				return nil, err
			}
			pv, err := walk(rv.MapIndex(k).Interface())
			if err != nil {
				return nil, err
			}
			out.Append(pk, pv)
		}
		return out, nil
	case reflect.Pointer:
		if rv.IsNil() {
			return nil, nil
		}
		if rv.Elem().Kind() != reflect.Struct {
			return walk(rv.Elem().Interface())
		}
		return nil, wrapHint(rv.Type().Elem())
	case reflect.Struct:
		return nil, wrapHint(rv.Type())
	case reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return uintValue(rv.Uint()), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.String:
		return rv.String(), nil
	}
	return nil, &pyrt.ConversionError{Message: fmt.Sprintf("Cannot convert Go %T to Monty value", v)}
}

// AsNamedTuple converts a struct to a named tuple of its exported fields, in
// declaration order, named by their `monty` tags or snake_case names.
func AsNamedTuple(v any) (pyrt.NamedTuple, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return pyrt.NamedTuple{}, &pyrt.ConversionError{Message: "AsNamedTuple expects a struct, got nil"}
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return pyrt.NamedTuple{}, &pyrt.ConversionError{Message: fmt.Sprintf("AsNamedTuple expects a struct, got %T", v)}
	}
	members := memberIndex(rv.Type())
	out := pyrt.NamedTuple{TypeName: rv.Type().Name(), FieldNames: append([]string(nil), members.fieldNames...)}
	if out.TypeName == "" {
		out.TypeName = "NamedTuple"
	}
	out.Values = make([]any, len(members.fieldNames))
	for i, name := range members.fieldNames {
		out.Values[i] = rv.FieldByIndex(members.fields[name]).Interface()
	}
	return out, nil
}

// NewNamedTuple builds a named tuple from field pairs; keys MUST be unique non-empty strings.
func NewNamedTuple(typeName string, pairs ...pyrt.Pair) (pyrt.NamedTuple, error) {
	if typeName == "" {
		return pyrt.NamedTuple{}, &pyrt.ValueError{Message: "NewNamedTuple: type name must not be empty"}
	}
	out := pyrt.NamedTuple{TypeName: typeName, FieldNames: make([]string, 0, len(pairs)), Values: make([]any, 0, len(pairs))}
	seen := map[string]struct{}{}
	for _, p := range pairs {
		name, ok := p.Key.(string)
		if !ok || name == "" {
			return pyrt.NamedTuple{}, &pyrt.ValueError{Message: fmt.Sprintf("NewNamedTuple: field name must be a non-empty string, got %v", p.Key)}
		}
		if _, dup := seen[name]; dup {
			return pyrt.NamedTuple{}, &pyrt.ValueError{Message: fmt.Sprintf("NewNamedTuple: duplicate field %q", name)}
		}
		seen[name] = struct{}{}
		out.FieldNames = append(out.FieldNames, name)
		out.Values = append(out.Values, p.Value)
	}
	return out, nil
}

func wrapHint(t reflect.Type) error {
	name := t.Name()
	if name == "" {
		name = "object"
	}
	return &pyrt.ConversionError{Message: fmt.Sprintf("Cannot convert %s instance to a Monty value — wrap it in ClassInstance(...)", name)}
}

func uintValue(x uint64) any {
	if x <= math.MaxInt64 {
		return int64(x)
	}
	return new(big.Int).SetUint64(x)
}

func prepareItems(items []any, walk func(any) (any, error)) ([]any, error) {
	out := make([]any, len(items))
	for i, it := range items {
		pv, err := walk(it)
		if err != nil {
			return nil, err
		}
		out[i] = pv
	}
	return out, nil
}

func sortKeys(keys []reflect.Value) {
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		switch {
		case a.Kind() == reflect.String && b.Kind() == reflect.String:
			return a.String() < b.String()
		case a.CanInt() && b.CanInt():
			return a.Int() < b.Int()
		case a.CanUint() && b.CanUint():
			return a.Uint() < b.Uint()
		case a.CanFloat() && b.CanFloat():
			return a.Float() < b.Float()
		}
		return fmt.Sprint(a.Interface()) < fmt.Sprint(b.Interface())
	})
}

func classTypeMarker(c *ClassType, store *InstanceStore, depth int) (value.Type, error) {
	pairs, err := c.eagerAttrs()
	if err != nil {
		return value.Type{}, err
	}
	var attrs *value.Dict
	if len(pairs) > 0 {
		attrs = &value.Dict{}
		for _, p := range pairs {
			pv, err := prepareInner(p.Value, store, depth+1)
			if err != nil {
				return value.Type{}, err
			}
			attrs.Append(p.Key, pv)
		}
	}
	return value.Type{Name: c.Name(), ID: c.ID(), Origin: value.OriginHost, Attrs: attrs}, nil
}

func instanceMarker(ci *ClassInstance, store *InstanceStore, depth int) (value.Instance, error) {
	if err := store.put(ci, false); err != nil {
		return value.Instance{}, err
	}
	pairs, err := ci.eagerAttrs()
	if err != nil {
		return value.Instance{}, err
	}
	attrs := &value.Dict{}
	for _, p := range pairs {
		pv, err := prepareInner(p.Value, store, depth+1)
		if err != nil {
			return value.Instance{}, err
		}
		attrs.Append(p.Key, pv)
	}
	if err := store.put(ci.classType, true); err != nil {
		return value.Instance{}, err
	}
	typ, err := classTypeMarker(ci.classType, store, depth)
	if err != nil {
		return value.Instance{}, err
	}
	return value.Instance{Type: typ, ID: ci.id, Attrs: attrs}, nil
}

func RestoreValue(v any, store *InstanceStore) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = RestoreValue(it, store)
		}
		return out
	case value.Tuple:
		out := make(value.Tuple, len(x))
		for i, it := range x {
			out[i] = RestoreValue(it, store)
		}
		return out
	case *value.Dict:
		out := &value.Dict{}
		for _, p := range x.Pairs() {
			out.Append(RestoreValue(p.Key, store), RestoreValue(p.Value, store))
		}
		return out
	case *value.Set:
		out := &value.Set{}
		for _, it := range x.Items() {
			out.Append(RestoreValue(it, store))
		}
		return out
	case *value.FrozenSet:
		out := &value.FrozenSet{}
		for _, it := range x.Items() {
			out.Append(RestoreValue(it, store))
		}
		return out
	case value.NamedTuple:
		out := x
		out.Values = make([]any, len(x.Values))
		for i, it := range x.Values {
			out.Values[i] = RestoreValue(it, store)
		}
		return out
	case value.Instance:
		if w, ok := store.Get(x.ID); ok {
			if ci, ok := w.(*ClassInstance); ok {
				return ci.instance
			}
		}
		attrs := &value.Dict{}
		for _, p := range x.Attrs.Pairs() {
			if key, ok := p.Key.(string); ok {
				attrs.Append(key, RestoreValue(p.Value, store))
			}
		}
		return &ClassProxy{Name: x.Type.Name, ID: x.ID, IsDataclass: x.Type.IsDataclass, Attributes: attrs, typ: x.Type}
	case value.Type:
		if x.Origin == value.OriginHost {
			if w, ok := store.Get(x.ID); ok {
				if ct, ok := w.(*ClassType); ok {
					return ct
				}
			}
		}
		return x
	}
	return v
}

func KwargsRecord(pairs []value.Pair, store *InstanceStore) Kwargs {
	kw := Kwargs{}
	for _, p := range pairs {
		if key, ok := p.Key.(string); ok && key != "__proto__" {
			kw[key] = RestoreValue(p.Value, store)
		}
	}
	return kw
}
