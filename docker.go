package montygo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultDockerStartTimeout = 2 * time.Minute
	defaultDockerStopTimeout  = 10 * time.Second
	dockerHealthInterval      = 100 * time.Millisecond
	dockerServerPort          = "8000"
)

// DockerOptions configure a monty-server container montygo runs through the
// local Docker CLI, and the pool that dials it.
type DockerOptions struct {
	// Image is a repository, or a reference pinned with :tag or @digest;
	// "" uses MONTYGO_DOCKER_IMAGE, then DefaultDockerImage.
	Image string
	// Version is the image tag; "" uses MONTYGO_DOCKER_VERSION, then the tags
	// derived from BindingVersion: the exact version, then its base release.
	Version string
	// Command is the Docker-compatible CLI; "" means docker.
	Command string
	// Env sets monty-server variables and overrides the supervisor's own values.
	Env map[string]string
	// RunArgs are passed to docker run before the image reference.
	RunArgs []string
	// StartTimeout bounds pull, start and health for the first start and each
	// restart: 0 means 2m.
	StartTimeout time.Duration
	// StopTimeout is docker stop's grace period: 0 means 10s.
	StopTimeout time.Duration
	// Reaper removes containers left by earlier processes; nil reaps nothing.
	Reaper OrphanReaper

	MaxProcesses    int
	CheckoutTimeout time.Duration
	RequestTimeout  time.Duration
	// Recovery bounds retries of a dial; RestartServer allows restarting the container.
	Recovery RecoveryPolicy
	// RotationMargin is the lead time of a session rotation: 0 means 30s.
	RotationMargin time.Duration
	Telemetry      *TelemetryComponents
	Stop           StopPolicy
}

func (o DockerOptions) validate() error {
	switch {
	case o.StartTimeout < 0:
		return &OptionError{Message: "startTimeout must not be negative"}
	case o.StopTimeout < 0:
		return &OptionError{Message: "stopTimeout must not be negative"}
	case o.MaxProcesses < 0:
		return &OptionError{Message: "maxProcesses must not be negative"}
	case o.Recovery.Attempts < 0:
		return &OptionError{Message: "recovery.attempts must not be negative"}
	case o.Recovery.AttemptTimeout < 0:
		return &OptionError{Message: "recovery.attemptTimeout must not be negative"}
	case o.RotationMargin < 0:
		return &OptionError{Message: "rotationMargin must not be negative"}
	}
	return nil
}

func (o DockerOptions) startTimeout() time.Duration {
	if o.StartTimeout == 0 {
		return defaultDockerStartTimeout
	}
	return o.StartTimeout
}

func (o DockerOptions) stopTimeout() time.Duration {
	if o.StopTimeout == 0 {
		return defaultDockerStopTimeout
	}
	return o.StopTimeout
}

func (o DockerOptions) maxProcesses() int {
	if o.MaxProcesses == 0 {
		return runtime.NumCPU()
	}
	return o.MaxProcesses
}

// DockerSupervisor runs monty-server in a container on the local Docker daemon.
// It is a ServerSupervisor: NewDocker owns one, and an application MAY pass it
// to NewWebSocket itself.
type DockerSupervisor struct {
	cli    *dockerCLI
	opts   DockerOptions
	image  string
	labels map[string]string
	env    map[string]string

	mu       sync.Mutex
	id       string
	endpoint ServerEndpoint
	info     *ServerInfo
	closed   bool
}

// NewDockerSupervisor resolves the image, starts a container and waits until the
// server is healthy and speaks this parent's protocol version.
func NewDockerSupervisor(ctx context.Context, opts DockerOptions) (*DockerSupervisor, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.startTimeout())
	defer cancel()
	d := &DockerSupervisor{opts: opts}
	d.env = d.serverEnv()
	cli, err := newDockerCLI(opts.Command, d.env)
	if err != nil {
		return nil, err
	}
	d.cli = cli
	reaper := opts.Reaper
	if reaper == nil {
		reaper = noopReaper{}
	}
	if err := reaper.Reap(ctx); err != nil {
		return nil, fmt.Errorf("reap orphaned monty-server containers: %w", err)
	}
	candidates, err := dockerImageCandidates(opts.Image, opts.Version, BindingVersion(), os.Getenv)
	if err != nil {
		return nil, err
	}
	ref, err := resolveDockerImage(ctx, cli, candidates)
	if err != nil {
		return nil, err
	}
	d.image = ref
	d.labels = map[string]string{
		"io.montygo.supervisor": randomHex(16),
		"io.montygo.version":    BindingVersion(),
		"io.montygo.pid":        strconv.Itoa(os.Getpid()),
	}
	if err := d.start(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// NewDocker starts a monty-server container and returns a pool whose sessions
// dial it. The pool owns the supervisor: Close and Shutdown stop the container.
func NewDocker(ctx context.Context, opts DockerOptions) (*Pool, error) {
	sup, err := NewDockerSupervisor(ctx, opts)
	if err != nil {
		return nil, err
	}
	p, err := newWebSocketPool(ctx, WebSocketOptions{
		Supervisor:      sup,
		Recovery:        opts.Recovery,
		RotateSessions:  true,
		RotationMargin:  opts.RotationMargin,
		MaxProcesses:    opts.MaxProcesses,
		CheckoutTimeout: opts.CheckoutTimeout,
		RequestTimeout:  opts.RequestTimeout,
		Telemetry:       opts.Telemetry,
		Stop:            opts.Stop,
	}, BackendDocker, sup.ServerInfo())
	if err != nil {
		_ = sup.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	p.owned = sup
	p.ownedStop = opts.stopTimeout()
	return p, nil
}

// Endpoint reports where sessions dial now.
func (d *DockerSupervisor) Endpoint(ctx context.Context) (ServerEndpoint, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ServerEndpoint{}, ErrSupervisorClosed
	}
	return d.endpoint, nil
}

// Restart replaces the container behind failed. A restart that already happened
// is reported as success, so concurrent callers restart once.
func (d *DockerSupervisor) Restart(ctx context.Context, failed ServerEndpoint) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrSupervisorClosed
	}
	if failed.URL != "" && failed.URL != d.endpoint.URL {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, d.opts.startTimeout())
	defer cancel()
	if d.cli.exists(ctx, d.id) {
		if err := d.cli.restart(ctx, d.id, d.opts.stopTimeout()); err != nil {
			return err
		}
		return d.bind(ctx, d.id)
	}
	return d.startLocked(ctx)
}

// Close stops and removes the container; it is idempotent.
func (d *DockerSupervisor) Close(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.id == "" {
		return nil
	}
	return d.cli.stopAndRemove(ctx, d.id, d.opts.stopTimeout())
}

// Image is the reference the container runs.
func (d *DockerSupervisor) Image() string { return d.image }

// ContainerID is the current container, or "" before the first start.
func (d *DockerSupervisor) ContainerID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.id
}

// ServerInfo is what the server reported at GET /info after its last start.
func (d *DockerSupervisor) ServerInfo() *ServerInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.info
}

func (d *DockerSupervisor) start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.startLocked(ctx)
}

func (d *DockerSupervisor) startLocked(ctx context.Context) error {
	id, err := d.cli.runContainer(ctx, d.runArgs())
	if err != nil {
		return err
	}
	if err := d.bind(ctx, id); err != nil {
		return err
	}
	return nil
}

// bind reads the published port of id and waits until its server is usable.
func (d *DockerSupervisor) bind(ctx context.Context, id string) error {
	port, err := d.cli.hostPort(ctx, id)
	if err != nil {
		d.discard(id)
		return err
	}
	ep := ServerEndpoint{URL: "ws://127.0.0.1:" + port + "/"}
	info, err := d.waitReady(ctx, ep)
	if err != nil {
		logs := d.cli.logsTail(context.WithoutCancel(ctx), id, 20)
		d.discard(id)
		msg := fmt.Sprintf("monty-server container %s did not become usable (DockerSupervisor supports local Docker daemons only): %v", shortContainerID(id), err)
		if logs != "" {
			msg += "\n" + logs
		}
		return errors.New(msg)
	}
	d.id, d.endpoint, d.info = id, ep, info
	return nil
}

func (d *DockerSupervisor) waitReady(ctx context.Context, ep ServerEndpoint) (*ServerInfo, error) {
	opts := WebSocketOptions{URL: ep.URL, RequestTimeout: 2 * time.Second}
	ticker := time.NewTicker(dockerHealthInterval)
	defer ticker.Stop()
	for {
		err := CheckWebSocketHealth(ctx, opts)
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
	info, err := FetchServerInfo(ctx, opts)
	if err != nil {
		return nil, err
	}
	if info.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("monty-server speaks protocol version %d, this montygo speaks %d", info.ProtocolVersion, ProtocolVersion)
	}
	return info, nil
}

// discard removes a container that never became usable.
func (d *DockerSupervisor) discard(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), d.opts.stopTimeout()+5*time.Second)
	defer cancel()
	_, _ = d.cli.run(ctx, "rm", "-f", id)
}

// serverEnv sets the limits a pool of local sessions needs: the caller's
// CheckoutOptions govern memory and duration, sessions never idle out, and the
// per-client quota cannot count this process's connections against itself. The
// session and turn timeouts stay at the image defaults, because rotation relies
// on the turn timeout bounding every execution.
func (d *DockerSupervisor) serverEnv() map[string]string {
	env := map[string]string{
		"MONTY_SERVER_DUMP_KEY":                randomHex(32),
		"MONTY_SERVER_MAX_SESSIONS":            strconv.Itoa(2 * d.opts.maxProcesses()),
		"MONTY_SERVER_MAX_SESSIONS_PER_CLIENT": "0",
		"MONTY_SERVER_IDLE_TIMEOUT":            "0",
		"MONTY_SERVER_MAX_MEMORY_MIB":          "0",
		"MONTY_SERVER_MAX_DURATION":            "0",
	}
	maps.Copy(env, d.opts.Env)
	return env
}

func (d *DockerSupervisor) runArgs() []string {
	args := []string{
		"run", "-d",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(max(512, 32*2*d.opts.maxProcesses())),
		"-p", "127.0.0.1::" + dockerServerPort,
	}
	for _, k := range sortedEnvKeys(d.labels) {
		args = append(args, "--label", k+"="+d.labels[k])
	}
	// Values stay in the CLI's environment, so the dump key never reaches ps.
	for _, k := range sortedEnvKeys(d.env) {
		args = append(args, "-e", k)
	}
	args = append(args, d.opts.RunArgs...)
	return append(args, d.image)
}

// resolveDockerImage returns the first candidate the daemon has or can pull.
func resolveDockerImage(ctx context.Context, cli *dockerCLI, candidates []string) (string, error) {
	failures := make([]string, 0, len(candidates))
	for _, ref := range candidates {
		if cli.imageExists(ctx, ref) {
			return ref, nil
		}
		if err := cli.pull(ctx, ref); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			failures = append(failures, fmt.Sprintf("%s: %v", ref, err))
			continue
		}
		return ref, nil
	}
	return "", fmt.Errorf("no monty-server image available:\n  %s", strings.Join(failures, "\n  "))
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("montygo: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
