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

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox/host"
	"github.com/asalimonov/montygo/sandbox/osaccess"
)

// backend names a local worker kind in MONTY_TEST_BACKENDS.
type backend string

const (
	backendNative backend = "native"
	backendWasm   backend = "wasm"
)

func (b backend) String() string { return string(b) }

var (
	poolsMu sync.Mutex
	pools   = map[backend]*montygo.Pool{}
)

// defaultRuntime is the runtime of feeds without an OS handler.
var defaultRuntime = mustRuntime(montygo.RuntimeOptions{})

func mustRuntime(opts montygo.RuntimeOptions) *montygo.Runtime {
	rt, err := montygo.NewRuntime(opts)
	if err != nil {
		panic(err)
	}
	return rt
}

func TestMain(m *testing.M) {
	if os.Getenv("MONTY_BIN") == "" {
		_, file, _, _ := runtime.Caller(0)
		candidate := filepath.Join(filepath.Dir(file), "..", "..", "..", "monty", "target", "debug", "monty")
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

func testBackends() []backend {
	spec := os.Getenv("MONTY_TEST_BACKENDS")
	if spec == "" {
		spec = "native,wasm"
	}
	var out []backend
	for _, name := range strings.Split(spec, ",") {
		switch strings.TrimSpace(name) {
		case "native":
			out = append(out, backendNative)
		case "wasm":
			out = append(out, backendWasm)
		}
	}
	return out
}

func testCtx(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func sharedPool(t testing.TB, b backend) *montygo.Pool {
	t.Helper()
	poolsMu.Lock()
	defer poolsMu.Unlock()
	if p, ok := pools[b]; ok {
		return p
	}
	opts := montygo.PoolOptions{MaxWorkers: 8}
	switch b {
	case backendNative:
		opts.Workers = montygo.Native(montygo.NativeOptions{})
	case backendWasm:
		opts.Workers = montygo.Wasm(montygo.WasmOptions{})
	}
	p, err := montygo.NewPool(context.Background(), opts)
	if err != nil {
		t.Skipf("backend %s unavailable: %v", b, err)
	}
	pools[b] = p
	return p
}

// montyTest runs a sandbox-driven test once per backend.
func montyTest(t *testing.T, name string, fn func(t *testing.T, b backend)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		for _, b := range testBackends() {
			t.Run(b.String(), func(t *testing.T) { fn(t, b) })
		}
	})
}

// runFeed executes code in a fresh session of rt; nil means defaultRuntime.
func runFeed(t testing.TB, b backend, rt *montygo.Runtime, code string, opts *montygo.FeedOptions) (any, error) {
	t.Helper()
	if rt == nil {
		rt = defaultRuntime
	}
	ctx := testCtx(t)
	s, err := sharedPool(t, b).Checkout(ctx, rt, montygo.CheckoutOptions{})
	require.NoError(t, err)
	defer s.Close(context.Background())
	return s.FeedRun(ctx, code, opts)
}

func run(t testing.TB, b backend, code string, handler host.OSHandler) (any, error) {
	t.Helper()
	return runFeed(t, b, mustRuntime(montygo.RuntimeOptions{OS: handler}), code, nil)
}

func mustRun(t testing.TB, b backend, code string, handler host.OSHandler) any {
	t.Helper()
	v, err := run(t, b, code, handler)
	require.NoError(t, err)
	return v
}

func requireRuntimeError(t testing.TB, err error, want string) *monterr.RuntimeError {
	t.Helper()
	require.Error(t, err)
	var rte *monterr.RuntimeError
	require.True(t, errors.As(err, &rte), "want *monterr.RuntimeError, got %T: %v", err, err)
	require.Equal(t, want, rte.Error())
	return rte
}

func requireRaised(t testing.TB, err error, excType, message string) {
	t.Helper()
	require.Error(t, err)
	var raised *monterr.RaisedError
	require.True(t, errors.As(err, &raised), "want *monterr.RaisedError, got %T: %v", err, err)
	require.Equal(t, excType, raised.ExcType)
	require.Equal(t, message, raised.Message)
}

func requireExcType(t testing.TB, err error, excType string) {
	t.Helper()
	require.Error(t, err)
	var raised *monterr.RaisedError
	require.True(t, errors.As(err, &raised), "want *monterr.RaisedError, got %T: %v", err, err)
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
