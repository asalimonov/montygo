package supervisor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/telemetry"
	"github.com/asalimonov/montygo/internal/telemetryhooks"
	"github.com/asalimonov/montygo/internal/worker"
)

// SortedHeaders orders a header map so an upgrade sends them deterministically.
func SortedHeaders(headers map[string]string) [][2]string {
	pairs := make([][2]string, 0, len(headers))
	for k, v := range headers {
		pairs = append(pairs, [2]string{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	return pairs
}

// ErrSupervisorClosed reports a supervisor that no longer serves endpoints.
var ErrSupervisorClosed = errors.New("montygo: server supervisor closed")

// ServerEndpoint is where a pool dials one attempt.
type ServerEndpoint struct {
	// URL is a ws:// or wss:// server URL.
	URL string
	// Headers are sent with every upgrade and HTTP request to this endpoint.
	Headers map[string]string
	// TLSConfig overrides WebSocketOptions.TLSConfig when set.
	TLSConfig *tls.Config
}

// ServerSupervisor owns where monty-server runs. Endpoint is called before every
// dial; Restart replaces the server behind a failed endpoint. Both MUST be safe
// for concurrent use. A supervisor that replaces a server MUST keep its dump key,
// or sessions rotated across the restart fail with an invalid signature.
type ServerSupervisor interface {
	Endpoint(ctx context.Context) (ServerEndpoint, error)
	Restart(ctx context.Context, failed ServerEndpoint) error
}

// RecoveryPolicy bounds how a supervised pool retries a dial.
type RecoveryPolicy struct {
	// Attempts per round: 0 means 3.
	Attempts int
	// AttemptTimeout bounds one attempt: 0 means 5s.
	AttemptTimeout time.Duration
	// RestartServer allows one supervisor restart after a failed round.
	RestartServer bool
}

const (
	defaultRecoveryAttempts       = 3
	defaultRecoveryAttemptTimeout = 5 * time.Second
)

func (p RecoveryPolicy) attempts() int {
	if p.Attempts == 0 {
		return defaultRecoveryAttempts
	}
	return p.Attempts
}

func (p RecoveryPolicy) attemptTimeout() time.Duration {
	if p.AttemptTimeout == 0 {
		return defaultRecoveryAttemptTimeout
	}
	return p.AttemptTimeout
}

// OrphanReaper removes servers left behind by a process that ended without
// closing its supervisor.
type OrphanReaper interface {
	Reap(ctx context.Context) error
}

type NoopReaper struct{}

func (NoopReaper) Reap(context.Context) error {
	// TODO: NotImplemented: list containers labelled io.montygo.supervisor, skip
	// those whose io.montygo.pid is still alive on this host, remove the rest.
	return nil
}

// Static serves a pool configured with a fixed URL.
type Static struct {
	url     string
	tls     *tls.Config
	headers func(ctx context.Context) (map[string]string, error)
}

func (s Static) Endpoint(ctx context.Context) (ServerEndpoint, error) {
	ep := ServerEndpoint{URL: s.url, TLSConfig: s.tls}
	if s.headers == nil {
		return ep, nil
	}
	headers, err := s.headers(ctx)
	if err != nil {
		return ServerEndpoint{}, err
	}
	ep.Headers = headers
	return ep, nil
}

func (Static) Restart(context.Context, ServerEndpoint) error {
	return errors.New("montygo: this pool has no server supervisor that can restart")
}

// Recoverer runs one supervised dial: attempts against the current endpoint,
// then at most one server restart, then the same attempts again.
type Recoverer struct {
	sup      ServerSupervisor
	policy   RecoveryPolicy
	rec      *telemetry.Recorder
	mu       sync.Mutex
	restarts map[string]*restartFlight
}

type restartFlight struct {
	done      chan struct{}
	err       error
	completed time.Time
}

func NewRecoverer(sup ServerSupervisor, policy RecoveryPolicy, rec *telemetry.Recorder) *Recoverer {
	return &Recoverer{sup: sup, policy: policy, rec: rec, restarts: map[string]*restartFlight{}}
}

type bindFunc func(ctx context.Context, ep ServerEndpoint) (*pool.Checkout, error)

// do returns the checkout, the number of attempts made, and the last failure.
func (r *Recoverer) Do(ctx context.Context, bind bindFunc) (*pool.Checkout, int, error) {
	var (
		last     error
		lastEP   ServerEndpoint
		lastAt   time.Time
		attempts int
	)
	for round := range 2 {
		for range r.policy.attempts() {
			if err := ctx.Err(); err != nil {
				return nil, attempts, err
			}
			attempts++
			ep, err := r.sup.Endpoint(ctx)
			if err != nil {
				last, lastAt = err, time.Now()
				continue
			}
			actx, cancel := context.WithTimeout(ctx, r.policy.attemptTimeout())
			co, err := bind(r.withEndpoint(actx, ep), ep)
			cancel()
			if err == nil {
				return co, attempts, nil
			}
			if !retryableDialError(ctx, err) {
				// A caller that gave up owns the outcome, not the last dial.
				if cerr := ctx.Err(); cerr != nil {
					return nil, attempts, cerr
				}
				return nil, attempts, err
			}
			last, lastEP, lastAt = err, ep, time.Now()
		}
		if !r.policy.RestartServer || round == 1 || lastEP.URL == "" {
			break
		}
		if err := r.restartOnce(ctx, lastEP, lastAt); err != nil {
			last = errors.Join(last, fmt.Errorf("restart server: %w", err))
			break
		}
	}
	if last == nil {
		last = errors.New("no dial attempt was made")
	}
	return nil, attempts, last
}

// withEndpoint carries the endpoint's URL, TLS config and headers to the dialer.
func (r *Recoverer) withEndpoint(ctx context.Context, ep ServerEndpoint) context.Context {
	ctx = worker.WithEndpoint(ctx, worker.Endpoint{URL: ep.URL, TLSConfig: ep.TLSConfig})
	headers := telemetryhooks.TraceContextHeaders(r.rec, ctx)
	if len(ep.Headers) > 0 {
		headers = append(headers[:len(headers):len(headers)], SortedHeaders(ep.Headers)...)
	}
	if len(headers) > 0 {
		ctx = pool.WithConnectHeaders(ctx, headers)
	}
	return ctx
}

// restartOnce restarts the server behind failed at most once per incident: a
// restart that completed after this caller's failure already covers it.
func (r *Recoverer) restartOnce(ctx context.Context, failed ServerEndpoint, failedAt time.Time) error {
	r.mu.Lock()
	f, ok := r.restarts[failed.URL]
	switch {
	case ok && !f.completed.IsZero() && f.completed.After(failedAt):
		r.mu.Unlock()
		return f.err
	case ok && f.completed.IsZero():
		r.mu.Unlock()
	default:
		f = &restartFlight{done: make(chan struct{})}
		r.restarts[failed.URL] = f
		r.mu.Unlock()
		go func() {
			err := r.sup.Restart(context.WithoutCancel(ctx), failed)
			r.mu.Lock()
			f.err, f.completed = err, time.Now()
			r.mu.Unlock()
			close(f.done)
		}()
	}
	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryableDialError reports failures a further attempt can fix: the server was
// unreachable, refused the connection, or died while the session was configured.
func retryableDialError(parent context.Context, err error) bool {
	if parent.Err() != nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSupervisorClosed) {
		return true
	}
	var perr *pool.Error
	if !errors.As(err, &perr) {
		return false
	}
	switch perr.Kind {
	case pool.KindSpawn, pool.KindDisconnected, pool.KindCrashed, pool.KindTimeout:
		return true
	}
	return false
}

// NewStatic returns a supervisor for a pool configured with a fixed URL: the
// endpoint never moves, and Restart always fails.
func NewStatic(url string, tlsConfig *tls.Config, headers func(ctx context.Context) (map[string]string, error)) ServerSupervisor {
	return Static{url: url, tls: tlsConfig, headers: headers}
}
