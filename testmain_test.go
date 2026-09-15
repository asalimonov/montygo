package monty_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

type poolKey struct {
	backend monty.Backend
	test    string
}

var (
	poolsMu sync.Mutex
	pools   = map[poolKey]*monty.Pool{}
)

func TestMain(m *testing.M) {
	if os.Getenv("MONTY_BIN") == "" {
		_, file, _, _ := runtime.Caller(0)
		candidate := filepath.Join(filepath.Dir(file), "..", "monty", "target", "debug", "monty")
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

// testBackends lists the backends from MONTY_TEST_BACKENDS (default native,wasm).
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

func topLevelTest(t testing.TB) string {
	name, _, _ := strings.Cut(t.Name(), "/")
	return name
}

// sharedPool returns the pool shared by one top-level test (the Go analogue of
// one pool per TS spec file); wasm pools recycle workers after every checkout.
func sharedPool(t testing.TB, b monty.Backend) *monty.Pool {
	t.Helper()
	poolsMu.Lock()
	defer poolsMu.Unlock()
	key := poolKey{backend: b, test: topLevelTest(t)}
	if p, ok := pools[key]; ok {
		return p
	}
	opts := monty.Options{Backend: b, MaxProcesses: 8}
	if b == monty.BackendWasm {
		opts.MaxCheckoutsPerWorker = 1
	}
	p, err := monty.New(context.Background(), opts)
	if err != nil {
		t.Skipf("backend %s unavailable: %v", b, err)
	}
	pools[key] = p
	return p
}

func closeTestPools(test string) {
	poolsMu.Lock()
	defer poolsMu.Unlock()
	for key, p := range pools {
		if key.test == test {
			_ = p.Close(context.Background())
			delete(pools, key)
		}
	}
}

// newPool creates a pool closed at test end.
func newPool(t testing.TB, b monty.Backend, opts monty.Options) *monty.Pool {
	t.Helper()
	opts.Backend = b
	p, err := monty.New(testCtx(t), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// eachBackend runs fn once per test backend as a subtest.
func eachBackend(t *testing.T, fn func(t *testing.T, b monty.Backend)) {
	t.Helper()
	if !strings.Contains(t.Name(), "/") {
		test := t.Name()
		t.Cleanup(func() { closeTestPools(test) })
	}
	for _, b := range testBackends() {
		t.Run(b.String(), func(t *testing.T) { fn(t, b) })
	}
}

// runOptions flattens checkout-level and feed-level options.
type runOptions struct {
	monty.CheckoutOptions
	monty.FeedOptions
}

// newSession checks out a session from the shared pool, closed at test end.
func newSession(t testing.TB, b monty.Backend, opts monty.CheckoutOptions) *monty.Session {
	t.Helper()
	s, err := sharedPool(t, b).Checkout(testCtx(t), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// run executes code in a fresh session.
func run(t testing.TB, b monty.Backend, code string, opts runOptions) (any, error) {
	t.Helper()
	ctx := testCtx(t)
	s, err := sharedPool(t, b).Checkout(ctx, opts.CheckoutOptions)
	if err != nil {
		return nil, err
	}
	defer s.Close(context.Background())
	feed := opts.FeedOptions
	return s.FeedRun(ctx, code, &feed)
}

// mustRun executes code and fails the test on error.
func mustRun(t testing.TB, b monty.Backend, code string, opts runOptions) any {
	t.Helper()
	v, err := run(t, b, code, opts)
	require.NoError(t, err)
	return v
}
