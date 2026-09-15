# Overview

montygo is a Go parent for [Monty](https://github.com/pydantic/monty) workers. It runs untrusted Python in a child process, inside a WebAssembly sandbox, or on a remote host, never as host code. It has no native in-process interpreter and no cgo.

The public surface is two packages: `montygo` (pools, sessions, snapshots, host objects, mounts, telemetry) and `osaccess` (in-memory OS helpers). Everything else is internal.

The repository also builds `monty-server`, a WebSocket server for Monty workers, and its Docker image.

## Artifacts

| Artifact | Built by | Contents |
|---|---|---|
| Go module `github.com/asalimonov/montygo` | `go build` | packages `montygo` and `osaccess`, internal packages, the embedded wasm worker |
| `internal/wasmblob/monty.wasm.zst` | `make build-wasm` | the worker for `wasm32-wasip1`, zstd-compressed and checked in |
| `examples` module | `make examples` | ports of the upstream examples and the REPL |
| `monty-server` binary | `cargo build` in `server/` | WebSocket server that runs one `monty subprocess` per session |
| `monty-server:<Version>-<UpstreamRev>` image | `make docker-build` | `scratch` image with `monty-server` and `monty` for `linux/amd64` and `linux/arm64` |
| `monty-pyclient:<Version>-<UpstreamRev>` image | `make docker-build-pyclient` | Python client fixture for `tests/network` |
| `tests/network` module | `make test-network` | integration tests of the server image |

## Key technologies

| Technology | Used for | Why |
|---|---|---|
| Go 1.25, no cgo | the whole parent | Builds with the standard toolchain and cross-compiles. An interpreter crash or allocator abort cannot corrupt the host process. |
| Monty worker (`monty subprocess`, Rust) | executing Python | The upstream worker is the only execution engine. montygo reuses it unchanged, so interpreter behaviour, type checking and error text match upstream exactly. |
| Protocol Buffers (`monty.v1`) with `protowire` | messages between parent and worker | It is upstream's wire format. A hand-written codec on `google.golang.org/protobuf/encoding/protowire` decodes straight into the Go value model, charges a memory budget and validates while decoding. Generated `montypb` types serve only as a test oracle. |
| wazero | the embedded worker | A pure-Go WebAssembly runtime, so the wasm backend needs no cgo and no native binary. It runs where a native worker is unavailable, such as Windows or locked-down hosts, and adds a WebAssembly sandbox around the interpreter. |
| `worker-wasm` crate (Rust, `wasm32-wasip1`) | the embedded worker binary | Upstream's `monty-runtime` does not build for WASI, because `monty-fs` pulls host filesystem code. The crate links only the protocol worker, allocator and types. |
| klauspost/compress (zstd) | unpacking the embedded worker | A pure-Go decoder. It keeps the wasm module compact inside the Go module. |
| coder/websocket | the remote backend | A small, context-aware client with close-frame and ping handling. |
| `os.Root` | host directory mounts | Every mount operation resolves relative to one directory descriptor, which blocks `..` and symlink escapes. |
| OpenTelemetry Go API | spans, logs and metrics | The same instrumentation surface as `@pydantic/monty`. Hosts bring their own SDK and exporters. |
| `monty-pool` (Rust) | the engine of `monty-server` | Upstream's pool already spawns workers, enforces request timeouts and emits Monty telemetry. `Checkout::turn_raw` relays decoded protobuf messages without converting values. |
| tokio and axum | HTTP and WebSocket handling in `monty-server` | tokio is the runtime `monty-pool` needs. axum serves the upgrade, `/health` and `/metrics` from one router. |
| prometheus-client, OpenTelemetry OTLP (Rust) | server metrics and trace export | `GET /metrics` for scrapers; OTLP/HTTP for any collector. |
| cargo-zigbuild with zig | cross-compiling the image binaries to musl | Both architectures build on the build platform without QEMU, and static binaries run on `scratch`. |
| testcontainers-go | `tests/network` | Starts, signals, recreates and removes server containers from Go tests. |
| testify | tests | Assertions in the ported upstream suites. |

Upstream is pinned in several places, which MUST move together: `proto/PROTO_REV` (the full SHA, the source of truth), `montygo.UpstreamRev`, the git revisions in `worker-wasm/Cargo.toml` and `server/Cargo.toml`, `MONTY_REV` in `server/src/version.rs`, both Dockerfiles and `.github/workflows/ci.yml`. `make check-pins` verifies every copy. The binding's own version comes from git tags through `scripts/version.sh`; see `versioning.md`.

## Process model

```
 host process                                         worker
┌─────────────────────────────────────────────┐
│ application                                 │
│   │                                         │
│ package montygo Pool · Session · Snapshot   │
│   │             answerer · host objects     │
│ internal/pool   worker pool · turn engine   │
│   │             mounts · telemetry observer │
│ internal/wire   framing · protobuf codec    │
│   │                                         │
│ internal/worker ─┬─ subprocess ─────────────┼─ stdio pipes ──▶ `monty subprocess` child (unix)
│                  ├─ wasm ── io.Pipe ──▶ wazero instance (monty.wasm)
│                  └─ websocket ──────────────┼─ ws:// or wss:// ─▶ relay or monty-server ─ stdio ─▶ `monty subprocess`
└─────────────────────────────────────────────┘
```

- **Native** workers are `monty subprocess` children started with an empty environment. The binary resolves from an explicit path, `MONTY_BIN`, `PATH`, or a nearby cargo target directory. This backend is unix-only.
- **Wasm** workers are module instances inside the host process. The zstd blob is verified against a pinned digest, compiled once per process and cached on disk. Each instance gets the host's monotonic and wall clocks and a crypto random source.
- **WebSocket** workers are remote and single-use: one dial per checkout, no prewarming, a close frame at the end. `TLSConfig` and `DialContext` shape the dial. See `websocket.md`.
- **monty-server** (`server/`, Rust) accepts one WebSocket session per connection. Each session checks out a fresh `monty subprocess` from `monty_pool` and relays every request through `Checkout::turn_raw`. See `server.md` and `docker.md`.
- `BackendAuto` picks native when a binary resolves, and wasm otherwise.

All backends implement one `Worker` interface: send a frame, receive a frame, kill, close, wait, and report an exit status. The pool and the session never branch on the transport, except to classify how a worker ended.

## Inter-process communication

### Channel

The parent and worker exchange `ParentRequest` and `ChildEvent` protobuf messages.

| Backend | Carrier | Framing |
|---|---|---|
| native | the child's stdin and stdout | 4-byte little-endian length prefix |
| wasm | in-memory `io.Pipe` pairs wired to the instance's WASI stdin and stdout | 4-byte little-endian length prefix |
| websocket | binary WebSocket messages | one message per protocol message |

- Stdio was chosen because upstream's worker already speaks it. It needs no ports, sockets or shared memory, and closing stdin ends the child at a frame boundary.
- The wasm backend reuses the same byte-stream framing over in-memory pipes, so the embedded worker is the same program as the native one.
- The worker's stderr carries diagnostics only, never protocol data.
- Each transport runs one reader goroutine that pumps complete frames into a queue. Receiving is therefore cancellable by context without losing a partial frame.

### Conversation

The exchange is strictly alternating: one request, zero or more `Print` events, then one turn-ending event. There is no pipelining, and one worker serves one checkout at a time.

```
parent                          worker
  Configure ─────────────────▶
            ◀───────────────── Ok
  Feed(code, inputs, cwd) ───▶
            ◀───────────────── Print … Print
            ◀───────────────── FunctionCall | OsCall | NameLookup | ResolveFutures
  ResumeCall | ResumeNameLookup | ResumeFutures | AbortFeed ─▶
            ◀───────────────── Print …
            ◀───────────────── Complete | Error | TypingError
  Dump / Load / InstallDependencies ─▶ …
  Reset ─────────────────────▶
            ◀───────────────── Ok        (worker returns to the pool)
```

- `Configure` starts a session with the script name, limits and type-check settings, and declares protocol version 3.
- A suspension hands control to the host. The session answers it from host functions, class wrappers, mounts or the OS handler, then resumes the worker.
- `Dump` and `Load` move a whole session, including a suspended one, between workers.
- `FatalError`, the end of the stream, a deadline or a malformed frame end the worker. `ShutdownDump` is valid from WebSocket workers only.

See `protocol.md` for the codec and `pool.md` for deadlines and failure classification.

## Internal design

### Layers

- **Transport** (`internal/worker`) moves opaque frames and reports how a worker ended.
- **Protocol** (`internal/wire`) frames, encodes and decodes messages, and renders exceptions exactly as Monty does.
- **Values** (`internal/value`) is the Go model of Python values: ordered dicts, sets, tuples, big ints, dates, paths, class markers. It implements Python equality and `repr`.
- **Pool** (`internal/pool`) owns worker lifecycle and the turn engine. The engine enforces the pending suspension, deadlines, the suspension budget and failure classification. It services mount calls and feeds the telemetry observer.
- **Mounts** (`internal/mountfs`) is a port of `monty-fs`. It serves read-only, read-write and overlay mounts from the host side. See `mounts.md`.
- **Telemetry** (`internal/telemetry`) observes each checkout's requests and events and records spans, log records and metrics. See `telemetry.md`.
- **Session** (package `montygo`) is the Go analogue of `MontySession` in `@pydantic/monty`. It converts values in both directions, answers suspensions and exposes snapshots. See `session.md`.

### Request flow of `FeedRun`

1. The session converts inputs into the value model and builds the feed's mount table.
2. The checkout sends `Feed` and streams `Print` segments to the print target while it waits.
3. A suspension goes to the answerer. It calls host code under a context carrying the caller's values and the current Monty span, then sends the matching resume.
4. `Complete` converts the result back into Go values. `Error` and `TypingError` become typed Go errors.

`FeedStart` runs the same engine but stops at each suspension and returns a single-use snapshot.

### Concurrency

- A pool is safe for concurrent use. Checkouts beyond `MaxProcesses` wait up to `CheckoutTimeout`.
- A session admits one execution, including paused snapshots; conflicting calls return ErrSessionBusy. Its lifecycle mutex covers only state transitions, not callbacks or I/O. `Run.Stop` and `Close(ctx, KillNow)` target the execution identity; bounded waits MUST be used if a callback invokes lifecycle APIs on its own session.
- Async host functions return a `*montygo.Future` that settles on its own goroutine. The session collects settled futures at a `ResolveFutures` suspension, or awaits one directly when the worker allows an eager await.
- Every blocking call takes a `context.Context`. Cancelling a feed context ends the run through the session's stop policy: the stop reason (`KeyboardInterrupt`) is delivered where Python yields and the session is kept; Python that never yields is killed when the policy's `Timeout` expires and the session is lost. `Run.Stop`, `Session.Stop` and `Session.Close` work from any goroutine; `Session.State`, `Done` and `Err` report the session's state and loss. See `session.md`.
- `Pool.Shutdown` closes open sessions with the stop policy and waits for every worker; `Pool.Close` retires idle workers only. `Pool.Run` is a one-shot checkout, feed and close; `Pool.Slot` holds one re-acquirable session. See `pool.md` and `session.md`.

## Architectural constraints and principles

- **The worker is untrusted.** Every frame is size-checked (256 MiB), and decoding is budgeted (1 GiB resident per frame) and validated. Frames queued host-side are bounded by `MaxPendingBytes`. Values sent to the worker are depth-checked. A limit the worker reports can tighten the parent's view but never loosen it.
- **Isolation over speed.** Python runs in a separate process or a WebAssembly sandbox, never as host code. A crash, memory breach, protocol violation or deadline ends that worker. Its state is never reused, and the session is poisoned. `monty-server` likewise gives every session a fresh worker process.
- **The worker's clock is the only execution clock.** Execution time comes from the worker's reported total, which only ratchets up. Parent deadlines are backstops: `RequestTimeout`, and the remaining `max_duration` plus grace.
- **No ambient authority for sandboxed code.** Native workers start with an empty environment. The worker never receives host paths: filesystem access happens in the parent, through mounts bound to an `os.Root` descriptor, or through an OS handler the host supplies. Errors crossing into the sandbox never contain host paths.
- **One protocol, many transports.** A backend MUST only implement the `Worker` interface. Pool, session, mount and telemetry logic MUST NOT depend on the transport.
- **Strict alternation.** Each worker has at most one request in flight. A resume MUST match the pending suspension, and snapshots are single-use.
- **Upstream parity is the specification.** Behaviour, error messages, traceback rendering, telemetry names and test titles follow upstream Monty at the pinned revision. Deviations are listed in `docs/parity`, those of `monty-server` in `docs/parity/server.md`.
- **Pure Go at build time.** The root module MUST build without cgo. The embedded worker is a checked-in artifact rebuilt by `make build-wasm`, and the native worker is an external binary.
- **Telemetry is a passive observer.** It sees traffic only after a frame is sent or fully decoded, and never alters the conversation. A failing OpenTelemetry component disables only its own signal. Sandbox-chosen names never become metric attributes.
- **Explicit ownership.** Pools and sessions MUST be closed. A closed pool or session rejects further use, and a snapshot resumes at most once. Host objects sent into a session stay registered until the session closes.

## Related documents

| Document | Topic |
|---|---|
| `protocol.md` | framing, codec, value depth, exception rendering |
| `pool.md` | pool lifecycle, accounting, shutdown, frame byte bound, deadlines, suspension budget, failure classification |
| `session.md` | lifecycle, stop policy, session state, slots, drive loop, print, value conversion, host objects, host registry |
| `versioning.md` | `MontyVersion`, `BindingVersion`, `scripts/version.sh`, pins |
| `mounts.md` | host filesystem mounts |
| `wasm.md` | embedded wasm worker |
| `websocket.md` | remote workers |
| `server.md` | `monty-server`: flags, sessions, timeouts, dumps, drain, metrics |
| `docker.md` | server image, build, cross-compilation, run recommendations |
| `osaccess.md` | in-memory OS helpers |
| `telemetry.md` | OpenTelemetry mirror |
| `testing.md` | backend matrix, parity suites, `tests/network` |
