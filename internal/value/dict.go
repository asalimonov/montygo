package value

import (
	"math"
	"math/big"
	"reflect"
)

// Pair is one key/value entry of a Dict.
type Pair struct {
	Key   any
	Value any
}

// Dict is an insertion-ordered Python dict. Keys compare with Python equality
// (True == 1 == 1.0); keys the host cannot compare are kept in order and
// rejected by the sandbox when unhashable.
type Dict struct {
	pairs []Pair
}

// NewDict builds a dict; a repeated key keeps its first position and last value.
func NewDict(pairs ...Pair) *Dict {
	d := &Dict{}
	for _, p := range pairs {
		d.Set(p.Key, p.Value)
	}
	return d
}

// Len returns the number of entries.
func (d *Dict) Len() int {
	if d == nil {
		return 0
	}
	return len(d.pairs)
}

func (d *Dict) find(k any) int {
	if d == nil {
		return -1
	}
	for i := range d.pairs {
		if Equal(d.pairs[i].Key, k) {
			return i
		}
	}
	return -1
}

// Get returns the value stored under k.
func (d *Dict) Get(k any) (any, bool) {
	if i := d.find(k); i >= 0 {
		return d.pairs[i].Value, true
	}
	return nil, false
}

// Has reports whether k is a key.
func (d *Dict) Has(k any) bool { return d.find(k) >= 0 }

// Set inserts or replaces the value for k.
func (d *Dict) Set(k, v any) {
	if i := d.find(k); i >= 0 {
		d.pairs[i].Value = v
		return
	}
	d.pairs = append(d.pairs, Pair{Key: k, Value: v})
}

// Append adds an entry without checking for an existing key.
func (d *Dict) Append(k, v any) {
	d.pairs = append(d.pairs, Pair{Key: k, Value: v})
}

// Delete removes k and reports whether it was present.
func (d *Dict) Delete(k any) bool {
	i := d.find(k)
	if i < 0 {
		return false
	}
	d.pairs = append(d.pairs[:i], d.pairs[i+1:]...)
	if len(d.pairs) == 0 {
		d.pairs = nil
	}
	return true
}

// Keys returns the keys in insertion order.
func (d *Dict) Keys() []any {
	out := make([]any, 0, d.Len())
	for _, p := range d.Pairs() {
		out = append(out, p.Key)
	}
	return out
}

// Values returns the values in insertion order.
func (d *Dict) Values() []any {
	out := make([]any, 0, d.Len())
	for _, p := range d.Pairs() {
		out = append(out, p.Value)
	}
	return out
}

// Pairs returns the entries in insertion order; the slice must not be modified.
func (d *Dict) Pairs() []Pair {
	if d == nil {
		return nil
	}
	return d.pairs
}

// Range calls fn for each entry until it returns false.
func (d *Dict) Range(fn func(k, v any) bool) {
	for _, p := range d.Pairs() {
		if !fn(p.Key, p.Value) {
			return
		}
	}
}

// StringMap returns the dict as a Go map when every key is a string.
func (d *Dict) StringMap() (map[string]any, bool) {
	out := make(map[string]any, d.Len())
	for _, p := range d.Pairs() {
		s, ok := p.Key.(string)
		if !ok {
			return nil, false
		}
		out[s] = p.Value
	}
	return out, true
}

// Clone returns a shallow copy.
func (d *Dict) Clone() *Dict {
	if d == nil {
		return nil
	}
	out := &Dict{}
	if len(d.pairs) > 0 {
		out.pairs = append([]Pair(nil), d.pairs...)
	}
	return out
}

// Equal reports Python equality with another dict (order-insensitive).
func (d *Dict) Equal(o *Dict) bool {
	if d.Len() != o.Len() {
		return false
	}
	for _, p := range d.Pairs() {
		v, ok := o.Get(p.Key)
		if !ok || !Equal(p.Value, v) {
			return false
		}
	}
	return true
}

func (d *Dict) String() string { return Repr(d) }

// Set is an insertion-ordered Python set.
type Set struct {
	items []any
}

// FrozenSet is an insertion-ordered Python frozenset.
type FrozenSet struct {
	items []any
}

// NewSet builds a set, dropping duplicates.
func NewSet(items ...any) *Set {
	s := &Set{}
	for _, it := range items {
		s.Add(it)
	}
	return s
}

// NewFrozenSet builds a frozenset, dropping duplicates.
func NewFrozenSet(items ...any) *FrozenSet {
	s := &FrozenSet{}
	for _, it := range items {
		if !containsEqual(s.items, it) {
			s.items = append(s.items, it)
		}
	}
	return s
}

func containsEqual(items []any, v any) bool {
	for _, it := range items {
		if Equal(it, v) {
			return true
		}
	}
	return false
}

func (s *Set) Add(v any) {
	if !containsEqual(s.items, v) {
		s.items = append(s.items, v)
	}
}

// Append adds an item without checking for duplicates.
func (s *Set) Append(v any) { s.items = append(s.items, v) }

func (s *Set) Has(v any) bool { return s != nil && containsEqual(s.items, v) }

func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.items)
}

// Items returns the items in insertion order; the slice must not be modified.
func (s *Set) Items() []any {
	if s == nil {
		return nil
	}
	return s.items
}

func (s *Set) String() string { return Repr(s) }

// Append adds an item without checking for duplicates.
func (s *FrozenSet) Append(v any) { s.items = append(s.items, v) }

func (s *FrozenSet) Has(v any) bool { return s != nil && containsEqual(s.items, v) }

func (s *FrozenSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.items)
}

// Items returns the items in insertion order; the slice must not be modified.
func (s *FrozenSet) Items() []any {
	if s == nil {
		return nil
	}
	return s.items
}

func (s *FrozenSet) String() string { return Repr(s) }

func setsEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !containsEqual(b, x) {
			return false
		}
	}
	return true
}

type numKind uint8

const (
	numNone numKind = iota
	numInt
	numBig
	numFloat
)

func asNumber(v any) (numKind, int64, *big.Int, float64) {
	switch x := v.(type) {
	case bool:
		if x {
			return numInt, 1, nil, 0
		}
		return numInt, 0, nil, 0
	case int:
		return numInt, int64(x), nil, 0
	case int8:
		return numInt, int64(x), nil, 0
	case int16:
		return numInt, int64(x), nil, 0
	case int32:
		return numInt, int64(x), nil, 0
	case int64:
		return numInt, x, nil, 0
	case uint:
		return uintNumber(uint64(x))
	case uint8:
		return numInt, int64(x), nil, 0
	case uint16:
		return numInt, int64(x), nil, 0
	case uint32:
		return numInt, int64(x), nil, 0
	case uint64:
		return uintNumber(x)
	case *big.Int:
		if x == nil {
			return numNone, 0, nil, 0
		}
		if x.IsInt64() {
			return numInt, x.Int64(), nil, 0
		}
		return numBig, 0, x, 0
	case float32:
		return numFloat, 0, nil, float64(x)
	case float64:
		return numFloat, 0, nil, x
	}
	return numNone, 0, nil, 0
}

func uintNumber(x uint64) (numKind, int64, *big.Int, float64) {
	if x <= math.MaxInt64 {
		return numInt, int64(x), nil, 0
	}
	return numBig, 0, new(big.Int).SetUint64(x), 0
}

// Equal reports whether a and b are equal under Python semantics for the
// value model (numbers compare across int/float/bool, containers deeply).
func Equal(a, b any) bool {
	ka, ia, ba, fa := asNumber(a)
	kb, ib, bb, fb := asNumber(b)
	if ka != numNone && kb != numNone {
		return numbersEqual(ka, ia, ba, fa, kb, ib, bb, fb)
	}
	if ka != numNone || kb != numNone {
		return false
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []byte:
		y, ok := b.([]byte)
		return ok && string(x) == string(y)
	case []any:
		y, ok := b.([]any)
		return ok && seqEqual(x, y)
	case Tuple:
		y, ok := b.(Tuple)
		return ok && seqEqual(x, y)
	case *Dict:
		y, ok := b.(*Dict)
		return ok && x.Equal(y)
	case *Set:
		switch y := b.(type) {
		case *Set:
			return setsEqual(x.Items(), y.Items())
		case *FrozenSet:
			return setsEqual(x.Items(), y.Items())
		}
		return false
	case *FrozenSet:
		switch y := b.(type) {
		case *Set:
			return setsEqual(x.Items(), y.Items())
		case *FrozenSet:
			return setsEqual(x.Items(), y.Items())
		}
		return false
	case NamedTuple:
		switch y := b.(type) {
		case NamedTuple:
			return seqEqual(x.Values, y.Values)
		case Tuple:
			return seqEqual(x.Values, y)
		}
		return false
	}
	if reflect.TypeOf(a) == reflect.TypeOf(b) && reflect.TypeOf(a).Comparable() {
		return a == b
	}
	return reflect.DeepEqual(a, b)
}

func seqEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func numbersEqual(ka numKind, ia int64, ba *big.Int, fa float64, kb numKind, ib int64, bb *big.Int, fb float64) bool {
	toBig := func(k numKind, i int64, b *big.Int) *big.Int {
		if k == numBig {
			return b
		}
		return big.NewInt(i)
	}
	switch {
	case ka == numInt && kb == numInt:
		return ia == ib
	case ka == numFloat && kb == numFloat:
		return fa == fb
	case ka == numFloat:
		return floatEqualsInt(fa, toBig(kb, ib, bb))
	case kb == numFloat:
		return floatEqualsInt(fb, toBig(ka, ia, ba))
	default:
		return toBig(ka, ia, ba).Cmp(toBig(kb, ib, bb)) == 0
	}
}

func floatEqualsInt(f float64, i *big.Int) bool {
	if math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) {
		return false
	}
	bf := new(big.Float).SetFloat64(f)
	bi, _ := bf.Int(nil)
	return bi.Cmp(i) == 0
}

// IntValue normalizes an integer: int64 when it fits, else *big.Int.
func IntValue(b *big.Int) any {
	if b.IsInt64() {
		return b.Int64()
	}
	return b
}
