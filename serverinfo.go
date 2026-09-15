package montygo

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/asalimonov/montygo/internal/worker"
)

// ErrNoServerInfo reports a server without a GET /info endpoint.
var ErrNoServerInfo = errors.New("montygo: server does not expose /info")

// ServerInfo is what a monty-server reports at GET /info.
type ServerInfo struct {
	// Version is the server's build version.
	Version string
	// MontyRev is the full upstream commit the server was built from.
	MontyRev string
	// ProtocolVersion is the wire protocol the server speaks.
	ProtocolVersion uint32
	// Limits are the server's effective timeouts and ceilings.
	Limits ServerLimits
}

// ServerLimits are a monty-server's effective limits; a zero duration or count means disabled.
type ServerLimits struct {
	IdleTimeout    time.Duration
	Keepalive      time.Duration
	SessionTimeout time.Duration
	TurnTimeout    time.Duration
	// MaxDuration is the sandbox execution ceiling per session.
	MaxDuration time.Duration
	// MaxMemory is the allocator ceiling per session, in bytes.
	MaxMemory uint64
	// MaxRecursionDepth is the call-stack ceiling; it cannot be disabled.
	MaxRecursionDepth    uint64
	MaxSessions          int
	MaxSessionsPerClient int
}

type serverInfoJSON struct {
	Version         string `json:"version"`
	MontyRev        string `json:"monty_rev"`
	ProtocolVersion uint32 `json:"protocol_version"`
	Limits          struct {
		IdleTimeoutS         uint64 `json:"idle_timeout_s"`
		KeepaliveS           uint64 `json:"keepalive_s"`
		SessionTimeoutS      uint64 `json:"session_timeout_s"`
		TurnTimeoutS         uint64 `json:"turn_timeout_s"`
		MaxDurationS         uint64 `json:"max_duration_s"`
		MaxMemoryBytes       uint64 `json:"max_memory_bytes"`
		MaxRecursionDepth    uint64 `json:"max_recursion_depth"`
		MaxSessions          int    `json:"max_sessions"`
		MaxSessionsPerClient int    `json:"max_sessions_per_client"`
	} `json:"limits"`
}

func (j *serverInfoJSON) toServerInfo() *ServerInfo {
	l := j.Limits
	return &ServerInfo{
		Version:         j.Version,
		MontyRev:        j.MontyRev,
		ProtocolVersion: j.ProtocolVersion,
		Limits: ServerLimits{
			IdleTimeout:          seconds(l.IdleTimeoutS),
			Keepalive:            seconds(l.KeepaliveS),
			SessionTimeout:       seconds(l.SessionTimeoutS),
			TurnTimeout:          seconds(l.TurnTimeoutS),
			MaxDuration:          seconds(l.MaxDurationS),
			MaxMemory:            l.MaxMemoryBytes,
			MaxRecursionDepth:    l.MaxRecursionDepth,
			MaxSessions:          l.MaxSessions,
			MaxSessionsPerClient: l.MaxSessionsPerClient,
		},
	}
}

func seconds(s uint64) time.Duration {
	const maxSeconds = uint64(1<<63-1) / uint64(time.Second)
	if s > maxSeconds {
		s = maxSeconds
	}
	return time.Duration(s) * time.Second
}

// FetchServerInfo reads GET <path>/info of the server behind opts.URL over the
// same transport a checkout dials. It sends the ConnectHeaders and is bounded by
// RequestTimeout: 0 means 10s, NoRequestTimeout leaves only ctx. A server without
// the endpoint yields ErrNoServerInfo.
func FetchServerInfo(ctx context.Context, opts WebSocketOptions) (*ServerInfo, error) {
	opts, err := opts.resolve(ctx)
	if err != nil {
		return nil, err
	}
	timeout := opts.RequestTimeout
	if timeout == 0 {
		timeout = defaultWebSocketRequestTimeout
	}
	headers, err := serverInfoHeaders(ctx, opts)
	if err != nil {
		return nil, err
	}
	var raw serverInfoJSON
	if err := opts.dialer(timeout).GetJSON(ctx, "/info", headers, &raw); err != nil {
		var he *worker.HTTPStatusError
		if errors.As(err, &he) && he.Code == http.StatusNotFound {
			return nil, ErrNoServerInfo
		}
		return nil, err
	}
	return raw.toServerInfo(), nil
}

func serverInfoHeaders(ctx context.Context, opts WebSocketOptions) ([][2]string, error) {
	if opts.ConnectHeaders == nil {
		return nil, nil
	}
	extra, err := opts.ConnectHeaders(ctx)
	if err != nil {
		return nil, err
	}
	return sortedHeaders(extra), nil
}
