// Package worker provides the transports that reach a Monty protocol child:
// a native `monty subprocess`, the embedded wasip1 worker under wazero, and a
// remote child over WebSocket.
package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/asalimonov/montygo/internal/wire"
)

// Kind identifies a transport.
type Kind uint8

const (
	KindSubprocess Kind = iota
	KindWasm
	KindWebSocket
)

func (k Kind) String() string {
	switch k {
	case KindSubprocess:
		return "subprocess"
	case KindWasm:
		return "wasm"
	case KindWebSocket:
		return "websocket"
	}
	return "unknown"
}

// Status is a worker's exit status.
type Status struct {
	Known  bool
	Exited bool
	Code   int
	Signal int
	Killed bool
}

var signalNames = map[int]string{
	1: "SIGHUP", 2: "SIGINT", 3: "SIGQUIT", 4: "SIGILL", 5: "SIGTRAP", 6: "SIGABRT", 7: "SIGBUS", 8: "SIGFPE",
	9: "SIGKILL", 10: "SIGUSR1", 11: "SIGSEGV", 12: "SIGUSR2", 13: "SIGPIPE", 14: "SIGALRM", 15: "SIGTERM",
}

// String renders the status like Rust's ExitStatus Display.
func (s Status) String() string {
	switch {
	case !s.Known:
		return ""
	case s.Exited:
		return fmt.Sprintf("exit status: %d", s.Code)
	case s.Signal != 0:
		if name, ok := signalNames[s.Signal]; ok {
			return fmt.Sprintf("signal: %d (%s)", s.Signal, name)
		}
		return fmt.Sprintf("signal: %d", s.Signal)
	case s.Killed:
		return "killed"
	}
	return ""
}

// Worker is one protocol child.
type Worker interface {
	// Send writes one protocol frame payload.
	Send(ctx context.Context, payload []byte) error
	// Recv returns the next frame payload, io.EOF / wire.ErrTruncated when the stream ends, or ctx.Err().
	Recv(ctx context.Context) ([]byte, error)
	// Kill terminates the worker immediately; it is idempotent.
	Kill()
	// Close ends the worker gracefully (a WebSocket close frame); defaults to Kill.
	Close()
	// Wait blocks until the worker exits or ctx is done.
	Wait(ctx context.Context) (Status, bool)
	PID() (int, bool)
	Kind() Kind
	Alive() bool
	// Done is closed once the worker can no longer serve frames.
	Done() <-chan struct{}
	// Err reports why the worker ended; nil while alive or after a clean end.
	Err() error
}

// Spawner creates workers.
type Spawner interface {
	Spawn(ctx context.Context) (Worker, error)
	Kind() Kind
	Close(ctx context.Context) error
}

// PendingBytesObserver receives the change in bytes a queue holds; nil ignores it.
type PendingBytesObserver func(delta int64)

// frameQueue decouples a blocking frame reader from Recv callers. It holds at
// most maxBytes of frames (a single larger frame is accepted when the queue is
// empty); the reader blocks past that bound until a consumer pops or the queue
// closes, so a flood of worker output cannot grow the parent without limit.
type frameQueue struct {
	mu       sync.Mutex
	frames   [][]byte
	bytes    int64
	maxBytes int64
	err      error
	notify   chan struct{}
	space    chan struct{}
	closed   chan struct{}
	observe  PendingBytesObserver
}

func newFrameQueue(maxBytes int64, observe PendingBytesObserver) *frameQueue {
	return &frameQueue{
		maxBytes: maxBytes,
		notify:   make(chan struct{}),
		space:    make(chan struct{}),
		closed:   make(chan struct{}),
		observe:  observe,
	}
}

func (q *frameQueue) push(frame []byte) error {
	n := int64(len(frame))
	q.mu.Lock()
	for q.maxBytes > 0 && q.bytes > 0 && q.bytes+n > q.maxBytes {
		wait := q.space
		q.mu.Unlock()
		select {
		case <-wait:
		case <-q.closed:
			return ErrWorkerGone
		}
		q.mu.Lock()
	}
	select {
	case <-q.closed:
		q.mu.Unlock()
		return ErrWorkerGone
	default:
	}
	q.frames = append(q.frames, frame)
	q.bytes += n
	close(q.notify)
	q.notify = make(chan struct{})
	q.mu.Unlock()
	if q.observe != nil {
		q.observe(n)
	}
	return nil
}

func (q *frameQueue) fail(err error) {
	q.mu.Lock()
	if q.err == nil {
		q.err = err
	}
	close(q.notify)
	q.notify = make(chan struct{})
	q.mu.Unlock()
}

// close releases a reader blocked on the bound and fails later pops.
func (q *frameQueue) close() {
	q.mu.Lock()
	select {
	case <-q.closed:
	default:
		close(q.closed)
	}
	if q.err == nil {
		q.err = ErrWorkerGone
	}
	close(q.notify)
	q.notify = make(chan struct{})
	q.mu.Unlock()
}

// terminalErr reports the error that ended the stream, nil while it is open or after a clean end.
func (q *frameQueue) terminalErr() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if errors.Is(q.err, io.EOF) || errors.Is(q.err, ErrWorkerGone) {
		return nil
	}
	return q.err
}

func (q *frameQueue) pop(ctx context.Context) ([]byte, error) {
	for {
		q.mu.Lock()
		if len(q.frames) > 0 {
			f := q.frames[0]
			q.frames[0] = nil
			q.frames = q.frames[1:]
			if len(q.frames) == 0 {
				q.frames = nil
			}
			q.bytes -= int64(len(f))
			close(q.space)
			q.space = make(chan struct{})
			q.mu.Unlock()
			if q.observe != nil {
				q.observe(-int64(len(f)))
			}
			return f, nil
		}
		if q.err != nil {
			err := q.err
			q.mu.Unlock()
			if errors.Is(err, ErrWorkerGone) {
				return nil, wire.ErrTruncated
			}
			return nil, err
		}
		ch := q.notify
		q.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func pumpFrames(r *wire.FrameReader, q *frameQueue) {
	for {
		frame, err := r.Next()
		if err != nil {
			q.fail(err)
			return
		}
		if err := q.push(frame); err != nil {
			return
		}
	}
}

// ErrWorkerGone reports a send to a worker that has exited.
var ErrWorkerGone = errors.New("worker is gone")
