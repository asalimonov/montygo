# Server supervisors, recovery and rotation

A remote pool reaches workers through a `monty-server`. This document covers who owns that server, how a pool survives losing it, and how a session outlives the server's session timeout.

`websocket.md` covers the transport, `server.md` the server itself, `docker.md` its image.

## Supervisor

```go
type ServerEndpoint struct {
	URL       string
	Headers   map[string]string
	TLSConfig *tls.Config
}

type ServerSupervisor interface {
	Endpoint(ctx context.Context) (ServerEndpoint, error)
	Restart(ctx context.Context, failed ServerEndpoint) error
}

func Remote(sup ServerSupervisor, opts RemoteOptions) WorkerSource
func StaticServer(url string, tlsConfig *tls.Config, headers func(ctx context.Context) (map[string]string, error)) ServerSupervisor
```

- The contract is declared in the root package, so an application implements it without importing anything else. `Remote(sup, opts)` is the worker source of a pool; a nil supervisor is an `OptionError`.
- `Endpoint` is called before every dial attempt, so an endpoint MAY move between attempts. It MUST be safe for concurrent use and SHOULD be cheap. An endpoint's `Headers` are sent with every upgrade and HTTP request; its `TLSConfig` overrides `RemoteOptions.TLSConfig`.
- `Restart` receives the endpoint that failed. A supervisor that has already replaced that server MUST return nil.
- A supervisor that replaces a server MUST keep its dump key. A rotated session's dump is signed, and another key rejects it with `ValueError: invalid session dump signature`.
- `StaticServer` is the supervisor of a fixed URL. Its endpoint never moves, its headers callback runs once per checkout and its error fails that checkout unchanged, and `Restart` always fails, so `RecoveryPolicy.RestartServer` has no effect there. A pool with a static server dials once per checkout, without the recovery loop; rotation still uses it, because state is at stake.
- montygo ships two implementations, and an application MAY add its own for servers on other hosts:

| Package | Server | Endpoint | Restart |
|---|---|---|---|
| `montygo/supervisor/docker` | a container on the local Docker daemon | `ws://127.0.0.1:<published port>/` | `docker restart`, or a new container when it is gone; the dump key survives |
| `montygo/supervisor/native` | a `monty-server` child process | `ws://127.0.0.1:<ephemeral port>/`, read from the line the server prints once bound | the process is drained with SIGTERM and respawned; the dump key survives |

- Both disable the server's idle timeout and its memory and duration ceilings, so the runtime's limits govern as on the local workers, and both size the server at `Options.MaxSessions` sessions (0 means `2 × runtime.NumCPU()`) with the per-client quota off. Pass twice the `MaxWorkers` of the pools that dial the server.
- Both probe readiness with `CheckServerHealth` and `FetchServerInfo` over a `StaticServer` of the published address, and refuse a server of another protocol version.
- Both import the root package. Neither builds a pool: the application creates the supervisor, passes it to `Remote`, and closes it after the pools.

## Recovery

```go
type RecoveryPolicy struct {
	Attempts       int           // 0 means 3
	AttemptTimeout time.Duration // 0 means 5s
	RestartServer  bool          // opt-in
}
```

`RemoteOptions.Recovery` holds it. The pool reserves capacity first, then dials under the policy. Capacity waiting is bounded by `CheckoutTimeout`, never by `AttemptTimeout`.

```
Reserve capacity (CheckoutTimeout)
  │
  ├─ round 1: Attempts × [ Endpoint → dial → Configure → (Load) ]   each within AttemptTimeout
  │             success ─▶ checkout
  ├─ RestartServer and no restart yet ─▶ one single-flight Restart
  └─ round 2: Attempts × [ … ]
                failure ─▶ SpawnError (checkout) | RotationError (rotation)
```

- Retryable: spawn, disconnect, crash and timeout failures from `internal/pool`, an attempt that exceeds `AttemptTimeout`, and an `Endpoint` error. An `Endpoint` error counts as an attempt and skips the dial.
- Not retryable: a closed pool, exhausted capacity, a runtime error such as a rejected dump, a protocol violation, and the caller's context ending. A caller that gave up gets `ctx.Err()`.
- Restarts are single-flight per endpoint URL. A caller whose failure happened before a completed restart of that URL joins it instead of starting another; a failure observed afterwards is a new incident.
- A restart runs under `context.WithoutCancel`, so the caller that started it can give up without abandoning the other waiters.
- `Checkout` uses the policy for every supervisor except `StaticServer`. Rotation always uses it.

## Rotation

A `monty-server` closes a session at `--session-timeout`, 3600 s by default, without a dump. Rotation moves the session to a new connection before that.

### Policy

`newRotationPolicy` reads `GET /info` once, at `NewPool`, when `RemoteOptions.RotateSessions` is set. Rotation stays off, without failing, when the server reports no `/info`, a disabled session or turn timeout, or a session timeout shorter than `2 × (turn timeout + margin)`. A hostile or misconfigured server can therefore cause no rotation, never a rotation storm.

`RemoteOptions.RotationMargin` is the lead time; 0 means 30 s. Rotation is off unless `RotateSessions` asks for it, for every supervisor alike.

### Invariant

The server bounds one complete request, host callbacks and suspensions included, by its turn timeout. montygo therefore rotates **before** starting work when less than `turn timeout + margin` of the session's life remains. An operation admitted earlier ends before the deadline, so no execution is cut short by the session timeout.

- Every public operation that sends a request calls `rotateIfDue` first: `FeedRun`, `FeedStart`, `Go`, `LoadSession`, `LoadSnapshot`, `Dump` and `InstallDependencies`.
- A session that stays idle is rotated by a timer at `deadline − margin`. A busy session is skipped; the next operation's check covers it.
- The deadline is `dialStart + session timeout`, and `dialStart` is taken before the dial, so the client's deadline is never later than the server's.

### Steps

```
Dump                    → signed envelope in the parent's memory
stop the loss watcher   → the planned close is not a session loss
Handoff                 → the connection closes, the capacity slot and budget stay
Bind(state)             → new connection, Configure, Load under the recovery policy
attach                  → new watcher, new deadline, new timer
```

- Rotation runs under a control reservation, so no execution is in flight and no suspension is pending.
- `Handoff` and `Bind` keep the suspension count, the duration budget and the working-directory flag. Upstream carries the execution clock and the limits inside the dump, so a rotated session neither gains budget nor loses it.
- The runtime, its host objects, the stop policy and `Session` identity are the parent's; they are unaffected.
- Telemetry keeps upstream names: the rotation appears as a `dump` span, a `load` span and a new `session` span.
- During a rotation the pool MAY hold one worker more than `MaxWorkers` while the old connection closes. This is why a supervisor's `MaxSessions` SHOULD be twice the pool's `MaxWorkers`.

### Failure

A failed rotation ends the session with `*monterr.RotationError`, which matches `errors.Is(err, monterr.ErrSessionLost)`.

| Failure | `Dump` | Recovery |
|---|---|---|
| `Dump` fails, including a dump over the 256 MiB frame limit | nil | the state is gone; start a new session |
| `Handoff` fails | the envelope | `LoadSession` it on a new session |
| every reconnect attempt fails | the envelope | same |
| `Load` is rejected, for example by another dump key | the envelope | the state cannot be restored on that server |

A rotation before an operation returns the error from that call, and the operation does not run. A rotation from the idle timer makes `Session.Err()` and the next call report it.

## Ownership

- A pool never owns its supervisor. `Pool.Shutdown` and `Pool.Close` touch workers and sessions only. The application closes the supervisor after the pools that dial it; a supervisor closed first makes later checkouts fail with `monterr.ErrSupervisorClosed` from `Endpoint`.
- `docker.Options.Reaper` is the hook for removing containers left by a process that died; nil reaps nothing.
