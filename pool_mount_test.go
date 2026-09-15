package montygo_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func TestPoolMounts(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("special files in mounts are rejected without blocking", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("mkfifo is unavailable on windows")
			}
			dir := t.TempDir()
			require.NoError(t, exec.Command("mkfifo", filepath.Join(dir, "pipe")).Run())
			mount, err := montygo.NewMountDir(montygo.MountDirOptions{HostPath: dir, VirtualPath: "/mnt", Mode: montygo.MountReadOnly})
			require.NoError(t, err)
			p := newPool(t, b, montygo.Options{})
			s := plCheckout(t, p, montygo.CheckoutOptions{})
			_, err = s.FeedRun(testCtx(t), "from pathlib import Path\nPath('/mnt/pipe').read_text()", &montygo.FeedOptions{
				Mount: []*montygo.MountDir{mount},
			})
			var rt *montygo.RuntimeError
			require.ErrorAs(t, err, &rt)
			require.EqualError(t, err, "PermissionError: [Errno 13] Permission denied: '/mnt/pipe'")
			plClose(t, s)
		})
	})
}
