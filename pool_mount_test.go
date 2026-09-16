package montygo_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
)

func TestPoolMounts(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		t.Run("special files in mounts are rejected without blocking", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("mkfifo is unavailable on windows")
			}
			dir := t.TempDir()
			require.NoError(t, exec.Command("mkfifo", filepath.Join(dir, "pipe")).Run())
			mount, err := sandbox.NewMountDir(sandbox.MountDirOptions{HostPath: dir, VirtualPath: "/mnt", Mode: sandbox.MountReadOnly})
			require.NoError(t, err)
			p := newPool(t, b, montygo.PoolOptions{})
			s := plCheckout(t, p, montygo.CheckoutOptions{})
			_, err = s.FeedRun(testCtx(t), "from pathlib import Path\nPath('/mnt/pipe').read_text()", &montygo.FeedOptions{
				Mount: []*sandbox.MountDir{mount},
			})
			var rt *monterr.RuntimeError
			require.ErrorAs(t, err, &rt)
			require.EqualError(t, err, "PermissionError: [Errno 13] Permission denied: '/mnt/pipe'")
			plClose(t, s)
		})
	})
}
