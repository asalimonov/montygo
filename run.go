package montygo

import "context"

// Run is a feed started by Session.Go.
type Run struct {
	s     *Session
	done  chan struct{}
	value any
	err   error
}

// Go runs FeedRun on its own goroutine and returns a handle to wait on or interrupt.
func (s *Session) Go(ctx context.Context, code string, opts *FeedOptions) *Run {
	r := &Run{s: s, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		r.value, r.err = s.FeedRun(ctx, code, opts)
	}()
	return r
}

// Done is closed when the feed has ended.
func (r *Run) Done() <-chan struct{} { return r.done }

// Wait blocks until the feed ends and returns its result.
func (r *Run) Wait() (any, error) {
	<-r.done
	return r.value, r.err
}

// Interrupt stops the feed; see Session.Interrupt.
func (r *Run) Interrupt(ctx context.Context, reason error) error {
	return r.s.Interrupt(ctx, reason)
}
