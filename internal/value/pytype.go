package value

import (
	"math/big"
	"reflect"
)

// PyTypeName returns the Python type name a host value presents as.
func PyTypeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, *big.Int:
		return "int"
	case float32, float64:
		return "float"
	case string:
		return "str"
	case []byte:
		return "bytes"
	case Tuple:
		return "tuple"
	case []any:
		return "list"
	case *Dict:
		return "dict"
	case *Set:
		return "set"
	case *FrozenSet:
		return "frozenset"
	case Date:
		return "date"
	case DateTime:
		return "datetime"
	case Time:
		return "time"
	case TimeDelta:
		return "timedelta"
	case TimeZone:
		return "timezone"
	case Exception:
		return x.ExcType
	case Type:
		return "type"
	case Path:
		return "PosixPath"
	case *FileHandle:
		return "TextIOWrapper"
	case EllipsisType:
		return "ellipsis"
	case NotImplementedType:
		return "NotImplementedType"
	case Function:
		return "function"
	case BuiltinFunction:
		return "builtin_function_or_method"
	case NamedTuple:
		return x.TypeName
	case Instance:
		return x.Type.Name
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		return "dict"
	case reflect.Slice, reflect.Array:
		return "list"
	case reflect.Func:
		return "function"
	}
	return "object"
}
