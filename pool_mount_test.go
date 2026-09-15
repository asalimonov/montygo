package monty_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

func TestPoolMounts(t *testing.T) {
	eachBackend(t, func(t *testing.T, b monty.Backend) {
		t.Run("special files in mounts are rejected without blocking", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("mkfifo is unavailable on windows")
			}
			dir := t.TempDir()
			require.NoError(t, exec.Command("mkfifo", filepath.Join(dir, "pipe")).Run())
			mount, err := monty.NewMountDir(monty.MountDirOptions{HostPath: dir, VirtualPath: "/mnt", Mode: monty.MountReadOnly})
			require.NoError(t, err)
			p := newPool(t, b, monty.Options{})
			s := plCheckout(t, p, monty.CheckoutOptions{})
			_, err = s.FeedRun(testCtx(t), "from pathlib import Path\nPath('/mnt/pipe').read_text()", &monty.FeedOptions{
				Mount: []*monty.MountDir{mount},
			})
			var rt *monty.RuntimeError
			require.ErrorAs(t, err, &rt)
			require.EqualError(t, err, "PermissionError: [Errno 13] Permission denied: '/mnt/pipe'")
			plClose(t, s)
		})
	})
}
