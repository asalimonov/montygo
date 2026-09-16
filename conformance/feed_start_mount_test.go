package montygo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func TestFeedStartMounts(t *testing.T) {
	eachBackend(t, func(t *testing.T, b montygo.Backend) {
		t.Run("mounts are re-supplied to loadSnapshot", func(t *testing.T) {
			ctx := testCtx(t)
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644))
			mount, err := montygo.NewMountDir(montygo.MountDirOptions{HostPath: dir, VirtualPath: "/data", Mode: "read-only"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = mount.Close() })
			mounts := []*montygo.MountDir{mount}
			code := "f()\nfrom pathlib import Path\nPath('/data/hello.txt').read_text()"

			first := newSession(t, b, montygo.CheckoutOptions{})
			snap := fstFunction(t)(first.FeedStart(ctx, code, &montygo.FeedOptions{Mount: mounts}))
			blob, err := snap.Dump(ctx)
			require.NoError(t, err)
			require.NoError(t, first.Close(ctx))

			{
				session := newSession(t, b, montygo.CheckoutOptions{})
				restored := fstFunction(t)(session.LoadSnapshot(ctx, blob, &montygo.LoadSnapshotOptions{Mount: mounts}))
				read := fstFunction(t)(restored.Resume(ctx, nil))
				require.True(t, read.IsOSFunction)
				done := fstComplete(t)(read.ResumeAuto(ctx))
				require.Equal(t, "hi", done.Output)
				require.NoError(t, session.Close(ctx))
			}

			{
				session := newSession(t, b, montygo.CheckoutOptions{})
				restored := fstFunction(t)(session.LoadSnapshot(ctx, blob, nil))
				osSnap := fstFunction(t)(restored.Resume(ctx, nil))
				require.True(t, osSnap.IsOSFunction)
				require.Equal(t, "Path.read_text", osSnap.FunctionName)
				_, err := osSnap.ResumeNotHandled(ctx)
				rerr := fstRuntimeError(t, err)
				require.Equal(t, "PermissionError: Permission denied: '/data/hello.txt'", rerr.Error())
				require.NoError(t, session.Close(ctx))
			}
		})
	})
}
