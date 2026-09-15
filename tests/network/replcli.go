package network

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var replBuild struct {
	once sync.Once
	dir  string
	path string
	err  error
}

// replBinary builds examples/repl once per test run.
func replBinary(t *testing.T) string {
	t.Helper()
	replBuild.once.Do(func() {
		dir, err := os.MkdirTemp("", "montygo-repl-")
		if err != nil {
			replBuild.err = err
			return
		}
		replBuild.dir = dir
		path := filepath.Join(dir, "repl")
		args := []string{"build", "-C", "../../examples"}
		if v := os.Getenv("MONTYGO_BUILD_VERSION"); v != "" {
			args = append(args, "-ldflags", "-X github.com/asalimonov/montygo.buildVersion="+v)
		}
		cmd := exec.Command("go", append(args, "-o", path, "./repl")...)
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
		if out, err := cmd.CombinedOutput(); err != nil {
			replBuild.err = fmt.Errorf("build examples/repl: %w\n%s", err, out)
			return
		}
		replBuild.path = path
	})
	require.NoError(t, replBuild.err)
	return replBuild.path
}

func replCleanup() {
	if replBuild.dir != "" {
		_ = os.RemoveAll(replBuild.dir)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ReplProc is a running examples/repl process.
type ReplProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *syncBuffer
	stderr *syncBuffer
	exited chan struct{}
}

// StartRepl runs the REPL against a WebSocket server URL.
func StartRepl(t *testing.T, url string, args ...string) *ReplProc {
	t.Helper()
	cmd := exec.Command(replBinary(t), append([]string{"-ws", url}, args...)...)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	r := &ReplProc{t: t, cmd: cmd, stdin: stdin, stdout: &syncBuffer{}, stderr: &syncBuffer{}, exited: make(chan struct{})}
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	require.NoError(t, cmd.Start())
	go func() {
		_ = cmd.Wait()
		close(r.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-r.exited:
		default:
			_ = cmd.Process.Kill()
			<-r.exited
		}
		if t.Failed() {
			t.Logf("repl stdout:\n%s\nrepl stderr:\n%s", r.stdout.String(), r.stderr.String())
		}
	})
	return r
}

func (r *ReplProc) Send(line string) {
	r.t.Helper()
	_, err := io.WriteString(r.stdin, line+"\n")
	require.NoError(r.t, err)
}

func waitFor(t *testing.T, what string, buf *syncBuffer, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !strings.Contains(buf.String(), substr) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never contained %q; got:\n%s", what, substr, buf.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *ReplProc) WaitStdout(substr string, timeout time.Duration) {
	r.t.Helper()
	waitFor(r.t, "stdout", r.stdout, substr, timeout)
}

func (r *ReplProc) WaitStderr(substr string, timeout time.Duration) {
	r.t.Helper()
	waitFor(r.t, "stderr", r.stderr, substr, timeout)
}

func (r *ReplProc) Stdout() string { return r.stdout.String() }
func (r *ReplProc) Stderr() string { return r.stderr.String() }

func (r *ReplProc) Interrupt() {
	r.t.Helper()
	require.NoError(r.t, r.cmd.Process.Signal(os.Interrupt))
}

func (r *ReplProc) CloseStdin() {
	_ = r.stdin.Close()
}

// Wait waits for the process to exit and returns its exit code.
func (r *ReplProc) Wait(timeout time.Duration) int {
	r.t.Helper()
	select {
	case <-r.exited:
		return r.cmd.ProcessState.ExitCode()
	case <-time.After(timeout):
		r.t.Fatalf("repl did not exit within %s", timeout)
		return -1
	}
}
