package network

import (
	"context"
	"crypto/tls"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/telemetry"
)

type setupOptions struct {
	cfg ServerConfig
}

type SetupOption func(*setupOptions)

// WithArgs appends monty-server flags.
func WithArgs(args ...string) SetupOption {
	return func(o *setupOptions) { o.cfg.Args = append(o.cfg.Args, args...) }
}

// WithEnv sets a container environment variable.
func WithEnv(key, value string) SetupOption {
	return func(o *setupOptions) {
		if o.cfg.Env == nil {
			o.cfg.Env = map[string]string{}
		}
		o.cfg.Env[key] = value
	}
}

// TestServer is a unit lent to one test.
type TestServer struct {
	Unit  *Unit
	t     *testing.T
	pool  *ContainerPool
	dirty bool
}

func testCtx(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func requireSlowTests(t *testing.T) {
	t.Helper()
	switch os.Getenv(EnvSlowTests) {
	case "", "0", "false":
		t.Skip("slow network tests disabled (set " + EnvSlowTests + "=1 to run)")
	}
}

func buildConfig(opts []SetupOption) ServerConfig {
	o := setupOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	return o.cfg
}

// SetupServer acquires a unit running the given options and releases it at test end.
func SetupServer(t *testing.T, opts ...SetupOption) *TestServer {
	t.Helper()
	cfg := buildConfig(opts)
	pool := GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	u, err := pool.Acquire(ctx, cfg)
	s := &TestServer{Unit: u, t: t, pool: pool, dirty: !cfg.isDefault()}
	if u != nil {
		t.Cleanup(func() {
			if t.Failed() && u.logs != nil {
				t.Logf("server log: %s", u.logs.Path())
			}
			pool.Release(t, u, s.dirty)
		})
	}
	require.NoError(t, err, "acquire unit")
	if u.logs != nil {
		u.logs.Rename(t.Name(), u.ID)
	}
	return s
}

func (s *TestServer) URL() string { return s.Unit.URL() }

// wsOptions describe a pool of one server URL: the StaticServer behind it, the
// RemoteOptions of its dials and the PoolOptions around them.
type wsOptions struct {
	URL            string
	ConnectHeaders func(ctx context.Context) (map[string]string, error)
	TLSConfig      *tls.Config
	DialContext    func(ctx context.Context, network, addr string) (net.Conn, error)
	RequestTimeout time.Duration
	MaxWorkers     int
	Telemetry      *telemetry.Components
}

func (o wsOptions) server() montygo.ServerSupervisor {
	return montygo.StaticServer(o.URL, o.TLSConfig, o.ConnectHeaders)
}

func (o wsOptions) remote() montygo.RemoteOptions {
	return montygo.RemoteOptions{DialContext: o.DialContext}
}

func (o wsOptions) pool() montygo.PoolOptions {
	return montygo.PoolOptions{
		Workers:        montygo.Remote(o.server(), o.remote()),
		MaxWorkers:     o.MaxWorkers,
		RequestTimeout: o.RequestTimeout,
		Telemetry:      o.Telemetry,
	}
}

// checkHealth probes GET /health of the server behind opts.
func checkHealth(ctx context.Context, opts wsOptions) error {
	return montygo.CheckServerHealth(ctx, opts.server(), opts.remote())
}

// fetchInfo reads GET /info of the server behind opts.
func fetchInfo(ctx context.Context, opts wsOptions) (*montygo.ServerInfo, error) {
	return montygo.FetchServerInfo(ctx, opts.server(), opts.remote())
}

// defaultRuntime is the runtime of every session without host extensions.
var defaultRuntime = mustRuntime(montygo.RuntimeOptions{})

func mustRuntime(opts montygo.RuntimeOptions) *montygo.Runtime {
	rt, err := montygo.NewRuntime(opts)
	if err != nil {
		panic(err)
	}
	return rt
}

// WSOptions returns client options for this server with a 30s request timeout.
func (s *TestServer) WSOptions() wsOptions {
	return wsOptions{URL: s.URL(), RequestTimeout: 30 * time.Second}
}

// NewPool builds a pool of remote workers on this server, closed at test end.
func (s *TestServer) NewPool(opts wsOptions) *montygo.Pool {
	s.t.Helper()
	if opts.URL == "" {
		opts.URL = s.URL()
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 30 * time.Second
	}
	p, err := montygo.NewPool(testCtx(s.t), opts.pool())
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// Checkout checks out a session of defaultRuntime, closed at test end.
func (s *TestServer) Checkout(ctx context.Context, p *montygo.Pool, opts montygo.CheckoutOptions) *montygo.Session {
	s.t.Helper()
	return s.CheckoutRT(ctx, p, defaultRuntime, opts)
}

// CheckoutRT checks out a session of rt, closed at test end.
func (s *TestServer) CheckoutRT(ctx context.Context, p *montygo.Pool, rt *montygo.Runtime, opts montygo.CheckoutOptions) *montygo.Session {
	s.t.Helper()
	session, err := p.Checkout(ctx, rt, opts)
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session
}

func (s *TestServer) Metrics(ctx context.Context) (Metrics, error) {
	return scrapeMetrics(ctx, s.Unit.HTTPBase())
}

// WaitMetric polls /metrics until the series reaches want.
func (s *TestServer) WaitMetric(name string, labels map[string]string, want float64, timeout time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	var got float64
	for {
		m, err := s.Metrics(context.Background())
		if err == nil {
			got = m.Get(name, labels)
			if got == want {
				return
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("metric %s: got %v, want %v (last error %v)", metricKey(name, labels), got, want, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Baseline snapshots the unit's metrics; units are reused, so counters MUST be compared as deltas.
func (s *TestServer) Baseline() Metrics {
	s.t.Helper()
	m, err := s.Metrics(context.Background())
	require.NoError(s.t, err)
	return m
}

// WaitMetricDelta polls /metrics until the series has grown by delta since base.
func (s *TestServer) WaitMetricDelta(base Metrics, name string, labels map[string]string, delta float64, timeout time.Duration) {
	s.t.Helper()
	s.WaitMetric(name, labels, base.Get(name, labels)+delta, timeout)
}

// Signal sends a signal to the server process; the unit is recreated after the test.
func (s *TestServer) Signal(signal string) {
	s.t.Helper()
	s.dirty = true
	require.NoError(s.t, dockerKill(context.Background(), s.Unit.ContainerID(), signal))
}

// WaitExited waits for the container to stop and returns its exit code.
func (s *TestServer) WaitExited(timeout time.Duration) int {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		state, err := s.Unit.container.State(context.Background())
		if err == nil && !state.Running {
			return state.ExitCode
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("container still running after %s (last error %v)", timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Recreate replaces the container, keeping the current configuration unless options are given.
func (s *TestServer) Recreate(opts ...SetupOption) {
	s.t.Helper()
	s.dirty = true
	cfg := s.Unit.cfg
	if len(opts) > 0 {
		cfg = buildConfig(opts)
	} else {
		cfg.Args = append([]string(nil), cfg.Args...)
		cfg.Env = maps.Clone(cfg.Env)
	}
	require.NoError(s.t, s.pool.Recreate(context.Background(), s.Unit, cfg))
	if s.Unit.logs != nil {
		s.Unit.logs.Rename(s.t.Name()+"-recreated", s.Unit.ID)
	}
}

// Logs returns the container log collected so far.
func (s *TestServer) Logs() string {
	rc, err := s.Unit.container.Logs(context.Background())
	if err != nil {
		return err.Error()
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	return string(b)
}

// RawDial opens a WebSocket without the montygo client.
func (s *TestServer) RawDial(ctx context.Context, header http.Header) (*websocket.Conn, *http.Response, error) {
	conn, resp, err := websocket.Dial(ctx, s.URL(), &websocket.DialOptions{HTTPHeader: header})
	if conn != nil {
		conn.SetReadLimit(-1)
	}
	return conn, resp, err
}

// Get performs a GET against the server's HTTP endpoints.
func (s *TestServer) Get(ctx context.Context, path string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Unit.HTTPBase()+path, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}
