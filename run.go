package montygo

import (
	"context"
	"maps"
)

// Run is a feed started by Session.Go.
type Run struct {
	s    *Session
	exec *execution
}

// Go runs FeedRun on its own goroutine and returns a handle to wait on or interrupt.
func (s *Session) Go(ctx context.Context, code string, opts *FeedOptions) *Run {
	e, err := s.reserveExecution(ctx)
	if err != nil {
		e = &execution{phase: executionFinished, done: make(chan struct{}), err: err}
		close(e.done)
		return &Run{s: s, exec: e}
	}
	r := &Run{s: s, exec: e}
	captured := copyFeedOptions(opts)
	go func() {
		value, err := s.feedRun(ctx, e, code, captured)
		s.finishExecution(e, value, err)
	}()
	return r
}

// Done is closed when the feed has ended.
func (r *Run) Done() <-chan struct{} { return r.exec.done }

// Wait blocks until the feed ends and returns its result.
func (r *Run) Wait() (any, error) {
	<-r.exec.done
	return r.exec.value, r.exec.err
}

// WaitContext waits within ctx's budget without interrupting the execution.
func (r *Run) WaitContext(ctx context.Context) (any, error) {
	if channelClosed(r.exec.done) {
		return r.exec.value, r.exec.err
	}
	select {
	case <-r.exec.done:
		return r.exec.value, r.exec.err
	case <-ctx.Done():
		if channelClosed(r.exec.done) {
			return r.exec.value, r.exec.err
		}
		return nil, ctx.Err()
	}
}

// Interrupt stops the feed; see Session.Interrupt.
func (r *Run) Interrupt(ctx context.Context, opts InterruptOptions) (InterruptResult, error) {
	if err := ctx.Err(); err != nil {
		return InterruptResult{}, err
	}
	req, immediate, err := r.s.requestInterrupt(r.exec, opts)
	if err != nil || req == nil {
		return immediate, err
	}
	return r.s.waitInterrupt(ctx, r.exec, req)
}

func copyFeedOptions(opts *FeedOptions) *FeedOptions {
	if opts == nil {
		return &FeedOptions{}
	}
	copy := *opts
	copy.Inputs = maps.Clone(opts.Inputs)
	copy.ExternalLookup = maps.Clone(opts.ExternalLookup)
	copy.Mount = append([]*MountDir(nil), opts.Mount...)
	return &copy
}
