package monty

import (
	"context"
	"fmt"
	"sync"
)

// Future is the asynchronous result of a host call: returning one from a host
// function lets other sandbox tasks run while it settles.
type Future struct {
	done  chan struct{}
	once  sync.Once
	value any
	err   error
}

// NewFuture returns an unsettled future and the function that settles it.
func NewFuture() (*Future, func(value any, err error)) {
	f := &Future{done: make(chan struct{})}
	return f, f.settle
}

// Async runs fn in a goroutine and returns its future.
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

func (f *Future) settled() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

func (f *Future) then(convert func(any) (any, error)) *Future {
	out, settle := NewFuture()
	go func() {
		<-f.done
		if f.err != nil {
			settle(nil, f.err)
			return
		}
		settle(convert(f.value))
	}()
	return out
}
