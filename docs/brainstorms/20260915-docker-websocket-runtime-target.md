# Target architecture: dockerized Monty runtime over WebSocket

Date: 2026-09-15. Source brainstorm: `20260915-docker-websocket-runtime.md`. Upstream pin: Monty `0.0.23` + `main@f8acf4fa` (`f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28`), wire protocol 3.

This document is the implementation specification. Work through it top to bottom. Items marked **verify** name an external API detail that MUST be confirmed against the dependency's source before coding; the decision around it is fixed.

---

## 1. Initial request

> Need to add support additional runtime for montygo - dockerized runtime of monty.
> What should be in this scope:
> 1. Ability to build monty docker images where should be a websocket server which hosts monty, it can be on Rust to avoid redundant serializations/marshaling etc.
> 2. `make docker-build` should build the docker images for amd64 and arm64 architectures to run natively on linux and macos w/o issues with architecture
> 3. Need to design and implement abstractions in montygo which can work with docker container via websocket, it would be good to investigate ../monty which possible already has this implementation. It should support to connecto to remote host with this container, not only localhost (we can use connection without TLS, but architecture should allow to secure this connection in future)
> 4. Need to write tests with testcontainers where it run backed docker image and tests interoperations. It should use conception of "pool" for parallel tests as in ../meridian-overlay project, you can copy and adapt some code from there - /Volumes/M4DATA/Users/speckzzz/reps/neria/meridian-overlay/tests/cluster
> 5. All the current adapted tests and cluster tests should pass. Cluster tests should also have testing of "repl mode"

Later directives from the session:

- Harness environment variables use the `MONTYGO_*` prefix instead of `MERIDIAN_*`.
- The test directory is `tests/network`, not `tests/cluster`. A unit is one container; there are no replicas and no spare units.

---

## 2. High-level architecture

### 2.1 Deployment view

```
 Go host process (montygo)                          Docker host (local or remote)
┌───────────────────────────────────────┐          ┌──────────────────────────────────────────────┐
│ application                           │          │ container monty-server:<version>-<rev>       │
│   │                                   │          │  (scratch, uid 65532, static musl binaries)  │
│ monty.NewWebSocket(WebSocketOptions{  │          │                                              │
│   URL, TLSConfig, DialContext,        │  ws://   │  monty-server (Rust, tokio + axum)           │
│   ConnectHeaders, RequestTimeout })   ├─────────▶│   ├─ GET /        info page                  │
│   │                                   │  wss://  │   ├─ GET /health  readiness                  │
│ internal/pool (turn engine)           │ (via TLS │   ├─ GET /metrics Prometheus                 │
│ internal/worker.WebSocketDialer       │  ingress │   └─ WS  /        one session per connection │
│   one dial per checkout               │  or test │        │ admission (503/429)                 │
│                                       │  proxy)  │        │ Session state machine               │
│ monty.CheckWebSocketHealth ───────────┼─────────▶│        │  clamp limits, sign/verify dumps,  │
└───────────────────────────────────────┘          │        │  timeouts, keepalive, drain         │
                                                   │        ▼                                     │
                                                   │  monty_pool::Pool (subprocess transport)     │
                                                   │   Checkout::turn_raw per request             │
                                                   │        │ 4-byte LE framed stdio              │
                                                   │        ▼                                     │
                                                   │  /usr/local/bin/monty subprocess             │
                                                   │   (fresh child per session, env cleared)     │
                                                   │                                              │
                                                   │  OTLP exporter ─────────▶ collector (opt.)   │
                                                   └──────────────────────────────────────────────┘
```

### 2.2 Build view

```
make docker-build
  ├─ docker-stage-src: git ls-files of $(MONTY_SRC) ─▶ build/monty-src/   (only when MONTY_SRC exists)
  └─ docker buildx build --platform linux/amd64,linux/arm64 --load -f docker/Dockerfile
        stage toolchain   rust:1.95-bookworm on $BUILDPLATFORM + cmake + zig + cargo-zigbuild
        stage monty-git   git fetch MONTY_REV
        stage monty-src   scratch + sources         ◀── replaced by --build-context monty-src=build/monty-src
        stage build       cargo zigbuild --target <musl triple of $TARGETARCH>
                            monty-runtime --no-default-features  ─▶ /out/monty
                            server (patched to /monty-src)      ─▶ /out/monty-server
        final             scratch: monty, monty-server, CA bundle, LICENSE files
  tags: monty-server:<Version>-<UpstreamRev>, monty-server:latest

make docker-build-pyclient   (host arch)
  docker/pyclient.Dockerfile: maturin build of crates/monty-python at the pin + PyPI 0.0.23 client venv
  tag: monty-pyclient:<Version>-<UpstreamRev>
```

### 2.3 Test view

```
make test-docker                                   make test-network
  docker run monty-server (1 container)              cd tests/network (own go.mod)
  MONTY_TEST_WS_URL=ws://127.0.0.1:<port>/            MONTYGO_NETWORK_TESTS=1
  go test . with MONTY_TEST_BACKENDS=websocket        TestMain ─▶ ContainerPool.Start (N units)
  root suite: eachBackend(native|wasm|websocket)      tests ─▶ SetupServer(t) ─▶ Acquire (queue)
                                                       ├─ server behaviour (admission, limits, timeouts,
                                                       │  dumps, drain, TLS, telemetry, parallel)
                                                       ├─ REPL session + examples/repl -ws CLI
                                                       └─ Python client containers (pinned + PyPI 0.0.23)
```

---

## 3. Decisions

| # | Question | Chosen | Rejected alternatives |
|---|---|---|---|
| R1 | Server feature level | Full Monty parity: `--max-sessions` (503), `--max-sessions-per-client` (429), `--trust-forwarded-for`, idle/keepalive/session/turn timeouts, limit ceilings, `/health`, `/`, drain with `ShutdownDump`, signed dumps, tracing | thin relay; operational subset |
| R2 | Test scope | Root suite gains `websocket` backend (`MONTY_TEST_WS_URL`); separate testcontainers module for server behaviour | module only; testcontainers in the root module |
| R3 | Dump signing | `MTYD` v1 envelope, HMAC-SHA256, `--dump-key` + `--dump-key-previous`, strict | opt-in unsigned; client unwrap helper |
| R4 | Location and sources | `server/` crate in montygo; Docker fetches the pinned rev, `make docker-build` overrides with staged `../monty` | `../monty` only; separate repository |
| R5 | Multi-arch | cargo-zigbuild cross-compile to musl on `$BUILDPLATFORM`; `scratch`; `--load` both arches | QEMU builds; host arch by default |
| R6 | Server engine | `monty_pool::Pool` + `Checkout::turn_raw` | own zero-copy relay; hybrid |
| R7 | Go abstraction | Extend `WebSocketOptions` (`TLSConfig`, `DialContext`), add `CheckWebSocketHealth` | exported `RemoteTransport` interface; public `montydocker` module |
| R8 | REPL mode | Session REPL scenarios + `examples/repl -ws` CLI driven over pipes | one of the two |
| R9 | Test pool | `tests/network`; unit = 1 container; queue; no spare | 2 replicas per unit; shared servers |
| R10 | Worker lifecycle | Fresh child per session (`max_checkouts_per_worker = 1`, `min_processes = 0`) | spawn-ahead; `Reset` reuse |
| R11 | Tracing exporter | Generic OTLP (`--otlp-endpoint`, `--otlp-protocol`) | Logfire + OTLP; stderr only |
| R12 | Observability | Prometheus `GET /metrics` | JSON on `/`; none |
| R13 | CI | `docker` job (amd64) + `docker-arm64` job; existing matrix unchanged | amd64 only; none |
| R14 | NOT CONSIDERED pulls | N15 (Python client interop) | N1, N6, N16 |
| R15 | Python client source | Image built from the pin with maturin; PyPI 0.0.23 client venv for the rejection test | rejection only; install from git at test time |
| D1 | Env naming | `MONTYGO_*` harness variables; `MONTY_SERVER_*` server; existing `MONTY_*` unchanged | — |
| F1 | Definition of done | adds cargo clippy/test, docker build, `test-docker`, `test-network`, lint/tidy in `tests/network` | — |
| F2 | Server texts | defined in `docs/architecture/server.md`, deviations in `docs/parity/server.md`; `monty-pool`/`monty-proto` texts reused verbatim | — |
| F3 | Backend selection | `websocket` auto-appended when `MONTY_TEST_WS_URL` is set; explicit `websocket` without URL fails `TestMain` | explicit opt-in only |
| F4 | Docs | new `server.md`, `docker.md`; update overview, websocket, testing, CLAUDE.md, README | — |
| F5 | Override context | stage `git ls-files` of `MONTY_SRC` into `build/monty-src` | pass `../monty` directly (3 GB `target/`) |
| F6 | Default mismatches | unchanged; documented | — |
| F7 | Failure forwarding | table in §7.9 | policy drops as `FatalError` |
| F8 | Upgrade checklist | adds server revs, Dockerfile `MONTY_REV`, image tag, CI jobs | — |

---

## 4. Key components

### 4.1 `monty-server` (Rust crate `server/`)

`monty-server` is a WebSocket front end for `monty_pool`. It owns one `Pool` configured for the subprocess transport with `max_processes = --max-sessions`, `min_processes = 0` and `max_checkouts_per_worker = Some(1)`. An HTTP router (axum 0.8) serves `/`, `/health` and `/metrics`, and upgrades WebSocket requests on `/`. Admission runs before the upgrade: a full server answers 503, a caller over its quota 429, a draining server 503. Each upgraded connection becomes one `Session` task. The session requires `Configure` first, checks the protocol version, clamps limits to the server ceilings, checks out a worker, and synthesizes the `Ok` the client expects. Every later frame is decoded into `pb::ParentRequest` and driven through `Checkout::turn_raw`; `Print` events are streamed as they arrive; the turn-ender is forwarded after dumps are signed. `Load` requests are verified and unwrapped before they reach the worker. The session enforces idle, keepalive, session and turn deadlines, and follows the drain protocol on SIGTERM. The binary also has a `probe` subcommand used by the image `HEALTHCHECK`, because `scratch` has no shell.

### 4.2 Dump envelope

Every dump that leaves the server (`DumpResult.state`, `ShutdownDump.dump`) is wrapped in a 41-byte header carrying a magic, a version, a key id and an HMAC-SHA256 over the header fields, the pinned upstream revision and the raw state. `Load` accepts only envelopes whose MAC verifies under the current key or the previous key. Any failure produces one fixed `ValueError` text and never reaches the worker. The envelope closes the hole where `Load` carries the session's limits inside opaque bytes. The format is montygo's own; Full Monty's is private.

### 4.3 Limit ceilings

The server has three ceilings: duration, memory and recursion depth. A client value above a ceiling is lowered to it; an absent client value takes the ceiling; a disabled ceiling (0) passes the client value through; recursion depth is always bounded. `gc_interval` and `max_suspensions` pass through. After a successful `Load`, the reply's echoed `max_duration_micros` and `max_suspensions` are compared with the ceilings (N6 stub); the memory limit is not echoed and is a documented gap.

### 4.4 Drain

SIGTERM closes the listener at once (new connections are refused), flips `monty_server_draining` to 1 and starts `--drain-grace`. A session with a turn in flight lets it finish and forwards its result. The next request of any session is not run: the server captures a dump through `turn_raw(Dump)` when a checkout exists, signs it, sends `ShutdownDump{dump}` and closes with 1001. A session with no checkout gets `ShutdownDump{}`. When the grace expires, every remaining session is closed with 1001 and no dump. A second SIGTERM or SIGINT closes everything immediately. The process then closes the pool, flushes telemetry and exits 0.

### 4.5 Docker image and build

`docker/Dockerfile` cross-compiles both binaries on the build platform with cargo-zigbuild for `x86_64-unknown-linux-musl` and `aarch64-unknown-linux-musl`, proven by the session spike including `aws-lc-sys`. Upstream sources come from a `monty-src` stage that fetches `MONTY_REV`, or from the named build context `monty-src` that the Makefile stages from `MONTY_SRC` without `target/`. The server is always built against `/monty-src` through Cargo path patches, so the worker and the server share one source tree. The final image is `scratch` with the two binaries, a CA bundle for OTLP over HTTPS, license files, `USER 65532:65532`, `ENTRYPOINT monty-server`, `CMD --host 0.0.0.0` and a `HEALTHCHECK` running `monty-server probe`.

### 4.6 Go client extensions

`WebSocketOptions` gains `TLSConfig` and `DialContext`. The dialer builds its transport from them and still captures the raw TCP connection underneath TLS, so `Kill` keeps unblocking pending reads. `CheckWebSocketHealth` derives `http(s)://host[/prefix]/health` from the URL and performs a GET over the same transport with the same connect headers and a timeout. No Docker type enters the root module.

### 4.7 Root suite on the `websocket` backend

`testmain_test.go` learns the backend name, appends it when `MONTY_TEST_WS_URL` is set, and routes every pool construction through one `openPool` helper that maps `Options` onto `WebSocketOptions`. Helpers that used to call `monty.New` directly (`telPool`, `TestInstallDependencies`, `TestPublicAPI`) use `openPool`. The telemetry child process decodes the backend by name. One memory test accepts the interpreter's own `MemoryError` text on the websocket backend, because the server ceiling fires before the allocator abort.

### 4.8 `tests/network` harness

A separate Go module adapted from meridian-overlay's cluster pool. `ContainerPool` holds `MONTYGO_TEST_PARALLEL` units; each unit is one `monty-server` container with a lifecycle goroutine and states `cold → ready → inUse → releasing → ready | dead`. `Start` returns when the first unit is ready; infrastructure failures abort fast. `Acquire` blocks on a channel, optionally recreating the container with per-test flags. `Release` checks `/health` and `monty_server_sessions_active == 0`; a unit that ran with overrides, was signalled, or fails the check is recreated in its lifecycle goroutine. Containers carry the label `montygo.test=network`; logs stream to `tests/network/output/containers/`. Tests are grouped by `Test<Area>_` prefixes.

### 4.9 `examples/repl -ws`

The REPL example gains `-ws URL`, `-ws-ca FILE` and `-ws-insecure-skip-verify`. With `-ws`, the pool is built with `monty.NewWebSocket`; everything after checkout is unchanged. `tests/network` builds the binary once with `go build -C ../../examples -o <tmp>/repl ./repl` and drives it over stdin and stdout.

### 4.10 Python client image and interop tests

`docker/pyclient.Dockerfile` builds `pydantic-monty-client` from `crates/monty-python` at the pin with maturin into `/opt/pin`, and installs `pydantic-monty-client==0.0.23` from PyPI into `/opt/pypi-0.0.23`. Test scripts live in `tests/network/pyclient/` and are copied into the image. Tests run the image as a one-shot container that dials `ws://<server ContainerIP>:8000/`, proving a non-loopback remote dial.

### 4.11 CI

Two new jobs build the image for one platform each and run `make test-docker` and `make test-network` (arm64 runs `test-network` only). The `MONTY_REV` workflow variable feeds the Dockerfile. Buildx uses the GitHub Actions cache.

### 4.12 Documentation

`docs/architecture/server.md` specifies the server: flags, session state machine, texts, close codes, metrics, envelope and drain. `docs/architecture/docker.md` specifies the image, build, override staging, run recommendations and hardening notes. `docs/parity/server.md` lists deviations from Full Monty. Existing documents are updated as listed in §6.9.

---

## 5. Schemas

### 5.1 Databases and caches

There is no database, ORM model or cache in this change. No existing table, migration or cache exists in montygo and none is added. The persistent and wire-level schemas of this change are listed below instead.

### 5.2 Server configuration schema

Every flag has an environment variable `MONTY_SERVER_<UPPER_SNAKE>` unless noted. A flag wins over its variable. Durations are whole seconds; `0` disables where stated.

| Flag | Env | Type | Default (binary) | Image default | Validation |
|---|---|---|---|---|---|
| `--host` | `MONTY_SERVER_HOST` | string | `127.0.0.1` | `0.0.0.0` (CMD) | resolvable |
| `--port` | `MONTY_SERVER_PORT` | u16 | `8000` | `8000` | `0` = ephemeral |
| `--monty-bin` | `MONTY_BIN` | path | `monty` on `PATH` | `/usr/local/bin/monty` | exists, executable |
| `--max-sessions` | `MONTY_SERVER_MAX_SESSIONS` | usize | `64` | | `≥ 1` |
| `--max-sessions-per-client` | `MONTY_SERVER_MAX_SESSIONS_PER_CLIENT` | usize | `10` | | `0` disables |
| `--idle-timeout` | `MONTY_SERVER_IDLE_TIMEOUT` | u64 s | `60` | | `0` disables |
| `--keepalive` | `MONTY_SERVER_KEEPALIVE` | u64 s | `5` | | `0` disables |
| `--session-timeout` | `MONTY_SERVER_SESSION_TIMEOUT` | u64 s | `3600` | | `0` disables |
| `--turn-timeout` | `MONTY_SERVER_TURN_TIMEOUT` | u64 s | `300` | | `0` disables |
| `--drain-grace` | `MONTY_SERVER_DRAIN_GRACE` | u64 s | `30` | | any |
| `--max-memory-mib` | `MONTY_SERVER_MAX_MEMORY_MIB` | u64 MiB | `64` | | `0` disables |
| `--max-duration` | `MONTY_SERVER_MAX_DURATION` | u64 s | `60` | | `0` disables |
| `--max-recursion-depth` | `MONTY_SERVER_MAX_RECURSION_DEPTH` | usize | `1000` | | `≥ 1` |
| `--trust-forwarded-for` | `MONTY_SERVER_TRUST_FORWARDED_FOR` | bool | off | | |
| `--dump-key` | `MONTY_SERVER_DUMP_KEY` | bytes (UTF-8 of the value) | required | required | `≥ 16` bytes |
| `--dump-key-previous` | `MONTY_SERVER_DUMP_KEY_PREVIOUS` | bytes | none | | `≥ 16` bytes, differs from `--dump-key` |
| `--otlp-endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` | URL | none (no export) | | `http`/`https` |
| `--otlp-protocol` | `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` \| `grpc` | `http/protobuf` | | |
| — | `OTEL_EXPORTER_OTLP_HEADERS` | read by the exporter | | | |
| — | `OTEL_SERVICE_NAME` | string | `monty-server` | | |

Subcommand `monty-server probe [--url URL]` (default `http://127.0.0.1:${MONTY_SERVER_PORT:-8000}/health`): exit 0 on HTTP 200 within 2 s, else 1.

Startup failures exit with code 2 and one stderr line `monty-server: <reason>`.

Fixed internal constants:

| Constant | Value | Why |
|---|---|---|
| `HANDSHAKE_TIMEOUT` | 10 s | N18: slow upgrade requests |
| `MAX_MESSAGE_LEN` | `monty_proto::MAX_FRAME_LEN` (256 MiB) | a client may send any frame the protocol accepts |
| `CHECKOUT_TIMEOUT` | 5 s | admission already bounds capacity; a wait means a leak |
| `CLOSE_WRITE_BUDGET` | 1 s | mirrors the client |
| `ENVELOPE_HEADER_LEN` | 41 bytes | §5.3 |

### 5.3 Dump envelope (`MTYD` v1)

```
offset  size  field       value
0       4     magic       0x4D 0x54 0x59 0x44  ("MTYD")
4       1     version     0x01
5       4     key_id      SHA-256(key)[0..4]
9       32    mac         HMAC-SHA256(key, magic ‖ version ‖ key_id ‖ MONTY_REV ‖ state)
41      n     state       raw worker dump bytes (DumpResult.state)
```

- `MONTY_REV` is the 40-character ASCII hex SHA constant in `server/src/version.rs`.
- MAC comparison MUST be constant-time (`Mac::verify_slice`).
- Keys are tried in order: current, previous. `key_id` only selects; a mismatching id is a failure.
- Signing a dump whose envelope would exceed `MAX_FRAME_LEN` is refused (N21, §7.9).

```rust
// server/src/envelope.rs
pub const MAGIC: [u8; 4] = *b"MTYD";
pub const VERSION: u8 = 1;
pub const HEADER_LEN: usize = 41;

pub struct DumpKeys {
    current: Key,
    previous: Option<Key>,
}

struct Key {
    secret: Vec<u8>,
    id: [u8; 4],
}

#[derive(Debug, PartialEq, Eq)]
pub enum VerifyError {
    TooShort,
    BadMagic,
    UnsupportedVersion(u8),
    UnknownKey,
    BadMac,
}
```

### 5.4 Prometheus metrics schema (`GET /metrics`)

| Family | Type | Labels | Incremented when |
|---|---|---|---|
| `monty_server_sessions_active` | gauge | — | +1 after upgrade, −1 when the session task ends |
| `monty_server_sessions_total` | counter | `outcome` ∈ `closed`, `error`, `timeout`, `drained`, `drain_dropped` | session task ends |
| `monty_server_rejections_total` | counter | `reason` ∈ `capacity`, `client_quota`, `draining`, `bad_request` | upgrade refused |
| `monty_server_timeouts_total` | counter | `kind` ∈ `idle`, `keepalive`, `session`, `turn` | a deadline closes a session |
| `monty_server_dumps_total` | counter | `op` ∈ `signed`, `verified`, `rejected` | envelope operation |
| `monty_server_workers_spawned_total` | counter | — | successful `pool.checkout` |
| `monty_server_draining` | gauge | — | 0 → 1 on first SIGTERM |
| `monty_server_build_info` | gauge = 1 | `version`, `monty_rev` | startup |

Content type `application/openmetrics-text; version=1.0.0; charset=utf-8` (prometheus-client encoder). `outcome=drain_dropped` is added over the brainstorm's R12 label set for sessions silent through `--drain-grace`.

### 5.5 Container and harness schema

| Item | Value |
|---|---|
| Image | `monty-server:<Version>-<UpstreamRev>` and `:latest` (e.g. `monty-server:0.0.23-f8acf4fa`) |
| Python client image | `monty-pyclient:<Version>-<UpstreamRev>` |
| Image labels | `org.opencontainers.image.source`, `.version`, `.revision` (montygo git SHA), `io.montygo.monty-rev` |
| Exposed port | `8000/tcp` |
| Test container label | `montygo.test=network` |
| Test dump key | `montygo-network-test-dump-key` (29 bytes) |
| Test log directory | `tests/network/output/containers/<test>-u<unit>.log` |

| Variable | Module | Default | Meaning |
|---|---|---|---|
| `MONTY_TEST_WS_URL` | root | unset | URL of a running server; enables the `websocket` backend |
| `MONTY_TEST_BACKENDS` | root | `native,wasm` (+`websocket` when URL set) | explicit backend list |
| `MONTYGO_NETWORK_TESTS` | tests/network | unset | must be `1` or `TestMain` exits 0 with a skip line |
| `MONTYGO_TEST_PARALLEL` | tests/network | `4` | pool size and `-parallel` budget |
| `MONTYGO_TEST_IMAGE` | tests/network | `monty-server:latest` | server image |
| `MONTYGO_PYCLIENT_IMAGE` | tests/network | `monty-pyclient:latest` | Python client image; absent image skips `TestPyClient_*` |
| `MONTYGO_DUMP_CONTAINER_LOGS` | tests/network | `1` | `0` disables log files |
| `MONTYGO_SLOW_TESTS_ENABLE` | tests/network | unset | enables `TestTimeouts_Keepalive*` and session-timeout tests |
| `MONTY_DOCKER_SRC` | Makefile | `auto` | `auto` stages `MONTY_SRC` when present, `git` forces the pinned fetch |
| `PLATFORMS` | Makefile | `linux/amd64,linux/arm64` | buildx platforms |
| `IMAGE`, `PYCLIENT_IMAGE`, `IMAGE_TAG` | Makefile | `monty-server`, `monty-pyclient`, `$(VERSION)-$(UPSTREAM_REV)` | tags |

---

## 6. Files, types and signatures

Legend: **CREATED** or **UPDATED**. Rust signatures are exact intent; bodies are in §8.

### 6.1 `server/` (Rust)

#### `server/Cargo.toml` — CREATED

```toml
[package]
name = "monty-server"
version = "0.0.23"
edition = "2024"
rust-version = "1.95"
license = "MIT"
publish = false

[lib]
name = "monty_server"
path = "src/lib.rs"

[[bin]]
name = "monty-server"
path = "src/main.rs"

[dependencies]
monty-pool = { git = "https://github.com/pydantic/monty", rev = "f8acf4fa", features = ["telemetry"] }
monty-proto = { git = "https://github.com/pydantic/monty", rev = "f8acf4fa" }
monty-types = { git = "https://github.com/pydantic/monty", rev = "f8acf4fa" }
tokio = { version = "1.53", features = ["rt-multi-thread", "macros", "net", "signal", "sync", "time"] }
tokio-util = { version = "0.7", features = ["rt"] }
axum = { version = "0.8.9", default-features = false, features = ["http1", "tokio", "ws"] }
futures-util = { version = "0.3", default-features = false, features = ["std", "sink"] }
bytes = "1"
prost = "=0.14.4"
clap = { version = "4.6", features = ["derive", "env"] }
hmac = "0.12.1"
sha2 = "0.10.9"
prometheus-client = "0.25.1"
opentelemetry = "0.32"
opentelemetry_sdk = { version = "0.32", features = ["rt-tokio", "logs"] }
opentelemetry-otlp = { version = "0.32", features = ["http-proto", "reqwest-client", "grpc-tonic", "logs", "trace"] }
opentelemetry-proto = "0.32"
reqwest = { version = "0.12", default-features = false, features = ["rustls-tls"] }

[dev-dependencies]
tokio-tungstenite = "0.27"
tempfile = "3"

[profile.release]
lto = "thin"
codegen-units = 1
strip = true

[workspace]
```

- **verify** the `opentelemetry-otlp 0.32` feature names and the `reqwest` major version it expects.
- `Cargo.lock` is committed. `cargo clippy --locked` in CI proves it is current.

#### `server/src/version.rs` — CREATED

```rust
pub const SERVER_VERSION: &str = env!("CARGO_PKG_VERSION");
/// Full upstream SHA baked into the dump MAC. Moves with `proto/PROTO_REV`.
pub const MONTY_REV: &str = "f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28";
```

Unit test `monty_rev_matches_lockfile`: parses `Cargo.lock` for the `monty-pool` source `git+…?rev=f8acf4fa#<sha>` and asserts `<sha> == MONTY_REV`.

#### `server/src/config.rs` — CREATED

```rust
#[derive(clap::Parser, Debug, Clone)]
#[command(name = "monty-server", version)]
pub struct Cli {
    #[command(subcommand)]
    pub command: Option<Command>,
    #[command(flatten)]
    pub serve: ServeArgs,
}

#[derive(clap::Subcommand, Debug, Clone)]
pub enum Command {
    /// Exit 0 when the health endpoint answers 200.
    Probe {
        #[arg(long)]
        url: Option<String>,
    },
}

#[derive(clap::Args, Debug, Clone)]
pub struct ServeArgs {
    #[arg(long, env = "MONTY_SERVER_HOST", default_value = "127.0.0.1")]
    pub host: String,
    #[arg(long, env = "MONTY_SERVER_PORT", default_value_t = 8000)]
    pub port: u16,
    #[arg(long, env = "MONTY_BIN", default_value = "monty")]
    pub monty_bin: PathBuf,
    #[arg(long, env = "MONTY_SERVER_MAX_SESSIONS", default_value_t = 64)]
    pub max_sessions: usize,
    #[arg(long, env = "MONTY_SERVER_MAX_SESSIONS_PER_CLIENT", default_value_t = 10)]
    pub max_sessions_per_client: usize,
    #[arg(long, env = "MONTY_SERVER_IDLE_TIMEOUT", default_value_t = 60)]
    pub idle_timeout: u64,
    #[arg(long, env = "MONTY_SERVER_KEEPALIVE", default_value_t = 5)]
    pub keepalive: u64,
    #[arg(long, env = "MONTY_SERVER_SESSION_TIMEOUT", default_value_t = 3600)]
    pub session_timeout: u64,
    #[arg(long, env = "MONTY_SERVER_TURN_TIMEOUT", default_value_t = 300)]
    pub turn_timeout: u64,
    #[arg(long, env = "MONTY_SERVER_DRAIN_GRACE", default_value_t = 30)]
    pub drain_grace: u64,
    #[arg(long, env = "MONTY_SERVER_MAX_MEMORY_MIB", default_value_t = 64)]
    pub max_memory_mib: u64,
    #[arg(long, env = "MONTY_SERVER_MAX_DURATION", default_value_t = 60)]
    pub max_duration: u64,
    #[arg(long, env = "MONTY_SERVER_MAX_RECURSION_DEPTH", default_value_t = 1000)]
    pub max_recursion_depth: usize,
    #[arg(long, env = "MONTY_SERVER_TRUST_FORWARDED_FOR")]
    pub trust_forwarded_for: bool,
    #[arg(long, env = "MONTY_SERVER_DUMP_KEY", hide_env_values = true)]
    pub dump_key: Option<String>,
    #[arg(long, env = "MONTY_SERVER_DUMP_KEY_PREVIOUS", hide_env_values = true)]
    pub dump_key_previous: Option<String>,
    #[arg(long, env = "OTEL_EXPORTER_OTLP_ENDPOINT")]
    pub otlp_endpoint: Option<String>,
    #[arg(long, env = "OTEL_EXPORTER_OTLP_PROTOCOL", default_value = "http/protobuf")]
    pub otlp_protocol: OtlpProtocol,
}

#[derive(clap::ValueEnum, Debug, Clone, Copy, PartialEq, Eq)]
pub enum OtlpProtocol {
    #[value(name = "http/protobuf")]
    HttpProtobuf,
    Grpc,
}

/// Validated, unit-converted configuration.
#[derive(Debug, Clone)]
pub struct Config {
    pub bind: String,                    // "host:port"
    pub monty_bin: PathBuf,              // resolved absolute path
    pub max_sessions: usize,
    pub max_sessions_per_client: Option<usize>,
    pub idle_timeout: Option<Duration>,
    pub keepalive: Option<Duration>,
    pub session_timeout: Option<Duration>,
    pub turn_timeout: Option<Duration>,
    pub drain_grace: Duration,
    pub ceilings: Ceilings,
    pub trust_forwarded_for: bool,
    pub dump_keys: Arc<DumpKeys>,
    pub otlp: Option<OtlpConfig>,
}

#[derive(Debug, Clone)]
pub struct OtlpConfig {
    pub endpoint: String,
    pub protocol: OtlpProtocol,
}

#[derive(Debug)]
pub struct ConfigError(pub String);

impl ServeArgs {
    pub fn validate(self) -> Result<Config, ConfigError>;
}

fn secs(v: u64) -> Option<Duration>;                          // 0 → None
fn resolve_binary(path: &Path) -> Result<PathBuf, ConfigError>; // PATH lookup, exec bit
```

#### `server/src/limits.rs` — CREATED

```rust
/// Server ceilings; `None` means disabled.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Ceilings {
    pub max_duration_micros: Option<u64>,
    pub max_memory_bytes: Option<u64>,
    pub max_recursion_depth: u64,
}

impl Ceilings {
    pub fn from_args(max_duration_s: u64, max_memory_mib: u64, max_recursion_depth: usize) -> Self;
    /// Lowers or fills a client's limits; passes gc_interval and max_suspensions through.
    pub fn clamp(&self, client: Option<pb::ResourceLimits>) -> pb::ResourceLimits;
    /// N6 stub: echoed budget of a restored session must not exceed the ceilings.
    pub fn admits_restored(&self, event: &pb::ChildEvent) -> bool;
}

fn clamp_opt(client: Option<u64>, ceiling: Option<u64>) -> Option<u64>;

/// Builds the monty-pool session config from a clamped client Configure.
pub fn repl_config(configure: &pb::Configure, limits: pb::ResourceLimits) -> ReplConfig;
```

`repl_config` maps:

| `pb::Configure` | `ReplConfig` |
|---|---|
| `script_name` (empty → `"main.py"`) | `script_name` |
| clamped limits → `ResourceLimits::from(pb)` | `limits: Some(..)` |
| `type_check`, `type_check_stubs` | same |
| `type_check_format` → `TypeCheckingFormat::from(pb::TypeCheckFormat)`, `type_check_color` | `type_check_config` |
| `assert_message_annotations` → `map_or_else(AssertMessageAnnotations::default, AssertMessageAnnotations::from_max_bytes)` | `assert_message_annotations` |
| `print_flush_interval_ms` → `Duration::from_millis` | `print_flush_interval` |
| `protocol_version`, `monty_version` | not mapped (checked / ignored) |

**verify** that `AssertMessageAnnotations::from_max_bytes` and the `From` conversions are public in `monty-types`/`monty-proto`.

#### `server/src/envelope.rs` — CREATED

```rust
impl DumpKeys {
    pub fn new(current: &[u8], previous: Option<&[u8]>) -> Result<Self, ConfigError>;
    /// Wraps a raw worker dump. None when the envelope would exceed MAX_FRAME_LEN.
    pub fn sign(&self, state: &[u8]) -> Option<Vec<u8>>;
    /// Returns the raw dump when the envelope verifies under the current or previous key.
    pub fn verify<'a>(&self, envelope: &'a [u8]) -> Result<&'a [u8], VerifyError>;
}

fn key_id(secret: &[u8]) -> [u8; 4];
fn mac(key: &Key, state: &[u8]) -> Hmac<Sha256>;             // over magic|version|key_id|MONTY_REV|state
```

#### `server/src/identity.rs` — CREATED

```rust
/// Caller identity for the per-client quota.
pub fn client_id(peer: SocketAddr, headers: &HeaderMap, trust_forwarded_for: bool) -> String;
```

Rule: with `trust_forwarded_for`, take the last comma-separated `X-Forwarded-For` entry, trim, parse as `IpAddr`; on any failure fall back to `peer.ip()`. The id is `IpAddr::to_string()` (canonical IPv6).

#### `server/src/admission.rs` — CREATED

```rust
pub struct Admission {
    max_sessions: usize,
    per_client: Option<usize>,
    state: Mutex<AdmissionState>,
    metrics: Arc<Metrics>,
}

struct AdmissionState {
    active: usize,
    by_client: HashMap<String, usize>,
}

#[derive(Debug, PartialEq, Eq)]
pub enum Rejection {
    Capacity,
    ClientQuota,
    Draining,
}

/// Holds one session slot; dropping it releases the slot.
pub struct SessionPermit {
    admission: Arc<Admission>,
    client: String,
}

impl Admission {
    pub fn new(max_sessions: usize, per_client: Option<usize>, metrics: Arc<Metrics>) -> Arc<Self>;
    pub fn try_admit(self: &Arc<Self>, client: String, draining: bool) -> Result<SessionPermit, Rejection>;
}

impl Drop for SessionPermit {
    fn drop(&mut self);
}
```

#### `server/src/metrics.rs` — CREATED

```rust
#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
pub struct OutcomeLabel { pub outcome: Outcome }
#[derive(Clone, Copy, Debug, Hash, PartialEq, Eq, EncodeLabelValue)]
pub enum Outcome { Closed, Error, Timeout, Drained, DrainDropped }

#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
pub struct ReasonLabel { pub reason: RejectReason }
#[derive(Clone, Copy, Debug, Hash, PartialEq, Eq, EncodeLabelValue)]
pub enum RejectReason { Capacity, ClientQuota, Draining, BadRequest }

#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
pub struct KindLabel { pub kind: TimeoutKind }
#[derive(Clone, Copy, Debug, Hash, PartialEq, Eq, EncodeLabelValue)]
pub enum TimeoutKind { Idle, Keepalive, Session, Turn }

#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
pub struct OpLabel { pub op: DumpOp }
#[derive(Clone, Copy, Debug, Hash, PartialEq, Eq, EncodeLabelValue)]
pub enum DumpOp { Signed, Verified, Rejected }

pub struct Metrics {
    registry: Registry,
    pub sessions_active: Gauge,
    pub sessions_total: Family<OutcomeLabel, Counter>,
    pub rejections_total: Family<ReasonLabel, Counter>,
    pub timeouts_total: Family<KindLabel, Counter>,
    pub dumps_total: Family<OpLabel, Counter>,
    pub workers_spawned_total: Counter,
    pub draining: Gauge,
}

impl Metrics {
    pub fn new() -> Arc<Self>;                 // registers families and build_info
    pub fn render(&self) -> String;            // prometheus_client::encoding::text::encode
}
```

#### `server/src/texts.rs` — CREATED

All server-originated texts live here, so `docs/architecture/server.md` mirrors one file.

```rust
pub const INVALID_DUMP: &str = "invalid session dump signature";
pub const HTTP_CAPACITY: &str = "monty server at capacity";
pub const HTTP_CLIENT_QUOTA: &str = "too many sessions for this client";
pub const HTTP_DRAINING: &str = "monty server is shutting down";
pub const CLOSE_EXPECTED_CONFIGURE: &str = "expected Configure as the first request";
pub const CLOSE_ALREADY_CONFIGURED: &str = "session already configured";
pub const CLOSE_LIFECYCLE: &str = "lifecycle requests (Reset/Shutdown) are not accepted";
pub const CLOSE_TEXT_MESSAGE: &str = "text messages are not part of the protocol";
pub const CLOSE_EMPTY_REQUEST: &str = "request has no kind";
pub const CLOSE_RESTORED_OVER_LIMITS: &str = "restored session exceeds server limits";
pub const CLOSE_SHUTTING_DOWN: &str = "server is shutting down";
pub const MEMORY_KILLED: &str = "the worker exceeded its memory limit and was terminated";

pub fn close_malformed(err: &dyn Display) -> String;              // "malformed request frame: {err}", ≤123 bytes
pub fn close_idle(limit: Duration) -> String;                     // "idle timeout of {n}s exceeded"
pub fn close_session(limit: Duration) -> String;                  // "session timeout of {n}s exceeded"
pub fn close_turn(limit: Duration) -> String;                     // "turn timeout of {n}s exceeded"
pub fn frame_too_large(len: usize, max: u32) -> String;           // "response frame of {len} bytes exceeds maximum of {max} bytes"
pub fn info_page(bound: &str) -> String;                          // "monty-server {v} (monty {rev})\nWebSocket endpoint: ws://{bound}/\n"
```

- Protocol refusal text: `monty_proto::check_protocol_version` output, verbatim.
- Worker failure text: `monty_pool::PoolError` `Display`, verbatim.
- `MEMORY_KILLED` equals montygo's native text in `internal/pool/checkout.go:285`.

#### `server/src/events.rs` — CREATED

```rust
pub fn encode(event: &pb::ChildEvent) -> Result<Bytes, FrameError>;   // monty_proto::encode_to_capped_vec
pub fn decode_request(bytes: &[u8]) -> Result<pb::ParentRequest, FrameError>; // monty_proto::decode_frame
pub fn ok(limits: &pb::ResourceLimits) -> pb::ChildEvent;             // Ok + max_duration_micros + max_suspensions
pub fn error(exc_type: ExcType, message: &str) -> pb::ChildEvent;     // Error{exception}
pub fn fatal(message: &str) -> pb::ChildEvent;                        // FatalError{message}
pub fn shutdown(dump: Option<Vec<u8>>) -> pb::ChildEvent;             // ShutdownDump{dump}
pub fn is_suspension(event: &pb::ChildEvent) -> bool;                 // FunctionCall|OsCall|NameLookup|ResolveFutures
```

- The server MUST NOT enable `monty-proto`'s `worker` feature (it links the interpreter). `fatal` builds `pb::FatalError` directly. **verify** field names of `pb::FatalError` and `pb::Error`.

#### `server/src/outbound.rs` — CREATED

```rust
pub enum Outbound {
    Frame(Bytes),
    Ping,
    Close { code: u16, reason: String },
}

/// Owns the sink. Ends after a Close or a write error.
pub async fn writer(mut sink: SplitSink<WebSocket, Message>, mut rx: mpsc::Receiver<Outbound>);

#[derive(Clone)]
pub struct OutboundTx(mpsc::Sender<Outbound>);

impl OutboundTx {
    pub async fn frame(&self, event: &pb::ChildEvent) -> Result<(), SessionEnd>;
    pub async fn ping(&self) -> Result<(), SessionEnd>;
    pub async fn close(&self, code: u16, reason: impl Into<String>);
}
```

Channel depth 16. Close codes: `1000` normal, `1001` going away, `1008` policy violation, `1011` internal error.

#### `server/src/inbound.rs` — CREATED

```rust
pub enum Inbound {
    Request(Bytes),
    Text,
    Closed,
}

/// Reads the stream; forwards binary messages, records pongs, reports end.
pub async fn reader(
    mut stream: SplitStream<WebSocket>,
    tx: mpsc::Sender<Inbound>,
    last_pong: Arc<AtomicU64>, // ms since session start
    started: Instant,
);
```

Channel depth 1: the protocol is strictly alternating, so a second queued request back-pressures the socket.

#### `server/src/session.rs` — CREATED

```rust
pub struct SessionDeps {
    pub pool: Arc<monty_pool::Pool>,
    pub config: Arc<Config>,
    pub metrics: Arc<Metrics>,
    pub drain: DrainSignals,
    pub telemetry: Option<Arc<Telemetry>>,
}

pub struct Session {
    deps: SessionDeps,
    permit: SessionPermit,
    client: String,
    trace_parent: Option<String>,
    started: Instant,
    state: State,
    out: OutboundTx,
    last_pong: Arc<AtomicU64>,
    keepalive: Keepalive,
    idle_deadline: Option<Instant>,
    turn_deadline: Option<Instant>,
    draining: bool,
}

enum State {
    AwaitingConfigure,
    Ready { checkout: Checkout },
    Suspended { checkout: Checkout },
    Closed,
}

/// Why a session ended; drives the outcome metric and the close frame.
pub enum SessionEnd {
    ClientClosed,
    Violation(String),          // close 1008
    Timeout(TimeoutKind),       // close 1008 (keepalive: no close frame)
    WorkerFailed,               // FatalError/Error already sent, close 1011 or 1000
    Drained,                    // ShutdownDump sent, close 1001
    DrainDropped,               // close 1001, no dump
    WriteFailed,
}

struct Keepalive {
    interval: Option<Duration>,
    ping_sent_at: Option<Instant>,
}

impl Session {
    pub fn new(deps: SessionDeps, permit: SessionPermit, client: String, trace_parent: Option<String>) -> Self;
    pub async fn run(self, socket: WebSocket);

    async fn serve(&mut self, inbound: &mut mpsc::Receiver<Inbound>) -> SessionEnd;
    async fn on_request(&mut self, bytes: Bytes) -> Result<(), SessionEnd>;
    async fn configure(&mut self, configure: pb::Configure) -> Result<(), SessionEnd>;
    async fn load(&mut self, envelope: Vec<u8>, trace_parent: Option<String>) -> Result<(), SessionEnd>;
    async fn turn(&mut self, request: pb::ParentRequest) -> Result<(), SessionEnd>;
    async fn forward(&mut self, request_kind: RequestKind, event: pb::ChildEvent) -> Result<(), SessionEnd>;
    async fn fail(&mut self, err: monty_pool::PoolError) -> SessionEnd;
    async fn shutdown_dump(&mut self) -> SessionEnd;
    fn next_deadline(&self) -> Option<(Instant, Wake)>;
    fn end(&mut self, end: &SessionEnd);   // metrics, telemetry event, stderr line
}

enum Wake { Idle, Session, Turn, Keepalive, DrainGrace }

enum RequestKind { Feed, Resume, Load, Dump, InstallDependencies, AbortFeed }
```

#### `server/src/drain.rs` — CREATED

```rust
#[derive(Clone)]
pub struct DrainSignals {
    pub soft: CancellationToken,    // first SIGTERM/SIGINT
    pub hard: CancellationToken,    // second signal or grace expiry
    pub grace_deadline: Arc<OnceLock<Instant>>,
}

impl DrainSignals {
    pub fn new() -> Self;
}

/// Waits for signals; cancels soft on the first, hard on the second or when grace expires.
pub async fn watch_signals(signals: DrainSignals, grace: Duration, metrics: Arc<Metrics>);
```

#### `server/src/http.rs` — CREATED

```rust
#[derive(Clone)]
pub struct AppState {
    pub deps: Arc<SharedDeps>,       // pool, config, metrics, drain, telemetry, admission
    pub bound: String,
    pub tracker: TaskTracker,
}

pub fn router(state: AppState) -> Router;

async fn root(
    State(state): State<AppState>,
    ConnectInfo(peer): ConnectInfo<SocketAddr>,
    headers: HeaderMap,
    upgrade: Result<WebSocketUpgrade, WebSocketUpgradeRejection>,
) -> Response;

async fn health(State(state): State<AppState>) -> Response;   // 200 empty; 503 while draining
async fn metrics(State(state): State<AppState>) -> Response;  // 200 text
```

**verify** axum 0.8 names: `WebSocketUpgrade::{max_message_size, max_frame_size, on_upgrade}`, `WebSocketUpgradeRejection`, `into_make_service_with_connect_info::<SocketAddr>()`, `Message::Binary(Bytes)`, `CloseFrame { code, reason: Utf8Bytes }`.

#### `server/src/telemetry.rs` — CREATED

```rust
pub struct Telemetry {
    handle: monty_pool::telemetry::TelemetryAdapterHandle,
    tracer: opentelemetry_sdk::trace::SdkTracer,
    providers: Providers,     // tracer, logger providers for flush and shutdown
}

pub struct OtlpAdapter {
    spans: opentelemetry_sdk::trace::BatchSpanProcessor,
    logs: opentelemetry_sdk::logs::BatchLogProcessor,
    metrics_client: reqwest::Client,
    metrics_url: String,       // {endpoint}/v1/metrics (http/protobuf); grpc: MetricsServiceClient
}

impl monty_pool::telemetry::TelemetryAdapter for OtlpAdapter {
    fn start_span(&self, span: &SpanData) -> bool;                     // true, no-op
    fn end_span(&self, span: &SpanData) -> bool;                       // spans.on_end(span.clone())
    fn emit_log(&self, parent_span_id: SpanId, record: &SdkLogRecord) -> bool;
    fn disable_root(&self, trace_id: TraceId, root_span_id: SpanId);   // no-op
    fn export_metrics(&self, payload: &[u8]);                          // fire-and-forget POST
}

impl Telemetry {
    pub fn install(cfg: &OtlpConfig) -> Result<Arc<Self>, ConfigError>;
    pub fn pool_metrics(&self) -> monty_pool::telemetry::Metrics;
    pub fn connection_span(&self, trace_parent: Option<&str>, client: &str, user_agent: Option<&str>) -> ConnectionSpan;
    pub async fn shutdown(&self);
}

pub struct ConnectionSpan { /* opentelemetry span + context */ }

impl ConnectionSpan {
    pub fn event(&self, name: &'static str, attrs: &[KeyValue]);
    pub fn checkout_context(&self) -> monty_pool::telemetry::TelemetryContext;
    pub fn end(self, outcome: Outcome);
}
```

- **verify** how `BatchSpanProcessor` accepts `SpanData` outside the SDK tracer in `opentelemetry_sdk 0.32`. If it cannot, `end_span` pushes into a bounded channel drained by a task that calls `SpanExporter::export` in batches of 512 or every 5 s.
- **verify** the `TelemetryContext` constructor that takes a remote parent (`context_with_remote` / `context_from_ids`).
- Without `--otlp-endpoint`, no adapter is configured, `PoolConfig::metrics` is `None` and checkouts carry no telemetry context.

#### `server/src/logline.rs` — CREATED

```rust
/// One logfmt line on stderr; no global tracing subscriber is installed.
pub fn log(level: Level, event: &'static str, fields: &[(&str, &dyn Display)]);
```

Events: `listening`, `session_start`, `session_end`, `rejected`, `timeout`, `dump_rejected`, `worker_failed`, `drain_started`, `drain_finished`.

#### `server/src/app.rs` — CREATED

```rust
pub struct Server {
    pub bound: SocketAddr,
    handle: JoinHandle<Result<(), ServeError>>,
    pub drain: DrainSignals,
}

/// Binds, builds the pool and router, prints the bound URL on stdout, serves until drained.
pub async fn start(config: Config) -> Result<Server, ServeError>;

impl Server {
    pub async fn wait(self) -> Result<(), ServeError>;
}

#[derive(Debug)]
pub enum ServeError {
    Bind(io::Error),
    Pool(monty_pool::PoolError),
    Io(io::Error),
}
```

#### `server/src/lib.rs` — CREATED

```rust
pub mod admission;
pub mod app;
pub mod config;
pub mod drain;
pub mod envelope;
pub mod events;
pub mod http;
pub mod identity;
pub mod inbound;
pub mod limits;
pub mod logline;
pub mod metrics;
pub mod outbound;
pub mod probe;
pub mod session;
pub mod telemetry;
pub mod texts;
pub mod version;
```

#### `server/src/probe.rs` — CREATED

```rust
/// Plain HTTP/1.1 GET over a TcpStream; no TLS. Exit code for `monty-server probe`.
pub fn probe(url: Option<&str>) -> ExitCode;
```

#### `server/src/main.rs` — CREATED

```rust
fn main() -> ExitCode;   // parse Cli; Probe → probe(); else validate, runtime, app::start, wait
```

#### Rust tests — CREATED

| File | Scope | Needs |
|---|---|---|
| `server/src/envelope.rs` `#[cfg(test)]` | sign/verify round trip, tamper each region, previous key, unknown key, too short, bad version, overflow → `None` | — |
| `server/src/limits.rs` `#[cfg(test)]` | clamp matrix (absent/lower/higher/disabled), recursion always bounded, `admits_restored` | — |
| `server/src/identity.rs` `#[cfg(test)]` | peer, XFF last entry, invalid XFF, IPv6 canonical | — |
| `server/src/admission.rs` `#[cfg(test)]` | capacity, quota, draining, permit drop releases | — |
| `server/src/config.rs` `#[cfg(test)]` | key length, zero rules, env precedence | — |
| `server/src/version.rs` `#[cfg(test)]` | `monty_rev_matches_lockfile` | — |
| `server/tests/session.rs` | in-process server on `127.0.0.1:0` with a real worker via `MONTY_BIN` (skip when unset), tokio-tungstenite client with `pb` frames: configure/clamp, feed with prints, dump sign + load, invalid dump, lifecycle refusal, version skew, drain | `MONTY_BIN` |

### 6.2 Docker

#### `.dockerignore` — CREATED (repository root)

```
*
!server/
server/target/
!docker/
!tests/network/pyclient/
!LICENSE
!THIRD_PARTY_NOTICES.md
```

#### `docker/Dockerfile` — CREATED

```dockerfile
# syntax=docker/dockerfile:1.7
ARG RUST_VERSION=1.95
ARG MONTY_REV=f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28

FROM --platform=$BUILDPLATFORM rust:${RUST_VERSION}-bookworm AS toolchain
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN apt-get update \
 && apt-get install -y --no-install-recommends cmake python3-pip git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN pip3 install --break-system-packages "ziglang==0.13.0" "cargo-zigbuild==0.20.1"
RUN rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl

FROM toolchain AS monty-git
ARG MONTY_REV
WORKDIR /src
RUN git init -q && git remote add origin https://github.com/pydantic/monty \
 && git fetch -q --depth 1 origin "${MONTY_REV}" && git checkout -q FETCH_HEAD

# Replaced wholesale by `--build-context monty-src=<dir>`.
FROM scratch AS monty-src
COPY --from=monty-git /src/ /

FROM toolchain AS build
ARG TARGETARCH
COPY --from=monty-src / /monty-src/
COPY server/ /work/server/
RUN mkdir -p /out
RUN --mount=type=cache,id=cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=monty-target-${TARGETARCH},target=/monty-src/target \
    --mount=type=cache,id=server-target-${TARGETARCH},target=/work/server/target \
    case "${TARGETARCH}" in \
      amd64) T=x86_64-unknown-linux-musl ;; \
      arm64) T=aarch64-unknown-linux-musl ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && cd /monty-src \
 && cargo zigbuild --locked --release --target "$T" -p monty-runtime --no-default-features \
 && cp "target/$T/release/monty" /out/monty \
 && cd /work/server \
 && P='patch."https://github.com/pydantic/monty"' \
 && cargo zigbuild --release --target "$T" \
      --config "$P.monty-pool.path=\"/monty-src/crates/monty-pool\"" \
      --config "$P.monty-proto.path=\"/monty-src/crates/monty-proto\"" \
      --config "$P.monty-types.path=\"/monty-src/crates/monty-types\"" \
      --config "$P.monty-fs.path=\"/monty-src/crates/monty-fs\"" \
 && cp "target/$T/release/monty-server" /out/monty-server \
 && cp /monty-src/LICENSE /out/LICENSE.monty

FROM scratch
ARG MONTY_REV
ARG MONTYGO_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/asalimonov/montygo" \
      org.opencontainers.image.version="0.0.23" \
      org.opencontainers.image.revision="${MONTYGO_REVISION}" \
      io.montygo.monty-rev="${MONTY_REV}"
COPY --from=toolchain /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/monty /usr/local/bin/monty
COPY --from=build /out/monty-server /usr/local/bin/monty-server
COPY --from=build /out/LICENSE.monty /usr/share/licenses/monty/LICENSE
COPY LICENSE /usr/share/licenses/montygo/LICENSE
COPY THIRD_PARTY_NOTICES.md /usr/share/licenses/montygo/THIRD_PARTY_NOTICES.md
ENV MONTY_BIN=/usr/local/bin/monty \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s CMD ["/usr/local/bin/monty-server", "probe"]
ENTRYPOINT ["/usr/local/bin/monty-server"]
CMD ["--host", "0.0.0.0"]
```

- **verify** pinned versions of `ziglang` and `cargo-zigbuild` at implementation time and write them here.
- The server build omits `--locked` because the path patches change the lock resolution; the committed `server/Cargo.lock` is checked by `cargo clippy --locked` outside Docker.
- `monty-fs` is patched too because `monty-pool` depends on it.

#### `docker/pyclient.Dockerfile` — CREATED

```dockerfile
# syntax=docker/dockerfile:1.7
ARG RUST_VERSION=1.95
ARG MONTY_REV=f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28

FROM rust:${RUST_VERSION}-bookworm AS monty-git
ARG MONTY_REV
WORKDIR /src
RUN git init -q && git remote add origin https://github.com/pydantic/monty \
 && git fetch -q --depth 1 origin "${MONTY_REV}" && git checkout -q FETCH_HEAD

FROM scratch AS monty-src
COPY --from=monty-git /src/ /

FROM rust:${RUST_VERSION}-bookworm AS wheel
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-dev python3-venv \
 && rm -rf /var/lib/apt/lists/*
RUN python3 -m venv /opt/maturin && /opt/maturin/bin/pip install "maturin>=1.9.4,<2.0"
COPY --from=monty-src / /monty-src/
WORKDIR /monty-src
RUN --mount=type=cache,id=cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=pyclient-target,target=/monty-src/target \
    /opt/maturin/bin/maturin build --release --locked -m crates/monty-python/Cargo.toml \
      -i python3.13 --compatibility off -o /wheels

FROM python:3.13-slim
COPY --from=wheel /wheels/ /wheels/
RUN python -m venv /opt/pin && /opt/pin/bin/pip install --no-cache-dir /wheels/pydantic_monty_client-*.whl \
 && python -m venv /opt/pypi-0.0.23 && /opt/pypi-0.0.23/bin/pip install --no-cache-dir "pydantic-monty-client==0.0.23"
COPY tests/network/pyclient/ /scripts/
USER 65532:65532
ENTRYPOINT ["/opt/pin/bin/python"]
```

- **verify** that `rust:1.95-bookworm` can provide `python3.13`; if bookworm ships 3.11 only, the wheel stage uses `python:3.13-bookworm` plus rustup instead, and the `-i` argument follows.
- Host architecture only (`docker build`, not buildx multi-platform).

### 6.3 `Makefile` — UPDATED

New variables and targets (existing targets unchanged):

```make
VERSION := $(shell sed -n 's/^[[:space:]]*Version = "\(.*\)"/\1/p' monty.go)
UPSTREAM_REV := $(shell sed -n 's/^[[:space:]]*UpstreamRev = "\(.*\)"/\1/p' monty.go)
MONTY_REV_FULL := $(shell sed -n 's/^[[:space:]]*MONTY_REV: //p' .github/workflows/ci.yml)
IMAGE ?= monty-server
PYCLIENT_IMAGE ?= monty-pyclient
IMAGE_TAG ?= $(VERSION)-$(UPSTREAM_REV)
PLATFORMS ?= linux/amd64,linux/arm64
MONTY_DOCKER_SRC ?= auto
DOCKER_SRC_STAGE := build/monty-src
TEST_DUMP_KEY := montygo-network-test-dump-key

ifeq ($(MONTY_DOCKER_SRC),auto)
DOCKER_SRC_CONTEXT := $(if $(wildcard $(MONTY_SRC)/crates/monty-proto),--build-context monty-src=$(DOCKER_SRC_STAGE),)
else
DOCKER_SRC_CONTEXT :=
endif

.PHONY: docker-stage-src
docker-stage-src: ## Stage tracked MONTY_SRC files (no target/) as the override build context
	@if [ -n "$(DOCKER_SRC_CONTEXT)" ]; then \
		rm -rf $(DOCKER_SRC_STAGE) && mkdir -p $(DOCKER_SRC_STAGE) && \
		git -C $(MONTY_SRC) ls-files -z --cached --others --exclude-standard | \
		rsync -a --from0 --files-from=- --ignore-missing-args $(MONTY_SRC)/ $(DOCKER_SRC_STAGE)/; \
	fi

.PHONY: docker-build
docker-build: docker-stage-src ## Build monty-server images for PLATFORMS and load them
	docker buildx build --platform $(PLATFORMS) --load -f docker/Dockerfile \
		--build-arg MONTY_REV=$(MONTY_REV_FULL) \
		--build-arg MONTYGO_REVISION=$(shell git rev-parse HEAD) \
		$(DOCKER_SRC_CONTEXT) \
		-t $(IMAGE):$(IMAGE_TAG) -t $(IMAGE):latest .

.PHONY: docker-build-pyclient
docker-build-pyclient: docker-stage-src ## Build the Python client test image (host arch)
	docker buildx build --load -f docker/pyclient.Dockerfile \
		--build-arg MONTY_REV=$(MONTY_REV_FULL) $(DOCKER_SRC_CONTEXT) \
		-t $(PYCLIENT_IMAGE):$(IMAGE_TAG) -t $(PYCLIENT_IMAGE):latest .

.PHONY: docker-push
docker-push: docker-stage-src ## Push the multi-arch manifest to REGISTRY
	@test -n "$(REGISTRY)" || { echo "REGISTRY is required" >&2; exit 2; }
	docker buildx build --platform $(PLATFORMS) --push -f docker/Dockerfile \
		--build-arg MONTY_REV=$(MONTY_REV_FULL) $(DOCKER_SRC_CONTEXT) \
		-t $(REGISTRY)/$(IMAGE):$(IMAGE_TAG) .

.PHONY: server-check
server-check: ## Clippy and tests for the Rust server
	cd server && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked

.PHONY: test-docker
test-docker: ## Run the root suite on the websocket backend against the image
	@cid=$$(docker run -d --rm -p 127.0.0.1::8000 \
		-e MONTY_SERVER_DUMP_KEY=$(TEST_DUMP_KEY) -e MONTY_SERVER_MAX_SESSIONS_PER_CLIENT=0 \
		--label montygo.test=docker $(IMAGE):$(IMAGE_TAG)) && \
	trap 'docker stop $$cid >/dev/null' EXIT && \
	port=$$(docker port $$cid 8000/tcp | head -1 | sed 's/.*://') && \
	for i in $$(seq 1 100); do curl -sf http://127.0.0.1:$$port/health >/dev/null && break; sleep 0.1; done && \
	MONTY_TEST_WS_URL=ws://127.0.0.1:$$port/ MONTY_TEST_BACKENDS=websocket $(GO) test -count=1 -timeout 30m .

.PHONY: test-network
test-network: ## Run tests/network against the images
	cd tests/network && MONTYGO_NETWORK_TESTS=1 \
		MONTYGO_TEST_IMAGE=$(IMAGE):$(IMAGE_TAG) MONTYGO_PYCLIENT_IMAGE=$(PYCLIENT_IMAGE):$(IMAGE_TAG) \
		$(GO) test -count=1 -timeout 25m -parallel $${MONTYGO_TEST_PARALLEL:-4} ./...

.PHONY: test-network-clean
test-network-clean: ## Remove leaked test containers
	docker ps -aq --filter label=montygo.test | xargs -r docker rm -f
```

### 6.4 Go root module

#### `websocket.go` — UPDATED

```go
package monty

// WebSocketOptions configure a pool of remote workers reached over WebSocket.
type WebSocketOptions struct {
	// URL is dialed verbatim for every checkout.
	URL string
	// MaxProcesses caps concurrent connections: 0 means runtime.NumCPU().
	MaxProcesses int
	// CheckoutTimeout bounds waiting for capacity: 0 waits forever.
	CheckoutTimeout time.Duration
	// RequestTimeout is the per-turn deadline and the dial budget: 0 means 10s, NoRequestTimeout disables it.
	RequestTimeout time.Duration
	// ConnectHeaders supplies upgrade headers. Checkout calls it once with its
	// ctx, before waiting for capacity and dialing; its error fails the checkout unchanged.
	ConnectHeaders func(ctx context.Context) (map[string]string, error)
	// TLSConfig configures wss:// and https:// dials; nil uses the system roots. It is cloned per dial.
	TLSConfig *tls.Config
	// DialContext opens TCP connections; nil uses a net.Dialer.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

func NewWebSocket(ctx context.Context, opts WebSocketOptions) (*Pool, error)

// CheckWebSocketHealth reports nil when the server behind opts.URL answers GET <path>/health with 200.
func CheckWebSocketHealth(ctx context.Context, opts WebSocketOptions) error

func (o WebSocketOptions) dialer(timeout time.Duration) *worker.WebSocketDialer
```

Health error texts:

| Case | Error |
|---|---|
| bad URL or scheme | same text as `Spawn` (`<url>: unsupported URL scheme "x"`) |
| header callback error | returned unchanged |
| transport error | `<health url>: <err>` |
| non-200 | `<health url>: health check returned <code>` |
| timeout | `<health url>: health check timed out after <d>` |

#### `internal/worker/websocket.go` — UPDATED

```go
type WebSocketDialer struct {
	URL string
	// DialTimeout bounds DNS, TCP, TLS and the upgrade: 0 means DefaultDialTimeout.
	DialTimeout time.Duration
	// UserAgent replaces DefaultUserAgent when set.
	UserAgent string
	// TLSConfig is cloned into the transport; nil keeps http.DefaultTransport's settings.
	TLSConfig *tls.Config
	// DialContext replaces net.Dialer.DialContext for the TCP connection.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d *WebSocketDialer) Spawn(ctx context.Context) (Worker, error)                  // uses d.transport
func (d *WebSocketDialer) HealthCheck(ctx context.Context, headers [][2]string) error // CREATED
func (d *WebSocketDialer) transport(raw *atomic.Pointer[net.Conn]) *http.Transport     // CREATED
func healthURL(raw string) (string, error)                                             // CREATED
```

#### `internal/worker/websocket_test.go` — UPDATED

New subtests:

- `TLSConfig is used for wss dials` (`httptest.NewTLSServer`, pool with the server's cert pool).
- `DialContext opens the connection` (counting dialer).
- `Kill unblocks a pending Recv over TLS`.
- `health URL derivation` (table: `ws://h:1/` → `http://h:1/health`, `wss://h/p` → `https://h/p/health`, `ws://h:1` → `http://h:1/health`).
- `HealthCheck reports non-200 and sends connect headers`.

#### `websocket_test.go` (root) — UPDATED

New subtests in `TestWebSocket`: `wss_through_tls_relay` (relay served with `httptest.NewUnstartedServer` + `StartTLS`), `health_check_against_relay` (relay gains a `/health` handler), `dial_context_is_used`.

#### `websocket_relay_test.go` — UPDATED

The relay mux answers `GET /health` with 200 and upgrades everything else; `wsStartRelay(t, tls bool)` gains the TLS switch.

#### `testmain_test.go` — UPDATED

```go
const wsURLEnv = "MONTY_TEST_WS_URL"

func TestMain(m *testing.M)                                     // exits 2 when websocket is named without a URL

// testBackends lists the backends from MONTY_TEST_BACKENDS (default native,wasm, plus websocket when MONTY_TEST_WS_URL is set).
func testBackends() []monty.Backend

// backendByName maps a Backend.String() value back to the Backend.
func backendByName(name string) (monty.Backend, bool)

// openPool builds a pool for b; websocket maps Options onto WebSocketOptions.
func openPool(ctx context.Context, b monty.Backend, opts monty.Options) (*monty.Pool, error)

func sharedPool(t testing.TB, b monty.Backend) *monty.Pool      // uses openPool; websocket errors fail, not skip
func newPool(t testing.TB, b monty.Backend, opts monty.Options) *monty.Pool // uses openPool
```

Option mapping:

| `monty.Options` | `monty.WebSocketOptions` |
|---|---|
| `MaxProcesses` | `MaxProcesses` (shared pools keep 8) |
| `CheckoutTimeout` | `CheckoutTimeout` |
| `RequestTimeout` 0 | `NoRequestTimeout` (keeps "0 disables" semantics) |
| `RequestTimeout` > 0 | same |
| `MinProcesses`, `MaxCheckoutsPerWorker`, `BinaryPath`, `DurationLimitGrace`, `Wasm*`, `WorkerStderr` | ignored |

#### `telemetry_test.go` — UPDATED

```go
func telRun(t *testing.T, test string, cases []telCase)   // child decodes backend with backendByName; unknown → t.Fatalf
func telPool(t *testing.T, b monty.Backend, opts monty.Options) *monty.Pool // uses openPool
```

#### `install_dependencies_test.go` — UPDATED

Both subtests use `openPool(ctx, b, monty.Options{})`.

#### `public_api_conformance_test.go` — UPDATED

`TestPublicAPI` factory uses `openPool(ctx, b, monty.Options{})`.

#### `pool_test.go` — UPDATED

```go
// plRequireMemoryError accepts the interpreter's own MemoryError text on the websocket backend,
// because the server's memory ceiling fires before the allocator abort.
func plRequireMemoryError(t *testing.T, b monty.Backend, err error)
```

Call sites pass `b`. `a refused allocation raises MemoryError and the pool recovers` uses the websocket branch; `exceeding maxMemory in the allocator …` keeps the exact text on every backend, because the server forwards `MEMORY_KILLED` verbatim. **verify** both at implementation by running `make test-docker`; if the second one differs, it gets the same branch and the difference is listed in `docs/parity/tests.md`.

#### `example_test.go` — UPDATED

`ExampleCheckWebSocketHealth` without `// Output:` (compiles, not run).

#### `testdata/public_api.golden` — UPDATED

Regenerated with `UPDATE_GOLDEN=1`. Added lines: `func CheckWebSocketHealth`, and the two `WebSocketOptions` fields if the golden format lists fields.

### 6.5 `tests/network` (Go module)

#### `tests/network/go.mod` — CREATED

```
module github.com/asalimonov/montygo/tests/network

go 1.25.0

require (
	github.com/asalimonov/montygo v0.0.0
	github.com/coder/websocket v1.8.15
	github.com/moby/moby/client <version pulled by testcontainers v0.44.0>
	github.com/stretchr/testify v1.12.1
	github.com/testcontainers/testcontainers-go v0.44.0
	go.opentelemetry.io/proto/otlp <current>
	google.golang.org/protobuf v1.36.12
)

replace github.com/asalimonov/montygo => ../..
```

#### `tests/network/main_test.go` — CREATED

```go
func TestMain(m *testing.M)   // gate MONTYGO_NETWORK_TESTS; resolve image; pool.Start; buildReplOnce lazily; m.Run; pool.Stop
```

#### `tests/network/pool.go` — CREATED

```go
const (
	DefaultParallelism    = 4
	EnvNetworkTests       = "MONTYGO_NETWORK_TESTS"
	EnvTestParallel       = "MONTYGO_TEST_PARALLEL"
	EnvTestImage          = "MONTYGO_TEST_IMAGE"
	EnvPyClientImage      = "MONTYGO_PYCLIENT_IMAGE"
	EnvDumpContainerLogs  = "MONTYGO_DUMP_CONTAINER_LOGS"
	EnvSlowTests          = "MONTYGO_SLOW_TESTS_ENABLE"
	DefaultImage          = "monty-server:latest"
	DefaultPyClientImage  = "monty-pyclient:latest"
	TestDumpKey           = "montygo-network-test-dump-key"
	TestDumpKeyRotated    = "montygo-network-test-dump-key-2"
	serverPort            = "8000/tcp"
	testLabelKey          = "montygo.test"
	testLabelValue        = "network"
)

type unitState int32

const (
	unitCold unitState = iota
	unitReady
	unitInUse
	unitReleasing
	unitDead
)

type unitCmdKind int

const (
	cmdStart unitCmdKind = iota
	cmdRelease
	cmdShutdown
)

type unitCmd struct {
	kind unitCmdKind
	t    *testing.T
	// recreate forces a fresh default container before the unit returns to the queue.
	recreate bool
}

// ServerConfig is the container's argument and environment overlay on top of the defaults.
type ServerConfig struct {
	Args []string
	Env  map[string]string
}

func (c ServerConfig) isDefault() bool
func defaultEnv() map[string]string   // MONTY_SERVER_DUMP_KEY=TestDumpKey, MONTY_SERVER_MAX_SESSIONS_PER_CLIENT=0

type Unit struct {
	ID        int
	state     atomic.Int32
	cmds      chan unitCmd
	done      chan struct{}
	container testcontainers.Container
	cfg       ServerConfig
	host      string
	port      string
	logs      *fileLogConsumer
}

func (u *Unit) URL() string                                   // ws://host:port/
func (u *Unit) HTTPBase() string                              // http://host:port
func (u *Unit) ContainerID() string
func (u *Unit) ContainerIP(ctx context.Context) (string, error)
func (u *Unit) setState(s unitState)
func (u *Unit) getState() unitState
func (u *Unit) tryTransition(from, to unitState) bool

type ContainerPool struct {
	units      []*Unit
	available  chan *Unit
	size       int
	image      string
	mu         sync.Mutex
	started    bool
	firstReady sync.Once
	readyCh    chan struct{}
	allDead    sync.Once
	deadCh     chan struct{}
	deadCount  atomic.Int32
	firstErr   atomic.Pointer[error]
	startDone  atomic.Bool
	stopping   atomic.Bool
}

func GetPool() *ContainerPool
func (p *ContainerPool) Size() int
func (p *ContainerPool) Start(ctx context.Context) error
func (p *ContainerPool) Stop(ctx context.Context) error
func (p *ContainerPool) Acquire(ctx context.Context, cfg ServerConfig) (*Unit, error)
func (p *ContainerPool) Release(t *testing.T, u *Unit, dirty bool)
func (p *ContainerPool) Recreate(ctx context.Context, u *Unit, cfg ServerConfig) error
func (u *Unit) lifecycle(p *ContainerPool)
func (p *ContainerPool) startContainer(ctx context.Context, u *Unit, cfg ServerConfig) error
func (p *ContainerPool) stopContainer(ctx context.Context, u *Unit) error
func (p *ContainerPool) healthyAndIdle(ctx context.Context, u *Unit) error
func (p *ContainerPool) signalReady()
func (p *ContainerPool) signalDead(err error)
func terminateAndVerify(ctx context.Context, c testcontainers.Container, label string) error
func isInfraFailure(err error) bool
```

`infraFailurePatterns`: `cannot connect to the docker daemon`, `no space left on device`, `no such image`, `pull access denied`, `error response from daemon: conflict`.

#### `tests/network/setup.go` — CREATED

```go
type setupOptions struct {
	cfg ServerConfig
}

type SetupOption func(*setupOptions)

func WithArgs(args ...string) SetupOption
func WithEnv(key, value string) SetupOption

type TestServer struct {
	Unit  *Unit
	t     *testing.T
	pool  *ContainerPool
	dirty bool
}

func SetupServer(t *testing.T, opts ...SetupOption) *TestServer
func (s *TestServer) URL() string
func (s *TestServer) WSOptions() monty.WebSocketOptions             // URL + RequestTimeout 30s
func (s *TestServer) NewPool(opts monty.WebSocketOptions) *monty.Pool // fills URL when empty; closed at cleanup
func (s *TestServer) Checkout(ctx context.Context, p *monty.Pool, opts monty.CheckoutOptions) *monty.Session
func (s *TestServer) Metrics(ctx context.Context) (Metrics, error)
func (s *TestServer) WaitMetric(name string, labels map[string]string, want float64, timeout time.Duration)
func (s *TestServer) Signal(sig string)                             // SIGTERM/SIGINT via Docker API; marks dirty
func (s *TestServer) WaitExited(timeout time.Duration) int          // container exit code
func (s *TestServer) Recreate(opts ...SetupOption)                  // same unit, fresh container; marks dirty
func (s *TestServer) Logs() string
func (s *TestServer) RawDial(ctx context.Context, headers http.Header) (*websocket.Conn, *http.Response, error)
func dockerKill(ctx context.Context, containerID, signal string) error // moby client ContainerKill; **verify** signature
```

#### `tests/network/wire.go` — CREATED

Raw protocol helpers built on `github.com/asalimonov/montygo/montypb`:

```go
func sendRequest(ctx context.Context, c *websocket.Conn, req *montypb.ParentRequest) error
func readEvent(ctx context.Context, c *websocket.Conn) (*montypb.ChildEvent, error)
func configureRequest(protocolVersion uint32) *montypb.ParentRequest
func feedRequest(code string) *montypb.ParentRequest
func requireClose(t *testing.T, err error, code websocket.StatusCode, reason string)
```

#### `tests/network/metrics.go` — CREATED

```go
// Metrics maps `name{k="v",...}` (labels sorted) to a value.
type Metrics map[string]float64

func scrapeMetrics(ctx context.Context, base string) (Metrics, error)
func (m Metrics) Get(name string, labels map[string]string) (float64, bool)
func metricKey(name string, labels map[string]string) string
```

#### `tests/network/logs.go` — CREATED

```go
type fileLogConsumer struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func newFileLogConsumer(unitID int) *fileLogConsumer
func (c *fileLogConsumer) Accept(l testcontainers.Log)
func (c *fileLogConsumer) Rename(testName string) string
func (c *fileLogConsumer) Close()
```

#### `tests/network/replcli.go` — CREATED

```go
var replOnce struct {
	sync.Once
	path string
	err  error
}

func replBinary(t *testing.T) string   // go build -C ../../examples -o <tmp>/repl ./repl

type ReplProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *syncBuffer
	stderr *syncBuffer
}

func StartRepl(t *testing.T, url string, args ...string) *ReplProc
func (r *ReplProc) Send(line string)
func (r *ReplProc) WaitStdout(substr string, timeout time.Duration)
func (r *ReplProc) WaitStderr(substr string, timeout time.Duration)
func (r *ReplProc) Interrupt()
func (r *ReplProc) CloseStdin()
func (r *ReplProc) Wait(timeout time.Duration) int

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}
```

#### `tests/network/pyclient.go` — CREATED

```go
type PyRun struct {
	ExitCode int
	Output   string
}

func pyClientImage(t *testing.T) string                              // skips when the image is absent
func runPyClient(t *testing.T, python, script string, env map[string]string) PyRun // one-shot container, wait.ForExit
func startPyClient(t *testing.T, python, script string, env map[string]string, readyLine string) testcontainers.Container
```

`python` is `/opt/pin/bin/python` or `/opt/pypi-0.0.23/bin/python`.

#### `tests/network/otlp.go` — CREATED

```go
// otlpReceiver is an in-test OTLP/HTTP endpoint reachable from containers via host-gateway.
type otlpReceiver struct {
	srv   *http.Server
	port  int
	mu    sync.Mutex
	spans []*tracepb.ResourceSpans
}

func startOTLPReceiver(t *testing.T) *otlpReceiver
func (r *otlpReceiver) ContainerEndpoint() string    // http://host.docker.internal:<port>
func (r *otlpReceiver) WaitSpan(name string, timeout time.Duration) *tracepb.Span
```

Server containers for telemetry tests add `HostConfigModifier` with `ExtraHosts: host.docker.internal:host-gateway`.

#### `tests/network/pyclient/*.py` — CREATED

| Script | Behaviour | Output contract |
|---|---|---|
| `feed_run.py` | `AsyncMontyWebsocket(MONTY_URL, request_timeout=30)`, checkout, `1 + 1`, state across feeds | prints `OK` |
| `host_function_dump_restore.py` | async `double`, `await double(n) + 1`; `x = 41`; `dump()`; new checkout `load_session`; `x + 1` | prints `OK 41 42` |
| `drain_capture.py` | checkout, `x = 1`, prints `READY`, loops `feed_run('x')` every 100 ms until `MontyShutdown` | prints `DUMP=<base64>` |
| `restore.py` | `load_session(base64(MONTY_DUMP))`, `x` | prints `OK x=1` |
| `protocol_rejected.py` | run under `/opt/pypi-0.0.23`; checkout; catches the crash error | prints `REJECTED <message>` |

#### Test files — CREATED

| File | Tests |
|---|---|
| `server_admission_test.go` | `TestAdmission_CapacityReturns503`, `TestAdmission_ClientQuotaReturns429`, `TestAdmission_TrustForwardedForKeysQuota`, `TestAdmission_InfoHealthMetricsPages`, `TestAdmission_ListenerRefusesDuringDrain` |
| `server_protocol_test.go` | `TestProtocol_FeedRunAndIsolation`, `TestProtocol_VersionSkewIsFatal`, `TestProtocol_FirstRequestMustBeConfigure`, `TestProtocol_LifecycleRequestsClose1008`, `TestProtocol_TextMessageCloses1008`, `TestProtocol_LargeFrameAccepted`, `TestProtocol_MemoryKillIsMemoryError`, `TestProtocol_RemoteDialByContainerIP` |
| `server_limits_test.go` | `TestLimits_MemoryClampedToCeiling`, `TestLimits_DurationClampedToCeiling`, `TestLimits_RecursionClampedToCeiling`, `TestLimits_LowerClientLimitWins`, `TestLimits_DisabledCeilingPassesClientValue` |
| `server_timeouts_test.go` | `TestTimeouts_IdleClosesSession`, `TestTimeouts_TurnTimeoutIncludesHostCallback`, `TestTimeouts_SessionTimeout` (slow), `TestTimeouts_KeepaliveDropsFrozenClient` (slow) |
| `server_dumps_test.go` | `TestDumps_SignedDumpRestoresAfterRecreate`, `TestDumps_TamperedDumpIsValueError`, `TestDumps_LocalWasmDumpRejected`, `TestDumps_PreviousKeyVerifies`, `TestDumps_UnknownKeyRejected`, `TestDumps_MetricsCountSignVerifyReject` |
| `server_drain_test.go` | `TestDrain_IdleSessionGetsShutdownDump`, `TestDrain_BeforeConfigureCarriesNoDump`, `TestDrain_InFlightTurnFinishesFirst`, `TestDrain_SilentSessionDroppedAfterGrace`, `TestDrain_SecondSignalDropsImmediately`, `TestDrain_ExitsZero` |
| `server_tls_test.go` | `TestTLS_WSSThroughReverseProxy`, `TestTLS_HealthCheckOverTLS`, `TestTLS_DialContextIsUsed` |
| `server_parallel_test.go` | `TestParallel_ManySessionsOneUnit`, `TestParallel_ActiveSessionsReturnToZero` |
| `server_telemetry_test.go` | `TestTelemetry_TraceparentParentsConnectionSpan`, `TestTelemetry_PolicyEventOnConnectionSpan` |
| `repl_session_test.go` | `TestRepl_StatePersistsAcrossFeeds`, `TestRepl_ErrorKeepsSession`, `TestRepl_DumpRestoreAcrossRecreate`, `TestRepl_DrainRestoresSuspendedFeed`, `TestRepl_TypeCheckStubsAcrossFeeds` |
| `repl_cli_test.go` | `TestReplCLI_StateAndPrintOverWebSocket`, `TestReplCLI_ContinuationOverWebSocket`, `TestReplCLI_ErrorsKeepSession`, `TestReplCLI_Mounts`, `TestReplCLI_InterruptReplacesSession`, `TestReplCLI_ServerUnavailable` |
| `pyclient_test.go` | `TestPyClient_FeedRunOverWebSocket`, `TestPyClient_HostFunctionAndDumpRestore`, `TestPyClient_DrainShutdownDumpRestore`, `TestPyClient_PyPIProtocol2IsRejected` |

Parallelism rule: every test calls `t.Parallel()`. Units are exclusive, so tests that signal or recreate their container are safe in parallel.

#### `tests/network/README.md` — CREATED

Architecture diagram of the pool, running instructions, environment table (§5.5), naming groups, how to write a test, time budget, traps (stale `:latest` tag, leaked containers → `make test-network-clean`).

### 6.6 `examples/`

#### `examples/repl/main.go` — UPDATED

```go
type options struct {
	scriptName string
	initial    string
	mounts     []*monty.MountDir
	cwd        string
	limits     monty.ResourceLimits
	ws         wsOptions   // CREATED field
}

type wsOptions struct {
	url                string
	caFile             string
	insecureSkipVerify bool
}

func parseOptions(args []string, errOut io.Writer) (*options, error) // new flags -ws, -ws-ca, -ws-insecure-skip-verify
func openPool(ctx context.Context, o *options) (*monty.Pool, error)  // CREATED: NewWebSocket when o.ws.url != "", else montyenv
func (w wsOptions) tlsConfig() (*tls.Config, error)                   // CREATED: nil unless caFile or skip-verify
func run(ctx context.Context, c console, args []string) error        // uses openPool
```

Argument errors: `-ws-ca` or `-ws-insecure-skip-verify` without `-ws` → `"-ws-ca and -ws-insecure-skip-verify require -ws"`.

#### `examples/repl/main_test.go` — UPDATED

`TestArgumentErrors` gains the two `-ws` cases. `TestWebSocketFlagParsing` checks the TLS config building.

#### `examples/README.md` — UPDATED

`repl` section: `-ws` usage with `make docker-build` and `docker run`.

### 6.7 CI — `.github/workflows/ci.yml` UPDATED

```yaml
  docker:
    runs-on: ubuntu-latest
    timeout-minutes: 90
    steps:
      - uses: actions/checkout@v7
      - uses: docker/setup-buildx-action@v3
      - uses: actions/setup-go@v7
        with:
          go-version: '1.25.x'
          cache-dependency-path: |
            go.sum
            tests/network/go.sum
            examples/go.sum
      - uses: dtolnay/rust-toolchain@stable
      - uses: Swatinem/rust-cache@v2
        with:
          workspaces: server
      - name: Server clippy and tests
        run: make server-check
      - name: Build image
        run: make docker-build PLATFORMS=linux/amd64 MONTY_DOCKER_SRC=git
        env:
          BUILDX_CACHE_FROM: type=gha
          BUILDX_CACHE_TO: type=gha,mode=max
      - name: Build Python client image
        run: make docker-build-pyclient MONTY_DOCKER_SRC=git
      - name: Root suite over websocket
        run: make test-docker
      - name: Vet and lint network tests
        working-directory: tests/network
        run: go vet ./... && go mod tidy -diff
      - name: Network tests
        run: make test-network

  docker-arm64:
    runs-on: ubuntu-24.04-arm
    timeout-minutes: 90
    steps:
      - uses: actions/checkout@v7
      - uses: docker/setup-buildx-action@v3
      - uses: actions/setup-go@v7
        with:
          go-version: '1.25.x'
          cache-dependency-path: tests/network/go.sum
      - name: Build image
        run: make docker-build PLATFORMS=linux/arm64 MONTY_DOCKER_SRC=git
      - name: Network tests
        run: make test-network
```

- `make server-check` in CI needs `MONTY_BIN` for `server/tests/session.rs`; without it that file skips. The `docker` job builds the native worker from the checkout of `pydantic/monty` at `MONTY_REV` before `server-check`, as the `test` job does.
- The Makefile passes cache flags when `BUILDX_CACHE_FROM`/`BUILDX_CACHE_TO` are set (`$(if $(BUILDX_CACHE_FROM),--cache-from $(BUILDX_CACHE_FROM),)`).

### 6.8 Repository files

| File | Change |
|---|---|
| `.gitignore` | UPDATED: `build/`, `server/target/`, `tests/network/output/` |
| `THIRD_PARTY_NOTICES.md` | UPDATED: the image redistributes the `monty` worker (MIT) and the server's Rust dependencies |
| `.golangci.yml` (if present) | UPDATED: include `tests/network` run |

### 6.9 Documentation

| File | Change |
|---|---|
| `docs/architecture/server.md` | CREATED: purpose, process model, flags (§5.2), admission, session state machine (§7.4–7.10), texts (`texts.rs`), close codes, envelope (§5.3), limits, drain, metrics (§5.4), telemetry, security notes |
| `docs/architecture/docker.md` | CREATED: image layout, build stages, cross-compilation, override staging, tags, `docker run` recommendations (`--read-only`, `--pids-limit`, `--memory`, `--cap-drop ALL`, `--security-opt no-new-privileges`), healthcheck, CI |
| `docs/architecture/overview.md` | UPDATED: artifact list (image), key technologies (axum, tokio, cargo-zigbuild, testcontainers-go), process model shows `monty-server`, websocket bullet mentions TLS/dialer, related documents table |
| `docs/architecture/websocket.md` | UPDATED: `TLSConfig`, `DialContext`, `CheckWebSocketHealth`, server behaviour a client observes (close codes → `DisconnectError`, `FatalError` → `CrashedError`) |
| `docs/architecture/testing.md` | UPDATED: `websocket` backend, `MONTY_TEST_WS_URL`, `make test-docker`, `tests/network` harness |
| `docs/parity/server.md` | CREATED: deviations from Full Monty (§10) |
| `docs/parity/api.md` | UPDATED: `TLSConfig`, `DialContext`, `CheckWebSocketHealth` rows (Python `AsyncMontyWebsocket` has no equivalents) |
| `docs/parity/tests.md` | UPDATED: root suite on websocket counts and adaptations; `tests/network` section; Python interop |
| `README.md` | UPDATED: "Dockerized server" section (build, run, connect, TLS via ingress), WebSocket options snippet |
| `CLAUDE.md` | UPDATED: layout rows (`server/`, `docker/`, `tests/network/`), prerequisites (Docker with buildx and containerd store), commands, environment variables, upstream-source rows (`server/` ↔ `docs/server.md` + `monty-pool` `turn_raw`; pyclient ↔ `crates/monty-python`), conventions (server texts), definition of done (F1), upgrade checklist (F8) |

---

## 7. Data flows

### 7.1 Build: `make docker-build`

1. Make reads `VERSION` and `UPSTREAM_REV` from `monty.go` and `MONTY_REV_FULL` from `ci.yml`.
2. `docker-stage-src`: when `MONTY_DOCKER_SRC=auto` and `$(MONTY_SRC)/crates/monty-proto` exists, copy tracked and untracked-unignored files into `build/monty-src/` (no `target/`).
3. Buildx resolves `monty-src`: the named context when given, else the `monty-git` fetch of `MONTY_REV`.
4. Per platform, the `build` stage runs natively on the build platform: worker with `--locked`, then the server with path patches.
5. The final stage assembles `scratch`; buildx loads both platform images into the containerd store under both tags.

| Failure | Result |
|---|---|
| `MONTY_REV` unreachable | `git fetch` fails; build error names the rev |
| Staged source not at the pin | build succeeds; the image's `MONTY_REV` label and `version.rs` still name the pin (documented: override builds are for development) |
| zig/cmake failure on `aws-lc-sys` | build error; pins in the Dockerfile are the fix |
| Containerd store disabled | buildx refuses multi-platform `--load`; message tells to enable it or set `PLATFORMS` to one platform |

### 7.2 Server startup

1. Parse CLI; `probe` short-circuits.
2. `validate`: dump keys, zero rules, binary resolution. Errors exit 2.
3. Build the tokio runtime.
4. Install telemetry when `--otlp-endpoint` is set.
5. Build `monty_pool::Pool` (`min_processes 0` means no spawn at startup).
6. Bind the listener. Print `ws://<bound>/` on stdout, flush.
7. Spawn `watch_signals`. Serve axum with graceful shutdown on `drain.soft`.
8. After the listener stops: wait for the session `TaskTracker` until `drain.hard`; close the pool; shut down telemetry; exit 0.

### 7.3 Upgrade admission

```
GET / (Upgrade: websocket)
  └─ root handler
       ├─ upgrade extractor failed ─▶ 200 info page (plain GET) | 400 (bad upgrade headers)
       ├─ drain.soft cancelled ─▶ 503 HTTP_DRAINING, rejections{draining}
       ├─ client_id(peer, headers, trust_forwarded_for)
       ├─ admission.try_admit
       │    ├─ Capacity ─▶ 503 HTTP_CAPACITY, rejections{capacity}
       │    └─ ClientQuota ─▶ 429 HTTP_CLIENT_QUOTA, rejections{client_quota}
       ├─ trace_parent = headers["traceparent"]
       └─ upgrade.max_message_size(MAX_FRAME_LEN).max_frame_size(MAX_FRAME_LEN)
              .on_upgrade(Session::new(deps, permit, client, trace_parent).run)
```

`/health` answers 200 while not draining, 503 while draining. `/metrics` always answers 200.

### 7.4 Session: `Configure`

1. `sessions_active += 1`; connection span starts; stderr `session_start`.
2. Idle deadline = now + idle timeout.
3. First binary message decodes into `pb::ParentRequest`.
4. Not `Configure` → close 1008 `CLOSE_EXPECTED_CONFIGURE`.
5. `check_protocol_version(configure.protocol_version)` fails → send `FatalError(<text>)`, close 1000, outcome `error`.
6. Draining → send `ShutdownDump{}`, close 1001, outcome `drained`.
7. `limits = ceilings.clamp(configure.limits)`; `repl = repl_config(&configure, limits)`.
8. `pool.checkout_with(&repl, options with telemetry context)` bounded by the turn timeout:
   - `Ok(checkout)` → `workers_spawned_total += 1`; send `events::ok(&limits)`; state `Ready`; idle deadline reset.
   - `Err(e)` → `fail(e)` (§7.9).

### 7.5 Session: execution turn with suspensions

```
client                     server session                           monty-pool / child
  Feed ─────────────────▶  state Ready: turn_deadline = now+turn
                           turn_raw(Feed, on_event) ──────────────▶ Feed
                                                     ◀────────────── Print*
  ◀─────────── Print*  ◀── on_event: encode + out.frame
                                                     ◀────────────── FunctionCall
  ◀──────── FunctionCall   forward: suspension → state Suspended
                           idle deadline = now+idle (host callback time)
  ResumeCall ───────────▶  state Suspended: keep turn_deadline
                           turn_raw(ResumeCall) ──────────────────▶ ResumeCall
                                                     ◀────────────── Complete
  ◀────────────── Complete forward: turn-ender → state Ready, turn_deadline = None, idle reset
```

While `turn_raw` runs, the session loop also watches: session deadline, turn deadline, keepalive tick, inbound `Closed`, `drain.hard`. `drain.soft` does not interrupt a turn.

### 7.6 Dump and Load

**Dump** (client `Dump` request):

1. `turn_raw(Dump)` → `DumpResult{state}`.
2. `keys.sign(state)` → `Some(env)`: replace `state`, `dumps{signed} += 1`, forward.
3. `None` (N21): send `FatalError(frame_too_large(..))`, close 1011, outcome `error`.

**Load** (client `Load{state}` request, state `Ready`):

1. `keys.verify(&state)`:
   - `Err(_)` → `dumps{rejected} += 1`; send `Error{ValueError, INVALID_DUMP}`; state unchanged; idle reset. The worker never sees the bytes.
   - `Ok(raw)` → `dumps{verified} += 1`.
2. `turn_raw(Load{raw})`:
   - reply `Ok` or a re-announced suspension → `ceilings.admits_restored(&event)`:
     - false → close 1008 `CLOSE_RESTORED_OVER_LIMITS`, outcome `error`, checkout dropped.
     - true → forward; suspension → state `Suspended` with a fresh turn deadline; `Ok` → `Ready`.
   - reply `Error` (child refused, e.g. `Load` after a feed) → forward; state unchanged.
   - `Err(e)` → `fail(e)`.

### 7.7 Timeouts

| Deadline | Armed | Cleared | On expiry |
|---|---|---|---|
| idle | after every event the server sends that awaits a client request (`Ok` after Configure, suspension, turn-ender), and at upgrade | when a request arrives | `timeouts{idle}`, close 1008 `close_idle`, outcome `timeout` |
| turn | when a non-resume request arrives in `Ready` | when a non-suspension turn-ender is forwarded | `timeouts{turn}`, drop the turn future and checkout, close 1008 `close_turn`, outcome `timeout` |
| session | at upgrade | never | `timeouts{session}`, close 1008 `close_session`, outcome `timeout` |
| keepalive | every `keepalive` interval, a ping is sent when none is outstanding | a pong | outstanding ping older than one interval → `timeouts{keepalive}`, drop without close frame, outcome `timeout` |
| drain grace | first signal | — | `drain.hard` cancels; sessions close 1001; outcome `drain_dropped` |

The pool's own `request_timeout` equals `--turn-timeout` and backstops each protocol turn inside the chain.

### 7.8 Client close and abnormal end

- Inbound `Closed` in `Ready`, `Suspended` or `AwaitingConfigure`: drop the turn future if any, call `checkout.finish()` when in `Ready` (it resets and then kills the worker, because `max_checkouts_per_worker = 1`), otherwise drop the checkout (kill on drop). Outcome `closed`.
- Inbound `Text`: close 1008 `CLOSE_TEXT_MESSAGE`, outcome `error`.
- Outbound write failure: drop everything, outcome `error`.

### 7.9 Failure forwarding (F7)

| Server-side outcome | Sent to client | Close | Outcome | montygo sees |
|---|---|---|---|---|
| `turn_raw` → `Ok(Print…)` | each `Print` | — | — | print target |
| `turn_raw` → `Ok(turn-ender)` except below | event | — | — | as native |
| `turn_raw` → `Ok(DumpResult)` | signed `DumpResult` | — | — | `[]byte` envelope |
| `turn_raw` → `Ok(FatalError)` | event | 1000 | `error` | `*CrashedError` (announced) |
| `PoolError::Runtime(exc)` with `exc` = `MemoryError` + `MEMORY_KILLED` | `Error{MemoryError, MEMORY_KILLED}` | 1011 | `error` | `*RuntimeError` MemoryError, then `*DisconnectError` |
| `PoolError::Runtime(other)` (not expected from `turn_raw`) | `Error{exc}` | — | — | `*RuntimeError` |
| `PoolError::Crashed` / `Timeout` / `Protocol` (worker) / `Spawn` / `Exhausted` / `Finished` | `FatalError{PoolError Display}` | 1011 | `error` | `*CrashedError` (announced) |
| `PoolError::Shutdown` (impossible for subprocess) | `FatalError{Display}` | 1011 | `error` | `*CrashedError` |
| protocol version refused | `FatalError{check_protocol_version text}` | 1000 | `error` | `*CrashedError` |
| undecodable frame | — | 1008 `close_malformed` | `error` | `*DisconnectError` |
| request with no kind | — | 1008 `CLOSE_EMPTY_REQUEST` | `error` | `*DisconnectError` |
| `Configure` in `Ready`/`Suspended` | — | 1008 `CLOSE_ALREADY_CONFIGURED` | `error` | `*DisconnectError` |
| `Reset` / `Shutdown` | — | 1008 `CLOSE_LIFECYCLE` | `error` | `*DisconnectError` |
| invalid dump envelope on `Load` | `Error{ValueError, INVALID_DUMP}` | — | — | `*RuntimeError` ValueError, session kept |
| restored budget over ceilings (N6) | — | 1008 `CLOSE_RESTORED_OVER_LIMITS` | `error` | `*DisconnectError` |
| idle / session / turn timeout | — | 1008 reason | `timeout` | `*DisconnectError` |
| keepalive | — | none (TCP drop) | `timeout` | `*DisconnectError` |
| drain, next request | `ShutdownDump{signed?}` | 1001 | `drained` | `*ShutdownError{Dump}` |
| drain grace expired | — | 1001 | `drain_dropped` | `*DisconnectError` |
| capacity / quota / draining at upgrade | HTTP 503 / 429 / 503 | — | — | `Pool.Checkout` dial error carrying the status |

The last row: montygo's dialer returns the HTTP status as a dial error, surfaced from `Pool.Checkout` as a spawn failure. `tests/network` asserts the status code text in that error.

### 7.10 Drain

```
SIGTERM #1 ─▶ drain.soft.cancel(); draining gauge = 1; grace_deadline = now + drain_grace
             axum stops accepting; /health answers 503 on already-open HTTP connections
per session:
  turn in flight ─▶ finish turn, forward result (bounded by turn deadline and drain.hard)
  next request arrives ─▶ shutdown_dump():
       state AwaitingConfigure        ─▶ ShutdownDump{}
       state Ready | Suspended        ─▶ turn_raw(Dump) (timeout min(turn, grace left))
                                          ├─ Ok(DumpResult{s}) ─▶ sign(s) ─▶ Some(env) ─▶ ShutdownDump{env}
                                          │                                └ None     ─▶ ShutdownDump{} + log (N21)
                                          └─ Err(_)            ─▶ ShutdownDump{}
       close 1001, outcome drained
  silent until grace_deadline ─▶ close 1001, outcome drain_dropped
SIGTERM #2 or grace expiry ─▶ drain.hard.cancel(): every session closes 1001 now
all sessions ended ─▶ pool.close() ─▶ telemetry.shutdown() ─▶ exit 0
```

`Load` or `Dump` in flight at SIGTERM follow "turn in flight". A `Dump` request arriving while draining is answered with `ShutdownDump` (it did not run).

### 7.11 Go client: dial with TLS and custom dialer

1. `Pool.Checkout` runs `ConnectHeaders`, waits for capacity, calls `WebSocketDialer.Spawn`.
2. `transport(raw)`: clone `http.DefaultTransport`; `TLSClientConfig = TLSConfig.Clone()` when set; `DialContext` wraps `d.DialContext` (or `net.Dialer`) and stores the TCP connection in `raw`.
3. `websocket.Dial` with that client, headers and `CompressionDisabled`; the TLS handshake runs over the stored TCP connection.
4. The worker keeps `raw`; `Kill` closes it, which unblocks TLS reads.

`CheckWebSocketHealth`: derive URL → headers from `ConnectHeaders` → transport as above (no capture needed) → GET with `ctx` bounded by `RequestTimeout` (0 → 10 s, `NoRequestTimeout` → ctx only) → 200 or error.

### 7.12 Root suite via `make test-docker`

1. `docker run` the image with the test dump key and quota disabled on a random loopback port.
2. Poll `/health`.
3. `go test .` with `MONTY_TEST_BACKENDS=websocket` and `MONTY_TEST_WS_URL`.
4. `eachBackend` runs subtests named `websocket`; `sharedPool`/`newPool` call `openPool` → `NewWebSocket`.
5. Native-only subtests skip with `websocket backend: <reason>`.
6. The trap stops the container.

### 7.13 `tests/network` pool lifecycle

**Start**

1. `TestMain` checks `MONTYGO_NETWORK_TESTS`, then `docker image inspect` of the image (missing → print "run make docker-build" and exit 1).
2. `GetPool` sizes the pool from `MONTYGO_TEST_PARALLEL`.
3. `Start` creates units, starts one lifecycle goroutine each, sends `cmdStart`.
4. Returns when the first unit is ready, or with the first error when all are dead or an infra failure happened.

**Acquire(ctx, cfg)**

1. Receive from `available` or `ctx.Done()`.
2. `ready → inUse`.
3. `cfg` not default or differs from `u.cfg` → `Recreate(ctx, u, cfg)` on the test goroutine.
4. Rename the log file to the test name.

**Release(t, u, dirty)**

1. `inUse → releasing`.
2. Send `cmdRelease{t, recreate: dirty || !u.cfg.isDefault()}`.

**Lifecycle cmdRelease**

1. `recreate` false → `healthyAndIdle` (health 200, `sessions_active == 0` within 5 s); failure sets `recreate`.
2. `recreate` → `stopContainer` + `startContainer(default)` with up to 3 attempts, 0.5 s / 1.5 s backoff.
3. Success → `releasing → ready`, push to `available`. Failure → `dead`, log, best-effort `t.Errorf`, the slot stays dead (no spare by design).

**Stop**: set `stopping`, send `cmdShutdown` to each unit, wait for `done`.

| Failure | Handling |
|---|---|
| image missing | `TestMain` exits 1 with instruction |
| container fails health wait | retry ×3, then unit dead |
| all units dead at start | `Start` returns first error; exit 1 |
| infra pattern | abort start immediately |
| every unit dead after start | `Acquire` blocks until the test timeout; `Release` logs `ERROR: pool has no live units` when the last unit dies |
| leaked containers from a killed run | `make test-network-clean` (label filter) |

### 7.14 REPL CLI over WebSocket

1. `SetupServer(t)`.
2. `StartRepl(t, s.URL())` runs the built binary with `-ws URL`.
3. `run` → `openPool` → `NewWebSocket` → checkout → banner on stderr.
4. Test sends lines; asserts stdout values, continuation prompts, error text on stderr.
5. `Interrupt` sends `os.Interrupt`; the REPL replaces its session (a new WebSocket connection; metrics show sessions_total increase).
6. `CloseStdin` ends the REPL; exit code 0.

### 7.15 Python interop

1. `pyClientImage` skips when the image is absent.
2. Server `ContainerIP` → `MONTY_URL=ws://<ip>:8000/`.
3. `runPyClient` starts the one-shot container with `wait.ForExit()`, reads logs, asserts the output contract.
4. Drain scenario: `startPyClient(..., "READY")` → `s.Signal("TERM")` → container exits → parse `DUMP=` → `s.Recreate()` → `runPyClient(restore.py, MONTY_DUMP)` on the new IP → `OK x=1`.
5. Protocol rejection: `/opt/pypi-0.0.23/bin/python protocol_rejected.py` → output contains `unsupported protocol version 2 (server supports protocol version 3, try updating to a newer client version)`.

### 7.16 CI

`docker` job: server check → amd64 image → pyclient image → `test-docker` → vet/tidy `tests/network` → `test-network`. `docker-arm64` job: arm64 image → `test-network` (Python tests skip because no pyclient image is built there).

### 7.17 Failure-mode summary

| Component | Failure | Detection | Effect |
|---|---|---|---|
| server | worker binary missing | startup validation | exit 2 |
| server | spawn fails at checkout | `PoolError::Spawn` | `FatalError`, close 1011 |
| server | worker OOM kill | `PoolError::Runtime(MemoryError)` | `Error` + close 1011 |
| server | worker hangs | pool `request_timeout` or turn deadline | `FatalError` or close 1008 |
| server | client vanishes | keepalive | drop, worker killed |
| server | forged dump | envelope verify | `ValueError`, session kept |
| server | ceilings lowered after dump | `admits_restored` (duration, suspensions) | close 1008; memory gap documented |
| server | OTLP collector down | exporter errors | logged once per minute; sessions unaffected |
| client | TLS misconfiguration | dial error | checkout fails with the TLS error text |
| harness | Docker daemon down | infra pattern | run aborted |
| image build | upstream rev mismatch with `version.rs` | `monty_rev_matches_lockfile` test | CI fails |

---

## 8. Pseudo-code for key methods

Go-flavoured pseudo-code. Server methods are implemented in Rust; names match §6.1.

### 8.1 `ServeArgs.validate`

```go
func (a ServeArgs) validate() (Config, error) {
	if a.DumpKey == nil {
		return Config{}, cfgErr("--dump-key is required (MONTY_SERVER_DUMP_KEY), at least 16 bytes")
	}
	if len(*a.DumpKey) < 16 {
		return Config{}, cfgErr("--dump-key must be at least 16 bytes")
	}
	if a.DumpKeyPrevious != nil && (len(*a.DumpKeyPrevious) < 16 || *a.DumpKeyPrevious == *a.DumpKey) {
		return Config{}, cfgErr("--dump-key-previous must be at least 16 bytes and differ from --dump-key")
	}
	if a.MaxSessions == 0 { return Config{}, cfgErr("--max-sessions must be at least 1") }
	if a.MaxRecursionDepth == 0 { return Config{}, cfgErr("--max-recursion-depth cannot be disabled") }
	bin, err := resolveBinary(a.MontyBin)             // absolute path, or PATH lookup; must be executable
	if err != nil { return Config{}, err }
	keys, _ := NewDumpKeys([]byte(*a.DumpKey), bytesOrNil(a.DumpKeyPrevious))
	var perClient *int
	if a.MaxSessionsPerClient > 0 { perClient = &a.MaxSessionsPerClient }
	var otlp *OtlpConfig
	if a.OtlpEndpoint != "" {
		if !hasScheme(a.OtlpEndpoint, "http", "https") { return Config{}, cfgErr("--otlp-endpoint must be an http(s) URL") }
		otlp = &OtlpConfig{Endpoint: strings.TrimSuffix(a.OtlpEndpoint, "/"), Protocol: a.OtlpProtocol}
	}
	return Config{
		Bind: net.JoinHostPort(a.Host, strconv.Itoa(int(a.Port))),
		MontyBin: bin, MaxSessions: a.MaxSessions, MaxSessionsPerClient: perClient,
		IdleTimeout: secs(a.IdleTimeout), Keepalive: secs(a.Keepalive),
		SessionTimeout: secs(a.SessionTimeout), TurnTimeout: secs(a.TurnTimeout),
		DrainGrace: time.Duration(a.DrainGrace) * time.Second,
		Ceilings: CeilingsFromArgs(a.MaxDuration, a.MaxMemoryMiB, a.MaxRecursionDepth),
		TrustForwardedFor: a.TrustForwardedFor, DumpKeys: keys, Otlp: otlp,
	}, nil
}
```

### 8.2 `app.start`

```go
func start(cfg Config) (*Server, error) {
	metrics := NewMetrics()
	var tel *Telemetry
	if cfg.Otlp != nil {
		t, err := InstallTelemetry(*cfg.Otlp)
		if err != nil { return nil, err }
		tel = t
	}
	poolCfg := monty_pool.PoolConfig_subprocess(cfg.MontyBin)
	poolCfg.MinProcesses = 0
	poolCfg.MaxProcesses = cfg.MaxSessions
	poolCfg.CheckoutTimeout = some(CHECKOUT_TIMEOUT)
	poolCfg.RequestTimeout = cfg.TurnTimeout             // nil when disabled
	poolCfg.MaxCheckoutsPerWorker = some(1)
	if tel != nil { poolCfg.Metrics = some(tel.PoolMetrics()) }
	pool, err := monty_pool.NewPool(poolCfg)             // spawns nothing
	if err != nil { return nil, ServeErrorPool(err) }

	listener, err := net.Listen("tcp", cfg.Bind)
	if err != nil { return nil, ServeErrorBind(err) }
	bound := listener.Addr().String()
	fmt.Fprintf(os.Stdout, "ws://%s/\n", bound)
	os.Stdout.Sync()
	logline(Info, "listening", "addr", bound, "monty_rev", MONTY_REV)

	drain := NewDrainSignals()
	go watchSignals(drain, cfg.DrainGrace, metrics)
	tracker := NewTaskTracker()
	shared := &SharedDeps{Pool: pool, Config: cfg, Metrics: metrics, Drain: drain, Telemetry: tel,
		Admission: NewAdmission(cfg.MaxSessions, cfg.MaxSessionsPerClient, metrics)}
	router := Router(AppState{Deps: shared, Bound: bound, Tracker: tracker})

	handle := spawn(func() error {
		err := axumServe(listener, router.WithConnectInfo()).WithGracefulShutdown(drain.Soft.Cancelled()).Wait()
		// listener closed: wait for sessions, but never past drain.hard
		tracker.Close()
		select {
		case <-tracker.Wait():
		case <-drain.Hard.Cancelled():
			<-tracker.WaitWithin(CLOSE_WRITE_BUDGET + time.Second)
		}
		pool.Close()
		if tel != nil { tel.Shutdown() }
		logline(Info, "drain_finished")
		return err
	})
	return &Server{Bound: bound, Handle: handle, Drain: drain}, nil
}
```

`axum::serve(...).with_graceful_shutdown` waits for open HTTP connections; upgraded sockets are tracked by `tracker`, not by axum. **verify** that axum's graceful shutdown does not wait on upgraded connections; if it does, the sessions' own `drain.hard` handling bounds it anyway.

### 8.3 `watch_signals`

```go
func watchSignals(d DrainSignals, grace time.Duration, m *Metrics) {
	sigs := notify(SIGTERM, SIGINT)
	<-sigs
	d.GraceDeadline.Set(time.Now().Add(grace))
	m.Draining.Set(1)
	logline(Info, "drain_started", "grace_s", grace.Seconds())
	d.Soft.Cancel()
	select {
	case <-sigs:
		logline(Warn, "drain_forced")
	case <-time.After(grace):
	}
	d.Hard.Cancel()
}
```

### 8.4 HTTP `root` handler

```go
func root(state AppState, peer SocketAddr, headers HeaderMap, up Result[WebSocketUpgrade]) Response {
	if up.IsErr() {
		if isPlainGet(headers) { return text(200, InfoPage(state.Bound)) }
		return up.Rejection().IntoResponse()             // 400/426 from axum
	}
	deps := state.Deps
	draining := deps.Drain.Soft.IsCancelled()
	client := ClientID(peer, headers, deps.Config.TrustForwardedFor)
	permit, rej := deps.Admission.TryAdmit(client, draining)
	switch rej {
	case Draining:
		deps.Metrics.Rejections(RejectDraining).Inc()
		logline(Info, "rejected", "reason", "draining", "client", client)
		return text(503, HTTP_DRAINING)
	case Capacity:
		deps.Metrics.Rejections(RejectCapacity).Inc()
		logline(Info, "rejected", "reason", "capacity", "client", client)
		return text(503, HTTP_CAPACITY)
	case ClientQuota:
		deps.Metrics.Rejections(RejectClientQuota).Inc()
		logline(Info, "rejected", "reason", "client_quota", "client", client)
		return text(429, HTTP_CLIENT_QUOTA)
	}
	traceParent := headers.Get("traceparent")
	userAgent := headers.Get("user-agent")
	return up.Unwrap().
		MaxMessageSize(MAX_FRAME_LEN).
		MaxFrameSize(MAX_FRAME_LEN).
		OnUpgrade(func(socket WebSocket) {
			state.Tracker.Spawn(func() {
				NewSession(deps.Session(), permit, client, traceParent, userAgent).Run(socket)
			})
		})
}
```

### 8.5 `Admission.try_admit`

```go
func (a *Admission) TryAdmit(client string, draining bool) (*SessionPermit, Rejection) {
	if draining { return nil, Draining }
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.active >= a.maxSessions { return nil, Capacity }
	if a.perClient != nil && a.state.byClient[client] >= *a.perClient { return nil, ClientQuota }
	a.state.active++
	a.state.byClient[client]++
	a.metrics.SessionsActive.Inc()
	return &SessionPermit{admission: a, client: client}, None
}

func (p *SessionPermit) Drop() {
	a := p.admission
	a.mu.Lock()
	a.state.active--
	if n := a.state.byClient[p.client] - 1; n == 0 {
		delete(a.state.byClient, p.client)
	} else {
		a.state.byClient[p.client] = n
	}
	a.mu.Unlock()
	a.metrics.SessionsActive.Dec()
}
```

### 8.6 `client_id`

```go
func ClientID(peer SocketAddr, headers HeaderMap, trustXFF bool) string {
	if trustXFF {
		if v := headers.GetAll("x-forwarded-for").Last(); v != "" {
			parts := strings.Split(v, ",")
			if ip, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return ip.Unmap().String()
			}
		}
	}
	return peer.IP().Unmap().String()
}
```

### 8.7 `Session.run`

```go
func (s *Session) Run(socket WebSocket) {
	sink, stream := socket.Split()
	outRx := make(chan Outbound, 16)
	s.out = OutboundTx(outRx)
	writerDone := spawn(func() { writer(sink, outRx) })
	inbound := make(chan Inbound, 1)
	spawn(func() { reader(stream, inbound, s.lastPong, s.started) })

	span := s.deps.Telemetry.ConnectionSpan(s.traceParent, s.client, s.userAgent)   // nil-safe
	logline(Info, "session_start", "client", s.client)
	s.idleDeadline = deadlineFrom(s.deps.Config.IdleTimeout)

	end := s.serve(inbound)

	s.end(end, span)
	s.releaseCheckout(end)                  // finish() on clean close in Ready, else drop
	close(outRx)
	waitWithin(writerDone, CLOSE_WRITE_BUDGET)
	s.permit.Drop()
}
```

### 8.8 `Session.serve` (idle loop)

```go
func (s *Session) serve(inbound <-chan Inbound) SessionEnd {
	for {
		wakeAt, wake := s.nextDeadline()                  // earliest of idle, session, keepalive, drain grace
		select {
		case msg := <-inbound:
			switch msg.Kind {
			case Closed:
				return ClientClosed
			case Text:
				s.out.Close(1008, CLOSE_TEXT_MESSAGE)
				return Violation(CLOSE_TEXT_MESSAGE)
			case Request:
				if err := s.onRequest(msg.Bytes); err != nil { return err }
			}
		case <-timerAt(wakeAt):
			switch wake {
			case WakeIdle:
				s.deps.Metrics.Timeouts(TimeoutIdle).Inc()
				s.out.Close(1008, closeIdle(*s.deps.Config.IdleTimeout))
				return Timeout(TimeoutIdle)
			case WakeSession:
				s.deps.Metrics.Timeouts(TimeoutSession).Inc()
				s.out.Close(1008, closeSession(*s.deps.Config.SessionTimeout))
				return Timeout(TimeoutSession)
			case WakeKeepalive:
				if end := s.keepaliveTick(); end != nil { return end }
			}
		case <-s.deps.Drain.Soft.Cancelled():
			s.draining = true                             // next request answers ShutdownDump
		case <-s.deps.Drain.Hard.Cancelled():
			s.out.Close(1001, CLOSE_SHUTTING_DOWN)
			return DrainDropped
		}
	}
}

func (s *Session) keepaliveTick() SessionEnd {
	k := &s.keepalive
	if k.pingSentAt != nil {
		if s.lastPongAfter(*k.pingSentAt) {
			k.pingSentAt = nil
		} else if time.Since(*k.pingSentAt) >= *k.interval {
			s.deps.Metrics.Timeouts(TimeoutKeepalive).Inc()
			return Timeout(TimeoutKeepalive)             // no close frame: the peer is gone
		}
	}
	if k.pingSentAt == nil {
		if err := s.out.Ping(); err != nil { return WriteFailed }
		now := time.Now()
		k.pingSentAt = &now
	}
	return nil
}
```

`next_deadline` ignores a disabled deadline, includes the keepalive tick only when enabled, and includes the drain grace deadline only while `draining`.

### 8.9 `Session.on_request`

```go
func (s *Session) onRequest(bytes []byte) SessionEnd {
	s.idleDeadline = nil
	req, err := decodeRequest(bytes)
	if err != nil {
		reason := closeMalformed(err)
		s.out.Close(1008, reason)
		return Violation(reason)
	}
	if req.Kind == nil {
		s.out.Close(1008, CLOSE_EMPTY_REQUEST)
		return Violation(CLOSE_EMPTY_REQUEST)
	}
	if s.draining {
		return s.shutdownDump()
	}
	switch k := req.Kind.(type) {
	case Configure:
		if s.state != AwaitingConfigure {
			s.out.Close(1008, CLOSE_ALREADY_CONFIGURED)
			return Violation(CLOSE_ALREADY_CONFIGURED)
		}
		return s.configure(k)
	case Reset, Shutdown:
		s.out.Close(1008, CLOSE_LIFECYCLE)
		return Violation(CLOSE_LIFECYCLE)
	}
	if s.state == AwaitingConfigure {
		s.out.Close(1008, CLOSE_EXPECTED_CONFIGURE)
		return Violation(CLOSE_EXPECTED_CONFIGURE)
	}
	if load, ok := req.Kind.(Load); ok {
		return s.load(load.State, req.TraceParent)
	}
	return s.turn(req)
}
```

### 8.10 `Session.configure`

```go
func (s *Session) configure(c pb.Configure) SessionEnd {
	if msg, bad := checkProtocolVersion(c.ProtocolVersion); bad {
		s.out.Frame(events.Fatal(msg))
		s.out.Close(1000, "")
		return WorkerFailed
	}
	limits := s.deps.Config.Ceilings.Clamp(c.Limits)
	repl := replConfig(c, limits)
	opts := monty_pool.CheckoutOptions{}
	if s.span != nil { opts = opts.WithTelemetry(some(s.span.CheckoutContext())) }

	checkout, err := withTimeout(s.deps.Config.TurnTimeout, s.hardOrClosed(),
		func() (Checkout, error) { return s.deps.Pool.CheckoutWith(repl, opts) })
	switch {
	case errors.Is(err, errTimedOut):
		s.deps.Metrics.Timeouts(TimeoutTurn).Inc()
		s.out.Close(1008, closeTurn(*s.deps.Config.TurnTimeout))
		return Timeout(TimeoutTurn)
	case errors.Is(err, errHard):
		s.out.Close(1001, CLOSE_SHUTTING_DOWN)
		return DrainDropped
	case errors.Is(err, errClientClosed):
		return ClientClosed
	case err != nil:
		return s.fail(err)
	}
	s.deps.Metrics.WorkersSpawned.Inc()
	s.state = Ready{checkout}
	if err := s.out.Frame(events.Ok(limits)); err != nil { return WriteFailed }
	s.idleDeadline = deadlineFrom(s.deps.Config.IdleTimeout)
	return nil
}
```

### 8.11 `Ceilings.clamp` and `admits_restored`

```go
func (c Ceilings) Clamp(client *pb.ResourceLimits) pb.ResourceLimits {
	in := pb.ResourceLimits{}
	if client != nil { in = *client }
	return pb.ResourceLimits{
		MaxDurationMicros: clampOpt(in.MaxDurationMicros, c.MaxDurationMicros),
		MaxMemoryBytes:    clampOpt(in.MaxMemoryBytes, c.MaxMemoryBytes),
		GcInterval:        in.GcInterval,
		MaxRecursionDepth: clampOpt(in.MaxRecursionDepth, some(c.MaxRecursionDepth)),
		MaxSuspensions:    in.MaxSuspensions,
	}
}

func clampOpt(client, ceiling *uint64) *uint64 {
	switch {
	case client == nil:
		return ceiling
	case ceiling == nil:
		return client
	case *client < *ceiling:
		return client
	default:
		return ceiling
	}
}

// N6 stub. NotImplemented: the memory limit of a restored dump is not echoed by the worker,
// so a dump signed under a higher --max-memory-mib still restores with that limit.
func (c Ceilings) AdmitsRestored(ev pb.ChildEvent) bool {
	if c.MaxDurationMicros != nil {
		if ev.MaxDurationMicros == nil || *ev.MaxDurationMicros > *c.MaxDurationMicros { return false }
	}
	return true
}
```

`max_suspensions` has no server ceiling, so only duration is compared; the field stays in the signature for a future ceiling.

### 8.12 `Session.load`

```go
func (s *Session) load(envelope []byte, traceParent *string) SessionEnd {
	raw, err := s.deps.Config.DumpKeys.Verify(envelope)
	if err != nil {
		s.deps.Metrics.Dumps(DumpRejected).Inc()
		logline(Info, "dump_rejected", "client", s.client, "cause", err)
		if werr := s.out.Frame(events.Error(ValueError, INVALID_DUMP)); werr != nil { return WriteFailed }
		s.idleDeadline = deadlineFrom(s.deps.Config.IdleTimeout)
		return nil
	}
	s.deps.Metrics.Dumps(DumpVerified).Inc()
	req := pb.ParentRequest{Kind: pb.Load{State: raw}, TraceParent: traceParent}
	return s.turn(req)                                    // forward() applies the N6 check for RequestKind Load
}
```

### 8.13 `Session.turn`

```go
func (s *Session) turn(req pb.ParentRequest) SessionEnd {
	kind := requestKind(req)
	checkout := s.state.Checkout()
	if s.state.IsReady() && kind != Resume {
		s.turnDeadline = deadlineFrom(s.deps.Config.TurnTimeout)
	}
	onEvent := func(ev pb.ChildEvent) Future {
		return async(func() { _ = s.out.Frame(ev) })     // Print events; a write error surfaces on the turn-ender send
	}
	fut := checkout.TurnRaw(req, onEvent)
	for {
		wakeAt, wake := s.turnWake()                      // earliest of turn, session, keepalive
		select {
		case res := <-fut:
			if res.Err != nil { return s.fail(res.Err) }
			return s.forward(kind, res.Event)
		case <-timerAt(wakeAt):
			switch wake {
			case WakeTurn:
				s.deps.Metrics.Timeouts(TimeoutTurn).Inc()
				s.dropCheckout()                          // cancels fut; the worker is killed on drop
				s.out.Close(1008, closeTurn(*s.deps.Config.TurnTimeout))
				return Timeout(TimeoutTurn)
			case WakeSession:
				s.deps.Metrics.Timeouts(TimeoutSession).Inc()
				s.dropCheckout()
				s.out.Close(1008, closeSession(*s.deps.Config.SessionTimeout))
				return Timeout(TimeoutSession)
			case WakeKeepalive:
				if end := s.keepaliveTick(); end != nil { s.dropCheckout(); return end }
			}
		case msg := <-s.inboundDuringTurn():             // only Closed/Text are acted on; a Request stays queued
			if msg.Kind == Closed { s.dropCheckout(); return ClientClosed }
			if msg.Kind == Text { s.dropCheckout(); s.out.Close(1008, CLOSE_TEXT_MESSAGE); return Violation(CLOSE_TEXT_MESSAGE) }
		case <-s.deps.Drain.Soft.Cancelled():
			s.draining = true                             // the turn continues
		case <-s.deps.Drain.Hard.Cancelled():
			s.dropCheckout()
			s.out.Close(1001, CLOSE_SHUTTING_DOWN)
			return DrainDropped
		}
	}
}
```

`inboundDuringTurn` peeks the depth-1 channel: a `Request` received mid-turn is held and replayed as the next `on_request` after the turn ends (strict alternation makes this a client bug; montygo never does it).

### 8.14 `Session.forward`

```go
func (s *Session) forward(kind RequestKind, ev pb.ChildEvent) SessionEnd {
	switch k := ev.Kind.(type) {
	case DumpResult:
		env, ok := s.deps.Config.DumpKeys.Sign(k.State)
		if !ok {
			msg := frameTooLarge(len(k.State)+HEADER_LEN, MAX_FRAME_LEN)
			s.out.Frame(events.Fatal(msg))
			s.out.Close(1011, "")
			return WorkerFailed
		}
		s.deps.Metrics.Dumps(DumpSigned).Inc()
		ev.Kind = DumpResult{State: env}
	case FatalError:
		s.out.Frame(ev)
		s.out.Close(1000, "")
		s.state = Closed
		return WorkerFailed
	}
	if kind == Load && (isOk(ev) || isSuspension(ev)) && !s.deps.Config.Ceilings.AdmitsRestored(ev) {
		s.dropCheckout()
		s.out.Close(1008, CLOSE_RESTORED_OVER_LIMITS)
		return Violation(CLOSE_RESTORED_OVER_LIMITS)
	}
	if err := s.out.Frame(ev); err != nil { return WriteFailed }
	if isSuspension(ev) {
		s.state = Suspended{s.state.Checkout()}
		if kind == Load { s.turnDeadline = deadlineFrom(s.deps.Config.TurnTimeout) }
	} else {
		s.state = Ready{s.state.Checkout()}
		s.turnDeadline = nil
	}
	s.idleDeadline = deadlineFrom(s.deps.Config.IdleTimeout)
	return nil
}
```

### 8.15 `Session.fail`

```go
func (s *Session) fail(err monty_pool.PoolError) SessionEnd {
	logline(Warn, "worker_failed", "client", s.client, "error", err.Display())
	if rt, ok := err.(PoolErrorRuntime); ok {
		s.dropCheckout()
		if rt.Exception.Type == MemoryError && rt.Exception.Message == MEMORY_KILLED {
			s.out.Frame(events.Error(MemoryError, MEMORY_KILLED))
			s.out.Close(1011, "")
			return WorkerFailed
		}
		s.out.Frame(events.ErrorFrom(rt.Exception))
		s.out.Close(1011, "")
		return WorkerFailed
	}
	s.dropCheckout()
	s.out.Frame(events.Fatal(err.Display()))
	s.out.Close(1011, "")
	return WorkerFailed
}
```

`PoolError::Runtime` other than the memory kill is not expected from `turn_raw` (it returns `Error` events as `Ok(event)`); forwarding it and closing keeps the client informed if upstream changes.

### 8.16 `Session.shutdown_dump`

```go
func (s *Session) shutdownDump() SessionEnd {
	var dump []byte
	if checkout := s.state.Checkout(); checkout != nil {
		budget := minDuration(s.deps.Config.TurnTimeout, time.Until(s.deps.Drain.GraceDeadline.Get()))
		res, err := withTimeout(budget, checkout.TurnRaw(pb.Dump{}, noEvents))
		if err == nil {
			if dr, ok := res.Kind.(DumpResult); ok {
				if env, ok := s.deps.Config.DumpKeys.Sign(dr.State); ok {
					s.deps.Metrics.Dumps(DumpSigned).Inc()
					dump = env
				} else {
					logline(Warn, "shutdown_dump_too_large", "client", s.client, "bytes", len(dr.State))
				}
			}
		}
	}
	s.dropCheckout()
	s.out.Frame(events.Shutdown(dump))                   // nil → field absent
	s.out.Close(1001, CLOSE_SHUTTING_DOWN)
	return Drained
}
```

### 8.17 `Session.end`

```go
func (s *Session) end(end SessionEnd, span *ConnectionSpan) {
	outcome := map[SessionEndKind]Outcome{
		ClientClosed: OutcomeClosed, WriteFailed: OutcomeError, Violation: OutcomeError,
		WorkerFailed: OutcomeError, Timeout: OutcomeTimeout, Drained: OutcomeDrained,
		DrainDropped: OutcomeDrainDropped,
	}[end.Kind]
	s.deps.Metrics.SessionsTotal(outcome).Inc()
	if span != nil {
		if end.Kind == Timeout { span.Event("timeout", kv("kind", end.TimeoutKind)) }
		if end.Kind == Violation { span.Event("violation", kv("reason", end.Reason)) }
		span.End(outcome)
	}
	logline(Info, "session_end", "client", s.client, "outcome", outcome,
		"duration_ms", time.Since(s.started).Milliseconds())
}
```

### 8.18 `DumpKeys.sign` and `verify`

```go
func (k *DumpKeys) Sign(state []byte) ([]byte, bool) {
	if len(state)+HEADER_LEN > MAX_FRAME_LEN { return nil, false }
	out := make([]byte, 0, HEADER_LEN+len(state))
	out = append(out, MAGIC[:]...)
	out = append(out, VERSION)
	out = append(out, k.current.id[:]...)
	mac := hmacSHA256(k.current.secret)
	mac.Write(MAGIC[:]); mac.Write([]byte{VERSION}); mac.Write(k.current.id[:])
	mac.Write([]byte(MONTY_REV)); mac.Write(state)
	out = append(out, mac.Sum(nil)...)
	return append(out, state...), true
}

func (k *DumpKeys) Verify(env []byte) ([]byte, error) {
	if len(env) < HEADER_LEN { return nil, TooShort }
	if !bytes.Equal(env[0:4], MAGIC[:]) { return nil, BadMagic }
	if env[4] != VERSION { return nil, UnsupportedVersion(env[4]) }
	id := env[5:9]
	var key *Key
	switch {
	case bytes.Equal(id, k.current.id[:]):
		key = &k.current
	case k.previous != nil && bytes.Equal(id, k.previous.id[:]):
		key = k.previous
	default:
		return nil, UnknownKey
	}
	mac := hmacSHA256(key.secret)
	mac.Write(env[0:9]); mac.Write([]byte(MONTY_REV)); mac.Write(env[HEADER_LEN:])
	if !mac.VerifySlice(env[9:41]) { return nil, BadMac }   // constant-time
	return env[HEADER_LEN:], nil
}
```

### 8.19 `OtlpAdapter.export_metrics`

```go
func (a *OtlpAdapter) ExportMetrics(payload []byte) {
	body := bytes.Clone(payload)
	spawn(func() {
		req := post(a.metricsURL, "application/x-protobuf", body)
		for k, v := range otlpHeadersFromEnv() { req.Header.Set(k, v) }
		resp, err := a.client.Do(req.WithTimeout(10 * time.Second))
		if err != nil || resp.StatusCode >= 300 {
			a.rateLimitedLog("otlp_metrics_export_failed", err, statusOf(resp))   // at most once per minute
		}
	})
}
```

### 8.20 Go: `WebSocketDialer.transport` and `HealthCheck`

```go
func (d *WebSocketDialer) transport(raw *atomic.Pointer[net.Conn]) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if d.TLSConfig != nil {
		t.TLSClientConfig = d.TLSConfig.Clone()
	}
	dial := d.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err == nil && raw != nil {
			raw.Store(&c)
		}
		return c, err
	}
	return t
}

func (d *WebSocketDialer) HealthCheck(ctx context.Context, pairs [][2]string) error {
	target, err := healthURL(d.URL)
	if err != nil {
		return fmt.Errorf("%s: %w", d.URL, err)
	}
	header, host, err := d.upgradeHeader(pairs)
	if err != nil {
		return err
	}
	timeout := d.DialTimeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := d.transport(nil)
	defer t.CloseIdleConnections()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	req.Header = header
	if host != "" {
		req.Host = host
	}
	resp, err := (&http.Client{Transport: t}).Do(req)
	switch {
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil && hctx.Err() != nil:
		return fmt.Errorf("%s: health check timed out after %s", target, timeout)
	case err != nil:
		return fmt.Errorf("%s: %w", target, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: health check returned %d", target, resp.StatusCode)
	}
	return nil
}

func healthURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "ws", "http":
		u.Scheme = "http"
	case "wss", "https":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/health"
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}
```

`Spawn` changes one line: `transport := d.transport(&raw)` replaces the inline clone.

### 8.21 Go: `monty.CheckWebSocketHealth`

```go
func CheckWebSocketHealth(ctx context.Context, opts WebSocketOptions) error {
	timeout := opts.RequestTimeout
	switch {
	case timeout == 0:
		timeout = defaultWebSocketRequestTimeout
	case timeout < 0:
		timeout = 0
	}
	var pairs [][2]string
	if opts.ConnectHeaders != nil {
		h, err := opts.ConnectHeaders(ctx)
		if err != nil {
			return err
		}
		pairs = sortedPairs(h)                             // same ordering helper as Checkout
	}
	return opts.dialer(timeout).HealthCheck(ctx, pairs)
}
```

### 8.22 Root harness: `testBackends` and `openPool`

```go
func testBackends() []monty.Backend {
	spec := os.Getenv("MONTY_TEST_BACKENDS")
	if spec == "" {
		spec = "native,wasm"
		if os.Getenv(wsURLEnv) != "" {
			spec += ",websocket"
		}
	}
	var out []monty.Backend
	for _, name := range strings.Split(spec, ",") {
		if b, ok := backendByName(strings.TrimSpace(name)); ok {
			out = append(out, b)
		}
	}
	return out
}

func openPool(ctx context.Context, b monty.Backend, opts monty.Options) (*monty.Pool, error) {
	if b != monty.BackendWebSocket {
		opts.Backend = b
		return monty.New(ctx, opts)
	}
	timeout := opts.RequestTimeout
	if timeout == 0 {
		timeout = monty.NoRequestTimeout
	}
	return monty.NewWebSocket(ctx, monty.WebSocketOptions{
		URL:             os.Getenv(wsURLEnv),
		MaxProcesses:    opts.MaxProcesses,
		CheckoutTimeout: opts.CheckoutTimeout,
		RequestTimeout:  timeout,
	})
}

func TestMain(m *testing.M) {
	// existing MONTY_BIN discovery
	for _, b := range testBackends() {
		if b == monty.BackendWebSocket && os.Getenv(wsURLEnv) == "" {
			fmt.Fprintln(os.Stderr, "MONTY_TEST_BACKENDS names websocket but MONTY_TEST_WS_URL is unset")
			os.Exit(2)
		}
	}
	// existing m.Run and pool cleanup
}
```

### 8.23 `tests/network`: `ContainerPool.Start`

```go
func (p *ContainerPool) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}
	p.units = make([]*Unit, p.size)
	p.available = make(chan *Unit, p.size)
	p.readyCh, p.deadCh = make(chan struct{}), make(chan struct{})
	for i := range p.size {
		u := &Unit{ID: i, cmds: make(chan unitCmd, 1), done: make(chan struct{})}
		p.units[i] = u
		go u.lifecycle(p)
		u.cmds <- unitCmd{kind: cmdStart}
	}
	select {
	case <-p.readyCh:
		p.startDone.Store(true)
		p.started = true
		return nil
	case <-p.deadCh:
		p.startDone.Store(true)
		for _, u := range p.units {
			select {
			case u.cmds <- unitCmd{kind: cmdShutdown}:
			default:
			}
		}
		for _, u := range p.units {
			<-u.done
		}
		if e := p.firstErr.Load(); e != nil {
			return *e
		}
		return fmt.Errorf("all %d units failed to start", p.size)
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

### 8.24 `tests/network`: `Unit.lifecycle`

```go
func (u *Unit) lifecycle(p *ContainerPool) {
	defer close(u.done)
	for cmd := range u.cmds {
		switch cmd.kind {
		case cmdStart:
			if err := p.startWithRetry(context.Background(), u, ServerConfig{}); err != nil {
				u.setState(unitDead)
				p.signalDead(err)
				continue
			}
			u.setState(unitReady)
			p.available <- u
			p.signalReady()

		case cmdRelease:
			if p.stopping.Load() {
				u.setState(unitDead)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			recreate := cmd.recreate
			if !recreate {
				if err := p.healthyAndIdle(ctx, u); err != nil {
					log.Printf("unit %d not idle after test: %v; recreating", u.ID, err)
					recreate = true
				}
			}
			var err error
			if recreate {
				_ = p.stopContainer(ctx, u)
				err = p.startWithRetry(ctx, u, ServerConfig{})
			}
			cancel()
			if err != nil {
				u.setState(unitDead)
				log.Printf("ERROR: unit %d could not be recreated: %v", u.ID, err)
				bestEffortErrorf(cmd.t, "pool.Release: unit %d recreate failed: %v", u.ID, err)
				p.warnIfNoLiveUnits()
				continue
			}
			if !u.tryTransition(unitReleasing, unitReady) {
				panic(fmt.Sprintf("unit %d release in state %d", u.ID, u.getState()))
			}
			p.available <- u

		case cmdShutdown:
			_ = p.stopContainer(context.Background(), u)
			u.setState(unitDead)
			return
		}
	}
}

func (p *ContainerPool) startWithRetry(ctx context.Context, u *Unit, cfg ServerConfig) error {
	backoff := []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}
	var last error
	for attempt := range 3 {
		if last = p.startContainer(ctx, u, cfg); last == nil {
			return nil
		}
		if isInfraFailure(last) || attempt == 2 {
			break
		}
		_ = p.stopContainer(ctx, u)
		time.Sleep(backoff[attempt])
	}
	return fmt.Errorf("start unit %d: %w", u.ID, last)
}
```

### 8.25 `tests/network`: `startContainer`

```go
func (p *ContainerPool) startContainer(ctx context.Context, u *Unit, cfg ServerConfig) error {
	env := defaultEnv()
	maps.Copy(env, cfg.Env)
	cmd := append([]string{"--host", "0.0.0.0"}, cfg.Args...)
	req := testcontainers.ContainerRequest{
		Image:        p.image,
		ExposedPorts: []string{serverPort},
		Env:          env,
		Cmd:          cmd,
		Labels:       map[string]string{testLabelKey: testLabelValue},
		WaitingFor:   wait.ForHTTP("/health").WithPort(serverPort).WithStartupTimeout(30 * time.Second),
	}
	if cfg.hostGateway {
		req.HostConfigModifier = func(hc *container.HostConfig) {
			hc.ExtraHosts = append(hc.ExtraHosts, "host.docker.internal:host-gateway")
		}
	}
	if os.Getenv(EnvDumpContainerLogs) != "0" {
		u.logs = newFileLogConsumer(u.ID)
		req.LogConsumerCfg = &testcontainers.LogConsumerConfig{Consumers: []testcontainers.LogConsumer{u.logs}}
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		if c != nil {
			_ = terminateAndVerify(ctx, c, fmt.Sprintf("unit %d after create error", u.ID))
		}
		return err
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = terminateAndVerify(ctx, c, "host lookup")
		return err
	}
	port, err := c.MappedPort(ctx, serverPort)
	if err != nil {
		_ = terminateAndVerify(ctx, c, "mapped port")
		return err
	}
	u.container, u.cfg, u.host, u.port = c, cfg, host, port.Port()
	return nil
}
```

`ServerConfig` gains an unexported `hostGateway bool`, set by `WithOTLPReceiver`. **verify** the `MappedPort` return type in v0.44 (`network.Port` vs `nat.Port`).

### 8.26 `tests/network`: `SetupServer`

```go
func SetupServer(t *testing.T, opts ...SetupOption) *TestServer {
	t.Helper()
	o := setupOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	pool := GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	u, err := pool.Acquire(ctx, o.cfg)
	require.NoError(t, err, "acquire unit")
	s := &TestServer{Unit: u, t: t, pool: pool, dirty: !o.cfg.isDefault()}
	if u.logs != nil {
		u.logs.Rename(t.Name())
	}
	t.Cleanup(func() {
		if t.Failed() && u.logs != nil {
			t.Logf("server logs: %s", u.logs.path)
		}
		pool.Release(t, u, s.dirty)
	})
	return s
}
```

### 8.27 Representative test: `TestDrain_IdleSessionGetsShutdownDump`

```go
func TestDrain_IdleSessionGetsShutdownDump(t *testing.T) {
	t.Parallel()
	s := SetupServer(t, WithArgs("--drain-grace", "10"))
	ctx := testCtx(t)
	p := s.NewPool(monty.WebSocketOptions{})
	sess := s.Checkout(ctx, p, monty.CheckoutOptions{})
	_, err := sess.FeedRun(ctx, "x = 41", nil)
	require.NoError(t, err)

	s.Signal("TERM")
	s.WaitMetric("monty_server_draining", nil, 1, 5*time.Second)

	_, err = sess.FeedRun(ctx, "x + 1", nil)
	var shut *monty.ShutdownError
	require.ErrorAs(t, err, &shut)
	require.NotEmpty(t, shut.Dump)
	require.Equal(t, "MTYD", string(shut.Dump[:4]))
	require.Equal(t, 0, s.WaitExited(15*time.Second))

	s.Recreate()                                            // same dump key
	p2 := s.NewPool(monty.WebSocketOptions{})
	restored := s.Checkout(ctx, p2, monty.CheckoutOptions{})
	require.NoError(t, restored.LoadSession(ctx, shut.Dump))
	v, err := restored.FeedRun(ctx, "x + 1", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), v)
}
```

### 8.28 Representative test: `TestProtocol_VersionSkewIsFatal`

```go
func TestProtocol_VersionSkewIsFatal(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	ctx := testCtx(t)
	c, _, err := s.RawDial(ctx, nil)
	require.NoError(t, err)
	defer c.CloseNow()
	require.NoError(t, sendRequest(ctx, c, configureRequest(2)))
	ev, err := readEvent(ctx, c)
	require.NoError(t, err)
	require.Equal(t,
		"unsupported protocol version 2 (server supports protocol version 3, try updating to a newer client version)",
		ev.GetFatalError().GetMessage())
	_, err = readEvent(ctx, c)
	requireClose(t, err, websocket.StatusNormalClosure, "")
}
```

### 8.29 Representative test: `TestReplCLI_ContinuationOverWebSocket`

```go
func TestReplCLI_ContinuationOverWebSocket(t *testing.T) {
	t.Parallel()
	s := SetupServer(t)
	r := StartRepl(t, s.URL())
	r.Send("def add(a, b):")
	r.WaitStderr("... ", 10*time.Second)
	r.Send("    return a + b")
	r.Send("")
	r.Send("add(20, 22)")
	r.WaitStdout("42", 10*time.Second)
	r.CloseStdin()
	require.Equal(t, 0, r.Wait(10*time.Second))
}
```

**verify** which stream carries continuation prompts in non-interactive mode (`examples/repl/main_test.go` `TestImplicitContinuationPrompts` is the reference) and adjust `WaitStderr`/`WaitStdout`.

### 8.30 Representative test: `TestPyClient_DrainShutdownDumpRestore`

```go
func TestPyClient_DrainShutdownDumpRestore(t *testing.T) {
	t.Parallel()
	image := pyClientImage(t)
	s := SetupServer(t, WithArgs("--drain-grace", "10"))
	ctx := testCtx(t)
	ip, err := s.Unit.ContainerIP(ctx)
	require.NoError(t, err)

	c := startPyClient(t, "/opt/pin/bin/python", "/scripts/drain_capture.py",
		map[string]string{"MONTY_URL": "ws://" + ip + ":8000/"}, "READY")
	s.Signal("TERM")
	out := waitPyExit(t, c, 30*time.Second)
	require.Equal(t, 0, out.ExitCode, out.Output)
	dump := parsePrefixedLine(t, out.Output, "DUMP=")

	s.Recreate()
	ip2, err := s.Unit.ContainerIP(ctx)
	require.NoError(t, err)
	res := runPyClient(t, "/opt/pin/bin/python", "/scripts/restore.py",
		map[string]string{"MONTY_URL": "ws://" + ip2 + ":8000/", "MONTY_DUMP": dump})
	require.Equal(t, 0, res.ExitCode, res.Output)
	require.Contains(t, res.Output, "OK x=1")
	_ = image
}
```

### 8.31 `examples/repl`: `openPool`

```go
func openPool(ctx context.Context, o *options) (*monty.Pool, error) {
	if o.ws.url == "" {
		return monty.New(ctx, montyenv.PoolOptions())
	}
	tlsCfg, err := o.ws.tlsConfig()
	if err != nil {
		return nil, err
	}
	return monty.NewWebSocket(ctx, monty.WebSocketOptions{
		URL:            o.ws.url,
		MaxProcesses:   2,                                  // the interrupt path checks out a replacement
		RequestTimeout: monty.NoRequestTimeout,
		TLSConfig:      tlsCfg,
	})
}

func (w wsOptions) tlsConfig() (*tls.Config, error) {
	if w.caFile == "" && !w.insecureSkipVerify {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: w.insecureSkipVerify}
	if w.caFile != "" {
		pem, err := os.ReadFile(w.caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", w.caFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
```

---

## 9. NOT CONSIDERED & TODO

| # | Item | Tags | Handling in this implementation |
|---|---|---|---|
| N1 | Server-side authentication (bearer token, mTLS client certs, per-tenant quotas) | postponed, porting:improvement-over-upstream | Doc only. Client already carries `ConnectHeaders` and `TLSConfig`. |
| N2 | Native TLS listener in the server | postponed, potential-improvement | Doc: terminate TLS at an ingress. |
| N3 | Automatic restore after `ShutdownError` in the Go client | too-complex, potential-improvement | README example of manual restore. |
| N4 | Spawn-ahead `--prewarm N` | potential-improvement | Not implemented. |
| N5 | Raw-bytes `turn_raw` to skip decode/re-encode | potential-improvement, porting:improvement-over-upstream | Not implemented; proposal for upstream. |
| N6 | Dump ceilings drift after an operator lowers ceilings | too-complex | Stub `admits_restored` (duration); memory `NotImplemented` comment (§8.11). |
| N7 | Container hardening profiles | postponed | Doc recommendations in `docker.md`. |
| N8 | Per-worker OS limits (`RLIMIT_AS`, CPU) | postponed | Not implemented. |
| N9 | Horizontal scaling and key distribution | postponed | Doc only. |
| N10 | Other image platforms | postponed | Not implemented. |
| N11 | Registry publishing, signing, SBOM, provenance | postponed | `make docker-push` only. |
| N12 | Docker on macOS CI runners | postponed | Not implemented. |
| N13 | Full Monty reverse proxy to a CPython sandbox | porting:deffered, too-complex | Not implemented. |
| N14 | `--logfire-token` exporter | porting:simplefication | Deviation in `docs/parity/server.md`. |
| N16 | `MONTY_EXAMPLES_BACKEND=websocket` for every example | postponed | Only `examples/repl -ws`. |
| N17 | permessage-deflate, RFC 8441 | potential-improvement | Not implemented. |
| N18 | DoS: `max_sessions × 256 MiB` buffers, slow handshakes | too-complex | 10 s handshake timeout constant; sizing doc. |
| N19 | Drain races | too-complex | Designed defaults in §7.10; covered by `server_drain_test.go`. |
| N20 | `--dump-key-file` / secret store | potential-improvement | Not implemented. |
| N21 | Signed dump over `MAX_FRAME_LEN` | too-complex | `DumpResult` → `FatalError` + 1011; `ShutdownDump` without dump + log. |
| N22 | Override builds (`build/monty-src` not at the pin) still sign with the pinned `MONTY_REV` | postponed | Doc: override images are for development; dumps may not be portable to pinned images. |
| N23 | Session-level `max_suspensions` ceiling | potential-improvement | Not implemented; client value passes through, `monty-pool` enforces it. |
| N24 | `/health` semantics beyond draining (worker binary missing at runtime, pool saturation) | postponed | `/health` reflects only the draining state. |
| N25 | Pinned versions of zig and cargo-zigbuild drift | postponed | Pinned in the Dockerfile; moved with the upgrade checklist. |

N15 is implemented (§4.10, §6.5, §7.15).

---

## 10. Parity deviations (`docs/parity/server.md`)

| Full Monty (`../monty/docs/server.md`) | monty-server | Reason |
|---|---|---|
| closed source, `linux/amd64` only | open source, `linux/amd64` + `linux/arm64` | task scope |
| private dump signature format | `MTYD` v1 envelope (§5.3) | format not public; dumps are not portable between the two servers |
| `--logfire-token` | `--otlp-endpoint`, `--otlp-protocol` | R11 |
| no metrics endpoint documented | `GET /metrics` | R12 |
| worker reuse wording ambiguous | fresh worker per session | R10 |
| server texts not public | texts in `server/src/texts.rs` | F2 |
| `/health` returns 200 while accepting | 200 while accepting, 503 while draining on already-open HTTP connections | the listener closes on SIGTERM, so new requests are refused either way |
| — | `monty-server probe` subcommand | `scratch` image healthcheck |
| — | `--dump-key-previous` | key rotation |
| — | N21 overflow handling | not specified upstream |

---

## 11. Implementation notes

Differences between this specification and the implementation, recorded after the implementation was verified.

| Area | Specification | Implementation | Reason |
|---|---|---|---|
| OTLP transport | `--otlp-protocol` `http/protobuf` or `grpc` | `grpc` is rejected at startup with "OTLP gRPC export is not supported; use --otlp-protocol http/protobuf" | the thread-based batch processors cannot drive a tonic exporter without the experimental async-runtime processors |
| OTLP clients | exporters from `opentelemetry-otlp` | exporters get an explicit `reqwest::blocking::Client`; `logfire` is a direct dependency with `export-http-protobuf` | feature unification enabled the async reqwest client, which panics in the batch processor threads ("no reactor running"); logfire's protocol match needs its HTTP feature once `http-proto` is on |
| Handshake timeout | `HANDSHAKE_TIMEOUT` 10 s constant | not implemented | axum's `serve` exposes no header-read timeout; N18 stays doc-only |
| Pool release | not specified | `internal/pool/pool.go` `release` closes a WebSocket worker's observer before the asynchronous retire | the session span of a WebSocket checkout ended after `Session.Close` returned, so telemetry tests failed on the `websocket` backend; upstream ends it synchronously when the worker is dropped |
| Turn deadline | armed while a turn runs | also armed while the session waits for the resume of a suspension | `--turn-timeout` includes host callback time |
| Cargo cache mounts | one `cargo-registry` cache | `cargo-registry-${TARGETARCH}` | concurrent platform builds raced on the shared registry unpack |
| Source staging | `rsync --files-from` | `git ls-files -z | tar --null -T -` | macOS openrsync lacks `--ignore-missing-args` |
| Metric assertions | absolute values | `Baseline` + `WaitMetricDelta` | units are reused, so counters accumulate across tests |
| Parallel tests | `TestParallel_ActiveSessionsReturnToZero` | `TestParallel_AbandonedTurnsReleaseSessions` | the zero-active assertion is part of both tests; the second test covers cancelled turns |
| Limit tests | `bytearray` allocations | string repetition | Monty has no `bytearray` |
| macOS test builds | not specified | `tests/network` links through clang, so `SDKROOT` MUST point at the 26.5 SDK on a beta SDK host | testcontainers dependencies use cgo on darwin |
| Telemetry test receiver | in-test OTLP/HTTP receiver in the Go process, server started `WithHostGateway` | receiver is a `python:3.13-slim-bookworm` container on the default bridge (`MONTYGO_PYTHON_IMAGE`) printing base64 request bodies; the server exports to its container IP; `WithHostGateway` and `ServerConfig.HostGateway` are removed | a host firewall with `INPUT DROP` (ufw on the Linux verification host) blocks container-to-host traffic, while container-to-container traffic is allowed |
