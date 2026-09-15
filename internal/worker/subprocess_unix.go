//go:build unix

package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/asalimonov/montygo/internal/wire"
)

// SubprocessSpawner starts `monty subprocess` children.
type SubprocessSpawner struct {
	BinaryPath string
	Stderr     io.Writer
	// MaxPendingBytes bounds frames buffered per worker; 0 or less means unbounded.
	MaxPendingBytes int64
	PendingBytes    PendingBytesObserver
}

func (s *SubprocessSpawner) Kind() Kind                  { return KindSubprocess }
func (s *SubprocessSpawner) Close(context.Context) error { return nil }

func (s *SubprocessSpawner) Spawn(ctx context.Context) (Worker, error) {
	cmd := exec.Command(s.BinaryPath, "subprocess")
	cmd.Env = []string{}
	if s.Stderr != nil {
		cmd.Stderr = s.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", s.BinaryPath, err)
	}
	w := &subprocess{cmd: cmd, stdin: stdin, queue: newFrameQueue(s.MaxPendingBytes, s.PendingBytes), done: make(chan struct{})}
	go func() {
		pumpFrames(wire.NewFrameReader(stdout), w.queue)
		_ = cmd.Wait()
		w.status = statusFromState(cmd.ProcessState)
		close(w.done)
	}()
	return w, nil
}

type subprocess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	queue  *frameQueue
	done   chan struct{}
	status Status
	sendMu sync.Mutex
	kill   sync.Once
}

func statusFromState(ps *os.ProcessState) Status {
	if ps == nil {
		return Status{}
	}
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok {
		return Status{Known: true, Exited: true, Code: ps.ExitCode()}
	}
	switch {
	case ws.Exited():
		return Status{Known: true, Exited: true, Code: ws.ExitStatus()}
	case ws.Signaled():
		return Status{Known: true, Signal: int(ws.Signal())}
	}
	return Status{Known: true, Exited: true, Code: ps.ExitCode()}
}

func (w *subprocess) Send(ctx context.Context, payload []byte) error {
	frame, err := wire.AppendFrameHeader(payload)
	if err != nil {
		return err
	}
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	stop := context.AfterFunc(ctx, w.Kill)
	defer stop()
	_, err = w.stdin.Write(frame)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (w *subprocess) Recv(ctx context.Context) ([]byte, error) { return w.queue.pop(ctx) }

func (w *subprocess) Kill() {
	w.kill.Do(func() {
		_ = w.cmd.Process.Kill()
		_ = w.stdin.Close()
		w.queue.close()
	})
}

func (w *subprocess) Close() { w.Kill() }

func (w *subprocess) Wait(ctx context.Context) (Status, bool) {
	select {
	case <-w.done:
		return w.status, true
	case <-ctx.Done():
		return Status{}, false
	}
}

func (w *subprocess) PID() (int, bool) { return w.cmd.Process.Pid, true }
func (w *subprocess) Kind() Kind       { return KindSubprocess }

func (w *subprocess) Alive() bool {
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}

func (w *subprocess) Done() <-chan struct{} { return w.done }
func (w *subprocess) Err() error            { return w.queue.terminalErr() }

// CloseStdin closes the worker's stdin so it exits at a frame boundary.
func (w *subprocess) CloseStdin() error { return w.stdin.Close() }
