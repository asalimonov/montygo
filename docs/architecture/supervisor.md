# Server supervisors, recovery and rotation

A WebSocket pool reaches workers through a `monty-server`. This document covers who owns that server, how a pool survives losing it, and how a session outlives the server's session timeout.

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
```

- `WebSocketOptions.Supervisor` replaces `URL` and `ConnectHeaders`; setting either with it is an `OptionError`. A pool with neither is an `OptionError`.
- `Endpoint` is called before every dial attempt, so an endpoint MAY move between attempts. It MUST be safe for concurrent use and SHOULD be cheap.
- `Restart` receives the endpoint that failed. A supervisor that has already replaced that server MUST return nil.
- A supervisor that replaces a server MUST keep its dump key. A rotated session's dump is signed, and another key rejects it with `ValueError: invalid session dump signature`.
- A pool with a fixed `URL` uses an internal static supervisor. Its `Restart` always fails, so `RecoveryPolicy.RestartServer` has no effect there.
- montygo ships two implementations, and an application MAY add its own for servers on other hosts:

| Package | Server | Endpoint | Restart |
|---|---|---|---|
| `montygo/supervisor/docker` | a container on the local Docker daemon | `ws://127.0.0.1:<published port>/` | `docker restart`, or a new container when it is gone; the dump key survives |
| `montygo/supervisor/native` | a `monty-server` child process | `ws://127.0.0.1:<ephemeral port>/`, read from the line the server prints once bound | the process is drained with SIGTERM and respawned; the dump key survives |

- Both disable the server's idle timeout and its memory and duration ceilings, so `CheckoutOptions.Limits` governs as on the local backends, and both size the server at `2 × MaxProcesses` sessions with the per-client quota off.
- `docker.NewPool` and `native.NewPool` return a pool that owns its supervisor; `docker.New` and `native.New` return the supervisor alone, for a caller that wants to share it.
- The root package re-exports the Docker spellings (`montygo.NewDocker`, `montygo.DockerOptions`) for compatibility; the native supervisor is reached through its own package.

## Recovery

```go
type RecoveryPolicy struct {
	Attempts       int           // 0 means 3
	AttemptTimeout time.Duration // 0 means 5s
	RestartServer  bool          // opt-in
}
```

The pool reserves capacity first, then dials under the policy. Capacity waiting is bounded by `CheckoutTimeout`, never by `AttemptTimeout`.

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
- `Checkout` uses the policy only for supervised pools. Rotation always uses it, because state is at stake.

## Rotation

A `monty-server` closes a session at `--session-timeout`, 3600 s by default, without a dump. Rotation moves the session to a new connection before that.

### Policy

`newRotationPolicy` reads `GET /info` once, at pool creation. Rotation stays off, without failing, when the server reports no `/info`, a disabled session or turn timeout, or a session timeout shorter than `2 × (turn timeout + margin)`. A hostile or misconfigured server can therefore cause no rotation, never a rotation storm.

`WebSocketOptions.RotationMargin` is the lead time; 0 means 30 s. `NewDocker` enables rotation; `NewWebSocket` needs `RotateSessions`.

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
- The host registry, host objects, the stop policy and `Session` identity are the parent's; they are unaffected.
- Telemetry keeps upstream names: the rotation appears as a `dump` span, a `load` span and a new `session` span.
- During a rotation the pool MAY hold one worker more than `MaxProcesses` while the old connection closes. `NewDocker` sizes the server at `2 × MaxProcesses` sessions for this reason.

### Failure

A failed rotation ends the session with `*RotationError`, which matches `errors.Is(err, ErrSessionLost)`.

| Failure | `Dump` | Recovery |
|---|---|---|
| `Dump` fails, including a dump over the 256 MiB frame limit | nil | the state is gone; start a new session |
| `Handoff` fails | the envelope | `LoadSession` it on a new session |
| every reconnect attempt fails | the envelope | same |
| `Load` is rejected, for example by another dump key | the envelope | the state cannot be restored on that server |

A rotation before an operation returns the error from that call, and the operation does not run. A rotation from the idle timer makes `Session.Err()` and the next call report it.

## Ownership

- `NewDocker` owns its `DockerSupervisor`. `Pool.Shutdown` stops the container after closing the sessions; `Pool.Close` stops it once the last open session closes, because `Close` leaves checked-out sessions running.
- A supervisor passed in `WebSocketOptions` belongs to the caller. The pool never closes it.
