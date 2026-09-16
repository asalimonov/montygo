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
	"github.com/asalimonov/montygo/supervisor/docker"
)

const wsURLEnv = "MONTY_TEST_WS_URL"

// testDumpKey signs the dumps of every container the docker backend starts.
const testDumpKey = "montygo-test-dump-key-0123456789"

// backend names a way of reaching workers in MONTY_TEST_BACKENDS.
type backend string

const (
	backendNative    backend = "native"
	backendWasm      backend = "wasm"
	backendWebSocket backend = "websocket"
	backendDocker    backend = "docker"
)

func (b backend) String() string { return string(b) }

type poolKey struct {
	backend backend
	test    string
}

var (
	poolsMu sync.Mutex
	pools   = map[poolKey]*montygo.Pool{}
	// supervisors are the docker supervisors the test pools dial, closed with them.
	supervisors = map[*montygo.Pool]*docker.Supervisor{}
)

// defaultRuntime is the runtime of every session that needs no host extensions.
var defaultRuntime = mustRuntime(montygo.RuntimeOptions{})

// mustRuntime builds a runtime or panics; tests of NewRuntime errors call it directly.
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
		candidate := filepath.Join(filepath.Dir(file), "..", "monty", "target", "debug", "monty")
		if _, err := os.Stat(candidate); err == nil {
			_ = os.Setenv("MONTY_BIN", candidate)
		}
	}
	for _, b := range testBackends() {
		if b == backendWebSocket && os.Getenv(wsURLEnv) == "" {
			fmt.Fprintln(os.Stderr, "MONTY_TEST_BACKENDS names websocket but "+wsURLEnv+" is unset")
			os.Exit(2)
		}
	}
	code := m.Run()
	poolsMu.Lock()
	for _, p := range pools {
		closePool(p)
	}
	poolsMu.Unlock()
	os.Exit(code)
}

// testBackends lists the backends from MONTY_TEST_BACKENDS (default native,wasm,
// plus websocket when MONTY_TEST_WS_URL is set).
func testBackends() []backend {
	spec := os.Getenv("MONTY_TEST_BACKENDS")
	if spec == "" {
		spec = "native,wasm"
		if os.Getenv(wsURLEnv) != "" {
			spec += ",websocket"
		}
	}
	var out []backend
	for _, name := range strings.Split(spec, ",") {
		if b, ok := backendByName(strings.TrimSpace(name)); ok {
			out = append(out, b)
		}
	}
	return out
}

// remoteBackend reports a backend whose workers live behind a WebSocket server:
// single-use sessions, signed dumps and disconnects instead of crashes.
func remoteBackend(b backend) bool {
	return b == backendWebSocket || b == backendDocker
}

// backendByName maps a backend name back to the backend.
func backendByName(name string) (backend, bool) {
	for _, b := range []backend{backendNative, backendWasm, backendWebSocket, backendDocker} {
		if b.String() == name {
			return b, true
		}
	}
	return "", false
}

// openPool builds a pool for b. Remote backends keep RequestTimeout 0 as
// "disabled", the meaning it has for local workers.
func openPool(ctx context.Context, b backend, opts montygo.PoolOptions) (*montygo.Pool, error) {
	switch b {
	case backendNative:
		opts.Workers = montygo.Native(montygo.NativeOptions{})
	case backendWasm:
		opts.Workers = montygo.Wasm(montygo.WasmOptions{})
	case backendWebSocket:
		if opts.RequestTimeout == 0 {
			opts.RequestTimeout = montygo.NoRequestTimeout
		}
		opts.Workers = montygo.Remote(montygo.StaticServer(os.Getenv(wsURLEnv), nil, nil), montygo.RemoteOptions{})
	case backendDocker:
		// One container per pool; a shared dump key lets dumps move between them.
		if opts.RequestTimeout == 0 {
			opts.RequestTimeout = montygo.NoRequestTimeout
		}
		sup, err := docker.New(ctx, docker.Options{
			Env:         map[string]string{"MONTY_SERVER_DUMP_KEY": testDumpKey},
			MaxSessions: 2 * max(opts.MaxWorkers, runtime.NumCPU()),
		})
		if err != nil {
			return nil, err
		}
		opts.Workers = montygo.Remote(sup, montygo.RemoteOptions{RotateSessions: true})
		p, err := montygo.NewPool(ctx, opts)
		if err != nil {
			_ = sup.Close(context.WithoutCancel(ctx))
			return nil, err
		}
		poolsMu.Lock()
		supervisors[p] = sup
		poolsMu.Unlock()
		return p, nil
	}
	return montygo.NewPool(ctx, opts)
}

// closePool closes a test pool and the container it dialed, if any.
func closePool(p *montygo.Pool) {
	ctx := context.Background()
	_ = p.Close(ctx)
	if sup, ok := supervisors[p]; ok {
		delete(supervisors, p)
		_ = sup.Close(ctx)
	}
}

// poolUnavailable skips a local backend that cannot start; a configured
// websocket server that cannot be reached fails the test.
func poolUnavailable(t testing.TB, b backend, err error) {
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
func sharedPool(t testing.TB, b backend) *montygo.Pool {
	t.Helper()
	poolsMu.Lock()
	defer poolsMu.Unlock()
	key := poolKey{backend: b, test: topLevelTest(t)}
	if p, ok := pools[key]; ok {
		return p
	}
	opts := montygo.PoolOptions{MaxWorkers: 8}
	if b == backendWasm {
		opts.MaxCheckoutsPerWorker = 1
	}
	poolsMu.Unlock()
	p, err := openPool(context.Background(), b, opts)
	poolsMu.Lock()
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
			closePool(p)
			delete(pools, key)
		}
	}
}

// newPool creates a pool closed at test end.
func newPool(t testing.TB, b backend, opts montygo.PoolOptions) *montygo.Pool {
	t.Helper()
	p, err := openPool(testCtx(t), b, opts)
	require.NoError(t, err)
	t.Cleanup(func() {
		poolsMu.Lock()
		defer poolsMu.Unlock()
		closePool(p)
	})
	return p
}

// eachBackend runs fn once per test backend as a subtest.
func eachBackend(t *testing.T, fn func(t *testing.T, b backend)) {
	t.Helper()
	if !strings.Contains(t.Name(), "/") {
		test := t.Name()
		t.Cleanup(func() { closeTestPools(test) })
	}
	for _, b := range testBackends() {
		t.Run(b.String(), func(t *testing.T) { fn(t, b) })
	}
}

// runOptions flatten the runtime, checkout-level and feed-level options of one run.
type runOptions struct {
	// Runtime is the session's runtime; nil means defaultRuntime.
	Runtime *montygo.Runtime
	montygo.CheckoutOptions
	montygo.FeedOptions
}

func (o runOptions) runtime() *montygo.Runtime {
	if o.Runtime != nil {
		return o.Runtime
	}
	return defaultRuntime
}

// newSession checks out a session of defaultRuntime from the shared pool, closed at test end.
func newSession(t testing.TB, b backend, opts montygo.CheckoutOptions) *montygo.Session {
	t.Helper()
	return newSessionRT(t, b, defaultRuntime, opts)
}

// newSessionRT checks out a session of rt from the shared pool, closed at test end.
func newSessionRT(t testing.TB, b backend, rt *montygo.Runtime, opts montygo.CheckoutOptions) *montygo.Session {
	t.Helper()
	s, err := sharedPool(t, b).Checkout(testCtx(t), rt, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// run executes code in a fresh session.
func run(t testing.TB, b backend, code string, opts runOptions) (any, error) {
	t.Helper()
	ctx := testCtx(t)
	s, err := sharedPool(t, b).Checkout(ctx, opts.runtime(), opts.CheckoutOptions)
	if err != nil {
		return nil, err
	}
	defer s.Close(context.Background())
	feed := opts.FeedOptions
	return s.FeedRun(ctx, code, &feed)
}

// mustRun executes code and fails the test on error.
func mustRun(t testing.TB, b backend, code string, opts runOptions) any {
	t.Helper()
	v, err := run(t, b, code, opts)
	require.NoError(t, err)
	return v
}
