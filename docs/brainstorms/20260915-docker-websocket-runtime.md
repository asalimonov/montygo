# Brainstorm: dockerized Monty runtime over WebSocket

Date: 2026-09-15. Skill: go-brainstorm. Status: done. Target: `20260915-docker-websocket-runtime-target.md`.

## 1. Initial input

> Need to add support additional runtime for montygo - dockerized runtime of monty.
> What should be in this scope:
> 1. Ability to build monty docker images where should be a websocket server which hosts monty, it can be on Rust to avoid redundant serializations/marshaling etc.
> 2. `make docker-build` should build the docker images for amd64 and arm64 architectures to run natively on linux and macos w/o issues with architecture
> 3. Need to design and implement abstractions in montygo which can work with docker container via websocket, it would be good to investigate ../monty which possible already has this implementation. It should support to connecto to remote host with this container, not only localhost (we can use connection without TLS, but architecture should allow to secure this connection in future)
> 4. Need to write tests with testcontainers where it run backed docker image and tests interoperations. It should use conception of "pool" for parallel tests as in ../meridian-overlay project, you can copy and adapt some code from there - /Volumes/M4DATA/Users/speckzzz/reps/neria/meridian-overlay/tests/cluster
> 5. All the current adapted tests and cluster tests should pass. Cluster tests should also have testing of "repl mode"

## 2. Research: current state

### 2.1 montygo already has the client half

- `monty.NewWebSocket(ctx, WebSocketOptions)` (`websocket.go`) builds a pool of single-use remote workers. It is a port of Python `AsyncMontyWebsocket` and of the `monty-pool` WebSocket transport.
- Transport: `internal/worker/websocket.go`, `WebSocketDialer` + `wsWorker`, on `github.com/coder/websocket`.
- Wire contract, as implemented and documented in `docs/architecture/websocket.md`:
  - one protocol message = one binary WebSocket message, no length prefix;
  - `User-Agent: monty-pool/0.0.23`, caller `ConnectHeaders` win case-insensitively;
  - compression disabled, read limit `wire.MaxFrameLen` (256 MiB);
  - no prewarm, no `Reset`: one dial per checkout, close frame (1 s budget) at the end;
  - any stream end is `DisconnectError`; a `ShutdownDump` event is `ShutdownError{Dump}`;
  - `RequestTimeout` default 10 s, also the dial budget.
- `ws://`, `wss://`, `http://`, `https://` are accepted. TLS uses a clone of `http.DefaultTransport`, so there is no hook for a custom `tls.Config`, CA pool, client certificate or custom dialer today.
- Tests:
  - root `websocket_test.go` runs 7 ported `test_websocket.py` cases against `websocket_relay_test.go`, an in-process Go relay that spawns `monty subprocess` per connection;
  - `internal/pool/websocket_test.go` runs 26 ported `websocket.rs` cases against a scripted fake child;
  - `internal/worker/websocket_test.go` covers the transport.
- The adapted root suite (`eachBackend`) runs only `native` and `wasm`. `MONTY_TEST_BACKENDS` does not know `websocket`.

### 2.2 Upstream has no open-source server

- `../monty/docs/server.md` describes **Full Monty**: a closed-source WebSocket server image, `linux/amd64` only ("Native ARM64 images are planned").
- Its documented behaviour is the de-facto specification of a server that the existing client targets:
  - one `monty` worker subprocess per connection, from an elastic pool, reset or replaced between sessions;
  - `--max-sessions` (503 on upgrade), `--max-sessions-per-client` (429), `--trust-forwarded-for`;
  - `--idle-timeout`, `--keepalive` pings, `--session-timeout`, `--turn-timeout`;
  - resource limits as ceilings: a client's `Configure` limits are clamped down to the server's `--max-duration`, `--max-memory-mib`, `--max-recursion-depth`;
  - `GET /health` (readiness), `GET /` (liveness, info page);
  - SIGTERM drain: stop listening, answer each session's next request with `ShutdownDump` (signed dump), drop silent sessions after `--drain-grace`;
  - `--dump-key` (required, ≥16 bytes) signs dumps so a dump restores on any replica with the same key;
  - image: `scratch` base, uid 65532, prints only the bound URL on stdout, logs to stderr;
  - "There is no authentication and the listener speaks plain `ws://`. Terminate TLS at an ingress."
- `../monty/scripts/websocket_relay.py` is the dev/test server: it spawns `monty subprocess` per connection and only adds or strips the 4-byte little-endian prefix. It never decodes protobuf.

### 2.3 Worker process model constraints

- `crates/monty-runtime/src/subprocess.rs` is a stdio shell around `monty_proto::worker::Child`. After each request it calls `monty_alloc::set_limit(...)`.
- `crates/monty-alloc/src/lib.rs` keeps `SOFT_LIMIT` and `HARD_LIMIT` as process-global `static AtomicUsize`.
- Conclusion: one OS process can enforce the memory limit of exactly one session. Stack overflow and allocator abort end the process with no final frame. **A server MUST NOT host interpreters in-process. It MUST spawn one `monty subprocess` child per session.**
- The native worker is `monty` from `crates/monty-runtime` (feature `standalone` can be dropped: `--no-default-features` builds the worker alone).
- Exit code 65 ("worker exceeded its memory limit") is how the native backend classifies a hard memory kill. Exit codes do not travel over WebSocket; the client sees `DisconnectError`.

### 2.4 "Avoid redundant serialization"

- A relay in any language does zero protobuf work: one read, one prefix add or strip, one write per frame.
- A protocol-aware server (limit clamping, `ShutdownDump` synthesis, dump signing) must decode `Configure`, `Load`, `DumpResult` and synthesize `ShutdownDump`. In Rust this reuses `monty-proto` prost types directly, pinned to the same revision as the worker. In Go it would reuse montygo's hand-written codec.
- Rust also fits the build: the worker is already built with cargo, a static musl server + static musl worker run on `scratch`.

### 2.5 Build environment

- Local: Docker Desktop 29.6.1, `linux/arm64` engine, buildx `desktop-linux` with `linux/amd64` emulation.
- `docker buildx build --platform linux/amd64,linux/arm64 --load` needs the containerd image store. Without it, multi-platform output must go to a registry or an OCI tarball.
- Rust under QEMU emulation is very slow. Cross-compiling in a `--platform=$BUILDPLATFORM` stage (musl targets) avoids emulation.
- Upstream sources: `../monty` is outside the build context. Options: a buildx named context (`--build-context monty=../monty`) or a git fetch of the pinned revision inside the Dockerfile.
- CI (`.github/workflows/ci.yml`) runs `go test` on ubuntu and macOS with a cargo-built native worker. It has no Docker job.

### 2.6 Go module constraints

- Root module: Go 1.25, no cgo, "New dependencies MUST support Go 1.25".
- `testcontainers-go` latest is `v0.44.0` with `go 1.25.0`: compatible. Its dependency tree (moby client, containerd, otel exporters) is large.
- `examples/` is already a separate module. A separate test module keeps testcontainers out of the root `go.mod`.

### 2.7 meridian-overlay cluster harness (`tests/cluster`)

- `ContainerPool` singleton, size = `MERIDIAN_TEST_PARALLEL` (6) + spare (1), cook concurrency 3.
- `DeploymentUnit` = Docker bridge network + PostgreSQL + 3 app containers, with a per-unit lifecycle goroutine (`cold → warming → ready → inUse → releasing → ready | dead`), command channel, replacement on failed rebuild.
- `Start` returns when the first unit is ready (early start) or all die; infrastructure failures (address pools exhausted, no space) abort fast.
- `Acquire` blocks on `available`; `Release` stops nodes synchronously, rebuilds asynchronously.
- `terminateAndVerify` trusts only `Inspect` returning not-found. `fileLogConsumer` streams container logs to `output/containers/`, renamed per test.
- Gated by `CLUSTER_TESTS=1`; `TestMain` starts and stops the pool.
- Most of the complexity (template databases, 3-node boot, raft leader wait, config overrides) is meridian-specific. The reusable core is: singleton pool, lifecycle goroutine, early start, infra-failure fail-fast, verified terminate, log capture, spare units.

### 2.8 "REPL mode" in montygo

- `repl_test.go` ports `repl.spec.ts`: state persists across feeds, runtime errors keep the session, session dump.
- `examples/repl` ports the `monty` CLI REPL: continuation detection by trial feed, multi-line input, mounts, interrupts.

## 3. Initial conclusions

1. The Go client abstraction mostly exists. The work in montygo is extending it (TLS and dialer hooks, health, test backend), not inventing it.
2. The main new component is an open-source Rust WebSocket server that is wire-compatible with Full Monty for the subset montygo uses, and runs one `monty subprocess` per connection.
3. In-process interpreter hosting is rejected (global allocator limits, crash blast radius).
4. The meridian pool is heavier than needed. A montygo unit is one server container, which is stateless across connections.

## 4. Questions and answers

### Q1. Server feature level

Options offered: thin relay; operational server (caps, timeouts, health, drain, clamping); Full Monty parity.

**Answer:** Full Monty parity. Session caps, timeouts, a health endpoint, graceful drain on SIGTERM and clamping of client limits are mandatory.

**Resolution R1.** The server implements every behaviour in `../monty/docs/server.md`:

- one `monty subprocess` per session, from an elastic pool, `Reset` reuse or replacement between sessions;
- `--max-sessions` (503) and `--max-sessions-per-client` (429), `--trust-forwarded-for`;
- `--idle-timeout`, `--keepalive`, `--session-timeout`, `--turn-timeout`;
- limit ceilings: `--max-duration`, `--max-memory-mib`, `--max-recursion-depth`, applied by rewriting `Configure` and checking `Load`;
- `GET /health`, `GET /` info page, bound URL as the only stdout line;
- SIGTERM drain with `ShutdownDump`, `--drain-grace`;
- `--dump-key` dump signing;
- tracing (exporter to be decided);
- `MONTY_SERVER_*` environment variables for every flag, `MONTY_BIN`, `LOGFIRE_TOKEN`.

Consequences:

- The server is protocol-aware. It decodes `Configure`, `Load`, `Dump`/`DumpResult`, and tracks the turn state (which request is in flight, whether the session is suspended) so drain can answer "the next request" with `ShutdownDump`.
- The signature format of Full Monty is not public. montygo defines its own. A dump signed by Full Monty will not load into this server and vice versa. This is a deviation to list in `docs/parity/`.
- Open sub-questions: dump format and cross-backend portability, tracing exporter, worker reuse policy.

### Q2. Test scope

Options offered: both layers; cluster module only; root suite only (testcontainers in root module).

**Answer:** both layers.

**Resolution R2.**

- The root suite gains a `websocket` backend in `MONTY_TEST_BACKENDS`. It dials `MONTY_TEST_WS_URL` through `monty.NewWebSocket`. The root `go.mod` gets no Docker dependency.
- `make test-docker` starts the image, exports the URL and runs root tests with `MONTY_TEST_BACKENDS=native,wasm,websocket` (or `websocket` alone).
- Cases that cannot hold over WebSocket (PID, worker reuse, exit code 65, wasm-only) skip on that backend with a reason, and the list goes to `docs/parity/tests.md`.
- A separate module `tests/cluster` (own `go.mod`, testcontainers-go v0.44) holds server-behaviour tests on a container pool adapted from meridian-overlay: caps, 429/503, timeouts, keepalive, drain + restore, signed dumps, parallel load, REPL mode.

### Q3. Dump signing

Finding that drove the question: `Load { bytes state }` carries the session's limits inside the opaque dump. The `Load` reply echoes only `max_duration_micros`, `max_suspensions` and `restored_script_name`, never the memory limit. An unsigned `Load` would let a client restore a crafted or foreign dump and bypass `--max-memory-mib` and `--max-duration`. Upstream `docs/security.md` also says to treat a snapshot from an untrusted source as untrusted serialized data.

Options offered: strict HMAC envelope; HMAC plus `--allow-unsigned-dumps`; HMAC plus a client-side unwrap helper.

**Answer:** strict HMAC envelope.

**Resolution R3.**

- Every `DumpResult.state` and `ShutdownDump.dump` leaving the server is wrapped:

  ```
  offset  size  field
  0       4     magic "MTYD"
  4       1     version = 1
  5       4     key_id = SHA-256(key)[0:4]
  9       32    HMAC-SHA256(key, magic | version | key_id | monty_rev | state)
  41      n     state (raw worker dump)
  ```

- `monty_rev` is the pinned upstream revision baked into the server. A dump from another Monty revision fails verification, which matches "dumps only load into a worker of the same Monty version".
- `--dump-key` (≥16 bytes) signs and verifies. `--dump-key-previous` verifies only, for rotation.
- `Load` with a missing, malformed or invalid envelope never reaches the worker. The server answers a turn-ending `Error` event, and the session stays usable.
- Local (native, wasm) dumps cannot load remotely. Remote dumps cannot load locally. The Go client needs no change: the envelope is opaque bytes.
- The envelope format differs from Full Monty's private format. This is listed in `docs/parity/`.

### Q4. Server location and upstream sources

Options offered: in-repo crate with pinned git sources and a local override; in-repo crate with `../monty` only; separate repository.

**Answer:** in-repo crate, pinned git + local override.

**Resolution R4.**

- New Rust crate `server/` (package and binary `monty-server`), its own Cargo workspace like `worker-wasm/`. It depends on `monty-proto` and `monty-types` by git revision, the same pin as `worker-wasm`.
- `docker/Dockerfile` builds the worker (`monty-runtime --no-default-features`) and the server. By default it fetches the pinned revision. `make docker-build` passes `--build-context monty-src=$(MONTY_SRC)` when the sibling checkout exists, mirroring `build-wasm`.
- The upgrade checklist in `CLAUDE.md` gains the server `Cargo.toml` revs, the Dockerfile `MONTY_REV` argument and the image tag.

Build facts gathered:

- `cargo tree -p monty-runtime --no-default-features -i cc` is empty on Linux: the worker is pure Rust, so musl cross-compilation needs only a linker.
- Docker Desktop here uses the containerd snapshotter (`driver-type: io.containerd.snapshotter.v1`), so `buildx build --platform linux/amd64,linux/arm64 --load` works locally.
- Upstream workspace pins: `tokio 1`, `tokio-tungstenite 0.27`, `futures-util 0.3`, `clap 4`, `prost =0.14.4`, `sha2 0.10`, `logfire 0.12`, `rustls 0.23` with `aws_lc_rs` (C and cmake), rust-version 1.95.

### Q5. Multi-arch build

Options offered: cross-compile and load locally; QEMU native builds; host arch by default with a separate multi-arch target.

**Answer:** cross-compile, load locally.

**Resolution R5.**

- Builder stage `FROM --platform=$BUILDPLATFORM rust:1.95`, `cargo-zigbuild` to `x86_64-unknown-linux-musl` or `aarch64-unknown-linux-musl` from `$TARGETARCH`. No QEMU for Rust compilation.
- Final stage `scratch`, `USER 65532:65532`, `ENTRYPOINT ["/usr/local/bin/monty-server"]`, `CMD ["--host","0.0.0.0"]`, `MONTY_BIN=/usr/local/bin/monty`.
- `make docker-build`: `docker buildx build --platform linux/amd64,linux/arm64 --load`, tags `monty-server:<version>` and `monty-server:latest`.
- `make docker-push REGISTRY=...` pushes the manifest list.

## 5. Research: `monty-pool` is the server engine upstream uses

- `crates/monty-pool/src/checkout.rs` has `Checkout::turn_raw(&pb::ParentRequest, OnRawEvent) -> Result<pb::ChildEvent, PoolError>`. Its doc comment: "For callers that already speak the wire (a relay bridging a remote client)" and "the driver must verify a dump is one it issued (**monty-server signs and checks them**)". Full Monty is therefore `monty-pool` + `turn_raw` + a WebSocket front end.
- `turn_raw` guarantees:
  - refuses `Configure`, `Reset`, `Shutdown` and an empty `kind` with `PoolError::Protocol`, leaving the worker usable;
  - arms `min(request_timeout, max_duration backstop)`, kills the worker on expiry (`PoolError::Timeout`);
  - enforces `max_suspensions` by sending `AbortFeed` itself;
  - re-adopts a `Load`'s budget from the reply;
  - forwards `Print` events through `on_event`, returns the turn-ender;
  - a `FatalError` turn-ender is returned after the worker is reaped; a `ShutdownDump` from a subprocess worker is a protocol violation;
  - an event with no kind is a protocol violation.
- `Pool`: `PoolConfig::subprocess(bin)`, `min_processes` prewarm, `max_processes` cap, `checkout_timeout`, `request_timeout`, `duration_limit_grace` (1 s), `max_checkouts_per_worker`, optional telemetry metrics. `Checkout::finish` sends `Reset` and returns the worker to the idle list. `Pool::close` sends `Shutdown` to idle workers.
- `ReplConfig` mirrors `Configure`: `script_name`, `limits`, `type_check`, `type_check_stubs`, `type_check_config`, `assert_message_annotations`, `print_flush_interval`. `Pool::checkout(&ReplConfig)` sends `Configure` to the child.
- Feature `telemetry` gives the same session, run and host-call spans as other Monty clients (Full Monty: "Beneath it are the same session, run and host-call spans").
- Build risk: `monty-pool` depends on `rustls` with `aws_lc_rs` unconditionally. `aws-lc-sys` compiles C with cmake, so the musl cross-compile of R5 must handle a C toolchain (zig cc + cmake). This needs a build spike.

### 5.1 Client request sequence over WebSocket (montygo and Python)

From `internal/pool/websocket_test.go` (`shutdown_hands_back_a_restorable_dump`, `shutdown_during_a_suspension_carries_the_suspended_dump`, `shutdown_without_a_session_carries_no_dump`):

```
dial ─▶ Configure ─▶ Ok
        [Load(state) ─▶ Ok | re-announced suspension]      restore path
        Feed ─▶ Print* ─▶ FunctionCall | OsCall | NameLookup | ResolveFutures
        Resume* ─▶ ... ─▶ Complete | Error | TypingError
        Dump ─▶ DumpResult
        (draining server) any request ─▶ ShutdownDump{dump?}
close frame                                               no Reset, no Shutdown
```

- `Load` follows `Configure` before any feed. The child accepts it because no REPL exists yet.
- `ShutdownDump` answers a request instead of running it, including a resume in a suspension (the dump is the suspended state).
- Before `Configure` completes, `ShutdownDump` carries no dump.

### 5.2 Consequences for a `monty-pool` based server

- `Pool::checkout(&ReplConfig)` sends the pool's own `PROTOCOL_VERSION`. The server MUST check the client's `Configure.protocol_version` with `monty_proto::check_protocol_version` and answer `FatalError` itself, or version skew is hidden.
- `checkout` swallows the child's `Ok`. The server synthesizes `ChildEvent{Ok, max_duration_micros, max_suspensions}` with the clamped values.
- A memory-limit kill classifies as `PoolError::Runtime(MemoryError)` in `monty-pool`. The server can forward it as an `Error` event and then close, so a WebSocket client sees the same `MemoryError` a native client sees, instead of a bare disconnect.
- `turn_raw` takes a decoded `pb::ParentRequest`: every client frame is decoded and re-encoded once in the server. This is the serialization cost the input wanted to avoid; it buys suspension budgets, deadlines and crash classification from upstream code.
- montygo's `Checkout.Finish` on a WebSocket worker only releases the slot (`internal/pool/checkout.go:603-607`). It never sends `Reset` or `Shutdown`, which fits `turn_raw` refusing them.

### Q6. Server engine

Options offered: `monty-pool` + `turn_raw`; own zero-copy relay; hybrid.

**Answer:** `monty-pool` + `turn_raw`.

**Resolution R6.**

- `monty-server` depends on `monty-pool` (feature `telemetry` decided later), `monty-proto`, `monty-types` at the pinned revision.
- One `monty_pool::Pool` per server, `PoolConfig::subprocess(MONTY_BIN)`, `max_processes = --max-sessions`.
- Per connection: first frame MUST be `Configure` → version check → clamp → `pool.checkout(ReplConfig)` → synthesized `Ok`. Every later frame goes through `turn_raw`, except `Load` (verify and strip the envelope first). `DumpResult` and `ShutdownDump` are signed on the way out. On close, `checkout.finish()`.
- The per-frame decode and re-encode is accepted. A raw-bytes `turn_raw` variant upstream is a potential improvement.
- The zero-copy relay and the hybrid are rejected: they re-implement tested budget, deadline and crash logic, and the hybrid breaks `turn_raw`'s "never interleave" rule.

### Q7. Go-side abstraction

Options offered: extend `WebSocketOptions`; a pluggable exported `RemoteTransport` interface; extend plus a public `montydocker` module.

**Answer:** extend `WebSocketOptions`.

**Resolution R7.**

- `WebSocketOptions` gains `TLSConfig *tls.Config` and `DialContext func(ctx, network, addr) (net.Conn, error)`. `worker.WebSocketDialer` gains the same fields. The dialer still wraps `DialContext` to capture the raw `net.Conn`, so `Kill` keeps working with TLS and custom dialers.
- New `monty.CheckWebSocketHealth(ctx, WebSocketOptions) error`: `GET /health` on the URL's host over the same transport (`ws→http`, `wss→https`).
- No Docker types in the root module. Container lifecycle stays in `tests/cluster`.
- Future security (wss, mTLS, bearer tokens through `ConnectHeaders`) needs no new constructor.

## 6. Research: root suite on a `websocket` backend

Background survey of every root `*_test.go`:

- 479 per-backend subtests: 449 work unchanged, 25 need adaptation, 5 must skip (already `plNativeOnly`).
- Harness changes required first:
  - `testBackends` (`testmain_test.go:52-59`) ignores `websocket`.
  - `sharedPool` calls `monty.New`, which returns an `OptionError` for `BackendWebSocket` (`pool.go:149`). The helper turns that into `t.Skipf`, so every test would be skipped silently.
  - `newPool` (`testmain_test.go:108-115`) must map `Options` → `WebSocketOptions` (`MaxProcesses`, `CheckoutTimeout`, `RequestTimeout`).
  - `telRun`/`telPool` (`telemetry_test.go:44-47`, `:82-86`) decode only native/wasm in the child process and call `monty.New`.
  - `install_dependencies_test.go:15,31` and `public_api_conformance_test.go:15` call `monty.New` directly.
- Adaptation list (25): telemetry 11, callback context 7, public API conformance 3, install dependencies 2, pool memory 2 (`pool_test.go:133-138`, `:146-148` expect the exit-65 text "the worker exceeded its memory limit and was terminated").
- Skips (5): `pool_test.go:102,113,207,243,281` (PIDs, SIGKILL text, `BinaryPath`). Their skip messages mention wasm and need a backend-neutral reason.
- Server configuration constraints for test runs:
  - all connections come from one peer IP (the Docker bridge gateway), so `--max-sessions-per-client` MUST be 0 for the root suite and for cluster tests that do not test the quota;
  - `--max-memory-mib` 64 clamps `limits_test.go:91` (`1<<33`), which still passes;
  - the server MUST accept messages up to `MAX_FRAME_LEN` (a test sends a 16 MiB source frame);
  - exact byte counts in `limits_test.go` hold only when the image's Monty revision equals the pin.
- Behaviour differences to accept:
  - zero `RequestTimeout` means none on native, 10 s on WebSocket, and it is also the dial budget;
  - `MinProcesses` and `MaxCheckoutsPerWorker` do not apply;
  - `example_test.go` and `wasm_*_test.go` never target WebSocket.

### Q8. REPL mode

Options offered: session REPL and CLI REPL; session REPL only; CLI REPL only.

**Answer:** session REPL + CLI REPL.

**Resolution R8.**

- Session-level REPL in `tests/cluster`: state across feeds, errors keep the session, dump on one server replica → restore on another, drain `ShutdownDump` → restore of a suspended feed, type-check stubs across feeds.
- CLI REPL: `examples/repl` gains a `-ws URL` flag (plus `-ws-insecure-skip-verify`/CA flags for TLS later). Cluster tests build the binary once and drive it over stdin/stdout against a container: continuation prompts, multi-line blocks, print streams, interrupt, mounts.
- Cross-replica restore requires every replica used by a test to share one `--dump-key`.
- `examples/repl/main.go:80` builds its pool with `monty.New(ctx, montyenv.PoolOptions())` and checks out with `ScriptName` and `Limits` (`:261`). A `-ws` flag branches to `monty.NewWebSocket`; everything after checkout is unchanged.

### Directive D1. Environment variable naming

**User note (while answering the pool unit question):** "Replace all MERIDIAN_* env vars on MONTYGO_* env vars."

**Resolution.** Every harness variable adapted from meridian-overlay uses the `MONTYGO_` prefix:

| meridian-overlay | montygo |
|---|---|
| `CLUSTER_TESTS` | `MONTYGO_CLUSTER_TESTS` |
| `MERIDIAN_TEST_PARALLEL` | `MONTYGO_TEST_PARALLEL` |
| `MERIDIAN_TEST_POOL_SPARE` | `MONTYGO_TEST_POOL_SPARE` |
| `MERIDIAN_TEST_COOK_PARALLEL` | `MONTYGO_TEST_COOK_PARALLEL` |
| `DUMP_CONTAINER_LOGS` | `MONTYGO_DUMP_CONTAINER_LOGS` |
| `MERIDIAN_SLOW_TESTS_ENABLE` | `MONTYGO_SLOW_TESTS_ENABLE` |
| image tag `meridian-cluster-test:latest` | `MONTYGO_TEST_IMAGE`, default `monty-server:<version>` |
| network prefix `meridian-test-` | `montygo-test-` |

Existing `MONTY_*` variables (`MONTY_BIN`, `MONTY_TEST_BACKENDS`, `MONTY_SRC`, `MONTY_EXAMPLES_BACKEND`) keep their names, because they already exist in the root module and upstream. The server's own variables stay `MONTY_SERVER_*` for Full Monty parity. New root-suite variable: `MONTY_TEST_WS_URL`.

### Q9. Pool unit shape

Options offered (twice): network + 2 replicas; single replica; shared servers.

**Answer (user note):** "There no need to replicas, it is just of pool of containers which feeds tests in queue, no need to spare. Rename `test/cluster` to `test/network`."

**Resolution R9.** Supersedes the `tests/cluster` name in R2 and R8, and the spare and cook rows of D1.

- Directory and module: `tests/network` (`github.com/asalimonov/montygo/tests/network`). Gate variable `MONTYGO_NETWORK_TESTS`, Make target `test-network`.
- Unit = one `monty-server` container with a host-mapped port. No replicas, no dedicated network, no sidecars.
- Pool size = `MONTYGO_TEST_PARALLEL` (default 4). No spare units, no cook-concurrency variable: a scratch-image container starts in about a second.
- Tests queue on `Acquire` until a container is free.
- Per-test flag overrides recreate the unit's container before handing it out. Release checks `/health` and zero active sessions; a unit that fails the check, or that ran with overrides, is recreated in the background. A failed recreate is retried, then the slot is marked dead and logged.
- Cross-instance scenarios run inside one unit: dump → recreate the container with the same `--dump-key` → restore; SIGTERM drain → `ShutdownDump` → recreate → restore; key rotation by recreating with `--dump-key-previous`.
- `wss://` scenarios do not need a sidecar: the test process runs an `httptest.NewTLSServer` reverse proxy in front of the container and dials it with `WebSocketOptions.TLSConfig`.
- `MONTYGO_TEST_POOL_SPARE` and `MONTYGO_TEST_COOK_PARALLEL` are dropped. `MONTYGO_CLUSTER_TESTS` becomes `MONTYGO_NETWORK_TESTS`.

### Q10. Worker lifecycle between sessions

Finding: `server.md` says both "reset or replaced between sessions" (intro) and "Workers are created on demand for active connections and exit when their sessions close" (sizing). `monty-pool`'s `release_worker` (`pool.rs:307-323`) kills a worker when `checkouts_served >= max_checkouts_per_worker`, otherwise returns it to the idle list.

Options offered: fresh worker per session; fresh worker plus `--prewarm N`; `Reset` reuse with `--max-checkouts-per-worker`.

**Answer:** fresh worker per session.

**Resolution R10.**

- `PoolConfig { min_processes: 0, max_processes: max_sessions, max_checkouts_per_worker: Some(1), .. }`.
- Session end calls `checkout.finish()`: `Reset` → `Ok` → `release_worker` drops (kills) the child and frees capacity. A checkout that errored is dropped, which also kills the child.
- No child serves two sessions. Type-checker caches, allocator state and any sandbox escape die with the session.
- No new flags. Spawn-ahead is a potential improvement.

## 7. Build spike (R5 de-risking)

Spike in the session scratchpad: `rust:1.95-bookworm` on `$BUILDPLATFORM` (arm64), `pip install ziglang cargo-zigbuild`, cmake, upstream fetched at `f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28`, `cargo zigbuild --locked --release`.

| Target | Artifact | Result |
|---|---|---|
| `x86_64-unknown-linux-musl` | `monty-pool` `--test websocket` binary (links `rustls` + `aws-lc-sys`) | built, 43 s |
| `x86_64-unknown-linux-musl` | `monty` worker, `--no-default-features` | built, 3 min 0 s |
| `aarch64-unknown-linux-musl` | `monty-pool` `--test websocket` binary | built, 34 s |
| `aarch64-unknown-linux-musl` | `monty` worker | built, 2 min 51 s |

Verification of the artifacts:

- `file`: all four are `ELF 64-bit LSB executable, statically linked, stripped`. Worker size 19 MB (arm64), 21 MB (x86_64). Test binaries 4.5 MB and 5.8 MB.
- `docker run --platform linux/arm64 alpine:3.20`: `monty --help` runs; the `monty-pool` `websocket` test binary reports `test result: ok. 35 passed; 0 failed`.
- `docker run --platform linux/amd64 alpine:3.20`: the x86_64 worker runs under emulation.

**Conclusion:** R5 holds. `aws-lc-sys` compiles and statically links for both musl targets with zig cc and cmake, with no QEMU compilation. A cold build of both arches takes about 7 minutes on this machine with an empty cargo cache.

### Q11. Tracing exporter

Options offered: Logfire + generic OTLP; generic OTLP only; stderr logs only.

**Answer:** generic OTLP only.

**Resolution R11.**

- `monty-server` enables `monty-pool/telemetry` and installs a `TelemetryAdapter` that forwards spans and logs to `opentelemetry-otlp` exporters (version matched to `opentelemetry 0.32`).
- Flags: `--otlp-endpoint` / `OTEL_EXPORTER_OTLP_ENDPOINT` (unset = no export), `--otlp-protocol` (`http/protobuf` default, `grpc`), standard `OTEL_EXPORTER_OTLP_HEADERS` and `OTEL_SERVICE_NAME` (default `monty-server`).
- No `--logfire-token` flag. Deviation listed in `docs/parity/`.
- Span model as Full Monty: one connection span, policy outcomes as events on it, `monty-pool` session, run and host-call spans beneath. The upgrade request's `traceparent` parents the connection span.
- Stderr always carries one log line per policy outcome.

### Q12. Observability for tests and operators

Options offered: Prometheus `/metrics`; JSON on `GET /`; no endpoint.

**Answer:** Prometheus `/metrics`.

**Resolution R12.**

- `GET /metrics`, Prometheus text format, served on the same listener. It is an extension over Full Monty and is listed in `docs/parity/`.
- Families:

  | Name | Type | Labels |
  |---|---|---|
  | `monty_server_sessions_active` | gauge | |
  | `monty_server_sessions_total` | counter | `outcome` = `closed`, `error`, `timeout`, `drained`, `rejected` |
  | `monty_server_rejections_total` | counter | `reason` = `capacity`, `client_quota`, `draining`, `bad_request` |
  | `monty_server_timeouts_total` | counter | `kind` = `idle`, `keepalive`, `session`, `turn` |
  | `monty_server_dumps_total` | counter | `op` = `signed`, `verified`, `rejected` |
  | `monty_server_workers_spawned_total` | counter | |
  | `monty_server_draining` | gauge | |
  | `monty_server_build_info` | gauge (1) | `version`, `monty_rev` |

- `tests/network` Release reads `monty_server_sessions_active == 0` before handing a unit back.

### Telemetry and child-process facts for R11

- `monty_pool::telemetry::configure_telemetry_adapter(Arc<dyn TelemetryAdapter>)` configures an internal `logfire` pipeline with `send_to_logfire(false)` and no console, and routes spans and logs to the adapter. It returns a `TelemetryAdapterHandle` whose `metrics()` feeds `PoolConfig::metrics`.
- `TelemetryAdapter`: `start_span`, `start_span_with_parent`, `end_span(&SpanData)`, `emit_log(SpanId, &SdkLogRecord)`, `disable_root`, `export_metrics(&[u8])` (an OTLP `ExportMetricsServiceRequest` protobuf), `record_metric`.
- The server's `OtlpAdapter`:
  - `end_span` → an `opentelemetry_sdk` batch span processor with an `opentelemetry-otlp` span exporter;
  - `emit_log` → a batch log processor with an OTLP log exporter;
  - `export_metrics` → `POST {endpoint}/v1/metrics` with `Content-Type: application/x-protobuf`;
  - `start_span` is a no-op returning `true`.
- Metrics from `monty-pool` (`monty.pool.*`, `monty.run.*`, `monty.turn.*`) go to OTLP only. The Prometheus `/metrics` families of R12 are the server's own counters.
- `monty-pool` spawns `monty subprocess` with `env_clear()`, inherited stderr and `kill_on_drop(true)` (`worker.rs:183-201`). In the image, child stderr lands in the container log.

### Q13. CI

Options offered: new ubuntu docker job (amd64 + arm64 runners); amd64 job only; no CI changes.

**Answer:** new ubuntu docker job.

**Resolution R13.**

- `.github/workflows/ci.yml` keeps the `test` matrix unchanged.
- Job `docker` on `ubuntu-latest`: set up buildx with GHA cache, `make docker-build PLATFORMS=linux/amd64`, `make test-docker`, `make test-network`.
- Job `docker-arm64` on `ubuntu-24.04-arm`: `make docker-build PLATFORMS=linux/arm64`, `make test-network`.
- The Dockerfile `MONTY_REV` build argument comes from the workflow's `MONTY_REV` (full SHA).

## 8. Verification (phase 2.3)

### 8.1 Additional facts checked

- **Named build context.** A spike with `--build-context monty-src=<dir>` showed BuildKit applies a `.dockerignore` placed at the root of the named directory (a 50 MB `target/` was excluded, 88 B transferred). `../monty` has no `.dockerignore` and its `target/` is 3.0 GB. montygo MUST NOT write into `../monty`.
- **Client mapping of server events** (`internal/pool/checkout.go:339-369`, `session.go:64-90`):

  | Server sends | montygo `pool.Error` | Public error | Session |
  |---|---|---|---|
  | `Error` event | `KindRuntime` | `*RuntimeError` (by exception type) | kept |
  | `TypingError` | `KindTyping` | `*TypingError` | kept |
  | `FatalError` event | `KindCrashed`, `Announced` | `*CrashedError` | poisoned |
  | `ShutdownDump` | `KindShutdown` | `*ShutdownError{Dump}` | poisoned |
  | close / EOF | `KindDisconnected` | `*DisconnectError` | poisoned |
  | nothing within `RequestTimeout` | `KindTimeout` | `*CrashedError{TimedOut}` | poisoned |

### 8.2 Findings: inconsistencies and controversies

- **F1.** `CLAUDE.md` defines "done" as Go vet, tests on both backends, race detector and golangci-lint. The Rust server and the Docker suites are outside it.
- **F2.** `CLAUDE.md`: "Error messages … MUST match upstream". Server-originated texts (invalid dump signature, lifecycle request refusal, capacity rejection) have no public upstream source, because Full Monty is closed.
- **F3.** `CLAUDE.md`: "Ported tests MUST … run on every backend through `eachBackend`". The `websocket` backend needs a running server, so it cannot run on a plain `go test ./...`.
- **F4.** `overview.md` (process model, "Pure Go at build time") and the `CLAUDE.md` layout and upstream-source tables do not know `server/`, `docker/`, `tests/network` or the image artifact.
- **F5.** The R4 local override would upload 3 GB of `../monty/target` per build.
- **F6.** Default mismatch between client and server:
  - client `RequestTimeout` 10 s vs server `--max-duration` 60 s and `--turn-timeout` 300 s;
  - server `--idle-timeout` 60 s ends a session whose host callback takes longer than 60 s, because no request is sent while the host computes.
- **F7.** How the server reports its own failures decides what montygo users see.
- **F8.** The "Supporting a new Monty release" checklist in `CLAUDE.md` does not move the server `Cargo.toml` revs, the Dockerfile `MONTY_REV`, the image tag or the CI docker jobs.

### 8.3 Proposed resolutions

- **F1.** Extend "done": `cargo clippy --all-targets -- -D warnings` and `cargo test` in `server/`, `make docker-build`, `make test-docker`, `make test-network`, plus `go vet`, `golangci-lint` and `go mod tidy -diff` in `tests/network`.
- **F2.** Server-originated texts are defined once in `docs/architecture/server.md` and listed as deviations in `docs/parity/server.md`. Texts that exist in `monty-pool` (`PoolError` Display) are reused verbatim.
- **F3.** `testBackends` defaults to `native,wasm`, and appends `websocket` automatically when `MONTY_TEST_WS_URL` is set. An explicit `MONTY_TEST_BACKENDS=websocket` without a URL fails `TestMain` with a clear message instead of skipping.
- **F4.** New `docs/architecture/server.md` and `docs/architecture/docker.md`. Update `overview.md` (process model, artifacts, key technologies), `websocket.md` (TLS, dialer, health), `testing.md` (websocket backend, `tests/network`), `CLAUDE.md` layout, commands, environment variables and upstream-source tables, and the README.
- **F5.** `make docker-build` stages the override context: `git -C $(MONTY_SRC) ls-files -z --cached --others --exclude-standard | rsync --files-from=- --from0` into `build/monty-src/` (gitignored), then `--build-context monty-src=build/monty-src`. Uncommitted upstream changes are included; `target/` is not.
- **F6.** No default changes. `docs/architecture/server.md` and the README state that clients running long scripts MUST raise `RequestTimeout`, and that host callbacks longer than `--idle-timeout` end the session. The `make test-docker` server runs with default flags except `--max-sessions-per-client 0`, because no root test waits 60 s.
- **F7.** Forwarding policy:

  | Server-side outcome | Sent to client | Then |
  |---|---|---|
  | `turn_raw` → `Ok(event)` | the event (dumps signed) | continue; `FatalError` → close 1000 |
  | `PoolError::Runtime(MemoryError)` (worker killed) | `Error{MemoryError, "the worker exceeded its memory limit and was terminated"}` | close 1011 |
  | `PoolError::Crashed`, `Timeout`, worker `Protocol`, `Spawn`, `Exhausted` | `FatalError{PoolError Display}` | close 1011 |
  | client misuse (`Configure` twice, `Reset`, `Shutdown`, undecodable frame, text frame) | nothing | close 1008 with reason |
  | invalid dump envelope on `Load` | `Error{ValueError, "invalid session dump signature"}` | continue |
  | idle, keepalive, session, turn timeout | nothing | close 1008 with reason |
  | drain | `ShutdownDump{signed dump?}` | close 1001 |
  | capacity / client quota at upgrade | HTTP 503 / 429 | no upgrade |

- **F8.** The checklist step 2 gains `server/Cargo.toml` revs, `docker/Dockerfile` `ARG MONTY_REV`, the image tag in the Makefile, and the CI docker jobs.

**Answer:** apply all proposals F1–F8.

## 9. NOT CONSIDERED & TODO

Each item has tags and a proposed handling. "Doc" means the behaviour stays as designed and is written down. "NotImplemented" means a code path exists and returns an explicit `NotImplemented` error or comment.

| # | Item | Tags | Proposed handling |
|---|---|---|---|
| N1 | Server-side authentication (bearer token, mTLS client certs, per-tenant quotas). Full Monty has none. | postponed, porting:improvement-over-upstream | Doc. Client side is ready (`ConnectHeaders`, `TLSConfig`). Server has no auth flag. |
| N2 | Native TLS listener in the server (`--tls-cert`, `--tls-key`). | postponed, potential-improvement | Doc: terminate TLS at an ingress, as Full Monty. |
| N3 | Automatic restore in the Go client after `ShutdownError` (reconnect, `LoadSession`/`LoadSnapshot`, resend the request). Host callbacks may run twice. | too-complex, potential-improvement | Doc + example in README. |
| N4 | Spawn-ahead (`--prewarm N`) to shave worker spawn latency. | potential-improvement | Not implemented. |
| N5 | Raw-bytes variant of `turn_raw` to avoid the per-frame decode and re-encode. | potential-improvement, porting:improvement-over-upstream | Not implemented; propose upstream. |
| N6 | Dump ceilings drift: a dump signed under higher ceilings loads after the operator lowers `--max-memory-mib`/`--max-duration`. The `Load` reply echoes duration and suspensions, never memory. | too-complex | Stub: after a successful `Load`, compare echoed `max_duration_micros` and `max_suspensions` with the ceilings and close 1008 when exceeded; memory gets a `NotImplemented` comment. |
| N7 | Container hardening: seccomp/AppArmor profile, read-only rootfs, `--pids-limit`, cgroup memory sizing guidance. | postponed | Doc: recommended `docker run` flags in `docs/architecture/docker.md`. |
| N8 | Per-worker OS limits (`RLIMIT_AS`, CPU time) in addition to the allocator limit. | postponed | Not implemented. |
| N9 | Horizontal scaling: load balancer affinity, dump restore across hosts, shared `--dump-key` distribution. | postponed | Doc only. |
| N10 | Other image platforms (`linux/arm/v7`, `riscv64`, Windows containers). | postponed | Not implemented. |
| N11 | Publishing to a registry, image signing (cosign), SBOM and provenance attestations. | postponed | `make docker-push` only. |
| N12 | Docker on macOS CI runners. | postponed | Not implemented (no Docker on GitHub macOS runners). |
| N13 | Full Monty's reverse proxy to a full CPython sandbox. | porting:deffered, too-complex | Not implemented. |
| N14 | `--logfire-token` exporter. | porting:simplefication | Deviation listed in parity (R11). |
| N15 | Interop test with the Python client `pydantic_monty.AsyncMontyWebsocket` against `monty-server`, proving drop-in compatibility with Full Monty clients. | potential-improvement | Not in scope. |
| N16 | `MONTY_EXAMPLES_BACKEND=websocket` for all example programs (only `examples/repl` gains `-ws`). | postponed | Not in scope. |
| N17 | permessage-deflate compression and HTTP/2 WebSocket (RFC 8441). | potential-improvement | Not implemented; compression stays disabled like the client. |
| N18 | DoS surface: up to `--max-sessions × 256 MiB` buffered messages, slow upgrade handshakes. | too-complex | Doc: size the container; upgrade handshake timeout 10 s. |
| N19 | Drain races: a second SIGTERM during drain, `Load` or `Dump` in flight at SIGTERM, drain before `Configure`. | too-complex | Designed default: second SIGTERM drops every session at once; in-flight turns finish (bounded by `--drain-grace`); pre-`Configure` sessions get `ShutdownDump{}` with no dump. Covered by `tests/network`. |
| N20 | Dump key from a file or secret store (`--dump-key-file`). | potential-improvement | Not implemented. |
| N21 | A signed dump exceeding `MAX_FRAME_LEN` after the 41-byte envelope. | too-complex | Stub: a signed `ShutdownDump` that would overflow is sent as `ShutdownDump{}` without a dump and logged; an overflowing `DumpResult` is answered with `FatalError("response frame of N bytes exceeds maximum of M bytes")` (the worker's own wording) and close 1011. |

### Q14. Items pulled into scope

Options offered (multi-select): N6 ceilings in the envelope; N15 Python client interop; N16 examples on websocket; N1 server bearer token.

**Answer:** N15 only.

**Resolution R14.**

- N15 moves into scope: `tests/network` runs the upstream Python WebSocket client against the unit's server. Design below after the protocol-version check.
- N6 keeps the stub (echoed duration and suspensions checked, memory `NotImplemented`). The envelope layout of R3 is unchanged.
- N1, N16 and every other item stay as listed in §9.

### 9.1 N15 feasibility: protocol versions

- Pinned `monty-proto` (`f8acf4fa`): `PROTOCOL_VERSION = 3`, `MIN_SUPPORTED_PROTOCOL_VERSION = 3`. Version 2 is not served: "its `Print` event carried a single stream and text".
- Tag `v0.0.23`: `PROTOCOL_VERSION = 2`. The bump came with #818 (`print(file=sys.stderr)`), after the tag. `f8acf4fa` is contained in no tag.
- PyPI `pydantic-monty 0.0.23` is a pure meta package requiring `pydantic-monty-client==0.0.23` and `pydantic-monty-runtime==0.0.23`.
- Consequence: a PyPI 0.0.23 Python client sends `protocol_version = 2`, and `monty-server` answers `FatalError("unsupported protocol version 2 (server supports protocol version 3, try updating to a newer client version)")`. PyPI can prove only the rejection path; a positive interop test needs a client built from the pinned revision.
- The refusal text comes from `monty_proto::check_protocol_version` and is reused verbatim (F2).

### Q15. Python client source for N15

Options offered: build a client image from the pin; rejection path only; `uv pip install git+…` at test time.

**Answer:** build the client image from the pin.

**Resolution R15.**

- `crates/monty-python` is `pydantic-monty-client`: a pyo3 `cdylib` built with maturin (`module-name = "pydantic_monty._monty"`), depending on `monty-pool` with `telemetry`. The remote test needs only the client, not `pydantic-monty-runtime`.
- `docker/pyclient.Dockerfile`: builder `rust:1.95-bookworm` + `python3.13` + `maturin`, sources from the same `MONTY_REV` (or the staged override context of F5), `maturin build --release -m crates/monty-python/Cargo.toml`, final `python:3.13-slim` with the wheel installed and `tests/network/pyclient/*.py` scripts copied. Host architecture only.
- `make docker-build-pyclient` tags `monty-pyclient:<version>`. `tests/network` reads `MONTYGO_PYCLIENT_IMAGE` (default that tag) and skips the positive scenarios when the image is absent.
- Networking: the client container dials `ws://<server ContainerIP>:8000/` on the default bridge (testcontainers `ContainerIP`). This also proves a non-loopback dial to a remote host.
- Tests: `TestPyClient_FeedRunOverWebSocket`, `TestPyClient_HostFunctionAndDumpRestore`, `TestPyClient_DrainShutdownDumpRestore` (pinned image), `TestPyClient_PyPIProtocol2IsRejected` (`python:3.13-slim` + `pip install pydantic-monty==0.0.23`, asserts the protocol-version `FatalError` text).
- Refinement for the target: the PyPI 0.0.23 client is installed into a second virtualenv (`/opt/pypi-0.0.23`) inside the same pyclient image at build time, so tests need no network at run time.

## 10. Summary

### 10.1 What exists and what is missing

- montygo already has the WebSocket client (`monty.NewWebSocket`), wire-compatible with upstream's closed "Full Monty" server and the Python `AsyncMontyWebsocket`.
- Upstream has no open server. Its closed server is built on `monty-pool`'s `Checkout::turn_raw`, which says so in its doc comment.
- The interpreter cannot be hosted in-process by a server: `monty-alloc` limits are process-global.

### 10.2 Resolutions

| # | Topic | Resolution |
|---|---|---|
| R1 | Server scope | Full Monty parity: caps, quotas, idle, keepalive, session and turn timeouts, limit ceilings, `/health`, drain with `ShutdownDump`, signed dumps, tracing |
| R2 | Test scope | Root suite gains a `websocket` backend via `MONTY_TEST_WS_URL`; a separate testcontainers module holds server tests |
| R3 | Dump signing | Strict `MTYD` v1 HMAC-SHA256 envelope; unsigned or foreign dumps are a `ValueError` |
| R4 | Location and sources | `server/` crate in montygo; Docker builds from the pinned git rev, or a staged `../monty` override |
| R5 | Multi-arch | Cross-compile with cargo-zigbuild to musl; `scratch` image; buildx loads both arches |
| R6 | Engine | `monty-pool` `Pool` + `Checkout::turn_raw` |
| R7 | Go API | `WebSocketOptions.TLSConfig`, `.DialContext`, `monty.CheckWebSocketHealth` |
| R8 | REPL mode | Session REPL scenarios and `examples/repl -ws` driven over stdin/stdout |
| R9 | Test pool | `tests/network`, one container per unit, queue, no spare, `MONTYGO_*` variables |
| R10 | Workers | Fresh `monty subprocess` per session, no reuse |
| R11 | Tracing | Generic OTLP exporter; no Logfire flag |
| R12 | Observability | Prometheus `/metrics` |
| R13 | CI | `docker` (amd64) and `docker-arm64` jobs |
| R14 | Scope pulls | N15 (Python client interop) only |
| R15 | Python client | Image built from the pin; PyPI 0.0.23 venv for the protocol-rejection test |
| D1 | Naming | `MONTYGO_*` for harness variables; `MONTY_SERVER_*` for the server |
| F1–F8 | Verification | All proposals applied (§8.3) |

### 10.3 Evidence gathered by running things

- Cross-compiling `monty-pool` (with `aws-lc-sys`) and the worker to both musl targets works with zig; binaries are static; `monty-pool`'s WebSocket suite passes on aarch64 Alpine.
- BuildKit honours `.dockerignore` only inside a named context; `../monty/target` is 3.0 GB, so the override is staged.
- PyPI `pydantic-monty 0.0.23` speaks protocol 2; the pin serves only protocol 3.
- testcontainers-go v0.44.0 supports Go 1.25.

### 10.4 Deviations from upstream to list in `docs/parity/server.md`

- Dump envelope format (Full Monty's is private).
- OTLP exporter instead of `--logfire-token`.
- `GET /metrics` extension.
- Server-originated error and close texts.
- `ShutdownDump` overflow handling (N21).

BRAINSTORM DONE
