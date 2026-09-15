package network

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const replWait = 30 * time.Second

func TestReplCLI_StateAndPrintOverWebSocket(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	r := StartRepl(t, s.URL())
	r.Send("x = 20")
	r.Send("print('hello from the sandbox')")
	r.Send("x + 22")
	r.WaitStdout("hello from the sandbox", replWait)
	r.WaitStdout("42", replWait)
	r.CloseStdin()
	require.Equal(t, 0, r.Wait(replWait))
	require.Contains(t, r.Stderr(), "REPL")
}

func TestReplCLI_ContinuationOverWebSocket(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	r := StartRepl(t, s.URL())
	r.Send("def add(a, b):")
	r.Send("    return a + b")
	r.Send("")
	r.Send("add(20, 22)")
	r.WaitStdout("42", replWait)
	r.Send("[1,")
	r.Send(" 2]")
	r.WaitStdout("[1, 2]", replWait)
	r.CloseStdin()
	require.Equal(t, 0, r.Wait(replWait))
}

func TestReplCLI_ErrorsKeepSession(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	r := StartRepl(t, s.URL())
	r.Send("x = 6")
	r.Send("1 / 0")
	r.WaitStderr("ZeroDivisionError", replWait)
	r.Send("x * 7")
	r.WaitStdout("42", replWait)
	r.CloseStdin()
	require.Equal(t, 0, r.Wait(replWait))
}

func TestReplCLI_Mounts(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data.txt"), []byte("payload-from-host"), 0o644))
	r := StartRepl(t, s.URL(), "-m", dir+"::/data")
	r.Send("open('/data/data.txt').read()")
	r.WaitStdout("payload-from-host", replWait)
	r.CloseStdin()
	require.Equal(t, 0, r.Wait(replWait))
}

func TestReplCLI_InterruptReplacesSession(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	r := StartRepl(t, s.URL())
	r.Send("x = 1")
	r.Send("while True: pass")
	time.Sleep(time.Second)
	r.Interrupt()
	r.Send("2 + 2")
	r.WaitStdout("4", replWait)
	r.CloseStdin()
	require.Equal(t, 0, r.Wait(replWait))
}

func TestReplCLI_ServerUnavailable(t *testing.T) {
	t.Parallel()
	r := StartRepl(t, "ws://127.0.0.1:1/")
	r.CloseStdin()
	require.Equal(t, 1, r.Wait(replWait))
	require.Contains(t, r.Stderr(), "error:")
}
