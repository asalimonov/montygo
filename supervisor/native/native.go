// Package native runs monty-server as a child process of the host and serves
// its endpoint to a pool. It is the supervisor for a machine that has the
// server binary but no container runtime.
package native

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
)

const (
	defaultStartTimeout = 30 * time.Second
	defaultStopTimeout  = 10 * time.Second
	healthInterval      = 50 * time.Millisecond

	// BinaryEnv names the variable that locates the server binary.
	BinaryEnv = "MONTYGO_SERVER_BIN"
	// DefaultBinary is the command looked up on PATH when nothing else resolves.
	DefaultBinary = "monty-server"
)

// Options configure the monty-server process.
type Options struct {
	// Binary is the monty-server executable; "" uses MONTYGO_SERVER_BIN, then
	// monty-server on PATH.
	Binary string
	// Env sets monty-server variables and overrides the supervisor's own values.
	// MONTY_BIN, which points the server at its worker, is inherited from the
	// process environment unless it is set here.
	Env map[string]string
	// Args are passed after the supervisor's own flags.
	Args []string
	// Stderr receives the server's diagnostics; nil discards them.
	Stderr io.Writer
	// StartTimeout bounds start and readiness, for the first start and each
	// restart: 0 means 30s.
	StartTimeout time.Duration
	// StopTimeout is how long a drain may take before the process is killed:
	// 0 means 10s.
	StopTimeout time.Duration
	// MaxSessions sizes the server: 0 means 2 × runtime.NumCPU(). Pass twice
	// the PoolOptions.MaxWorkers of the pools that dial it.
	MaxSessions int
}

func (o Options) validate() error {
	switch {
	case o.StartTimeout < 0:
		return &monterr.OptionError{Message: "startTimeout must not be negative"}
	case o.StopTimeout < 0:
		return &monterr.OptionError{Message: "stopTimeout must not be negative"}
	case o.MaxSessions < 0:
		return &monterr.OptionError{Message: "maxSessions must not be negative"}
	}
	return nil
}

func (o Options) startTimeout() time.Duration {
	if o.StartTimeout == 0 {
		return defaultStartTimeout
	}
	return o.StartTimeout
}

func (o Options) stopTimeout() time.Duration {
	if o.StopTimeout == 0 {
		return defaultStopTimeout
	}
	return o.StopTimeout
}

func (o Options) maxSessions() int {
	if o.MaxSessions == 0 {
		return 2 * runtime.NumCPU()
	}
	return o.MaxSessions
}

// Supervisor runs one monty-server process and reports where it listens. It
// is a montygo.ServerSupervisor for montygo.Remote; the application closes it
// after the pools that dial it.
type Supervisor struct {
	path string
	opts Options
	env  map[string]string

	mu       sync.Mutex
	cmd      *exec.Cmd
	endpoint montygo.ServerEndpoint
	info     *montygo.ServerInfo
	closed   bool
}

// New starts a monty-server process and waits until it is usable.
func New(ctx context.Context, opts Options) (*Supervisor, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	path, err := resolveBinary(opts.Binary)
	if err != nil {
		return nil, err
	}
	s := &Supervisor{path: path, opts: opts}
	s.env = s.serverEnv()
	ctx, cancel := context.WithTimeout(ctx, opts.startTimeout())
	defer cancel()
	if err := s.start(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// resolveBinary locates the server: the explicit path, MONTYGO_SERVER_BIN, then PATH.
func resolveBinary(explicit string) (string, error) {
	candidates := []string{explicit, os.Getenv(BinaryEnv)}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return c, nil
		}
		return "", &monterr.OptionError{Message: "monty-server not found at " + c}
	}
	path, err := exec.LookPath(DefaultBinary)
	if err != nil {
		return "", &monterr.OptionError{Message: fmt.Sprintf(
			"could not locate %s (tried Options.Binary, %s, PATH); build it with `cargo build -p monty-server` or set %s",
			DefaultBinary, BinaryEnv, BinaryEnv)}
	}
	return path, nil
}

// Endpoint reports where sessions dial now.
func (s *Supervisor) Endpoint(context.Context) (montygo.ServerEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return montygo.ServerEndpoint{}, monterr.ErrSupervisorClosed
	}
	return s.endpoint, nil
}

// Restart replaces the process behind failed. A restart that already happened
// is reported as success, so concurrent callers restart once.
func (s *Supervisor) Restart(ctx context.Context, failed montygo.ServerEndpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return monterr.ErrSupervisorClosed
	}
	if failed.URL != "" && failed.URL != s.endpoint.URL {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.startTimeout())
	defer cancel()
	s.stopLocked()
	return s.startLocked(ctx)
}

// Close stops the process; it is idempotent. The server drains on SIGTERM, so a
// session that is mid-turn ends with a shutdown dump when it asks for one.
func (s *Supervisor) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.stopLocked()
	return nil
}

// ServerInfo is what the server reported at GET /info after its last start.
func (s *Supervisor) ServerInfo() *montygo.ServerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// PID is the server process id while it runs.
func (s *Supervisor) PID() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return 0, false
	}
	return s.cmd.Process.Pid, true
}

func (s *Supervisor) start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked(ctx)
}

// startLocked spawns the server and waits for the line it prints once bound.
func (s *Supervisor) startLocked(ctx context.Context) error {
	args := append([]string{"--host", "127.0.0.1", "--port", "0"}, s.opts.Args...)
	cmd := exec.Command(s.path, args...)
	cmd.Env = append(os.Environ(), envList(s.env)...)
	if s.opts.Stderr != nil {
		cmd.Stderr = s.opts.Stderr
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	s.cmd = cmd
	url, err := readBoundURL(ctx, stdout)
	if err != nil {
		s.stopLocked()
		return fmt.Errorf("monty-server did not report a bound address: %w", err)
	}
	ep := montygo.ServerEndpoint{URL: url}
	info, err := waitReady(ctx, ep)
	if err != nil {
		s.stopLocked()
		return fmt.Errorf("monty-server at %s did not become usable: %w", url, err)
	}
	s.endpoint, s.info = ep, info
	return nil
}

// readBoundURL reads the single line the server prints once it is listening.
func readBoundURL(ctx context.Context, stdout io.ReadCloser) (string, error) {
	lines := make(chan string, 1)
	fail := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			lines <- strings.TrimSpace(scanner.Text())
			// Drain the rest so the process never blocks on a full pipe.
			for scanner.Scan() {
			}
			return
		}
		if err := scanner.Err(); err != nil {
			fail <- err
			return
		}
		fail <- io.EOF
	}()
	select {
	case line := <-lines:
		if !strings.HasPrefix(line, "ws://") {
			return "", fmt.Errorf("unexpected first line %q", line)
		}
		return line, nil
	case err := <-fail:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// waitReady polls /health and then checks that the server speaks this protocol.
func waitReady(ctx context.Context, ep montygo.ServerEndpoint) (*montygo.ServerInfo, error) {
	sup := montygo.StaticServer(ep.URL, nil, nil)
	opts := montygo.RemoteOptions{DialTimeout: 2 * time.Second}
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for {
		err := montygo.CheckServerHealth(ctx, sup, opts)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w (last health check: %v)", ctx.Err(), err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (last health check: %v)", ctx.Err(), err)
		case <-ticker.C:
		}
	}
	info, err := montygo.FetchServerInfo(ctx, sup, opts)
	if err != nil {
		return nil, err
	}
	if info.ProtocolVersion != montygo.ProtocolVersion {
		return nil, fmt.Errorf("monty-server speaks protocol version %d, this montygo speaks %d",
			info.ProtocolVersion, montygo.ProtocolVersion)
	}
	return info, nil
}

// stopLocked drains the server, then kills it if it outstays the grace period.
func (s *Supervisor) stopLocked() {
	cmd := s.cmd
	s.cmd = nil
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.opts.stopTimeout()):
		_ = cmd.Process.Kill()
		<-done
	}
}

// serverEnv sets the limits local pools need, mirroring the Docker supervisor:
// the runtime's limits govern memory and duration, sessions never idle out, and
// the per-client quota cannot count this process against itself. The session
// and turn timeouts stay at their defaults, because rotation relies on the turn
// timeout bounding every execution.
func (s *Supervisor) serverEnv() map[string]string {
	env := map[string]string{
		"MONTY_SERVER_DUMP_KEY":                randomHex(32),
		"MONTY_SERVER_MAX_SESSIONS":            strconv.Itoa(s.opts.maxSessions()),
		"MONTY_SERVER_MAX_SESSIONS_PER_CLIENT": "0",
		"MONTY_SERVER_IDLE_TIMEOUT":            "0",
		"MONTY_SERVER_MAX_MEMORY_MIB":          "0",
		"MONTY_SERVER_MAX_DURATION":            "0",
	}
	maps.Copy(env, s.opts.Env)
	return env
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("montygo: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf)
}
