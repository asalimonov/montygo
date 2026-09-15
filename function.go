package monty

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/asalimonov/montygo/internal/value"
)

// Kwargs are the keyword arguments of a sandbox call.
type Kwargs map[string]any

// Function is a host function the sandbox can call.
type Function interface {
	Call(ctx context.Context, args []any, kwargs Kwargs) (any, error)
}

// FunctionFunc adapts an ordinary function to Function.
type FunctionFunc func(ctx context.Context, args []any, kwargs Kwargs) (any, error)

// Call implements Function.
func (f FunctionFunc) Call(ctx context.Context, args []any, kwargs Kwargs) (any, error) {
	return f(ctx, args, kwargs)
}

type notHandledSentinel struct{ _ byte }

// NotHandled is returned by an OSHandler to decline a call.
var NotHandled = &notHandledSentinel{}

// OSHandler answers OS calls (Path.read_text, os.getenv, ...) no mount covered.
type OSHandler func(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error)

var (
	contextType = reflect.TypeFor[context.Context]()
	kwargsType  = reflect.TypeFor[Kwargs]()
	errorType   = reflect.TypeFor[error]()
)

// Func adapts a Go function to Function. Parameters are converted from sandbox
// values; an optional leading context.Context and trailing Kwargs are
// supported; results may be (T), (error), (T, error), or a *Future.
func Func(fn any) (Function, error) {
	switch f := fn.(type) {
	case Function:
		return f, nil
	case func(context.Context, []any, Kwargs) (any, error):
		return FunctionFunc(f), nil
	}
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func || rv.IsNil() {
		return nil, &ValueError{Message: fmt.Sprintf("Func expects a function, got %T", fn)}
	}
	rt := rv.Type()
	rf := &reflectFunction{fv: rv, ft: rt, name: funcName(rv)}
	first := 0
	if rt.NumIn() > 0 && rt.In(0) == contextType {
		rf.hasCtx = true
		first = 1
	}
	last := rt.NumIn()
	if last > first && rt.In(last-1) == kwargsType && !rt.IsVariadic() {
		rf.hasKwargs = true
		last--
	}
	rf.positional = last - first
	if rt.NumOut() > 2 || (rt.NumOut() == 2 && rt.Out(1) != errorType) {
		return nil, &ValueError{Message: fmt.Sprintf("%s: results must be (T), (error) or (T, error)", rf.name)}
	}
	return rf, nil
}

// MustFunc is Func that panics on error.
func MustFunc(fn any) Function {
	f, err := Func(fn)
	if err != nil {
		panic(err)
	}
	return f
}

type reflectFunction struct {
	fv         reflect.Value
	ft         reflect.Type
	name       string
	hasCtx     bool
	hasKwargs  bool
	positional int
}

func funcName(rv reflect.Value) string {
	f := runtime.FuncForPC(rv.Pointer())
	if f == nil {
		return "function"
	}
	name := f.Name()
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func typeErr(format string, args ...any) error {
	return &RaisedError{ExcType: "TypeError", Message: fmt.Sprintf(format, args...)}
}

func (r *reflectFunction) Call(ctx context.Context, args []any, kwargs Kwargs) (any, error) {
	if len(kwargs) > 0 && !r.hasKwargs {
		keys := make([]string, 0, len(kwargs))
		for k := range kwargs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, typeErr("%s() got an unexpected keyword argument '%s'", r.name, keys[0])
	}
	in := make([]reflect.Value, 0, r.ft.NumIn())
	offset := 0
	if r.hasCtx {
		in = append(in, reflect.ValueOf(ctx))
		offset = 1
	}
	variadic := r.ft.IsVariadic()
	fixed := r.positional
	if variadic {
		fixed--
		if len(args) < fixed {
			return nil, typeErr("%s() missing %d required positional arguments", r.name, fixed-len(args))
		}
	} else if len(args) != fixed {
		return nil, typeErr("%s() takes %d positional arguments but %d were given", r.name, fixed, len(args))
	}
	for i := 0; i < fixed; i++ {
		v, err := assign(r.ft.In(offset+i), args[i])
		if err != nil {
			return nil, typeErr("%s() argument %d: %v", r.name, i+1, err)
		}
		in = append(in, v)
	}
	if variadic {
		elem := r.ft.In(r.ft.NumIn() - 1).Elem()
		for i := fixed; i < len(args); i++ {
			v, err := assign(elem, args[i])
			if err != nil {
				return nil, typeErr("%s() argument %d: %v", r.name, i+1, err)
			}
			in = append(in, v)
		}
	}
	if r.hasKwargs {
		if kwargs == nil {
			kwargs = Kwargs{}
		}
		in = append(in, reflect.ValueOf(kwargs))
	}
	out := r.fv.Call(in)
	switch len(out) {
	case 0:
		return nil, nil
	case 1:
		if r.ft.Out(0) == errorType {
			return nil, asError(out[0])
		}
		return out[0].Interface(), nil
	}
	return out[0].Interface(), asError(out[1])
}

func asError(v reflect.Value) error {
	if v.IsNil() {
		return nil
	}
	return v.Interface().(error)
}

// assign converts a sandbox value into a Go parameter of type dst.
func assign(dst reflect.Type, src any) (reflect.Value, error) {
	if src == nil {
		switch dst.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Slice, reflect.Map, reflect.Func:
			return reflect.Zero(dst), nil
		}
		return reflect.Value{}, fmt.Errorf("cannot convert None to %s", dst)
	}
	sv := reflect.ValueOf(src)
	if sv.Type().AssignableTo(dst) {
		return sv, nil
	}
	fail := func() (reflect.Value, error) {
		return reflect.Value{}, fmt.Errorf("cannot convert %s to %s", value.PyTypeName(src), dst)
	}
	switch dst.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, ok := toInt64(src)
		out := reflect.New(dst).Elem()
		if !ok || out.OverflowInt(i) {
			return fail()
		}
		out.SetInt(i)
		return out, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		i, ok := toInt64(src)
		out := reflect.New(dst).Elem()
		if !ok || i < 0 || out.OverflowUint(uint64(i)) {
			if b, isBig := src.(*big.Int); isBig && b.IsUint64() && dst.Kind() == reflect.Uint64 {
				out.SetUint(b.Uint64())
				return out, nil
			}
			return fail()
		}
		out.SetUint(uint64(i))
		return out, nil
	case reflect.Float32, reflect.Float64:
		out := reflect.New(dst).Elem()
		switch x := src.(type) {
		case float64:
			out.SetFloat(x)
		case int64:
			out.SetFloat(float64(x))
		case *big.Int:
			f, _ := new(big.Float).SetInt(x).Float64()
			out.SetFloat(f)
		default:
			return fail()
		}
		return out, nil
	case reflect.String:
		out := reflect.New(dst).Elem()
		switch x := src.(type) {
		case string:
			out.SetString(x)
		case value.Path:
			out.SetString(string(x))
		default:
			return fail()
		}
		return out, nil
	case reflect.Bool:
		b, ok := src.(bool)
		if !ok {
			return fail()
		}
		out := reflect.New(dst).Elem()
		out.SetBool(b)
		return out, nil
	case reflect.Slice:
		items, ok := sequenceItems(src)
		if !ok {
			if b, isBytes := src.([]byte); isBytes && dst.Elem().Kind() == reflect.Uint8 {
				out := reflect.MakeSlice(dst, len(b), len(b))
				reflect.Copy(out, reflect.ValueOf(b))
				return out, nil
			}
			return fail()
		}
		out := reflect.MakeSlice(dst, len(items), len(items))
		for i, it := range items {
			v, err := assign(dst.Elem(), it)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(v)
		}
		return out, nil
	case reflect.Array:
		items, ok := sequenceItems(src)
		if !ok || len(items) != dst.Len() {
			return fail()
		}
		out := reflect.New(dst).Elem()
		for i, it := range items {
			v, err := assign(dst.Elem(), it)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(v)
		}
		return out, nil
	case reflect.Map:
		d, ok := src.(*value.Dict)
		if !ok {
			return fail()
		}
		out := reflect.MakeMapWithSize(dst, d.Len())
		for _, p := range d.Pairs() {
			k, err := assign(dst.Key(), p.Key)
			if err != nil {
				return reflect.Value{}, err
			}
			v, err := assign(dst.Elem(), p.Value)
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(k, v)
		}
		return out, nil
	case reflect.Pointer:
		inner, err := assign(dst.Elem(), src)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(dst.Elem())
		out.Elem().Set(inner)
		return out, nil
	case reflect.Struct:
		if sv.Kind() == reflect.Pointer && sv.Type().Elem().AssignableTo(dst) && !sv.IsNil() {
			return sv.Elem(), nil
		}
	}
	return fail()
}

func toInt64(src any) (int64, bool) {
	switch x := src.(type) {
	case int64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case *big.Int:
		if x.IsInt64() {
			return x.Int64(), true
		}
	case float64:
		if x == math.Trunc(x) && x >= math.MinInt64 && x < math.MaxInt64 {
			return int64(x), true
		}
	}
	return 0, false
}

func sequenceItems(src any) ([]any, bool) {
	switch x := src.(type) {
	case []any:
		return x, true
	case value.Tuple:
		return x, true
	case *value.Set:
		return x.Items(), true
	case *value.FrozenSet:
		return x.Items(), true
	}
	return nil, false
}
