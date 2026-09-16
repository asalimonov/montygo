//go:build unix

package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeDockerScript answers the CLI calls DockerSupervisor makes and appends every
// invocation to a log, so a test can assert the arguments and the environment.
const fakeDockerScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DOCKER_LOG"
printf 'env dump_key=%s sessions=%s session_timeout=%s\n' \
  "$MONTY_SERVER_DUMP_KEY" "$MONTY_SERVER_MAX_SESSIONS" "$MONTY_SERVER_SESSION_TIMEOUT" >> "$FAKE_DOCKER_LOG"
case "$1 $2" in
  "image inspect")
    for ref in $FAKE_DOCKER_LOCAL; do
      [ "$ref" = "$5" ] && { echo "sha256:localimage"; exit 0; }
    done
    echo "Error: No such image: $5" >&2
    exit 1
    ;;
esac
case "$1" in
  pull)
    for ref in $FAKE_DOCKER_PULLABLE; do
      [ "$ref" = "$3" ] && { echo "$3"; exit 0; }
    done
    echo "Error response from daemon: $3: not found" >&2
    exit 1
    ;;
  run) echo "$FAKE_DOCKER_CONTAINER"; exit 0 ;;
  port) printf '127.0.0.1:%s\n' "$(cat "$FAKE_DOCKER_PORT_FILE")"; exit 0 ;;
  container) echo "sha256:container"; exit 0 ;;
  restart|stop|rm|logs) exit 0 ;;
esac
exit 0
`

type fakeDocker struct {
	command  string
	logPath  string
	portPath string
}

// setPort publishes a new port, as a container restart does. The CLI reads it
// from a file, because dockerCLI snapshots the environment when it is built.
func (f *fakeDocker) setPort(t *testing.T, server *httptest.Server) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.portPath, []byte(serverPort(t, server)), 0o600))
}

// newFakeDocker installs the script and points the supervisor's environment at
// server, the stand-in for the container's monty-server.
func newFakeDocker(t *testing.T, server *httptest.Server) *fakeDocker {
	t.Helper()
	dir := t.TempDir()
	command := filepath.Join(dir, "docker")
	require.NoError(t, os.WriteFile(command, []byte(fakeDockerScript), 0o700))
	logPath := filepath.Join(dir, "calls.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o600))
	portPath := filepath.Join(dir, "port")
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("FAKE_DOCKER_CONTAINER", "c0ffee0c0ffee0c0ffee0c0ffee0c0ffee0c0ffee0c0ffee0c0ffee0c0ffee0")
	t.Setenv("FAKE_DOCKER_LOCAL", "")
	t.Setenv("FAKE_DOCKER_PULLABLE", "")
	t.Setenv("FAKE_DOCKER_PORT_FILE", portPath)
	f := &fakeDocker{command: command, logPath: logPath, portPath: portPath}
	f.setPort(t, server)
	return f
}

func (f *fakeDocker) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.logPath)
	require.NoError(t, err)
	return string(data)
}

func serverPort(t *testing.T, s *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(s.URL)
	require.NoError(t, err)
	return u.Port()
}

// fakeServer answers the endpoints a supervisor probes before it returns.
func fakeServer(t *testing.T, protocol uint32, healthy *bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if healthy != nil && !*healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":"0.3.0","monty_rev":%q,"protocol_version":%d,
			"limits":{"idle_timeout_s":0,"keepalive_s":5,"session_timeout_s":3600,"turn_timeout_s":300,
			"max_duration_s":0,"max_memory_bytes":0,"max_recursion_depth":1000,
			"max_sessions":8,"max_sessions_per_client":0}}`, upstreamRev, protocol)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func testDockerOptions(f *fakeDocker) DockerOptions {
	return DockerOptions{
		Image:        "example.test/monty-server",
		Version:      "test",
		Command:      f.command,
		StartTimeout: 5 * time.Second,
		StopTimeout:  time.Second,
		MaxProcesses: 4,
	}
}

func TestDockerSupervisorStartsAContainer(t *testing.T) {
	server := fakeServer(t, protocolVersion, nil)
	f := newFakeDocker(t, server)
	t.Setenv("FAKE_DOCKER_LOCAL", "example.test/monty-server:test")

	sup, err := NewDockerSupervisor(context.Background(), testDockerOptions(f))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sup.Close(context.Background()) })

	ep, err := sup.Endpoint(context.Background())
	require.NoError(t, err)
	require.Equal(t, "ws://127.0.0.1:"+serverPort(t, server)+"/", ep.URL)
	require.Equal(t, "example.test/monty-server:test", sup.Image())
	require.NotEmpty(t, sup.ContainerID())
	require.Equal(t, protocolVersion, sup.ServerInfo().ProtocolVersion)

	calls := f.calls(t)
	require.Contains(t, calls, "--read-only")
	require.Contains(t, calls, "--cap-drop ALL")
	require.Contains(t, calls, "--security-opt no-new-privileges")
	require.Contains(t, calls, "-p 127.0.0.1::8000")
	require.Contains(t, calls, "--label io.montygo.supervisor=")
	require.Contains(t, calls, "--label io.montygo.pid=")
	require.Contains(t, calls, "-e MONTY_SERVER_DUMP_KEY")
	require.NotContains(t, calls, "MONTY_SERVER_DUMP_KEY=", "the key travels in the environment, not in argv")
	require.Contains(t, calls, "-e MONTY_SERVER_MAX_SESSIONS_PER_CLIENT")
	require.Regexp(t, `env dump_key=[0-9a-f]{64} sessions=8`, calls, "max sessions is twice MaxProcesses")
}

func TestDockerSupervisorPullsWhenTheImageIsAbsent(t *testing.T) {
	server := fakeServer(t, protocolVersion, nil)
	f := newFakeDocker(t, server)
	t.Setenv("FAKE_DOCKER_PULLABLE", "example.test/monty-server:test")

	sup, err := NewDockerSupervisor(context.Background(), testDockerOptions(f))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sup.Close(context.Background()) })
	require.Contains(t, f.calls(t), "pull --quiet example.test/monty-server:test")
}

func TestDockerSupervisorTriesEveryCandidate(t *testing.T) {
	server := fakeServer(t, protocolVersion, nil)
	f := newFakeDocker(t, server)
	// Neither candidate exists locally; only the base release can be pulled.
	t.Setenv("FAKE_DOCKER_PULLABLE", "example.test/monty-server:0.3.0")
	candidates, err := dockerImageCandidates(testDockerOptions(f).Image, "", "0.3.0-3f2a9c1", noEnv)
	require.NoError(t, err)
	require.Len(t, candidates, 2)

	cli, err := newDockerCLI(f.command, nil)
	require.NoError(t, err)
	ref, err := resolveDockerImage(context.Background(), cli, candidates)
	require.NoError(t, err)
	require.Equal(t, "example.test/monty-server:0.3.0", ref)
}

func TestDockerSupervisorReportsEveryFailedCandidate(t *testing.T) {
	server := fakeServer(t, protocolVersion, nil)
	f := newFakeDocker(t, server)
	cli, err := newDockerCLI(f.command, nil)
	require.NoError(t, err)
	_, err = resolveDockerImage(context.Background(), cli, []string{"a/b:1", "a/b:2"})
	require.ErrorContains(t, err, "a/b:1")
	require.ErrorContains(t, err, "a/b:2")
	require.ErrorContains(t, err, "not found")
}

func TestDockerSupervisorRemovesAContainerThatNeverGetsHealthy(t *testing.T) {
	healthy := false
	server := fakeServer(t, protocolVersion, &healthy)
	f := newFakeDocker(t, server)
	t.Setenv("FAKE_DOCKER_LOCAL", "example.test/monty-server:test")
	opts := testDockerOptions(f)
	opts.StartTimeout = 300 * time.Millisecond

	_, err := NewDockerSupervisor(context.Background(), opts)
	require.ErrorContains(t, err, "did not become usable")
	require.ErrorContains(t, err, "local Docker daemons only")
	require.Contains(t, f.calls(t), "rm -f")
}

func TestDockerSupervisorRefusesAnotherProtocolVersion(t *testing.T) {
	server := fakeServer(t, protocolVersion+1, nil)
	f := newFakeDocker(t, server)
	t.Setenv("FAKE_DOCKER_LOCAL", "example.test/monty-server:test")

	_, err := NewDockerSupervisor(context.Background(), testDockerOptions(f))
	require.ErrorContains(t, err, "protocol version")
	require.Contains(t, f.calls(t), "rm -f")
}

func TestDockerSupervisorEnvOverridesTheDefaults(t *testing.T) {
	server := fakeServer(t, protocolVersion, nil)
	f := newFakeDocker(t, server)
	t.Setenv("FAKE_DOCKER_LOCAL", "example.test/monty-server:test")
	opts := testDockerOptions(f)
	opts.Env = map[string]string{"MONTY_SERVER_SESSION_TIMEOUT": "20"}
	opts.RunArgs = []string{"--memory", "2g"}

	sup, err := NewDockerSupervisor(context.Background(), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sup.Close(context.Background()) })

	calls := f.calls(t)
	require.Contains(t, calls, "-e MONTY_SERVER_SESSION_TIMEOUT")
	require.Contains(t, calls, "--memory 2g")
	require.Contains(t, calls, "session_timeout=20")
}

func TestDockerSupervisorRestartRebindsTheEndpoint(t *testing.T) {
	first := fakeServer(t, protocolVersion, nil)
	f := newFakeDocker(t, first)
	t.Setenv("FAKE_DOCKER_LOCAL", "example.test/monty-server:test")

	sup, err := NewDockerSupervisor(context.Background(), testDockerOptions(f))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sup.Close(context.Background()) })
	before, err := sup.Endpoint(context.Background())
	require.NoError(t, err)

	// A restart publishes a new port, as Docker does.
	second := fakeServer(t, protocolVersion, nil)
	f.setPort(t, second)
	require.NoError(t, sup.Restart(context.Background(), before))

	after, err := sup.Endpoint(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, before.URL, after.URL)
	require.Contains(t, f.calls(t), "restart -t 1")
}

func TestDockerSupervisorIgnoresARestartOfAStaleEndpoint(t *testing.T) {
	server := fakeServer(t, protocolVersion, nil)
	f := newFakeDocker(t, server)
	t.Setenv("FAKE_DOCKER_LOCAL", "example.test/monty-server:test")

	sup, err := NewDockerSupervisor(context.Background(), testDockerOptions(f))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sup.Close(context.Background()) })

	require.NoError(t, sup.Restart(context.Background(), ServerEndpoint{URL: "ws://127.0.0.1:1/"}))
	require.NotContains(t, f.calls(t), "restart")
}

func TestDockerSupervisorCloseStopsAndRemoves(t *testing.T) {
	server := fakeServer(t, protocolVersion, nil)
	f := newFakeDocker(t, server)
	t.Setenv("FAKE_DOCKER_LOCAL", "example.test/monty-server:test")

	sup, err := NewDockerSupervisor(context.Background(), testDockerOptions(f))
	require.NoError(t, err)
	require.NoError(t, sup.Close(context.Background()))
	require.NoError(t, sup.Close(context.Background()), "close is idempotent")

	calls := f.calls(t)
	require.Contains(t, calls, "stop -t 1")
	require.Contains(t, calls, "rm -f")
	require.Equal(t, 1, strings.Count(calls, "stop -t 1"))

	_, err = sup.Endpoint(context.Background())
	require.ErrorIs(t, err, ErrSupervisorClosed)
}

func TestDockerSupervisorRejectsAMissingCLI(t *testing.T) {
	opts := DockerOptions{Command: filepath.Join(t.TempDir(), "no-such-docker"), Version: "test"}
	_, err := NewDockerSupervisor(context.Background(), opts)
	require.ErrorAs(t, err, new(*OptionError))
	require.ErrorContains(t, err, "docker CLI not found")
}
