package montygo_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
)

const serverInfoBody = `{
  "version": "0.1.0-3f2a9c1",
  "monty_rev": "f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28",
  "protocol_version": 3,
  "limits": {
    "idle_timeout_s": 60,
    "keepalive_s": 5,
    "session_timeout_s": 0,
    "turn_timeout_s": 300,
    "max_duration_s": 60,
    "max_memory_bytes": 67108864,
    "max_recursion_depth": 1000,
    "max_sessions": 64,
    "max_sessions_per_client": 10
  },
  "future_field": true
}`

// fixedServer is the supervisor of a test URL without headers.
func fixedServer(url string) montygo.ServerSupervisor { return montygo.StaticServer(url, nil, nil) }

func TestFetchServerInfo(t *testing.T) {
	t.Run("decodes /info under the URL's base path", func(t *testing.T) {
		var seen atomic.Pointer[http.Request]
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/base/info" {
				http.NotFound(w, r)
				return
			}
			seen.Store(r.Clone(context.Background()))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(serverInfoBody))
		}))
		defer srv.Close()
		sup := montygo.StaticServer(
			"ws://"+strings.TrimPrefix(srv.URL, "http://")+"/base/",
			nil,
			func(context.Context) (map[string]string, error) {
				return map[string]string{"Authorization": "Bearer token"}, nil
			},
		)
		info, err := montygo.FetchServerInfo(context.Background(), sup, montygo.RemoteOptions{})
		require.NoError(t, err)
		require.Equal(t, &montygo.ServerInfo{
			Version:         "0.1.0-3f2a9c1",
			MontyRev:        "f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28",
			ProtocolVersion: 3,
			Limits: montygo.ServerLimits{
				IdleTimeout:          60 * time.Second,
				Keepalive:            5 * time.Second,
				SessionTimeout:       0,
				TurnTimeout:          300 * time.Second,
				MaxDuration:          60 * time.Second,
				MaxMemory:            64 << 20,
				MaxRecursionDepth:    1000,
				MaxSessions:          64,
				MaxSessionsPerClient: 10,
			},
		}, info)
		req := seen.Load()
		require.NotNil(t, req)
		require.Equal(t, "Bearer token", req.Header.Get("Authorization"))
		require.True(t, strings.HasPrefix(req.Header.Get("User-Agent"), "monty-pool/"), req.Header.Get("User-Agent"))
	})

	t.Run("404 is ErrNoServerInfo", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		defer srv.Close()
		_, err := montygo.FetchServerInfo(context.Background(), fixedServer(srv.URL), montygo.RemoteOptions{})
		require.ErrorIs(t, err, monterr.ErrNoServerInfo)
	})

	t.Run("other statuses are reported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		_, err := montygo.FetchServerInfo(context.Background(), fixedServer(srv.URL), montygo.RemoteOptions{})
		require.Error(t, err)
		require.NotErrorIs(t, err, monterr.ErrNoServerInfo)
		require.Contains(t, err.Error(), "503")
	})

	t.Run("malformed body is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer srv.Close()
		_, err := montygo.FetchServerInfo(context.Background(), fixedServer(srv.URL), montygo.RemoteOptions{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "decoding response")
	})

	t.Run("connect header errors fail unchanged", func(t *testing.T) {
		boom := errors.New("no credentials")
		sup := montygo.StaticServer("ws://127.0.0.1:1/", nil, func(context.Context) (map[string]string, error) { return nil, boom })
		_, err := montygo.FetchServerInfo(context.Background(), sup, montygo.RemoteOptions{})
		require.ErrorIs(t, err, boom)
	})

	t.Run("a supervisor is required", func(t *testing.T) {
		_, err := montygo.FetchServerInfo(context.Background(), nil, montygo.RemoteOptions{})
		var oe *monterr.OptionError
		require.ErrorAs(t, err, &oe)
	})

	t.Run("unsupported scheme", func(t *testing.T) {
		_, err := montygo.FetchServerInfo(context.Background(), fixedServer("ftp://127.0.0.1/"), montygo.RemoteOptions{})
		require.Error(t, err)
		require.Contains(t, err.Error(), `unsupported URL scheme "ftp"`)
	})

	t.Run("dial timeout applies", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(release)
		_, err := montygo.FetchServerInfo(context.Background(), fixedServer(srv.URL), montygo.RemoteOptions{DialTimeout: 50 * time.Millisecond})
		require.Error(t, err)
		require.Contains(t, err.Error(), "timed out")
	})
}
