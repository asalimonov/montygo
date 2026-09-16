package docker

import (
	"bytes"
	"context"
	"fmt"
	pyrt "github.com/asalimonov/montygo/runtime"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// dockerCLI runs a Docker-compatible command line. It inherits the process
// environment, so Docker contexts, DOCKER_HOST and credential helpers apply.
type dockerCLI struct {
	path string
	env  []string
}

var hostPortRE = regexp.MustCompile(`^(?:127\.0\.0\.1|0\.0\.0\.0|\[::1\]|\[::\]):(\d+)$`)

func newDockerCLI(command string, serverEnv map[string]string) (*dockerCLI, error) {
	if command == "" {
		command = "docker"
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return nil, &pyrt.OptionError{Message: fmt.Sprintf("docker CLI not found: %s", command)}
	}
	return &dockerCLI{path: path, env: appendEnv(os.Environ(), serverEnv)}, nil
}

// appendEnv puts the server variables last, so they win over the host's.
func appendEnv(base []string, extra map[string]string) []string {
	out := append([]string(nil), base...)
	for _, k := range sortedEnvKeys(extra) {
		out = append(out, k+"="+extra[k])
	}
	return out
}

func sortedEnvKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *dockerCLI) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.path, args...)
	cmd.Env = c.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, detail)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (c *dockerCLI) imageExists(ctx context.Context, ref string) bool {
	_, err := c.run(ctx, "image", "inspect", "--format", "{{.Id}}", ref)
	return err == nil
}

func (c *dockerCLI) pull(ctx context.Context, ref string) error {
	_, err := c.run(ctx, "pull", "--quiet", ref)
	return err
}

func (c *dockerCLI) runContainer(ctx context.Context, args []string) (string, error) {
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(lastLine(out))
	if id == "" {
		return "", fmt.Errorf("docker run printed no container id")
	}
	return id, nil
}

func (c *dockerCLI) hostPort(ctx context.Context, id string) (string, error) {
	out, err := c.run(ctx, "port", id, "8000/tcp")
	if err != nil {
		return "", err
	}
	return parseHostPort(out)
}

// parseHostPort reads the first published loopback mapping docker port prints.
func parseHostPort(out string) (string, error) {
	for line := range strings.SplitSeq(out, "\n") {
		if m := hostPortRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("no published port for 8000/tcp: %q", strings.TrimSpace(out))
}

func (c *dockerCLI) exists(ctx context.Context, id string) bool {
	_, err := c.run(ctx, "container", "inspect", "--format", "{{.Id}}", id)
	return err == nil
}

func (c *dockerCLI) restart(ctx context.Context, id string, stop time.Duration) error {
	_, err := c.run(ctx, "restart", "-t", strconv.Itoa(int(stop.Seconds())), id)
	return err
}

func (c *dockerCLI) stopAndRemove(ctx context.Context, id string, stop time.Duration) error {
	_, stopErr := c.run(ctx, "stop", "-t", strconv.Itoa(int(stop.Seconds())), id)
	_, rmErr := c.run(ctx, "rm", "-f", id)
	if stopErr != nil && rmErr == nil {
		return nil
	}
	if rmErr != nil {
		return rmErr
	}
	return nil
}

func (c *dockerCLI) logsTail(ctx context.Context, id string, lines int) string {
	out, err := c.run(ctx, "logs", "--tail", strconv.Itoa(lines), id)
	if err != nil {
		return ""
	}
	return out
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[len(lines)-1]
}
