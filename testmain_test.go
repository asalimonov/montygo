package montygo_test

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

	"github.com/asalimonov/montygo"
)

const wsURLEnv = "MONTY_TEST_WS_URL"

// testDumpKey signs the dumps of every container the docker backend starts.
const testDumpKey = "montygo-test-dump-key-0123456789"

type poolKey struct {
	backend montygo.Backend
	test    string
}

var (
	poolsMu sync.Mutex
	pools   = map[poolKey]*montygo.Pool{}
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
		if b == montygo.BackendWebSocket && os.Getenv(wsURLEnv) == "" {
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
func testBackends() []montygo.Backend {
	spec := os.Getenv("MONTY_TEST_BACKENDS")
	if spec == "" {
		spec = "native,wasm"
		if os.Getenv(wsURLEnv) != "" {
			spec += ",websocket"
		}
	}
	var out []montygo.Backend
	for _, name := range strings.Split(spec, ",") {
		if b, ok := backendByName(strings.TrimSpace(name)); ok {
			out = append(out, b)
		}
	}
	return out
}

// remoteBackend reports a backend whose workers live behind a WebSocket server:
// single-use sessions, signed dumps and disconnects instead of crashes.
func remoteBackend(b montygo.Backend) bool {
	return b == montygo.BackendWebSocket || b == montygo.BackendDocker
}

// backendByName maps a Backend.String() value back to the Backend.
func backendByName(name string) (montygo.Backend, bool) {
	for _, b := range []montygo.Backend{montygo.BackendNative, montygo.BackendWasm, montygo.BackendWebSocket, montygo.BackendDocker} {
		if b.String() == name {
			return b, true
		}
	}
	return 0, false
}

// openPool builds a pool for b; websocket maps Options onto WebSocketOptions,
// where RequestTimeout 0 keeps its "disabled" meaning.
func openPool(ctx context.Context, b montygo.Backend, opts montygo.Options) (*montygo.Pool, error) {
	if !remoteBackend(b) {
		opts.Backend = b
		return montygo.New(ctx, opts)
	}
	timeout := opts.RequestTimeout
	if timeout == 0 {
		timeout = montygo.NoRequestTimeout
	}
	if b == montygo.BackendDocker {
		// One container per pool; a shared dump key lets dumps move between them.
		return montygo.NewDocker(ctx, montygo.DockerOptions{
			MaxProcesses:    opts.MaxProcesses,
			CheckoutTimeout: opts.CheckoutTimeout,
			RequestTimeout:  timeout,
			Telemetry:       opts.Telemetry,
			Stop:            opts.Stop,
			Env:             map[string]string{"MONTY_SERVER_DUMP_KEY": testDumpKey},
		})
	}
	return montygo.NewWebSocket(ctx, montygo.WebSocketOptions{
		URL:             os.Getenv(wsURLEnv),
		MaxProcesses:    opts.MaxProcesses,
		CheckoutTimeout: opts.CheckoutTimeout,
		RequestTimeout:  timeout,
		Telemetry:       opts.Telemetry,
		Stop:            opts.Stop,
	})
}

// poolUnavailable skips a local backend that cannot start; a configured
// websocket server that cannot be reached fails the test.
func poolUnavailable(t testing.TB, b montygo.Backend, err error) {
	t.Helper()
	if remoteBackend(b) {
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
func sharedPool(t testing.TB, b montygo.Backend) *montygo.Pool {
	t.Helper()
	poolsMu.Lock()
	defer poolsMu.Unlock()
	key := poolKey{backend: b, test: topLevelTest(t)}
	if p, ok := pools[key]; ok {
		return p
	}
	opts := montygo.Options{MaxProcesses: 8}
	if b == montygo.BackendWasm {
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
func newPool(t testing.TB, b montygo.Backend, opts montygo.Options) *montygo.Pool {
	t.Helper()
	p, err := openPool(testCtx(t), b, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// eachBackend runs fn once per test backend as a subtest.
func eachBackend(t *testing.T, fn func(t *testing.T, b montygo.Backend)) {
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
	montygo.CheckoutOptions
	montygo.FeedOptions
}

// newSession checks out a session from the shared pool, closed at test end.
func newSession(t testing.TB, b montygo.Backend, opts montygo.CheckoutOptions) *montygo.Session {
	t.Helper()
	s, err := sharedPool(t, b).Checkout(testCtx(t), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// run executes code in a fresh session.
func run(t testing.TB, b montygo.Backend, code string, opts runOptions) (any, error) {
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
func mustRun(t testing.TB, b montygo.Backend, code string, opts runOptions) any {
	t.Helper()
	v, err := run(t, b, code, opts)
	require.NoError(t, err)
	return v
}
