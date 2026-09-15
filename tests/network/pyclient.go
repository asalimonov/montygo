package network

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	pythonPinned = "/opt/pin/bin/python"
	pythonPyPI   = "/opt/pypi-0.0.23/bin/python"
)

// PyRun is the result of a one-shot Python client container.
type PyRun struct {
	ExitCode int
	Output   string
}

// pyClientImage returns the Python client image, skipping the test when it is not built.
func pyClientImage(t *testing.T) string {
	t.Helper()
	image := os.Getenv(EnvPyClientImage)
	if image == "" {
		image = DefaultPyClientImage
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("python client image %s is not available (make docker-build-pyclient): %v", image, err)
	}
	return image
}

func pyRequest(image, python, script string, env map[string]string, waiting wait.Strategy) testcontainers.GenericContainerRequest {
	return testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:      image,
			Entrypoint: []string{python},
			Cmd:        []string{"/scripts/" + script},
			Env:        env,
			Labels:     map[string]string{testLabelKey: testLabelValue},
			WaitingFor: waiting,
		},
		Started: true,
	}
}

// runPyClient runs a script to completion and returns its exit code and output.
func runPyClient(t *testing.T, image, python, script string, env map[string]string) PyRun {
	t.Helper()
	ctx := testCtx(t)
	c, err := testcontainers.GenericContainer(ctx, pyRequest(image, python, script, env,
		wait.ForExit().WithExitTimeout(2*time.Minute)))
	if c != nil {
		t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	}
	require.NoError(t, err)
	return waitPyExit(t, c, 2*time.Minute)
}

// startPyClient starts a script and returns once it prints ready.
func startPyClient(t *testing.T, image, python, script string, env map[string]string, ready string) testcontainers.Container {
	t.Helper()
	ctx := testCtx(t)
	c, err := testcontainers.GenericContainer(ctx, pyRequest(image, python, script, env,
		wait.ForLog(ready).WithStartupTimeout(2*time.Minute)))
	if c != nil {
		t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	}
	require.NoError(t, err)
	return c
}

func waitPyExit(t *testing.T, c testcontainers.Container, timeout time.Duration) PyRun {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for {
		state, err := c.State(ctx)
		require.NoError(t, err)
		if !state.Running {
			rc, err := c.Logs(ctx)
			require.NoError(t, err)
			defer func() { _ = rc.Close() }()
			out, _ := io.ReadAll(rc)
			return PyRun{ExitCode: state.ExitCode, Output: string(out)}
		}
		if time.Now().After(deadline) {
			t.Fatalf("python client still running after %s", timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// prefixedLine returns the rest of the first output line starting with prefix.
func prefixedLine(t *testing.T, output, prefix string) string {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), prefix); ok {
			return rest
		}
	}
	t.Fatalf("no line starting with %q in:\n%s", prefix, output)
	return ""
}
