package network

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	DefaultParallelism   = 4
	EnvNetworkTests      = "MONTYGO_NETWORK_TESTS"
	EnvTestParallel      = "MONTYGO_TEST_PARALLEL"
	EnvTestImage         = "MONTYGO_TEST_IMAGE"
	EnvPyClientImage     = "MONTYGO_PYCLIENT_IMAGE"
	EnvDumpContainerLogs = "MONTYGO_DUMP_CONTAINER_LOGS"
	EnvSlowTests         = "MONTYGO_SLOW_TESTS_ENABLE"
	DefaultImage         = "monty-server:latest"
	DefaultPyClientImage = "monty-pyclient:latest"
	TestDumpKey          = "montygo-network-test-dump-key"
	TestDumpKeyRotated   = "montygo-network-test-dump-key-2"
	serverPort           = "8000/tcp"
	testLabelKey         = "montygo.test"
	testLabelValue       = "network"
)

type unitState int32

const (
	unitCold unitState = iota
	unitReady
	unitInUse
	unitReleasing
	unitDead
)

type unitCmdKind int

const (
	cmdStart unitCmdKind = iota
	cmdRelease
	cmdShutdown
)

type unitCmd struct {
	kind unitCmdKind
	t    *testing.T
	// recreate forces a fresh default container before the unit returns to the queue.
	recreate bool
}

// ServerConfig is the container's argument and environment overlay on top of the defaults.
type ServerConfig struct {
	Args []string
	Env  map[string]string
}

func (c ServerConfig) isDefault() bool {
	return len(c.Args) == 0 && len(c.Env) == 0
}

func (c ServerConfig) equal(o ServerConfig) bool {
	return slices.Equal(c.Args, o.Args) && maps.Equal(c.Env, o.Env)
}

func defaultEnv() map[string]string {
	return map[string]string{
		"MONTY_SERVER_DUMP_KEY":                TestDumpKey,
		"MONTY_SERVER_MAX_SESSIONS_PER_CLIENT": "0",
	}
}

// Unit is one lendable monty-server container.
type Unit struct {
	ID        int
	state     atomic.Int32
	cmds      chan unitCmd
	done      chan struct{}
	container testcontainers.Container
	cfg       ServerConfig
	host      string
	port      string
	logs      *fileLogConsumer
}

func (u *Unit) URL() string      { return "ws://" + u.host + ":" + u.port + "/" }
func (u *Unit) HTTPBase() string { return "http://" + u.host + ":" + u.port }

func (u *Unit) ContainerID() string { return u.container.GetContainerID() }

func (u *Unit) ContainerIP(ctx context.Context) (string, error) {
	return u.container.ContainerIP(ctx)
}

func (u *Unit) setState(s unitState) { u.state.Store(int32(s)) }
func (u *Unit) getState() unitState  { return unitState(u.state.Load()) }

func (u *Unit) tryTransition(from, to unitState) bool {
	return u.state.CompareAndSwap(int32(from), int32(to))
}

// ContainerPool lends monty-server containers to tests; tests queue for a free unit.
type ContainerPool struct {
	units      []*Unit
	available  chan *Unit
	size       int
	image      string
	mu         sync.Mutex
	started    bool
	firstReady sync.Once
	readyCh    chan struct{}
	allDead    sync.Once
	deadCh     chan struct{}
	deadCount  atomic.Int32
	firstErr   atomic.Pointer[error]
	startDone  atomic.Bool
	stopping   atomic.Bool
}

var (
	poolOnce     sync.Once
	poolInstance *ContainerPool
)

// GetPool returns the singleton pool sized by MONTYGO_TEST_PARALLEL.
func GetPool() *ContainerPool {
	poolOnce.Do(func() {
		size := DefaultParallelism
		if v, err := strconv.Atoi(os.Getenv(EnvTestParallel)); err == nil && v > 0 {
			size = v
		}
		image := os.Getenv(EnvTestImage)
		if image == "" {
			image = DefaultImage
		}
		poolInstance = &ContainerPool{size: size, image: image}
	})
	return poolInstance
}

func (p *ContainerPool) Size() int     { return p.size }
func (p *ContainerPool) Image() string { return p.image }

// Start creates every unit and returns when the first is ready or all have failed.
func (p *ContainerPool) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}
	p.units = make([]*Unit, p.size)
	p.available = make(chan *Unit, p.size)
	p.readyCh = make(chan struct{})
	p.deadCh = make(chan struct{})
	for i := range p.size {
		u := &Unit{ID: i, cmds: make(chan unitCmd, 1), done: make(chan struct{})}
		p.units[i] = u
		go u.lifecycle(p)
		u.cmds <- unitCmd{kind: cmdStart}
	}
	select {
	case <-p.readyCh:
		p.startDone.Store(true)
		p.started = true
		return nil
	case <-p.deadCh:
		p.startDone.Store(true)
		for _, u := range p.units {
			select {
			case u.cmds <- unitCmd{kind: cmdShutdown}:
			default:
			}
		}
		for _, u := range p.units {
			<-u.done
		}
		if e := p.firstErr.Load(); e != nil {
			return *e
		}
		return fmt.Errorf("all %d units failed to start", p.size)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop shuts every unit down and waits for their lifecycle goroutines.
func (p *ContainerPool) Stop(context.Context) error {
	p.stopping.Store(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	for _, u := range p.units {
		select {
		case u.cmds <- unitCmd{kind: cmdShutdown}:
		default:
			select {
			case <-u.cmds:
			default:
			}
			u.cmds <- unitCmd{kind: cmdShutdown}
		}
	}
	for _, u := range p.units {
		<-u.done
	}
	p.started = false
	return nil
}

// Acquire blocks until a unit is free and makes it run cfg.
func (p *ContainerPool) Acquire(ctx context.Context, cfg ServerConfig) (*Unit, error) {
	select {
	case u := <-p.available:
		if !u.tryTransition(unitReady, unitInUse) {
			panic(fmt.Sprintf("unit %d in state %d at Acquire", u.ID, u.getState()))
		}
		if !cfg.equal(u.cfg) {
			if err := p.Recreate(ctx, u, cfg); err != nil {
				return u, fmt.Errorf("recreate unit %d: %w", u.ID, err)
			}
		}
		return u, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Release hands a unit back; a dirty or reconfigured unit is recreated before reuse.
func (p *ContainerPool) Release(t *testing.T, u *Unit, dirty bool) {
	if !u.tryTransition(unitInUse, unitReleasing) {
		log.Printf("WARN: Release on unit %d not in use (state=%d)", u.ID, u.getState())
		return
	}
	u.cmds <- unitCmd{kind: cmdRelease, t: t, recreate: dirty || !u.cfg.isDefault()}
}

// Recreate replaces the unit's container with a fresh one running cfg.
func (p *ContainerPool) Recreate(ctx context.Context, u *Unit, cfg ServerConfig) error {
	_ = p.stopContainer(ctx, u)
	return p.startWithRetry(ctx, u, cfg)
}

func (u *Unit) lifecycle(p *ContainerPool) {
	defer close(u.done)
	for cmd := range u.cmds {
		switch cmd.kind {
		case cmdStart:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err := p.startWithRetry(ctx, u, ServerConfig{})
			cancel()
			if err != nil {
				u.setState(unitDead)
				log.Printf("ERROR: unit %d failed to start: %v", u.ID, err)
				p.signalDead(err)
				continue
			}
			u.setState(unitReady)
			p.available <- u
			p.signalReady()

		case cmdRelease:
			if p.stopping.Load() {
				u.setState(unitDead)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			recreate := cmd.recreate
			if !recreate {
				if err := p.healthyAndIdle(ctx, u); err != nil {
					log.Printf("unit %d not idle after test: %v; recreating", u.ID, err)
					recreate = true
				}
			}
			var err error
			if recreate {
				err = p.Recreate(ctx, u, ServerConfig{})
			}
			cancel()
			if err != nil {
				u.setState(unitDead)
				log.Printf("ERROR: unit %d could not be recreated: %v", u.ID, err)
				bestEffortErrorf(cmd.t, "pool.Release: unit %d recreate failed: %v", u.ID, err)
				p.warnIfNoLiveUnits()
				continue
			}
			if !u.tryTransition(unitReleasing, unitReady) {
				panic(fmt.Sprintf("unit %d release in state %d", u.ID, u.getState()))
			}
			p.available <- u

		case cmdShutdown:
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			_ = p.stopContainer(ctx, u)
			cancel()
			u.setState(unitDead)
			return
		}
	}
}

func bestEffortErrorf(t *testing.T, format string, args ...any) {
	if t == nil {
		return
	}
	defer func() { _ = recover() }()
	t.Errorf(format, args...)
}

func (p *ContainerPool) warnIfNoLiveUnits() {
	for _, u := range p.units {
		if u.getState() != unitDead {
			return
		}
	}
	log.Printf("ERROR: pool has no live units")
}

func (p *ContainerPool) signalReady() {
	if p.startDone.Load() {
		return
	}
	p.firstReady.Do(func() { close(p.readyCh) })
}

func (p *ContainerPool) signalDead(err error) {
	if p.startDone.Load() {
		return
	}
	p.firstErr.CompareAndSwap(nil, &err)
	if isInfraFailure(err) || p.deadCount.Add(1) >= int32(p.size) {
		p.allDead.Do(func() { close(p.deadCh) })
	}
}

var infraFailurePatterns = []string{
	"cannot connect to the docker daemon",
	"no space left on device",
	"no such image",
	"pull access denied",
	"error response from daemon: conflict",
}

func isInfraFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, pattern := range infraFailurePatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

func (p *ContainerPool) startWithRetry(ctx context.Context, u *Unit, cfg ServerConfig) error {
	backoff := []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}
	var last error
	for attempt := range 3 {
		if last = p.startContainer(ctx, u, cfg); last == nil {
			return nil
		}
		if isInfraFailure(last) || attempt == 2 {
			break
		}
		_ = p.stopContainer(ctx, u)
		time.Sleep(backoff[attempt])
	}
	return fmt.Errorf("start unit %d: %w", u.ID, last)
}

func (p *ContainerPool) startContainer(ctx context.Context, u *Unit, cfg ServerConfig) error {
	env := defaultEnv()
	maps.Copy(env, cfg.Env)
	req := testcontainers.ContainerRequest{
		Image:        p.image,
		ExposedPorts: []string{serverPort},
		Env:          env,
		Cmd:          append([]string{"--host", "0.0.0.0"}, cfg.Args...),
		Labels:       map[string]string{testLabelKey: testLabelValue},
		WaitingFor:   wait.ForHTTP("/health").WithPort(serverPort).WithStartupTimeout(30 * time.Second),
	}
	if os.Getenv(EnvDumpContainerLogs) != "0" {
		if consumer := newFileLogConsumer(u.ID); consumer != nil {
			u.logs = consumer
			req.LogConsumerCfg = &testcontainers.LogConsumerConfig{Consumers: []testcontainers.LogConsumer{consumer}}
		}
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		if c != nil {
			_ = terminate(ctx, c)
		}
		return err
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = terminate(ctx, c)
		return err
	}
	port, err := c.MappedPort(ctx, serverPort)
	if err != nil {
		_ = terminate(ctx, c)
		return err
	}
	u.container, u.cfg, u.host, u.port = c, cfg, host, port.Port()
	return nil
}

func (p *ContainerPool) stopContainer(ctx context.Context, u *Unit) error {
	if u.logs != nil {
		defer func() {
			u.logs.Close()
			u.logs = nil
		}()
	}
	if u.container == nil {
		return nil
	}
	c := u.container
	u.container = nil
	return terminate(ctx, c)
}

func terminate(ctx context.Context, c testcontainers.Container) error {
	var last error
	for attempt := range 3 {
		last = c.Terminate(ctx, testcontainers.StopTimeout(time.Second))
		if last == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
	return fmt.Errorf("terminate %s: %w", c.GetContainerID(), last)
}

// healthyAndIdle waits until the unit answers /health and has no open sessions.
func (p *ContainerPool) healthyAndIdle(ctx context.Context, u *Unit) error {
	if u.container == nil {
		return errors.New("no container")
	}
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		last = func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.HTTPBase()+"/health", nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("health returned %d", resp.StatusCode)
			}
			m, err := scrapeMetrics(ctx, u.HTTPBase())
			if err != nil {
				return err
			}
			if active := m.Get("monty_server_sessions_active", nil); active != 0 {
				return fmt.Errorf("%v sessions still active", active)
			}
			return nil
		}()
		if last == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

func dockerKill(ctx context.Context, containerID, signal string) error {
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	_, err = cli.ContainerKill(ctx, containerID, client.ContainerKillOptions{Signal: signal})
	return err
}
