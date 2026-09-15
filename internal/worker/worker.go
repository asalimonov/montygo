// Package worker provides the transports that reach a Monty protocol child:
// a native `monty subprocess`, the embedded wasip1 worker under wazero, and a
// remote child over WebSocket.
package worker

import (
	"context"
	"errors"
	"fmt"
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
}

// Spawner creates workers.
type Spawner interface {
	Spawn(ctx context.Context) (Worker, error)
	Kind() Kind
	Close(ctx context.Context) error
}

// frameQueue decouples a blocking frame reader from Recv callers so the
// reader never blocks on a consumer and can observe the stream end promptly.
type frameQueue struct {
	mu     sync.Mutex
	frames [][]byte
	err    error
	notify chan struct{}
}

func newFrameQueue() *frameQueue {
	return &frameQueue{notify: make(chan struct{})}
}

func (q *frameQueue) push(frame []byte) {
	q.mu.Lock()
	q.frames = append(q.frames, frame)
	close(q.notify)
	q.notify = make(chan struct{})
	q.mu.Unlock()
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

func (q *frameQueue) pop(ctx context.Context) ([]byte, error) {
	for {
		q.mu.Lock()
		if len(q.frames) > 0 {
			f := q.frames[0]
			q.frames[0] = nil
			q.frames = q.frames[1:]
			q.mu.Unlock()
			return f, nil
		}
		if q.err != nil {
			err := q.err
			q.mu.Unlock()
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
		q.push(frame)
	}
}

// ErrWorkerGone reports a send to a worker that has exited.
var ErrWorkerGone = errors.New("worker is gone")
