package montygo

import "github.com/asalimonov/montygo/runtime/host"

// Generic functions cannot be aliased, so the facade forwards them.

// Expose exposes exactly the methods of the interface T under their sandbox
// names, so widening the surface means editing the interface. T MUST be an
// interface type.
func Expose[T any]() AttrPolicy { return host.Expose[T]() }

// NewClassType wraps the class of T for the sandbox.
func NewClassType[T any](opts ClassTypeOptions) (*ClassType, error) {
	return host.NewClassType[T](opts)
}

// MustClassType is NewClassType that panics on error.
func MustClassType[T any](opts ClassTypeOptions) *ClassType {
	return host.MustClassType[T](opts)
}
