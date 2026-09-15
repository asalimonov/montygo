package osaccess_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/osaccess"
)

var (
	poolsMu sync.Mutex
	pools   = map[monty.Backend]*monty.Pool{}
)

func TestMain(m *testing.M) {
	if os.Getenv("MONTY_BIN") == "" {
		_, file, _, _ := runtime.Caller(0)
		candidate := filepath.Join(filepath.Dir(file), "..", "..", "monty", "target", "debug", "monty")
		if _, err := os.Stat(candidate); err == nil {
			_ = os.Setenv("MONTY_BIN", candidate)
		}
	}
	code := m.Run()
	poolsMu.Lock()
	for _, p := range pools {
		_ = p.Close(context.Background())
	}
	poolsMu.Unlock()
	os.Exit(code)
}

func testBackends() []monty.Backend {
	spec := os.Getenv("MONTY_TEST_BACKENDS")
	if spec == "" {
		spec = "native,wasm"
	}
	var out []monty.Backend
	for _, name := range strings.Split(spec, ",") {
		switch strings.TrimSpace(name) {
		case "native":
			out = append(out, monty.BackendNative)
		case "wasm":
			out = append(out, monty.BackendWasm)
		}
	}
	return out
}

func testCtx(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func sharedPool(t testing.TB, b monty.Backend) *monty.Pool {
	t.Helper()
	poolsMu.Lock()
	defer poolsMu.Unlock()
	if p, ok := pools[b]; ok {
		return p
	}
	p, err := monty.New(context.Background(), monty.Options{Backend: b, MaxProcesses: 8})
	if err != nil {
		t.Skipf("backend %s unavailable: %v", b, err)
	}
	pools[b] = p
	return p
}

// montyTest runs a sandbox-driven test once per backend.
func montyTest(t *testing.T, name string, fn func(t *testing.T, b monty.Backend)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		for _, b := range testBackends() {
			t.Run(b.String(), func(t *testing.T) { fn(t, b) })
		}
	})
}

func runFeed(t testing.TB, b monty.Backend, code string, opts *monty.FeedOptions) (any, error) {
	t.Helper()
	ctx := testCtx(t)
	s, err := sharedPool(t, b).Checkout(ctx, monty.CheckoutOptions{})
	require.NoError(t, err)
	defer s.Close(context.Background())
	return s.FeedRun(ctx, code, opts)
}

func run(t testing.TB, b monty.Backend, code string, handler monty.OSHandler) (any, error) {
	t.Helper()
	return runFeed(t, b, code, &monty.FeedOptions{OS: handler})
}

func mustRun(t testing.TB, b monty.Backend, code string, handler monty.OSHandler) any {
	t.Helper()
	v, err := run(t, b, code, handler)
	require.NoError(t, err)
	return v
}

func requireRuntimeError(t testing.TB, err error, want string) *monty.RuntimeError {
	t.Helper()
	require.Error(t, err)
	var rte *monty.RuntimeError
	require.True(t, errors.As(err, &rte), "want *monty.RuntimeError, got %T: %v", err, err)
	require.Equal(t, want, rte.Error())
	return rte
}

func requireRaised(t testing.TB, err error, excType, message string) {
	t.Helper()
	require.Error(t, err)
	var raised *monty.RaisedError
	require.True(t, errors.As(err, &raised), "want *monty.RaisedError, got %T: %v", err, err)
	require.Equal(t, excType, raised.ExcType)
	require.Equal(t, message, raised.Message)
}

func requireExcType(t testing.TB, err error, excType string) {
	t.Helper()
	require.Error(t, err)
	var raised *monty.RaisedError
	require.True(t, errors.As(err, &raised), "want *monty.RaisedError, got %T: %v", err, err)
	require.Equal(t, excType, raised.ExcType)
}

func mem(path string, content any, permissions ...int64) osaccess.File {
	return osaccess.NewMemoryFile(path, content, permissions...)
}

func newFS(t testing.TB, files ...osaccess.File) *osaccess.OSAccess {
	t.Helper()
	fs, err := osaccess.New(files, nil)
	require.NoError(t, err)
	return fs
}

func newEnvFS(t testing.TB, environ map[string]string) *osaccess.OSAccess {
	t.Helper()
	fs, err := osaccess.New(nil, environ)
	require.NoError(t, err)
	return fs
}

func must[T any](t testing.TB) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		require.NoError(t, err)
		return v
	}
}
