package worker

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/wasmblob"
	"github.com/asalimonov/montygo/internal/wire"
)

func plCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func plWasmSpawner(t *testing.T) *WasmSpawner {
	t.Helper()
	blob, err := wasmblob.Bytes()
	require.NoError(t, err)
	dir, err := wasmblob.DefaultCacheDir()
	if err != nil {
		dir = ""
	}
	s, err := SharedWasmSpawner(context.Background(), blob, wasmblob.SHA256(), dir)
	require.NoError(t, err)
	return s
}

func plSpawn(t *testing.T, s Spawner) Worker {
	t.Helper()
	w, err := s.Spawn(plCtx(t))
	require.NoError(t, err)
	t.Cleanup(w.Kill)
	return w
}

func plSend(t *testing.T, ctx context.Context, w Worker, req wire.Request) {
	t.Helper()
	payload, err := wire.EncodeRequest(req, "")
	require.NoError(t, err)
	require.NoError(t, w.Send(ctx, payload))
}

func plRequest(t *testing.T, ctx context.Context, w Worker, req wire.Request) *wire.Event {
	t.Helper()
	plSend(t, ctx, w, req)
	frame, err := w.Recv(ctx)
	require.NoError(t, err)
	ev, err := wire.DecodeEvent(frame)
	require.NoError(t, err)
	return ev
}

func plConfigure() wire.Configure {
	return wire.Configure{ScriptName: "main.py", ProtocolVersion: 3, MontyVersion: "0.0.23"}
}

func plWait(t *testing.T, w Worker, within time.Duration) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	status, ok := w.Wait(ctx)
	require.True(t, ok, "worker did not exit within %s", within)
	return status
}

func plCheckShutdown(t *testing.T, w Worker) {
	ctx := plCtx(t)
	require.True(t, w.Alive())
	require.Equal(t, wire.EventOk, plRequest(t, ctx, w, plConfigure()).Kind)
	require.Equal(t, wire.EventOk, plRequest(t, ctx, w, wire.Shutdown{}).Kind)
	status := plWait(t, w, 10*time.Second)
	require.Equal(t, Status{Known: true, Exited: true, Code: 0}, status)
	require.Equal(t, "exit status: 0", status.String())
	require.False(t, w.Alive())
	_, err := w.Recv(ctx)
	require.True(t, errors.Is(err, io.EOF) || errors.Is(err, wire.ErrTruncated), "unexpected Recv error after exit: %v", err)
	payload, err := wire.EncodeRequest(plConfigure(), "")
	require.NoError(t, err)
	require.Error(t, w.Send(ctx, payload))
}

func plCheckKillIdle(t *testing.T, w Worker, want Status) {
	ctx := plCtx(t)
	require.Equal(t, wire.EventOk, plRequest(t, ctx, w, plConfigure()).Kind)
	w.Kill()
	w.Kill()
	require.Equal(t, want, plWait(t, w, 2*time.Second))
	require.False(t, w.Alive())
}

func plCheckKillBusy(t *testing.T, w Worker, want Status) {
	ctx := plCtx(t)
	require.Equal(t, wire.EventOk, plRequest(t, ctx, w, plConfigure()).Kind)
	plSend(t, ctx, w, wire.Feed{Code: "while True:\n    pass"})
	time.Sleep(200 * time.Millisecond)
	require.True(t, w.Alive())
	w.Kill()
	require.Equal(t, want, plWait(t, w, 2*time.Second))
	require.False(t, w.Alive())
	_, err := w.Recv(ctx)
	require.Error(t, err)
}

func TestWasmWorker(t *testing.T) {
	s := plWasmSpawner(t)
	require.Equal(t, KindWasm, s.Kind())

	t.Run("wasm transport discards a component after shutdown", func(t *testing.T) {
		w := plSpawn(t, s)
		require.Equal(t, KindWasm, w.Kind())
		_, ok := w.PID()
		require.False(t, ok)
		plCheckShutdown(t, w)
	})

	t.Run("kill stops an idle worker promptly", func(t *testing.T) {
		plCheckKillIdle(t, plSpawn(t, s), Status{Known: true, Killed: true})
	})

	t.Run("kill stops a busy worker promptly", func(t *testing.T) {
		plCheckKillBusy(t, plSpawn(t, s), Status{Known: true, Killed: true})
	})

	t.Run("shared spawner is reused per blob", func(t *testing.T) {
		require.Same(t, s, plWasmSpawner(t))
	})
}
