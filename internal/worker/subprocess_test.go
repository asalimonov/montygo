//go:build unix

package worker

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func plBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("MONTY_BIN")
	if bin == "" {
		_, file, _, _ := runtime.Caller(0)
		bin = filepath.Join(filepath.Dir(file), "..", "..", "..", "monty", "target", "debug", "monty")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("native monty binary unavailable: %v", err)
	}
	return bin
}

func TestSubprocessWorker(t *testing.T) {
	s := &SubprocessSpawner{BinaryPath: plBinary(t)}
	require.Equal(t, KindSubprocess, s.Kind())
	killed := Status{Known: true, Signal: 9}

	t.Run("subprocess transport discards a worker after shutdown", func(t *testing.T) {
		w := plSpawn(t, s)
		require.Equal(t, KindSubprocess, w.Kind())
		pid, ok := w.PID()
		require.True(t, ok)
		require.Positive(t, pid)
		plCheckShutdown(t, w)
	})

	t.Run("kill stops an idle worker promptly", func(t *testing.T) {
		w := plSpawn(t, s)
		plCheckKillIdle(t, w, killed)
		require.Equal(t, "signal: 9 (SIGKILL)", killed.String())
	})

	t.Run("kill stops a busy worker promptly", func(t *testing.T) {
		plCheckKillBusy(t, plSpawn(t, s), killed)
	})

	t.Run("spawn of a missing binary fails", func(t *testing.T) {
		_, err := (&SubprocessSpawner{BinaryPath: filepath.Join(t.TempDir(), "missing")}).Spawn(plCtx(t))
		require.Error(t, err)
	})
}
