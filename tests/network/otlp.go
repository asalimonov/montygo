package network

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	EnvPythonImage     = "MONTYGO_PYTHON_IMAGE"
	DefaultPythonImage = "python:3.13-slim-bookworm"
)

//go:embed otlp/receiver.py
var otlpReceiverScript string

// otlpReceiver is an OTLP/HTTP endpoint in a container on the default bridge; server containers reach it
// container-to-container, so host firewalls do not matter.
type otlpReceiver struct {
	container testcontainers.Container
	ip        string
	// hostURL reaches the receiver from the test process, for GET /requests.
	hostURL string
}

func startOTLPReceiver(t *testing.T) *otlpReceiver {
	t.Helper()
	image := os.Getenv(EnvPythonImage)
	if image == "" {
		image = DefaultPythonImage
	}
	ctx := testCtx(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			Cmd:          []string{"python", "-u", "/receiver.py"},
			ExposedPorts: []string{"4318/tcp"},
			Labels:       map[string]string{testLabelKey: testLabelValue},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(otlpReceiverScript),
				ContainerFilePath: "/receiver.py",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForLog("LISTENING").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if c != nil {
		t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	}
	require.NoError(t, err)
	ip, err := c.ContainerIP(ctx)
	require.NoError(t, err)
	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "4318/tcp")
	require.NoError(t, err)
	return &otlpReceiver{container: c, ip: ip, hostURL: "http://" + net.JoinHostPort(host, port.Port())}
}

// Endpoint is the collector base URL as seen from another container.
func (r *otlpReceiver) Endpoint() string {
	return "http://" + r.ip + ":4318"
}

func (r *otlpReceiver) spans(t *testing.T) []*tracepb.Span {
	t.Helper()
	rc, err := r.container.Logs(context.Background())
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	out, err := io.ReadAll(rc)
	require.NoError(t, err)
	var spans []*tracepb.Span
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[0] != "OTLP" || fields[1] != "/v1/traces" {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(fields[2])
		require.NoError(t, err)
		// ExportTraceServiceRequest and TracesData share one wire encoding.
		data := &tracepb.TracesData{}
		require.NoError(t, proto.Unmarshal(body, data))
		for _, rs := range data.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				spans = append(spans, ss.GetSpans()...)
			}
		}
	}
	return spans
}

// WaitSpan waits for a span with the given name.
func (r *otlpReceiver) WaitSpan(t *testing.T, name string, timeout time.Duration) *tracepb.Span {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		spans := r.spans(t)
		names := make([]string, 0, len(spans))
		for _, span := range spans {
			if span.GetName() == name {
				return span
			}
			names = append(names, span.GetName())
		}
		if time.Now().After(deadline) {
			t.Fatalf("span %q not exported within %s; got %v\nreceiver %s at %s (%s)\nrequests seen in its log: %s\nrequests it reports over HTTP: %s",
				name, timeout, names, r.container.GetContainerID()[:12], r.ip, r.hostURL, r.requests(t), r.requestsOverHTTP())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// requests summarises what the receiver has been sent: one "<path> <bytes>" per
// request, so a missing span can be told apart from an unreachable receiver.
func (r *otlpReceiver) requests(t *testing.T) string {
	t.Helper()
	rc, err := r.container.Logs(context.Background())
	if err != nil {
		return err.Error()
	}
	defer func() { _ = rc.Close() }()
	out, err := io.ReadAll(rc)
	if err != nil {
		return err.Error()
	}
	var seen []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && fields[0] == "OTLP" {
			seen = append(seen, fmt.Sprintf("%s %dB", fields[1], len(fields[2])*3/4))
		}
	}
	if len(seen) == 0 {
		return "none"
	}
	return strings.Join(seen, ", ")
}

// requestsOverHTTP asks the receiver itself what it received.
func (r *otlpReceiver) requestsOverHTTP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.hostURL+"/requests", nil)
	if err != nil {
		return err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err.Error()
	}
	return string(body)
}
