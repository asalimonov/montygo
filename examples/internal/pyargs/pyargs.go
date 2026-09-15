// Package pyargs binds sandbox call arguments to Python-style signatures.
package pyargs

import (
	"fmt"
	"math"
	"sort"

	"github.com/asalimonov/montygo"
)

// Param is one parameter of a Python signature.
type Param struct {
	Name     string
	Default  any
	Optional bool
}

// Required declares a parameter without a default.
func Required(name string) Param { return Param{Name: name} }

// Optional declares a parameter with a default.
func Optional(name string, def any) Param { return Param{Name: name, Default: def, Optional: true} }

// Bind maps positional and keyword arguments onto params, filling defaults.
func Bind(fn string, args []any, kwargs montygo.Kwargs, params ...Param) ([]any, error) {
	if len(args) > len(params) {
		return nil, typeError("%s() takes %d positional arguments but %d were given", fn, len(params), len(args))
	}
	out := make([]any, len(params))
	set := make([]bool, len(params))
	for i, a := range args {
		out[i], set[i] = a, true
	}
	keys := make([]string, 0, len(kwargs))
	for k := range kwargs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		idx := -1
		for i, p := range params {
			if p.Name == k {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, typeError("%s() got an unexpected keyword argument '%s'", fn, k)
		}
		if set[idx] {
			return nil, typeError("%s() got multiple values for argument '%s'", fn, k)
		}
		out[idx], set[idx] = kwargs[k], true
	}
	for i, p := range params {
		if set[i] {
			continue
		}
		if !p.Optional {
			return nil, typeError("%s() missing 1 required positional argument: '%s'", fn, p.Name)
		}
		out[i] = p.Default
	}
	return out, nil
}

func typeError(format string, args ...any) error {
	return montygo.Raise("TypeError", fmt.Sprintf(format, args...))
}

func wrongType(name, want string, v any) error {
	return typeError("argument '%s' must be %s, not %s", name, want, typeName(v))
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case string:
		return "str"
	case bool:
		return "bool"
	case int64:
		return "int"
	case float64:
		return "float"
	case []any:
		return "list"
	case *montygo.Dict:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

// String converts a str argument.
func String(name string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", wrongType(name, "str", v)
	}
	return s, nil
}

// OptionalString converts a str | None argument.
func OptionalString(name string, v any) (*string, error) {
	if v == nil {
		return nil, nil
	}
	s, err := String(name, v)
	return &s, err
}

// Int converts an int argument.
func Int(name string, v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, wrongType(name, "int", v)
}

// Float converts a float argument, accepting int.
func Float(name string, v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return math.NaN(), wrongType(name, "float", v)
}

// Bool converts a bool argument.
func Bool(name string, v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, wrongType(name, "bool", v)
	}
	return b, nil
}
