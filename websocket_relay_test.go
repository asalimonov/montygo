package montygo_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/asalimonov/montygo"
)

// wsRelay bridges each WebSocket connection to a fresh `monty subprocess`
// child, translating one binary message per frame to 4-byte-LE-prefixed stdio.
type wsRelay struct {
	URL string
	// TLS trusts the relay certificate; it is nil for a ws:// relay.
	TLS      *tls.Config
	mu       sync.Mutex
	headers  []http.Header
	health   []http.Header
	info     relayInfo
	upgrades int
	refuse   bool
}

// relayInfo are the limits GET /info reports; a zero value answers 404, like a
// server built before the endpoint existed.
type relayInfo struct {
	sessionTimeoutS int
	turnTimeoutS    int
}

func (i relayInfo) served() bool { return i.sessionTimeoutS > 0 || i.turnTimeoutS > 0 }

// upgradeCount is how many connections the relay has accepted.
func (r *wsRelay) upgradeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.upgrades
}

// setRefuse makes further upgrades fail with 503, like a server at capacity.
func (r *wsRelay) setRefuse(refuse bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refuse = refuse
}

// wsStartRelay serves the relay on an ephemeral loopback port for the test's
// lifetime, over TLS when secure is set. GET /health answers 200.
func wsStartRelay(t *testing.T, secure bool) *wsRelay {
	t.Helper()
	return wsStartRelayInfo(t, secure, relayInfo{})
}

// wsStartRelayInfo serves the relay with the limits info reports at GET /info.
func wsStartRelayInfo(t *testing.T, secure bool, info relayInfo) *wsRelay {
	t.Helper()
	bin := os.Getenv("MONTY_BIN")
	if bin == "" {
		t.Skip("MONTY_BIN is not set")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("monty binary unavailable: %v", err)
	}
	relay := &wsRelay{info: info}
	ctx, cancel := context.WithCancel(context.Background())
	var bridges sync.WaitGroup
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			relay.mu.Lock()
			relay.health = append(relay.health, r.Header.Clone())
			relay.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/info" {
			if !relay.info.served() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"version":"relay","monty_rev":%q,"protocol_version":%d,
				"limits":{"idle_timeout_s":0,"keepalive_s":0,"session_timeout_s":%d,"turn_timeout_s":%d,
				"max_duration_s":0,"max_memory_bytes":0,"max_recursion_depth":1000,
				"max_sessions":16,"max_sessions_per_client":0}}`,
				montygo.UpstreamRev, montygo.ProtocolVersion, relay.info.sessionTimeoutS, relay.info.turnTimeoutS)
			return
		}
		relay.mu.Lock()
		relay.headers = append(relay.headers, r.Header.Clone())
		refuse := relay.refuse
		if !refuse {
			relay.upgrades++
		}
		relay.mu.Unlock()
		if refuse {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		bridges.Add(1)
		defer bridges.Done()
		wsBridge(ctx, conn, bin)
	}))
	if secure {
		srv.StartTLS()
		roots := x509.NewCertPool()
		roots.AddCert(srv.Certificate())
		relay.TLS = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		relay.URL = "wss" + strings.TrimPrefix(srv.URL, "https")
	} else {
		srv.Start()
		relay.URL = "ws" + strings.TrimPrefix(srv.URL, "http")
	}
	t.Cleanup(func() {
		srv.Close()
		cancel()
		done := make(chan struct{})
		go func() {
			bridges.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("relay bridges did not finish")
		}
	})
	return relay
}

// captured returns the headers of every upgrade request received so far.
func (r *wsRelay) captured() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.headers...)
}

// healthChecks returns the headers of every health request received so far.
func (r *wsRelay) healthChecks() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.health...)
}

func wsBridge(ctx context.Context, conn *websocket.Conn, bin string) {
	defer conn.CloseNow()
	conn.SetReadLimit(-1)
	cmd := exec.Command(bin, "subprocess")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "failed to spawn monty")
		return
	}
	ended := make(chan struct{}, 2)
	go func() {
		defer func() { ended <- struct{}{} }()
		defer stdin.Close()
		for {
			_, body, err := conn.Read(ctx)
			if err != nil {
				return
			}
			frame := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(body)), uint32(len(body)))
			if _, err := stdin.Write(append(frame, body...)); err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { ended <- struct{}{} }()
		var prefix [4]byte
		for {
			if _, err := io.ReadFull(stdout, prefix[:]); err != nil {
				return
			}
			body := make([]byte, binary.LittleEndian.Uint32(prefix[:]))
			if _, err := io.ReadFull(stdout, body); err != nil {
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, body); err != nil {
				return
			}
		}
	}()
	<-ended
	_ = conn.CloseNow()
	_ = cmd.Process.Kill()
	<-ended
	_ = cmd.Wait()
}
