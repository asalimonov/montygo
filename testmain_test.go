package monty_test

import (
	"context"
	"fmt"
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

const wsURLEnv = "MONTY_TEST_WS_URL"

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
	for _, b := range testBackends() {
		if b == monty.BackendWebSocket && os.Getenv(wsURLEnv) == "" {
			fmt.Fprintln(os.Stderr, "MONTY_TEST_BACKENDS names websocket but "+wsURLEnv+" is unset")
			os.Exit(2)
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

// testBackends lists the backends from MONTY_TEST_BACKENDS (default native,wasm,
// plus websocket when MONTY_TEST_WS_URL is set).
func testBackends() []monty.Backend {
	spec := os.Getenv("MONTY_TEST_BACKENDS")
	if spec == "" {
		spec = "native,wasm"
		if os.Getenv(wsURLEnv) != "" {
			spec += ",websocket"
		}
	}
	var out []monty.Backend
	for _, name := range strings.Split(spec, ",") {
		if b, ok := backendByName(strings.TrimSpace(name)); ok {
			out = append(out, b)
		}
	}
	return out
}

// backendByName maps a Backend.String() value back to the Backend.
func backendByName(name string) (monty.Backend, bool) {
	for _, b := range []monty.Backend{monty.BackendNative, monty.BackendWasm, monty.BackendWebSocket} {
		if b.String() == name {
			return b, true
		}
	}
	return 0, false
}

// openPool builds a pool for b; websocket maps Options onto WebSocketOptions,
// where RequestTimeout 0 keeps its "disabled" meaning.
func openPool(ctx context.Context, b monty.Backend, opts monty.Options) (*monty.Pool, error) {
	if b != monty.BackendWebSocket {
		opts.Backend = b
		return monty.New(ctx, opts)
	}
	timeout := opts.RequestTimeout
	if timeout == 0 {
		timeout = monty.NoRequestTimeout
	}
	return monty.NewWebSocket(ctx, monty.WebSocketOptions{
		URL:             os.Getenv(wsURLEnv),
		MaxProcesses:    opts.MaxProcesses,
		CheckoutTimeout: opts.CheckoutTimeout,
		RequestTimeout:  timeout,
	})
}

// poolUnavailable skips a local backend that cannot start; a configured
// websocket server that cannot be reached fails the test.
func poolUnavailable(t testing.TB, b monty.Backend, err error) {
	t.Helper()
	if b == monty.BackendWebSocket {
		t.Fatalf("backend %s unavailable: %v", b, err)
	}
	t.Skipf("backend %s unavailable: %v", b, err)
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
	opts := monty.Options{MaxProcesses: 8}
	if b == monty.BackendWasm {
		opts.MaxCheckoutsPerWorker = 1
	}
	p, err := openPool(context.Background(), b, opts)
	if err != nil {
		poolUnavailable(t, b, err)
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
	p, err := openPool(testCtx(t), b, opts)
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
