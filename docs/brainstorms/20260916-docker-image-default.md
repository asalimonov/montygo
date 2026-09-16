# Brainstorm: montygo uses the published monty-server image by default

Date: 2026-09-16

## Initial input

User request, item 3 of the v0.3.0 release request, verbatim:

> montygo package should try to use monty docker image from this repo with the same verion as and this package, excep cases when version and path to docker image specified in parameters or env var ( this need to brainstrom and implement)

Items 1 and 2 of the same request: publish the Go module as `v0.3.0`, and build and publish the `monty-server` image to GHCR, linked to `github.com/asalimonov/montygo`.

Questions for this session:

- What "use the image" means: a new backend, or a pool over WebSocket to a managed container.
- How the default image reference is derived from the package version.
- Override precedence between options and environment variables.
- What happens for versions that have no published image: pseudo-versions, `(devel)`, `0.0.0-unknown`, `-dirty` builds.

## Current state

### Backends

| Backend | Constructor | Workers | Reuse |
|---|---|---|---|
| `BackendAuto` | `New` | native when `FindMontyBinary` resolves, else wasm | per backend |
| `BackendNative` | `New` | `monty subprocess`, unix only, empty environment | prewarm, `Reset`, reuse |
| `BackendWasm` | `New` | embedded wasip1 worker under wazero | prewarm, `Reset`, reuse |
| `BackendWebSocket` | `NewWebSocket` | one remote worker per checkout | single-use, no prewarm |

- `BackendAuto` never touches the network or Docker.
- The package has no Docker code. The image is referenced only by the Makefile, CI and `tests/network`.
- `resolveSpawner` rejects `BackendWebSocket` in `New`: "use NewWebSocket for the WebSocket backend".
- `Pool` has no hook that runs on `Close` or `Shutdown`.

### Image

- `docker/Dockerfile`, final stage `scratch`, `linux/amd64` and `linux/arm64`.
- `/usr/local/bin/monty-server` is the entrypoint. `/usr/local/bin/monty` is the upstream worker at `MONTY_REV`.
- `USER 65532:65532`, `EXPOSE 8000`, `CMD --host 0.0.0.0`.
- The server refuses to start without `MONTY_SERVER_DUMP_KEY`.
- Server defaults, reported by `GET /info`, differ from local worker defaults:

| Limit | monty-server default | local worker default |
|---|---|---|
| max memory | 64 MiB | worker default, unlimited via `Unlimited` |
| max duration | 60 s | none |
| max sessions | 64 | `MaxProcesses` |
| max sessions per client | 10 | n/a |
| idle timeout | 60 s | none |
| turn timeout | 300 s | `RequestTimeout`, 0 disables |

### Release pipeline

- The `release` job in `.github/workflows/ci.yml` runs on `v*` tags after `docker` and `docker-arm64`. It runs `make docker-push REGISTRY=ghcr.io/<owner>`, which pushes `ghcr.io/asalimonov/monty-server:$(VERSION)` only. There is no `latest`.
- As of 2026-09-16 nothing is published: GHCR has no `monty-server` package, and `proxy.golang.org` lists no versions of the module.

### Package version at run time

| Consumer state | `BindingVersion()` | Image tag exists |
|---|---|---|
| `go get github.com/asalimonov/montygo@v0.3.0` | `0.3.0` | yes, after the release job |
| `go get` of an untagged commit | `0.3.1-0.20260916…-<hash>` | no |
| `replace … => ../` without ldflags | `(devel)` | no |
| this repository's tests without ldflags | `0.0.0-unknown` | no |
| `make` builds after a tag | `0.3.0-<hash>`, `-dirty` | no |

Only an exact `X.Y.Z[-pre]` release has an image tag.

## Measurements

Local Docker 29.6.1, linux/arm64 VM on macOS, image `monty-server:0.2.0-dirty`, already pulled.

| Scenario | Time |
|---|---|
| `docker run -i --rm --entrypoint /usr/local/bin/monty IMAGE subprocess`, start to exit on stdin EOF | 276–301 ms |
| `docker run -d --rm -p 127.0.0.1::8000 IMAGE`, start to `/health` 200 | 243 ms |

A pull of the 60 MB image is not included.

## Thoughts

### Option A: Docker subprocess backend

Each pool worker is one container running `monty subprocess`, reached through `docker run -i` stdio. Framing is the native 4-byte framing.

- Pool semantics stay unchanged: prewarm, `Reset`, reuse, `MaxCheckoutsPerWorker`, plain local dumps.
- No server, no dump key, no port.
- About 290 ms per worker start. Prewarm and reuse hide it after the first checkout.
- Killing the `docker` CLI does not stop the container. SIGKILL cannot be proxied. `Kill` needs `docker kill <name>`, so every worker needs a unique container name.
- The exit status arrives as the CLI's exit code: 137 instead of signal 9. Exit code 65 (memory kill) survives. Failure classification needs a mapping.
- When the host process dies, stdin closes. A worker blocked on stdin exits; Python in a busy loop runs until its `MaxDuration`, or forever without one.
- The CLI needs the host environment (`DOCKER_HOST`, `DOCKER_CONFIG`, `HOME`). The native rule "empty environment" applies to the container, not to the CLI.
- It adds a fourth `worker.Worker` transport that every ported test runs on.

### Option B: managed monty-server container behind a WebSocket pool

`NewDocker` starts one server container bound to `127.0.0.1` on an ephemeral port, waits for `/health`, checks `/info`, and returns a WebSocket pool that stops the container on `Close` and `Shutdown`.

- No new transport. It reuses the server and the WebSocket worker that `make test-docker` and `tests/network` already test.
- Workers inside the container spawn natively in milliseconds.
- One container per pool. The server owns worker kills.
- WebSocket semantics leak into the pool: single-use workers, `DisconnectError`, signed `MTYD` dumps that local workers reject, a 10 s default `RequestTimeout`.
- Server limits must be lifted to match local defaults: memory, duration, sessions per client, idle and turn timeouts.
- The port is unauthenticated. Any local user can open sessions while the pool lives.
- `Pool` needs an owner hook to stop the container.
- When the host process dies, the container keeps running.

### Option C: reference only

The package exposes the default image reference and its override. The caller runs the container and uses `NewWebSocket`. It does not satisfy "try to use".

## Questions and answers

### Q1. What does "use the image" mean?

Options offered: managed server (B), container per worker (A), reference only (C). Recommendation: B.

**Answer: B, managed server.** `NewDocker(ctx, DockerOptions)` starts one `monty-server` container and returns a WebSocket pool that stops the container on `Close` and `Shutdown`.

Consequences:

- No new `worker.Worker`. The container lifecycle belongs to package `montygo`, not to `internal/worker` or `internal/pool`.
- `Pool` gains an owner hook that runs after the inner pool has closed.
- Sessions are single-use, dumps are signed `MTYD` envelopes, and failures follow the WebSocket table in `docs/architecture/websocket.md`.
- The server limits that would silently tighten local defaults must be set explicitly on the container.

### Q2. Does `BackendAuto` try Docker?

Options offered: opt-in only; auto order native, Docker, wasm; an environment switch that makes `New` delegate to `NewDocker`. Recommendation: opt-in only.

**Answer: opt-in only.** Docker is reached only through `NewDocker`. `BackendAuto` stays native, then wasm, offline. "Try to use the image" applies inside `NewDocker`: the image reference is derived from the package version unless overridden.

Consequences:

- `New` and `Options` do not change. `resolveSpawner` keeps rejecting non-local backends.
- `BackendDocker` is a reporting value of `Pool.Backend()` only, like `BackendWebSocket`. `New` with `BackendDocker` fails with an `OptionError` that names `NewDocker`.
- No existing caller pulls an image or opens a port.

### Q3. Default tag when nothing overrides it

Options offered: release tag else error; release tag else `latest`; release tag else base tag. Every option kept a `GET /info` compatibility check.

**Answer, free form:**

> Release tag. When montygo has version X.Y.Z-{hash}/{dirty}, then it should try the same version `X.Y.Z-{hash}/{dirty}`, when this version is not available - then try `X.Y.Z` version to avoid the most situations when we change montygo but image state the same quite long time. Internal version of `monty` in our docker image (like 0.0.23) should stay internal.

Resolution:

- Candidate tags come from `BindingVersion()`, in order: the exact version, then its base release `X.Y.Z[-pre]`. A release version yields one candidate.
- Image tags carry the montygo version only. `MontyVersion` and `UpstreamRev` never appear in a tag. Old local tags such as `monty-server:0.0.23-f8acf4fa` are not candidates.
- The base is the leading `X.Y.Z[-pre]` of the `scripts/version.sh` form `base[-hash][-dirty]`.
- Upstream compatibility stays internal: the server refuses a foreign protocol version, and the worker compares `Configure.monty_version` with its own build.

Open after Q3: where a candidate is "available" (local daemon or registry), and what Go pseudo-versions, `(devel)` and `0.0.0-unknown` resolve to.

### Q4. Where a candidate is available

Options offered: local then pull; local plus CI dev images; registry only. Recommendation: local then pull.

**Answer: local, then pull.**

- For each candidate in order: `docker image inspect <ref>` succeeds → use it. Otherwise `docker pull <ref>` succeeds → use it. Otherwise try the next candidate.
- Any pull failure moves to the next candidate; error text is not classified. Observed on Docker 29.6.1: a missing tag exits non-zero with `failed to resolve reference "<ref>": <ref>: not found`, on GHCR and Docker Hub alike.
- When every candidate fails, `NewDocker` returns one error listing each reference and its failure.
- A non-release candidate is pulled too. Against GHCR it costs one failed lookup, because CI publishes only release tags. It is kept because an overridden repository MAY hold dev tags.
- CI keeps publishing only `v*` release tags.
- `make docker-build` additionally tags `ghcr.io/asalimonov/monty-server:$(VERSION)`, so a local dev build resolves as the exact candidate.

### Q5. Go pseudo-versions, `(devel)` and `0.0.0-unknown`

Options offered: base else error; base else `latest`; error for all. Recommendation: base else error.

**Answer: base, else error.**

Candidate derivation from `BindingVersion()`:

| Version | Form | Candidates |
|---|---|---|
| `0.3.0`, `0.4.0-rc.1` | release | `0.3.0` / `0.4.0-rc.1` |
| `0.3.0-3f2a9c1`, `0.3.0-3f2a9c1-dirty`, `0.4.0-rc.1-3f2a9c1` | `scripts/version.sh` dev | exact, then base |
| `0.3.1-0.20260916120000-abcdef123456` | Go pseudo-version after release `v0.3.0` | `0.3.0` |
| `0.4.0-rc.1.0.20260916120000-abcdef123456` | Go pseudo-version after prerelease `v0.4.0-rc.1` | `0.4.0-rc.1` |
| `0.0.0-20260916120000-abcdef123456` | Go pseudo-version, no tag | none → error |
| `(devel)`, `0.0.0-unknown` | unknown | none → error |
| `0.0.0-3f2a9c1` | `scripts/version.sh`, no tag reachable | exact, then `0.0.0`; both fail at pull |

- Pseudo-version forms follow the Go modules reference: `vX.0.0-yyyymmddhhmmss-abcdefabcdef`, `vX.Y.Z-pre.0.yyyymmddhhmmss-abcdefabcdef`, `vX.Y.(Z+1)-0.yyyymmddhhmmss-abcdefabcdef` (https://go.dev/ref/mod#pseudo-versions).
- A pseudo-version is detected by its 14-digit timestamp and 12-hex-digit revision, so a `scripts/version.sh` form with a 7+ digit short hash is not mistaken for one.
- A pseudo-version never yields an exact candidate: no registry holds such a tag.
- No candidate is an `OptionError` that names the image and version overrides and the `-ldflags -X github.com/asalimonov/montygo.buildVersion=` stamp.

### Q6. Override knobs, names and precedence

Options offered: `MONTYGO_DOCKER_*` with two knobs; `MONTY_DOCKER_*` with two knobs; one reference knob. Recommendation: `MONTYGO_DOCKER_*`.

**Answer: `MONTYGO_DOCKER_*`, two knobs.**

| Knob | Option | Variable | Default |
|---|---|---|---|
| repository | `DockerOptions.Image` | `MONTYGO_DOCKER_IMAGE` | `ghcr.io/asalimonov/monty-server` |
| version | `DockerOptions.Version` | `MONTYGO_DOCKER_VERSION` | derived from `BindingVersion()` (Q3, Q5) |

- Each knob resolves independently: a non-empty option, else a non-empty variable, else the default.
- An explicit version is one candidate, `<repository>:<version>`, with no fallback.
- A repository with a `:tag` or `@digest` is one candidate, used verbatim. An explicit version as well is an `OptionError`, whether each came from an option or a variable.
- An overridden repository keeps the derived candidates: `registry.local/monty-server:0.3.0-abc1234`, then `registry.local/monty-server:0.3.0`.
- A tag is detected by a `:` after the last `/`, so `localhost:5000/monty-server` has no tag.
- CLAUDE.md changes: `MONTYGO_*` names library variables as well as `tests/network` variables.

### Q7. How montygo talks to Docker

Options offered: the `docker` CLI through `os/exec`; the Engine API over the socket; the Docker Go SDK. Recommendation: CLI.

**Answer: the `docker` CLI through `os/exec`.**

- No new Go dependency. The root module stays cgo-free and lean.
- The CLI inherits the host environment, so Docker contexts, `DOCKER_HOST`, `DOCKER_CONFIG` and credential helpers work. The native worker's empty-environment rule applies to sandbox children, not to the CLI.
- `DockerOptions.Command` (default `docker`) lets a compatible CLI such as `podman` stand in. Compatibility of other CLIs is not tested.
- Parsed outputs: the container ID printed by `docker run -d`, and `docker port <id> 8000/tcp`.

Probe with the hardened flags, Docker 29.6.1:

```
docker run -d --rm --read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 512 \
  --label io.montygo.pool=probe -p 127.0.0.1::8000 \
  -e MONTY_SERVER_DUMP_KEY=… -e MONTY_SERVER_MAX_SESSIONS=4 -e MONTY_SERVER_MAX_SESSIONS_PER_CLIENT=0 \
  -e MONTY_SERVER_MAX_DURATION=0 -e MONTY_SERVER_MAX_MEMORY_MIB=0 \
  -e MONTY_SERVER_IDLE_TIMEOUT=0 -e MONTY_SERVER_SESSION_TIMEOUT=0 -e MONTY_SERVER_TURN_TIMEOUT=0 \
  monty-server:0.2.0-dirty
```

| Step | Result |
|---|---|
| `docker port <id> 8000/tcp` | `127.0.0.1:55759` |
| start to `/health` 200 | 332 ms |
| `/info` limits | every timeout and ceiling 0, `max_sessions` 4, `max_sessions_per_client` 0 |
| `docker ps -q --filter label=io.montygo.pool=probe` | the container |
| `docker stop -t 10` with no sessions | 261 ms, and `--rm` removed the container |

### Q8. Server limits of the managed container

Status: paused for clarification. The explanation is in `docs/reports/20260916-docker-server-limits.md`.

User clarification, free form:

> I suppose there is no specified lifespan of monty server which handles websocket connection. WebSocket session has 3600 seconds and should be re-established in 1h or sometime to avoid losing data/info between. The client should reestablish it to monty rust server insider the container.

Verified against `../monty` at `f8acf4fa`:

- `../monty` has no server code. `docs/server.md` specifies Full Monty, closed-source, distributed as an image. `server/` here reimplements it.
- The server has no lifetime limit. `--session-timeout`, default 3600, is the "maximum total session lifetime" of one WebSocket session.
- Idle, session and turn timeouts close the connection without a dump. Only SIGTERM drain sends `ShutdownDump` with a dump.
- The upstream Python client raises `MontyDisconnectError` and advises "Retry on a fresh session". It has no reconnect with state.
- montygo raises `*DisconnectError`. `Pool.Slot` re-checks out without restoring state.
- Re-establishing without data loss needs a dump before the timeout and a load on the new session. No client implements this today.

Options offered after the clarification: disable every optional limit; keep the 3600 s session timeout and add proactive rotation; keep upstream defaults and leave `DisconnectError` to the caller.

**Answer: keep the session timeout, add proactive rotation.**

#### Rotation model

A rotation moves one `Session` onto a new connection, and the caller keeps the same `*Session`.

```
Dump on the old connection      → signed envelope in Go memory
close the old connection        → server releases the permit, kills worker 1
checkout a new connection       → same wire.Configure, new worker 2
Restore(envelope)               → suspension count carried over, not reset
swap Session.co, re-arm the loss watcher
```

Deadline knowledge:

- `GET /info` gives `session_timeout_s` and `turn_timeout_s`. `NewDocker` reads it after `/health`.
- The pool records each connection's upgrade time. `deadline = upgrade + session_timeout`.

Trigger invariant:

- The server bounds one complete request, including host callbacks and suspensions, by `turn_timeout`.
- Before admitting an execution, if `now + turn_timeout + margin ≥ deadline`, the session rotates first.
- An execution admitted earlier ends, by the turn timeout, before the deadline. No feed crosses the session deadline.
- An idle session gets a timer at `deadline − margin` that rotates it while it stays idle.

Preconditions found in code:

- `Session` holds one `co *pool.Checkout` (`session.go:41`) and does not keep its `wire.Configure`. Rotation needs it stored.
- `Checkout.Restore` resets `budget.suspensionsSeen` (`internal/pool/checkout.go:519`). Rotation needs a restore that keeps it.
- `Checkout.Dump` exists (`internal/pool/checkout.go:494`).

Dependencies on other limits:

- The invariant needs `turn_timeout > 0` and `turn_timeout + margin < session_timeout`.
- The idle timeout drops an idle session regardless of rotation, unless it is disabled or longer than the session lifetime.

### Q9. Other server limits of the managed container

Options offered: keep session and turn timeouts; keep every upstream default; keep only the session timeout. Recommendation: keep session and turn.

**Answer: keep session and turn timeouts.**

| Server variable | Value set by `NewDocker` | Reason |
|---|---|---|
| `MONTY_SERVER_SESSION_TIMEOUT` | image default, 3600 | kept; rotation handles it |
| `MONTY_SERVER_TURN_TIMEOUT` | image default, 300 | kept; bounds every execution, so the rotation invariant holds |
| `MONTY_SERVER_IDLE_TIMEOUT` | `0` | an idle `Slot` or REPL session MUST survive |
| `MONTY_SERVER_MAX_MEMORY_MIB` | `0` | `CheckoutOptions.Limits` governs, as on native |
| `MONTY_SERVER_MAX_DURATION` | `0` | same |
| `MONTY_SERVER_MAX_SESSIONS_PER_CLIENT` | `0` | every connection shares one peer IP |
| `MONTY_SERVER_MAX_SESSIONS` | `2 × MaxProcesses` | a rotation's new upgrade MUST NOT hit 503 while the old permit is being released |
| `MONTY_SERVER_DUMP_KEY` | random 32 bytes, hex, per container | required; dumps stay inside one container |

- `DockerOptions.Env` overrides any of these. `NewDocker` then reads the effective values from `GET /info`, never from its own table.
- Accepted cost: one execution longer than the turn timeout fails on Docker and not on native.
- A paused `FeedStart` snapshot is also bounded: the server's turn deadline keeps running through suspensions (`docs/architecture/server.md`, Turns).

Findings in `lifecycle.go`:

- `Session.attach` starts a watcher that calls `terminateSession` when `co.Done()` fires (`lifecycle.go:79–121`). Closing the old connection during a rotation would end the session. The watcher MUST be cancelled per attachment before the old connection closes.
- The watcher reports `DisconnectError` only when `s.pool.backend == BackendWebSocket`. A `BackendDocker` pool would report `CrashedError`. Classification MUST use `co.Kind() == worker.KindWebSocket`, which is a transport-kind check the conventions allow.

### Q10. Which pools rotate

Options offered: both, Docker on by default; `NewDocker` only; both, on by default. Recommendation: both, Docker on by default.

**Answer: both pools; on for `NewDocker`, opt-in for `NewWebSocket`.**

- Rotation lives in package `montygo`, in the session layer, keyed on the pool's rotation settings. `internal/pool` gains only the primitives it needs.
- `NewDocker` always enables rotation with the limits read from `GET /info`.
- `WebSocketOptions.RotateSessions bool`, default false. When true, `NewWebSocket` reads `GET /info` once. `ErrNoServerInfo`, `session_timeout_s == 0` or `turn_timeout_s == 0` disables rotation for that pool without failing. Any other `/info` error fails `NewWebSocket`.
- Off by default keeps `NewWebSocket` at parity with upstream `AsyncMontyWebsocket`, which never rotates. `docs/parity/api.md` lists rotation as a Go addition.
- Local backends never rotate.

### Q11. Rotation failure

Options offered: `RotationError` carrying the dump; reuse `ShutdownError`; retry, then `RotationError`. Recommendation: `RotationError` with the dump.

**Answer, free form:**

> It should user retry policy (3 times within in 5 seconds each), if was not successfull try to stop and start the container again.

Interpretation, pending confirmation:

- Up to 3 attempts. Each attempt, a new connection plus `Restore`, is bounded by 5 s.
- After the third failure, a `NewDocker` pool restarts its container once, then retries.
- A `NewWebSocket` pool has no container. After the third failure the session ends with an error carrying the dump.

Consequences found while modelling:

- The dump is not retried. A failed `Dump` means the old connection and its state are gone, so only the new connection and the restore are retryable.
- The dump key MUST be generated once per pool and reused after a restart. A container with another key rejects the envelope with `ValueError: invalid session dump signature`.
- A restart ends every other session of that container. On SIGTERM, each session's next request gets `ShutdownError` with a dump; sessions silent through `--drain-grace` are dropped.
- A restart can change the published host port. The pool's dialer URL MUST be replaceable.
- Restarts MUST be single-flight per pool. Rotations that fail together share one restart, tracked by a container generation number.

Measurement, Docker 29.6.1, container without `--rm`, `-p 127.0.0.1::8000`:

| Step | Result |
|---|---|
| `docker restart -t 5`, then `/health` 200 | 354 ms |
| host port across three restarts | 56505 → 56509 → 56512 → 56514 |
| `docker kill` + `docker start`, then `/health` 200 | 373 ms, port 56517 |
| environment after restart | kept, including `MONTY_SERVER_DUMP_KEY` |

- A restart keeps the dump key but changes the port every time. After a restart the endpoint MUST be resolved again.

### Q12. When a container may restart

Options offered: only when unhealthy; always after 3 failed attempts; a background supervisor. Recommendation: only when unhealthy.

**Answer, free form:**

> Only when it is enabled. This library should allow to implement external supervisor which can start containers on remote hosts and pass to montygo just paths and creds for connection to establish websocket connections.

Resolution:

- Server restarts are opt-in. Default: no restart. After the attempts fail, the session ends with an error that carries the dump.
- The pool does not own server placement. It asks a supervisor for the current endpoint (URL and credentials) before each dial, and for a restart only when restarts are enabled.
- `NewDocker` is one supervisor built into the library: a local `docker` CLI starting `monty-server`.
- An external supervisor MAY start servers anywhere (remote hosts, Kubernetes) and hand montygo only the endpoint and credentials.
- The dump key obligation moves to the supervisor: a restarted or replacement server MUST verify dumps signed before the restart.

### Q13. Supervisor contract

Options offered: an exported interface; function fields on `WebSocketOptions`; a separate constructor. Recommendation: interface.

**Answer: interface.**

```go
type ServerEndpoint struct {
	URL       string            // ws:// or wss://
	Headers   map[string]string // credentials
	TLSConfig *tls.Config
}

type ServerSupervisor interface {
	Endpoint(ctx context.Context) (ServerEndpoint, error)
	Restart(ctx context.Context, failed ServerEndpoint) error
}

type RecoveryPolicy struct {
	Attempts       int           // 0 means 3
	AttemptTimeout time.Duration // 0 means 5s
	RestartServer  bool          // opt-in
}
```

- `WebSocketOptions.Supervisor` replaces `URL` and `ConnectHeaders`. Setting it with either is an `OptionError`.
- montygo makes restarts single-flight: failures against the same endpoint share one `Restart` call.
- `NewDocker` builds and owns montygo's docker supervisor. `Pool.Shutdown` and `Pool.Close` stop its container.
- The pool never closes an external supervisor. Its creator does.

Findings in `internal/worker/websocket.go`:

- `WebSocketDialer.URL` and `TLSConfig` are fixed fields. `Spawn`, `HealthCheck` and `GetJSON` read `d.URL`.
- Per-checkout headers already reach `Spawn` through the context: `WithConnectHeaders` and `ConnectHeaders(ctx)`.
- A resolved endpoint MAY travel the same way: `WithEndpoint(ctx, url, tls)`, read by `Spawn`, with `d.URL` as the fallback for plain pools.

### Q14. Scope of the recovery policy

Options offered: every dial; rotation only; checkout only. Recommendation: every dial.

**Answer: every dial.**

```
dial(ctx):                       // Checkout and rotation
  for round in [before restart, after restart]:
    for i in 1..Attempts:
      ep := supervisor.Endpoint(ctx)
      w, err := dial(ep) within AttemptTimeout
      if ok: return w
    if !RestartServer or round == after: break
    restartOnce(ep)              // single-flight
  return SpawnError (Checkout) | RotationError{Dump} (rotation)
```

- A connection that drops mid-session is not recovered: no dump exists. It ends with `DisconnectError`, as today.
- New checkouts and `Slot` re-checkouts recover after a server crash without caller code.

### User statement: local and external supervisors

> I suppost montygo should implement own local supervisor. And application which uses montygo can implement its own.

Resolution:

- montygo exports `DockerSupervisor`, a `ServerSupervisor` driving the local `docker` CLI.
- `NewDockerSupervisor(ctx, DockerOptions)` resolves the image (Q3–Q6), starts the container (Q9) and returns the supervisor.
- `NewDocker(ctx, DockerOptions)` is `NewDockerSupervisor` plus `NewWebSocket` with that supervisor, rotation on, and ownership: the pool closes the supervisor on `Shutdown` and `Close`.
- An application MAY pass a `DockerSupervisor` to `NewWebSocket` itself, wrap it, or implement `ServerSupervisor` for remote hosts.

### Q15. Local container lifecycle and orphans

Options offered: eager, labelled, no reaping; eager with reaping of dead-PID containers; one shared named container per image. Recommendation: eager, labelled, no reaping.

**Answer, free form:**

> Add reaper as interface and stub implementation with TODOs for future tasks.

Resolution:

- Eager start: `NewDockerSupervisor` resolves and pulls the image, runs the container, waits for `/health` and checks `/info` before it returns.
- One container per supervisor, without `--rm`, because `Restart` needs the container to persist.
- Labels: `io.montygo.supervisor=<random id>`, `io.montygo.version=<BindingVersion>`, `io.montygo.pid=<os.Getpid()>`.
- `Close` runs `docker stop`, then `docker rm -f`.
- Reaping is an interface, called once by `NewDockerSupervisor` before `docker run`:

```go
// OrphanReaper removes servers left behind by a process that ended without closing its supervisor.
type OrphanReaper interface {
	Reap(ctx context.Context) error
}
```

- `DockerOptions.Reaper` nil uses a stub that reaps nothing and returns nil. Its body carries `TODO` and `NotImplemented` markers naming the future work: list containers by the `io.montygo.supervisor` label, skip live owners, remove the rest.
- A reaper error fails `NewDockerSupervisor`, so a real reaper cannot fail silently.
- The docs give the manual cleanup: `docker rm -f $(docker ps -aq --filter label=io.montygo.supervisor)`.

### Q16. Testing

Options offered: three layers; integration only; Docker in the default matrix. Recommendation: three layers.

**Answer: three layers.**

| Layer | Runs | Covers |
|---|---|---|
| root unit tests | `go test ./...`, no Docker | candidate tags, option and variable precedence, reference parsing, the `docker` command sequence through a fake CLI script in `DockerOptions.Command`, single-flight restart, rotation and recovery with a fake `ServerSupervisor` over the WebSocket test relay |
| root suite, `docker` backend | `make test-docker` with `MONTY_TEST_BACKENDS=docker` and `MONTYGO_DOCKER_IMAGE=$(IMAGE):$(IMAGE_TAG)` | every ported test through `NewDocker`; replaces the hand-written `docker run` in the Makefile |
| `tests/network` | `make test-network` | rotation end to end with `MONTY_SERVER_SESSION_TIMEOUT=20` and `MONTY_SERVER_TURN_TIMEOUT=3`; `docker kill` with `RestartServer` on and off |

## Verification

### Facts checked in upstream `crates/monty-proto/proto/monty/v1/monty.proto`

| Topic | Upstream text | Effect on rotation |
|---|---|---|
| `Dump` | "an opaque serialized snapshot of the current session state (idle or suspended). The child stays usable afterwards" | the old connection stays usable until closed |
| limits | "the limits travel inside the opaque state bytes" | the restored session keeps its limits |
| execution clock | `total_execution_micros` "survives Dump/Load" | the duration budget is neither reset nor extended |
| `cwd` | "Empty keeps the session's current directory"; the parent sends the first mount on the session's first feed | the new checkout MUST inherit `cwdSet`, or the next feed resets the directory |
| `InstallDependencies` | "Only the embedded-CPython worker honors this; the Monty sandbox child rejects it with an `Error`" | not applicable to the image's worker |

### Inconsistencies with the current design, and proposed resolutions

| # | Current rule or text | Conflict | Proposed resolution |
|---|---|---|---|
| I1 | CLAUDE.md: pool and session code MUST NOT branch on the transport | rotation is session logic that only WebSocket pools use | rotation is keyed on the pool's rotation settings (`p.rotation != nil`), never on the transport; the loss watcher classifies by `co.Kind()`, which the rule allows |
| I2 | overview: the worker is untrusted; a reported limit never loosens a configured one | `/info` values drive rotation timing | rotation is enabled only when `session_timeout_s > 0`, `turn_timeout_s > 0` and `session ≥ 2 × (turn + margin)`; otherwise it is off, without failing, so a hostile server can cause at most no rotation, not a rotation storm |
| I3 | `pool.md`: a restored dump resets the suspension count | rotation would reset it | an internal restore carries `suspensionsSeen` and `cwdSet`; user-facing `LoadSession` and `LoadSnapshot` keep today's reset |
| I4 | `websocket.md`: `RequestTimeout` is the dial budget | the recovery policy adds `AttemptTimeout` | with a supervisor, `AttemptTimeout` bounds each attempt (dial, `Configure`, and `Load` during rotation); `RequestTimeout` stays the per-turn deadline |
| I5 | `pool.go`/`pool.md`: `Close` retires idle workers; checked-out sessions continue | Q13 said `Close` stops the container | `Shutdown` stops the container after closing sessions; `Close` marks the pool closed and stops the container once the last open session closes |
| I6 | `docker.md`: the dump key SHOULD NOT be on a command line | `docker run -e KEY=value` puts it in the host's `ps` | the CLI runs with `MONTY_SERVER_DUMP_KEY` in its own environment and passes `-e MONTY_SERVER_DUMP_KEY` without a value |
| I7 | telemetry names MUST match upstream | rotation emits spans the caller did not request | no new names: rotation appears as upstream `dump` and `load` housekeeping spans and a new `session {script_name}` span; `docs/parity/api.md` records it |
| I8 | `changelogs/v0.3.0.md`, README, `docs/parity/api.md`, golden file | new exported API | all updated in the same change; the feature ships in v0.3.0 |
| I9 | `docker.md`: `-p 127.0.0.1::8000` | with a remote `DOCKER_HOST`, the port opens on the remote host | `DockerSupervisor` supports local daemons only; a health wait that fails names this case; remote hosts use an external `ServerSupervisor` |

**Answer: I1–I9 accepted as proposed.**

Rotation margin: `WebSocketOptions.RotationMargin`, 0 means 30 s. With image defaults, a feed starts only when more than 330 s remain, and the idle timer fires at 3570 s.

## NOT CONSIDERED & TODO

| # | Item | Tags | Proposed handling |
|---|---|---|---|
| N1 | real `OrphanReaper` implementation | postponed | stub reaper; body marked `TODO` and `NotImplemented` (Q15) |
| N2 | a dump larger than the 256 MiB envelope limit | too-complex | rotation fails with `RotationError{Dump: nil}`; code comment names the scenario and `NotImplemented` chunked dumps |
| N3 | `Slot` restoring from `RotationError.Dump` automatically | potential-improvement, porting:improvement-over-upstream | default behaviour: `Slot` re-checks out without state, as today |
| N4 | remote Docker daemons (`DOCKER_HOST=ssh://`, `tcp://`) in `DockerSupervisor` | postponed | not supported (I9); documented |
| N5 | `podman` or `nerdctl` through `DockerOptions.Command` | potential-improvement | accepted but untested; documented |
| N6 | authentication on the loopback port | too-complex | none, matching Full Monty; documented in security notes |
| N7 | draining or rotating other sessions before a restart | too-complex | they end with `ShutdownError` carrying a dump if they send a request within `--drain-grace`, else `DisconnectError` |
| N8 | jitter so sessions created together do not rotate together | potential-improvement | no jitter |
| N9 | host sleep or suspend past the deadline | potential-improvement | the monotonic deadline is already missed; the session ends with `DisconnectError` |
| N10 | pushing `latest` and prerelease policy for image tags | postponed | release job pushes the exact tag only |
| N11 | Windows hosts (named pipes, Docker Desktop) | postponed | not tested |
| N12 | verifying image signatures or provenance before `docker run` | potential-improvement | not done |
| N13 | default `--memory` and `--cpus` for the container | potential-improvement | none; `DockerOptions.RunArgs` |

**Answer: N1–N13 accepted as proposed.**

## Summary

### Findings

- The published image is a server image. Using it as-is places `monty-server` between montygo and the worker, and the server applies per-session policy on top of the caller's limits.
- `../monty` has no server code. Its `docs/server.md` specifies closed-source Full Monty. `server/` reimplements it with the same defaults.
- The server has no lifetime limit. Idle, session and turn timeouts apply per WebSocket session, close without a dump, and neither upstream nor montygo clients reconnect with state.
- A dump carries limits and the execution clock. A `docker restart` keeps the environment but changes the published port.

### Resolutions

| # | Decision |
|---|---|
| Q1 | `NewDocker` starts a `monty-server` container and returns a WebSocket pool |
| Q2 | opt-in only; `BackendAuto` unchanged |
| Q3 | tag from `BindingVersion()`: exact, then base release; Monty version never in a tag |
| Q4 | each candidate: local image, else `docker pull`, else next |
| Q5 | Go pseudo-versions resolve to their base release; no base is an `OptionError` |
| Q6 | `DockerOptions.Image` / `MONTYGO_DOCKER_IMAGE` and `DockerOptions.Version` / `MONTYGO_DOCKER_VERSION`; option > variable > default |
| Q7 | `docker` CLI through `os/exec` |
| Q8 | keep the session timeout; montygo rotates sessions before it |
| Q9 | container keeps session and turn timeouts; idle, memory, duration, per-client quota off; max sessions `2 × MaxProcesses`; random dump key |
| Q10 | rotation on for `NewDocker`, opt-in `RotateSessions` for `NewWebSocket` |
| Q11 | retries: 3 attempts, 5 s each; then an optional server restart |
| Q12 | restart only when enabled; placement belongs to a supervisor |
| Q13 | exported `ServerSupervisor` interface in `WebSocketOptions.Supervisor` |
| Q14 | the recovery policy applies to every dial, checkout and rotation |
| — | montygo exports `DockerSupervisor`; applications MAY implement their own |
| Q15 | eager, labelled container per supervisor; `OrphanReaper` interface with a stub |
| Q16 | root unit tests with fakes, a `docker` root backend, `tests/network` end to end |
| I1–I9 | accepted as proposed |
| N1–N13 | accepted as proposed |

The target architecture is in `docs/brainstorms/20260916-docker-image-default-target.md`.
