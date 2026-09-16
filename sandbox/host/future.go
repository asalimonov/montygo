package host

import (
	"context"
	"fmt"
	"sync"
)

// panicError turns a recovered panic value into the error a host call reports.
func panicError(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	return fmt.Errorf("%v", r)
}

// Future is the asynchronous result of a host call: returning one from a host
// function lets other sandbox tasks run while it settles.
type Future struct {
	done  chan struct{}
	once  sync.Once
	value any
	err   error
}

// Result returns a settled future's value and error. It does not wait: callers
// that collect settled futures check IsSettled first.
func (f *Future) Result() (any, error) { return f.value, f.err }

// NewFuture returns an unsettled future and the function that settles it.
func NewFuture() (*Future, func(value any, err error)) {
	f := &Future{done: make(chan struct{})}
	return f, f.settle
}

// AsyncContext runs fn in a goroutine with ctx, which SHOULD be the context the
// host function received so an interrupt or cancelled feed ends the work.
func AsyncContext(ctx context.Context, fn func(context.Context) (any, error)) *Future {
	return Async(func() (any, error) { return fn(ctx) })
}

// Async runs fn in a goroutine and returns its future; the work cannot be cancelled.
func Async(fn func() (any, error)) *Future {
	f, settle := NewFuture()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				settle(nil, fmt.Errorf("%v", r))
			}
		}()
		settle(fn())
	}()
	return f
}

func (f *Future) settle(value any, err error) {
	f.once.Do(func() {
		f.value, f.err = value, err
		close(f.done)
	})
}

// Done is closed once the future settles.
func (f *Future) Done() <-chan struct{} { return f.done }

// Wait blocks until the future settles or ctx is done.
func (f *Future) Wait(ctx context.Context) (any, error) {
	select {
	case <-f.done:
		return f.value, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *Future) IsSettled() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

func (f *Future) thenContext(ctx context.Context, convert func(any) (any, error)) *Future {
	out, settle := NewFuture()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				settle(nil, panicError(r))
			}
		}()
		select {
		case <-ctx.Done():
			settle(nil, ctx.Err())
			return
		case <-f.done:
		}
		if err := ctx.Err(); err != nil {
			settle(nil, err)
			return
		}
		if f.err != nil {
			settle(nil, f.err)
			return
		}
		settle(convert(f.value))
	}()
	return out
}
