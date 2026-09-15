# Target architecture: feedback fixes and tag-based versioning

Date: 2026-09-15. Source brainstorm: `20260915-feedback-fixes-and-versioning.md`. Report: `docs/reports/feedback.md`. Baseline: `cf99bdd` on `feat/dockerized-runtime`, Monty `0.0.23` + `f8acf4fa`, protocol 3.

This document is the implementation specification. Work through it top to bottom. Every exported change lands in `testdata/public_api.golden`, `docs/parity/api.md`, the README and `changelogs/v0.1.0.md`.

---

## 1. Initial request

> fix all confirmed issues with medium, high and critical priority from docs/reports/feedback.md
> montygo should have versioning based on tags, when version built not from tagged commit it should use `{verion}-{short-hash}` version and it should be resolved during build time. Example of it can be found in ../neria/meridian-overlay project

In scope: report rows 1–21. Out of scope: rows 22–23 (low), rows 25–35 (`montygo-test`).

---

## 2. High-level view

### 2.1 Where each row lands

```
 package monty (public)                      internal/                         server/ · docker/ · scripts/
┌──────────────────────────────────┐        ┌──────────────────────────────┐  ┌──────────────────────────────┐
│ version.go   BindingVersion()  R2│        │ worker/worker.go             │  │ scripts/version.sh        R1 │
│ monty.go     MontyVersion      10│        │   frameQueue byte bound    1 │  │ scripts/check-pins.sh     10 │
│ session.go   life state, Interrupt│        │ worker/websocket.go          │  │ server/build.rs           R2 │
│              CloseNow Done Err  2,3,5│      │   close reason, Done/Err   5 │  │ server/src/http.rs /info  11 │
│ run.go       Session.Go, Run    2 │        │ pool/pool.go                 │  │ docker/Dockerfile version 10 │
│ answer.go    abort on cancel    3 │        │   counters, reaper,        4 │  │ Makefile IMAGE_TAG, ldflags  │
│ host.go      Host registry  12,18│        │   Shutdown                16 │  │ .github/workflows/ci.yml  21 │
│ classinstance.go All(), Expose,  │        │ pool/checkout.go Abort     3 │  └──────────────────────────────┘
│              store bound     7,8 │        │ pool/budget.go Unlimited   6 │
│ errors.go    ErrSessionLost,     │        │ telemetry/ per-pool        17│
│              KnownExceptionNames │        │ wire/ fuzz + bench         19│
│              DisconnectError 5,9 │        └──────────────────────────────┘
│ options.go   Unlimited, limits 6,8,15│
│ print.go     Lines, Flush     14 │
│ future.go    AsyncContext     15 │
│ telemetry.go Options.Telemetry 17│
│ convert.go   AsNamedTuple     13 │
│ websocket.go FetchServerInfo  11 │
└──────────────────────────────────┘
```

### 2.2 Version flow

```
git tag v0.1.0 ── scripts/version.sh ──┬─ Makefile: VERSION, IMAGE_TAG
                                        ├─ go build/test -ldflags "-X github.com/asalimonov/montygo.buildVersion=$(VERSION)"
                                        ├─ docker build --build-arg MONTY_SERVER_VERSION=$(VERSION)
                                        │     └─ server/build.rs → env!("MONTY_SERVER_VERSION") → SERVER_VERSION
                                        └─ image tag monty-server:$(VERSION), label org.opencontainers.image.version

consumer `go get github.com/asalimonov/montygo@v0.1.0`
   └─ debug.ReadBuildInfo().Deps[montygo].Version = "v0.1.0" → BindingVersion() = "0.1.0"
```

### 2.3 Session lifecycle after the change

```
FeedRun(ctx) ──── s.mu held ─────────────────────────────────────────────────────────┐
   Feed ──▶ worker executes Python ──▶ suspension ──▶ host callback (cbCtx) ──▶ resume ─┘
                                                      │
   Session.Interrupt(reason) / ctx cancelled ─────────┤ callback pending: cancel cbCtx ─▶ AbortFeed ─▶ Error(KeyboardInterrupt) ─▶ FeedRun returns *RuntimeError, session usable
                                                      └ no callback pending: wait InterruptGrace ─▶ co.Abandon() ─▶ worker killed ─▶ session poisoned (ErrSessionLost)
   Session.CloseNow() ─▶ life.closed=true; co.Abandon() ─▶ FeedRun returns ErrSessionClosed
   Session.Done()/Err() ─▶ closed when the worker's Done() closes or the session is poisoned/closed
```

---

## 3. Decisions

| # | Question | Chosen | Rejected |
|---|---|---|---|
| R1 | dev version rule | last reachable `v*` tag as-is + short hash; `-dirty` | patch bump (meridian); `git describe` |
| R2 | runtime version source | `-X buildVersion` → build info → `0.0.0-unknown` | generated file; ldflags only |
| R3 | cancel while host call pending | `AbortFeed(KeyboardInterrupt)` by default | opt-in flag; `Interrupt` only |
| R4 | API breaks | allowed before `v0.1.0` | additive only |
| R5 | telemetry ownership | per pool, global installation as default | per-pool only; keep global |
| 1 | frame bound | bytes, 64 MiB default, producer blocks | frame count; drop frames |
| 4 | retirement | four counters, bounded reaper, `Kill` on discard | separate hard ceiling |
| 5 | lost sessions | close reason in `DisconnectError`, `Done`/`Err`, `ErrSessionLost` | probe feeds |
| 6 | limits | `Unlimited` sentinel; recursion excluded | negative values |
| 7 | exposure | `All()` function, `Expose[T]` | `MethodProvider` only |
| 8 | host objects | `MaxHostObjects` 10 000 | explicit release |
| 11 | server info | `GET /info` JSON | extend `/` |
| 12 | registry | `Host` with build-time validation and stubs | keep map only |
| 14 | print | `Lines` + `Flush` at turn end | change worker flush |
| 15 | futures | `AsyncContext` + `MaxPendingFutures` | remove `Async` |
| 16 | shutdown | `Pool.Shutdown(ctx)` beside `Close` | change `Close` |
| 18 | restore | pinned IDs through `Host` | new wire message |
| 19 | codec | benchmarks + fuzz + field coverage test | switch to generated code |
| 20 | `montypb` | public, documented unstable | move to internal |
| 21 | release | race, govulncheck, cargo audit, fuzz, check-pins, SBOM, licences | — |

---

## 4. Key components

### 4.1 Version resolution (`scripts/version.sh`, `version.go`, `server/build.rs`)

`scripts/version.sh` prints one line. Tag at HEAD wins; otherwise the highest reachable `v*` tag plus the short hash; `-dirty` when the tree has changes; `0.0.0-<hash>` with a warning when no tag is reachable. The Makefile calls it once into `VERSION` and passes it to every Go build, test and Docker build. In Go, `BindingVersion()` prefers the stamped `buildVersion`, then the module's version from `debug.ReadBuildInfo`, then `0.0.0-unknown`. The Rust server bakes `MONTY_SERVER_VERSION` at compile time through `build.rs`, defaulting to `CARGO_PKG_VERSION`. `MontyVersion` stays the upstream release and still goes into `Configure.monty_version`, because the worker compares it with its own build in diagnostics.

### 4.2 Pin consistency (`scripts/check-pins.sh`)

`proto/PROTO_REV` is the single source of the upstream revision, `monty.go` of `MontyVersion`. The script reads both and greps every file that repeats them; a mismatch fails with the file name and the expected value. `make check-pins` runs it and CI calls it in every job.

### 4.3 Bounded frame queue (`internal/worker/worker.go`)

`frameQueue` tracks `bytes` and `maxBytes`. `push` blocks while the next frame would exceed the bound, waking on `pop` or on `close`. `Kill` and `Close` close the queue so a blocked pump exits. The native and wasm spawners receive the bound from `pool.Config.MaxPendingBytes`. A gauge `monty.pool.pending_frame_bytes` reports the backlog.

### 4.4 Session lifecycle state (`session.go`, `run.go`)

`Session` gains a second mutex-protected structure, `life`, holding the interrupt reason, the running callback's cancel function, the `done` channel and the terminal error. `Interrupt`, `CloseNow`, `Done` and `Err` touch only `life`; `FeedRun`, `FeedStart`, `Load*`, `Dump` and `Close` keep `s.mu`. `Session.Go` starts `FeedRun` on a goroutine and returns a `Run` handle with `Done`, `Wait` and `Interrupt`.

### 4.5 Abort on cancel (`answer.go`, `internal/pool/checkout.go`)

The answerer registers the callback context's cancel function in `life` before every host call and checks `life.interrupt` first. After the callback returns, if its context was cancelled, the answerer calls `Checkout.Abort` with the exception (`KeyboardInterrupt`, or the interrupt reason). `Abort` sends `AbortFeed` under a fresh bounded context, reads the `Error` turn-ender through the existing `abortFlight` path, and returns it as a `KindRuntime` error. The session stays usable. `Interrupt` with a suspended snapshot and no turn in flight calls `Abort` directly under `s.mu`.

### 4.6 Pool accounting and shutdown (`internal/pool/pool.go`)

`total` splits into `starting`, `active`, `idle`, `retiring`. `acquire` bounds their sum by `MaxProcesses`. `release` and `discard` move a slot to `retiring` and hand it to a bounded reaper; the reaper decrements after the worker exited. `discard` kills without a close handshake on every transport. `Shutdown(ctx)` marks the pool closed, invokes a callback that the root pool uses to `CloseNow` checked-out sessions when `ctx` ends, and waits for `active+retiring == 0`. `Stats()` exposes the counters.

### 4.7 Lost-session signalling (`internal/worker`, `errors.go`)

`Worker` gains `Done() <-chan struct{}` and `Err() error`. `wsWorker.read` stores the `websocket.CloseError` before closing `done`; `Send` fails immediately once `done` is closed. The pool surfaces the worker error into `pool.Error{Kind: KindDisconnected, CloseCode, CloseReason}`; `Session.mapError` builds `DisconnectError{Code, Reason}`. `ErrSessionLost` is matched by every lost-session error through `Is`.

### 4.8 Limits (`options.go`, `internal/pool/budget.go`)

`Unlimited` is `math.MaxUint64`. `MaxMemory: Unlimited` and `MaxDuration: time.Duration(Unlimited)`... duration is `int64`, so `UnlimitedDuration = time.Duration(math.MaxInt64)`; both omit the wire field. `MaxSuspensions: Unlimited` sends `math.MaxUint64`, and the parent budget treats it as no limit. `MaxRecursionDepth: Unlimited` is an `OptionError`. `MaxHostObjects` and `MaxPendingFutures` are new checkout limits enforced host-side.

### 4.9 Exposure and host registry (`classinstance.go`, `host.go`)

`All()` returns the policy value. `Expose[T]` reflects on interface `T` and returns `Names(...)` of its methods in sandbox naming. `Host` holds validated functions and objects; `CheckoutOptions.Host` registers objects (with pinned IDs) into the session's store at checkout and merges functions under `ExternalLookup`. `Host.Stubs()` renders type-check stubs; `Host.Restorable()` reports objects without a pinned ID.

### 4.10 Print and conversion (`print.go`, `convert.go`)

`Lines` reassembles per-stream lines and flushes remainders at turn end through the new `FlushingPrintTarget` interface, which `printTarget.finish` calls. `AsNamedTuple` and `NewNamedTuple` build named tuples from tagged structs and validated pairs.

### 4.11 Futures (`future.go`, `answer.go`)

`AsyncContext` runs the function with a context derived from the callback context; cancellation propagates. The answerer counts pending futures against `MaxPendingFutures` and settles the rest with `ErrSessionLost` when the session ends.

### 4.12 Per-pool telemetry (`telemetry.go`, `internal/telemetry`)

`Options.Telemetry` and `WebSocketOptions.Telemetry` build a recorder stored in `pool.Config.Recorder`; nil resolves the global installation at creation. `Current()` disappears; `telemetry_hooks.go` and `NewCheckout` take the recorder as a parameter.

### 4.13 Server info (`server/src/http.rs`, `websocket.go`)

`GET /info` returns JSON with the versions and the effective limits. `FetchServerInfo` performs the GET over the WebSocket dialer's transport and decodes `ServerInfo`.

### 4.14 Codec evidence (`internal/wire`)

Benchmarks against `montypb`, fuzz targets seeded from the differential corpus, and a proto-field coverage test.

### 4.15 Release engineering (`.github/workflows/ci.yml`, `Makefile`, `docker/`)

Race on Linux, `govulncheck`, `cargo audit`, 30 s fuzz per target, `check-pins`, SBOM and provenance on push, bundled Rust licences, changelog, and the first tag.

---

## 5. Schemas

### 5.1 Databases and caches

None. montygo has no database, ORM or cache; no tables exist and none are added.

### 5.2 Version string grammar

```
version   = base [ "-" hash ] [ "-dirty" ]
base      = major "." minor "." patch [ "-" prerelease ]      ; from tag vX.Y.Z[-pre]
hash      = 7*40 hexdigit                                     ; git rev-parse --short HEAD
```

| State | `scripts/version.sh` | `BindingVersion()` in a consumer via `go get` |
|---|---|---|
| tag `v0.1.0` at HEAD | `0.1.0` | `0.1.0` (module `v0.1.0`, leading `v` stripped) |
| 2 commits after `v0.1.0` | `0.1.0-3f2a9c1` | `0.1.1-0.20260915120000-3f2a9c1abcde` (Go pseudo-version, unchanged) |
| dirty tree | `0.1.0-3f2a9c1-dirty` | n/a |
| no tag | `0.0.0-3f2a9c1` | `0.0.0-20260915120000-3f2a9c1abcde` |
| directory `replace` | n/a | `(devel)` |
| this repository's own tests without ldflags | n/a | `0.0.0-unknown` |

### 5.3 Config schema changes

| Struct | Field | Type | Default | Meaning |
|---|---|---|---|---|
| `Options` | `MaxPendingBytes` | `int64` | `0` → 64 MiB; `UnlimitedPendingBytes` (-1) disables | bound of buffered worker frames per worker (native, wasm) |
| `Options` | `Telemetry` | `*TelemetryComponents` | nil → global installation | per-pool telemetry |
| `WebSocketOptions` | `Telemetry` | `*TelemetryComponents` | nil | same |
| `CheckoutOptions` | `Host` | `*Host` | nil | session host registry |
| `CheckoutOptions` | `MaxHostObjects` | `uint64` | `0` → 10 000; `Unlimited` | instance store bound |
| `CheckoutOptions` | `MaxPendingFutures` | `uint64` | `0` → 1000; `Unlimited` | unresolved futures bound |
| `CheckoutOptions` | `InterruptGrace` | `time.Duration` | `0` → 100 ms | wait for a suspension before `Interrupt` kills |
| `ResourceLimits` | `MaxSuspensions`, `MaxMemory` | accept `Unlimited` | | |
| `ResourceLimits` | `MaxDuration` | accept `UnlimitedDuration` | | |
| `pool.Config` | `MaxPendingBytes`, `Recorder`, `OnShutdown` | | | internal wiring |

### 5.4 `GET /info` JSON

```json
{
  "version": "0.1.0-3f2a9c1",
  "monty_rev": "f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28",
  "protocol_version": 3,
  "limits": {
    "idle_timeout_s": 60,
    "keepalive_s": 5,
    "session_timeout_s": 3600,
    "turn_timeout_s": 300,
    "max_duration_s": 60,
    "max_memory_bytes": 67108864,
    "max_recursion_depth": 1000,
    "max_sessions": 64,
    "max_sessions_per_client": 10
  }
}
```

Disabled timeouts and ceilings are `0`. Content type `application/json`.

### 5.5 New metric

| Name | Type | Unit | Description |
|---|---|---|---|
| `monty.pool.pending_frame_bytes` | UpDownCounter | `By` | Bytes of worker frames buffered by the parent and not yet consumed. |
| `monty.pool.workers.retiring` | UpDownCounter | `{worker}` | Workers being shut down that still count toward capacity. |

Both are added to `internal/telemetry/metrics.go` and documented in `docs/architecture/telemetry.md`.

---

## 6. Files, types and signatures

### 6.1 Versioning

#### `scripts/version.sh` — CREATED

```bash
#!/usr/bin/env bash
# Prints the montygo version: vX.Y.Z tag at HEAD, else <highest reachable tag>-<short hash>,
# else 0.0.0-<short hash>; "-dirty" when the tree has changes.
set -euo pipefail
main()                       # see §8.1
is_loose_semver "$1"         # X.Y.Z[-pre][+meta]
strip_v "$1"                 # v0.1.0 → 0.1.0
pick_highest_at_head         # git tag --points-at HEAD --list 'v*'
pick_highest_reachable       # git tag --merged HEAD --list 'v*'
semver_cmp "$a" "$b"         # ported from meridian
```

#### `scripts/check-pins.sh` — CREATED

```bash
# Exit 1 with "<file>: expected <value>" for every file that does not carry proto/PROTO_REV / MontyVersion.
REV=$(cat proto/PROTO_REV); SHORT=${REV:0:8}; MV=$(sed -n 's/.*MontyVersion *= *"\(.*\)".*/\1/p' monty.go)
checks: monty.go UpstreamRev=$SHORT; worker-wasm/Cargo.toml rev="$SHORT" ×3; server/Cargo.toml rev="$SHORT" ×3;
        server/src/version.rs MONTY_REV="$REV"; docker/Dockerfile ARG MONTY_REV=$REV; docker/pyclient.Dockerfile ARG MONTY_REV=$REV;
        .github/workflows/ci.yml MONTY_REV: $REV; internal/worker/websocket.go "monty-pool/$MV"; worker-wasm/Cargo.toml version = "$MV"
```

#### `version.go` — CREATED

```go
package monty

// buildVersion is stamped by -ldflags "-X github.com/asalimonov/montygo.buildVersion=<version>".
var buildVersion string

const unknownVersion = "0.0.0-unknown"

// BindingVersion reports this module's release: the stamped build version, else the
// module version recorded in the binary's build info, else "0.0.0-unknown".
func BindingVersion() string
func moduleVersion() (string, bool)   // debug.ReadBuildInfo lookup, strips a leading "v", "(devel)" under replace
```

#### `monty.go` — UPDATED

```go
const (
    // MontyVersion is the Monty release this binding tracks.
    MontyVersion = "0.0.23"
    // Version is MontyVersion.
    //
    // Deprecated: use MontyVersion for the upstream release or BindingVersion for this module.
    Version = MontyVersion
    UpstreamRev = "f8acf4fa"
    ProtocolVersion uint32 = 3
    MaxValueDepth = 48
)
```

#### `internal/worker/websocket.go` — UPDATED (user agent)

```go
// DefaultUserAgent identifies the pool on every WebSocket upgrade request; the pool appends " montygo/<BindingVersion>".
const DefaultUserAgent = "monty-pool/0.0.23"
```

`websocket.go` (root) sets `WebSocketDialer.UserAgent = worker.DefaultUserAgent + " montygo/" + BindingVersion()`.

#### `telemetry.go` — UPDATED (scope version)

`Instrumentation.Version()` and the three `With*InstrumentationVersion` calls use `BindingVersion()`.

#### `server/build.rs` — CREATED

```rust
fn main() {
    println!("cargo:rerun-if-env-changed=MONTY_SERVER_VERSION");
    let version = std::env::var("MONTY_SERVER_VERSION").unwrap_or_else(|_| env!("CARGO_PKG_VERSION").to_owned());
    println!("cargo:rustc-env=MONTY_SERVER_VERSION={version}");
}
```

#### `server/src/version.rs` — UPDATED

```rust
pub const SERVER_VERSION: &str = env!("MONTY_SERVER_VERSION");
pub const MONTY_REV: &str = "f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28";
```

`server/Cargo.toml`: `version = "0.1.0"`, `build = "build.rs"`.

#### `docker/Dockerfile` — UPDATED

`ARG MONTY_SERVER_VERSION=0.0.0-unknown` in the `build` stage, exported as `ENV MONTY_SERVER_VERSION=$MONTY_SERVER_VERSION` before `cargo zigbuild`; final stage `LABEL org.opencontainers.image.version="${MONTY_SERVER_VERSION}"` (the constant `0.0.23` label is removed).

#### `Makefile` — UPDATED

```make
VERSION ?= $(shell scripts/version.sh)
GO_LDFLAGS := -X github.com/asalimonov/montygo.buildVersion=$(VERSION)
IMAGE_TAG ?= $(VERSION)
MONTY_VERSION := $(shell sed -n 's/^[[:space:]]*MontyVersion = "\(.*\)"/\1/p' monty.go)

test / test-native / test-wasm / examples / test-docker / test-network: add -ldflags '$(GO_LDFLAGS)'
docker-build / docker-push: --build-arg MONTY_SERVER_VERSION=$(VERSION)
docker-push: --sbom=true --provenance=true
check-pins: scripts/check-pins.sh
version: @echo $(VERSION)
fuzz: for each Fuzz target: $(GO) test ./internal/wire -run=^$$ -fuzz=$$f -fuzztime=30s
```

#### `tests/network/replcli.go` — UPDATED

`go build -ldflags "-X github.com/asalimonov/montygo.buildVersion=<value of MONTYGO_BUILD_VERSION or unknown>"`; the Makefile exports `MONTYGO_BUILD_VERSION=$(VERSION)` for `test-network`.

#### `examples/repl/main.go` — UPDATED

`banner = "Monty v" + monty.MontyVersion + " REPL (montygo " + monty.BindingVersion() + "). Type `exit` to exit.\n"`; `examples/repl/main_test.go` matches the prefix only.

### 6.2 Frame queue (row 1)

#### `internal/worker/worker.go` — UPDATED

```go
// Worker is one protocol child.
type Worker interface {
    Send(ctx context.Context, payload []byte) error
    Recv(ctx context.Context) ([]byte, error)
    Kill()
    Close()
    Wait(ctx context.Context) (Status, bool)
    PID() (int, bool)
    Kind() Kind
    Alive() bool
    // Done is closed when the worker can no longer serve frames.
    Done() <-chan struct{}
    // Err reports why the worker ended, nil while alive or after a clean end.
    Err() error
}

// Spawner creates workers.
type Spawner interface {
    Spawn(ctx context.Context) (Worker, error)
    Kind() Kind
    Close(ctx context.Context) error
}

// PendingBytesObserver receives the buffered-bytes delta of a queue; nil ignores it.
type PendingBytesObserver func(delta int64)

type frameQueue struct {
    mu       sync.Mutex
    frames   [][]byte
    bytes    int64
    maxBytes int64          // <= 0 means unbounded
    err      error
    notify   chan struct{}  // consumer wake
    space    chan struct{}  // producer wake
    closed   chan struct{}
    observe  PendingBytesObserver
}

func newFrameQueue(maxBytes int64, observe PendingBytesObserver) *frameQueue
func (q *frameQueue) push(frame []byte) error         // blocks on the bound; ErrWorkerGone after close
func (q *frameQueue) pop(ctx context.Context) ([]byte, error)
func (q *frameQueue) fail(err error)
func (q *frameQueue) close()                           // wakes a blocked push
func pumpFrames(r *wire.FrameReader, q *frameQueue)    // stops on push error
```

#### `internal/worker/subprocess_unix.go`, `internal/worker/wasm.go` — UPDATED

`SubprocessSpawner` and `WasmSpawner` gain `MaxPendingBytes int64` and `PendingBytes PendingBytesObserver`; `Spawn` passes them to `newFrameQueue`; `Kill` calls `queue.close()`; both implement `Done()`/`Err()` (`done` channel already exists; `Err` returns the queue's terminal error).

#### `internal/pool/pool.go` — UPDATED (config)

`Config.MaxPendingBytes int64` is forwarded by the root `newPool` to the spawner constructors (`newSubprocessSpawner(bin, stderr, maxPending, observer)`, `worker.SharedWasmSpawner(..., maxPending, observer)`).

#### `pool.go` (root) — UPDATED

```go
// UnlimitedPendingBytes disables the buffered frame bound.
const UnlimitedPendingBytes int64 = -1
const defaultMaxPendingBytes int64 = 64 << 20

type Options struct {
    ... existing ...
    // MaxPendingBytes bounds worker output buffered by the parent per native or wasm worker:
    // 0 means 64 MiB, UnlimitedPendingBytes disables the bound.
    MaxPendingBytes int64
    // Telemetry selects this pool's telemetry; nil uses the process-wide installation.
    Telemetry *TelemetryComponents
}
```

### 6.3 Session lifecycle, abort, run handle (rows 2, 3)

#### `session.go` — UPDATED

```go
type Session struct {
    pool       *Pool
    co         *pool.Checkout
    mu         sync.Mutex
    closed     bool
    driven     bool
    broken     error
    store      *instanceStore
    scriptName string
    life       lifecycle
    limits     sessionLimits   // MaxHostObjects, MaxPendingFutures, InterruptGrace
}

// lifecycle is the state Interrupt, CloseNow, Done and Err share without taking s.mu.
type lifecycle struct {
    mu        sync.Mutex
    done      chan struct{}
    err       error                 // terminal error, set once
    interrupt error                 // pending interrupt reason, nil when none
    cancelCb  context.CancelFunc    // cancels the running host callback, nil when none
    inFeed    bool                  // a FeedRun/ResumeAuto turn is in flight
    closing   bool
}

// Interrupt stops the running feed. While the worker waits on a host call, the call's context is
// cancelled and the feed ends with a KeyboardInterrupt (or reason) inside the sandbox; the session
// stays usable. While Python is executing, the worker is killed after InterruptGrace and the session
// is lost. Safe to call from any goroutine; nil when no feed is running.
func (s *Session) Interrupt(ctx context.Context, reason error) error

// CloseNow ends the session at once: a running feed returns ErrSessionClosed and the worker is killed.
func (s *Session) CloseNow() error

// Done is closed when the session is closed, lost or its worker ended.
func (s *Session) Done() <-chan struct{}

// Err reports why the session is unusable; nil while usable.
func (s *Session) Err() error

// Stats reports host-side counters of the session.
func (s *Session) Stats() SessionStats

type SessionStats struct {
    HostObjects     int
    PeakHostObjects int
    PendingFutures  int
    Suspensions     uint64
}

func (s *Session) poison(err error) error          // UPDATED: also life.finish(err)
func (s *Session) Close(ctx context.Context) error // UPDATED: life.finish(ErrSessionClosed) after Finish
func (l *lifecycle) finish(err error)              // sets err once, closes done
func (l *lifecycle) beginCallback(cancel context.CancelFunc) (interrupted error)
func (l *lifecycle) endCallback()
```

`Session.mapError` maps `pool.KindDisconnected` with the close fields, and every poisoning branch calls `life.finish`.

#### `run.go` — CREATED

```go
// Run is a feed started by Session.Go.
type Run struct {
    s      *Session
    done   chan struct{}
    value  any
    err    error
}

// Go runs FeedRun on its own goroutine.
func (s *Session) Go(ctx context.Context, code string, opts *FeedOptions) *Run
func (r *Run) Done() <-chan struct{}
func (r *Run) Wait() (any, error)                                  // blocks until the feed ends
func (r *Run) Interrupt(ctx context.Context, reason error) error   // Session.Interrupt
```

#### `answer.go` — UPDATED

```go
type answerer struct {
    s       *Session
    lookup  map[string]any
    host    *Host                 // CheckoutOptions.Host, may be nil
    os      OSHandler
    pt      *printTarget
    futures map[uint32]*Future
}

// callHost runs fn under a cancellable callback context registered with the session lifecycle.
// A cancelled context after the call returns is answered with AbortFeed instead of a resume.
func (a *answerer) callHost(ctx, cbCtx context.Context, run func(cb context.Context) (any, error)) (result any, aborted *wire.Event, err error)
func (a *answerer) abort(ctx context.Context, reason error) (*wire.Event, error)   // Checkout.Abort with exceptionParts(reason)
func (a *answerer) lookupEntry(name string) (any, bool)                            // ExternalLookup, then Host
func asFunction(entry any) (Function, error)                                       // UPDATED: returns the Func error
func (a *answerer) registerFuture(callID uint32, f *Future) error                  // MaxPendingFutures
func (a *answerer) settleAll(err error)                                            // on session end
```

`answerFunctionCall`, `answerMethodCall`, `answerOsCall`, `answerObjectLookup` and `answerResolveFutures` call `callHost`; `answerResolveFutures` treats a cancelled wait the same way (abort instead of `hostFailure`).

#### `internal/pool/checkout.go` — UPDATED

```go
// Abort ends the pending suspension with exc; the worker MUST answer Error, returned as a KindRuntime error.
// Valid while a suspension is pending and no turn is in flight.
func (c *Checkout) Abort(ctx context.Context, exc *wire.Exception, onPrint OnPrint) error

// Pending reports whether a suspension awaits an answer.
func (c *Checkout) Pending() bool

// Worker exposes the transport's Done channel and Err for lifecycle signalling.
func (c *Checkout) Done() <-chan struct{}
func (c *Checkout) WorkerErr() error
```

`Abort` reuses `turn(ctx, wire.AbortFeed{...}, false, onPrint)` with `abortFlight = true` so `dispatch`'s existing rule ("answered AbortFeed with something other than Error" is a protocol violation) applies. The abort context is `context.WithoutCancel(ctx)` bounded by `min(RequestTimeout, 5s)`.

#### `snapshot.go` — UPDATED

`ResumeAuto`, `Resume`, `ResumeError` and friends check `s.life.interrupt` first: when set and a suspension is pending, they call `Abort` and return its error.

### 6.4 Pool accounting and shutdown (rows 4, 16)

#### `internal/pool/pool.go` — UPDATED

```go
type Config struct {
    ... existing ...
    MaxPendingBytes int64
    Recorder        *telemetry.Recorder   // may be nil
    // OnShutdown is called by Shutdown once with the sessions still checked out.
    OnShutdown func(force func())
}

type Pool struct {
    cfg      Config
    mu       sync.Mutex
    idle     []*slot
    starting int
    active   int
    retiring int
    notify   chan struct{}
    closed   bool
    reaper   chan *slot          // buffered MaxProcesses
    reaperWG sync.WaitGroup
    checkouts map[*Checkout]struct{}
}

type Stats struct{ Starting, Active, Idle, Retiring int }

func New(ctx context.Context, cfg Config) (*Pool, error)     // starts min(MaxProcesses, 8) reaper goroutines
func (p *Pool) Stats() Stats
func (p *Pool) Size() (live, idle int)                        // live = starting+active+idle+retiring (kept for compatibility)
func (p *Pool) live() int                                     // locked helper
func (p *Pool) acquire(ctx context.Context) (*slot, error)    // bound on live()
func (p *Pool) release(s *slot)                               // idle or retiring
func (p *Pool) discard(s *slot, reason string)                // Kill, retiring
func (p *Pool) reap()                                         // goroutine: shutdownWorker or Wait, then retiring--
func (p *Pool) Close(ctx context.Context) error               // unchanged semantics
func (p *Pool) Shutdown(ctx context.Context) error            // CREATED
func (p *Pool) track(c *Checkout) / untrack(c *Checkout)
```

#### `pool.go` (root) — UPDATED

```go
// Shutdown closes the pool and waits for every session. When ctx ends first, open sessions are closed
// with CloseNow and the wait continues for the workers to exit.
func (p *Pool) Shutdown(ctx context.Context) error

// Stats reports the pool's worker counts.
func (p *Pool) Stats() PoolStats
type PoolStats struct{ Starting, Active, Idle, Retiring int }
```

The root pool keeps `sessions map[*Session]struct{}` under a mutex to implement `OnShutdown`.

### 6.5 Lost sessions (row 5)

#### `internal/worker/websocket.go` — UPDATED

```go
type wsWorker struct {
    ... existing ...
    closeMu     sync.Mutex
    closeCode   int      // 0 when no close frame was seen
    closeReason string
    err         error
}

func (w *wsWorker) read()                       // stores CloseError before drop; err = &CloseErrorInfo{...}
func (w *wsWorker) Send(ctx, payload) error     // returns ErrWorkerGone wrapping w.err when done is closed
func (w *wsWorker) Done() <-chan struct{}
func (w *wsWorker) Err() error

// ClosedError reports a WebSocket close frame received from the peer.
type ClosedError struct{ Code int; Reason string }
func (e *ClosedError) Error() string             // "closed by server (1008): idle timeout of 60s exceeded"
```

#### `internal/pool/errors.go` — UPDATED

`Error` gains `CloseCode int`, `CloseReason string`; `Checkout.poison` fills them from `slot.w.Err()` for `KindDisconnected`; `Error()` for `KindDisconnected` renders `monty worker connection closed while <doing>` and, when a close frame was seen, `: closed by server (<code>): <reason>`.

#### `errors.go` — UPDATED

```go
// ErrSessionLost matches every error that leaves a session unusable.
var ErrSessionLost = errors.New("monty: session lost")

type DisconnectError struct {
    Message string
    // Code and Reason carry the server's close frame; Code is 0 when the connection dropped without one.
    Code   int
    Reason string
}
func (e *DisconnectError) Is(target error) bool   // ErrSessionLost
func (e *CrashedError) Is(target error) bool      // ErrSessionLost
func (e *ShutdownError) Is(target error) bool     // ErrSessionLost
func (e *ProtocolError) Is(target error) bool     // ErrSessionLost, plus existing cause matching
var errSessionClosed = &sessionClosedError{}      // ErrSessionClosed keeps its identity and text; Is(ErrSessionLost) true

// KnownExceptionNames lists the Python exception types host errors may raise by name.
func KnownExceptionNames() []string
var knownExceptions map[string]struct{}           // private, built in init; PythonExceptionNames removed
```

`ErrSessionClosed` becomes a typed error value with the same message so it can match `ErrSessionLost`; `errors.Is(err, ErrSessionClosed)` keeps working through identity.

### 6.6 Limits and store (rows 6, 8, 15)

#### `options.go` — UPDATED

```go
// Unlimited disables a limit that accepts it.
const Unlimited uint64 = math.MaxUint64
// UnlimitedDuration disables MaxDuration.
const UnlimitedDuration time.Duration = math.MaxInt64

type ResourceLimits struct {
    // MaxDuration bounds sandbox execution per session; 0 means no limit, UnlimitedDuration is explicit.
    MaxDuration time.Duration
    // MaxMemory bounds allocator bytes; 0 means the worker default, Unlimited disables.
    MaxMemory uint64
    GCInterval uint64
    // MaxRecursionDepth: 0 means 1000; Unlimited is rejected.
    MaxRecursionDepth uint64
    // MaxSuspensions bounds host round trips per session, reset by LoadSession and LoadSnapshot:
    // 0 means 1000, Unlimited disables.
    MaxSuspensions uint64
}

type CheckoutOptions struct {
    ... existing ...
    Host              *Host
    MaxHostObjects    uint64
    MaxPendingFutures uint64
    InterruptGrace    time.Duration
}

func (o CheckoutOptions) configure() (wire.Configure, error)   // UPDATED for Unlimited
func (o CheckoutOptions) sessionLimits() (sessionLimits, error)
```

#### `internal/pool/budget.go` — UPDATED

`suspensionLimit == math.MaxUint64` is never exceeded; `observe` keeps the min rule (a smaller reported limit still tightens).

#### `classinstance.go` — UPDATED

```go
// All exposes every public name.
func All() AttrPolicy
// Expose exposes exactly the methods of interface T, under their sandbox names. T MUST be an interface type.
func Expose[T any]() AttrPolicy

type instanceStore struct {
    mu    sync.Mutex
    m     map[string]wrapper
    limit uint64
    peak  int
}
func newInstanceStore(limit uint64) *instanceStore
func (s *instanceStore) put(w wrapper, onlyIfAbsent bool) error   // *ResourceError when a new id exceeds limit
func (s *instanceStore) stats() (count, peak int)

// ResourceError reports a host-side limit reached by the sandbox.
type ResourceError struct{ Resource string; Limit uint64 }
func (e *ResourceError) Error() string      // "host object limit 10000 exceeded"
func (e *ResourceError) Exception() ExceptionInfo   // RuntimeError
```

`ResourceError` implements `Error`, so `exceptionParts` raises it as `RuntimeError` inside the sandbox.

#### `future.go` — UPDATED

```go
// AsyncContext runs fn with a context that ends when the feed is cancelled, interrupted or the session ends.
func AsyncContext(ctx context.Context, fn func(context.Context) (any, error)) *Future
// Async runs fn without a context; it cannot be cancelled.
func Async(fn func() (any, error)) *Future
```

The answerer wraps a `*Future` returned from a host function unchanged; `AsyncContext` is a convenience that derives from the callback context the host function received.

### 6.7 Host registry, exposure, structs (rows 7, 12, 13, 18)

#### `host.go` — CREATED

```go
// Host is a validated set of functions and objects a session exposes to the sandbox.
type Host struct {
    mu      sync.Mutex
    funcs   map[string]hostFunc      // name → Function + signature for stubs
    objects map[string]*ClassInstance
}

type hostFunc struct {
    fn  Function
    sig reflect.Type   // nil for a Function value
}

func NewHost() *Host
// Func registers fn under name; the signature is validated now.
func (h *Host) Func(name string, fn any) error
// Object registers a host object; opts.ID MUST be set for restore across sessions (see Restorable).
func (h *Host) Object(name string, v any, opts ClassInstanceOptions) error
func (h *Host) Names() []string
// Stubs renders Python stub declarations for TypeCheckStubs.
func (h *Host) Stubs() string
// Restorable reports the first object without a pinned ID, or nil.
func (h *Host) Restorable() error
func (h *Host) lookup() map[string]any             // merged view for the answerer
func (h *Host) register(store *instanceStore) error // put every object at checkout / before Load
```

#### `session.go` — UPDATED (registry)

`Pool.Checkout` calls `opts.Host.register(s.store)` after creating the session; `LoadSession` and `LoadSnapshot` accept the same host through `CheckoutOptions.Host` (already registered) and `LoadSnapshotOptions.ExternalLookup` still overrides names.

#### `convert.go` — UPDATED

```go
// AsNamedTuple converts a struct to a named tuple using exported fields and `monty` tags.
func AsNamedTuple(v any) (NamedTuple, error)
// NewNamedTuple builds a named tuple from pairs, rejecting duplicate or empty names.
func NewNamedTuple(typeName string, pairs ...Pair) (NamedTuple, error)
type Pair struct{ Name string; Value any }
```

`walk` keeps the wrap hint for untagged structs.

#### `examples/**`, `README.md`, `example_test.go` — UPDATED

Every `monty.All()` becomes `monty.All()`; instances that expose methods use `monty.Names(...)` or `monty.Expose[Iface]()`.

### 6.8 Print (row 14)

#### `print.go` — UPDATED

```go
// FlushingPrintTarget is a PrintTarget that buffers; Flush is called when a turn ends.
type FlushingPrintTarget interface {
    PrintTarget
    Flush() error
}

// Lines delivers complete lines per stream and flushes the remainder at turn end.
func Lines(fn func(stream Stream, line string) error) PrintTarget
type lineTarget struct{ mu sync.Mutex; fn func(Stream, string) error; partial map[Stream]string }
func (t *lineTarget) Print(stream Stream, text string) error
func (t *lineTarget) Flush() error
```

#### `session.go` — UPDATED (`printTarget.finish`)

```go
func (p *printTarget) finish() error   // calls Flush on a FlushingPrintTarget; the error joins pt.failure
```

`Session.drive` and `snapshotDriver.advance` call `finish` on every turn-ending event.

### 6.9 Telemetry (row 17)

#### `telemetry.go` — UPDATED

```go
type Options struct { ... Telemetry *TelemetryComponents }
type WebSocketOptions struct { ... Telemetry *TelemetryComponents }
func resolveRecorder(c *TelemetryComponents) *telemetry.Recorder   // per-pool recorder or the installed one
```

#### `internal/telemetry/recorder.go` — UPDATED

```go
func NewRecorder(c Components) *Recorder          // CREATED
func Install(newOwner any, c Components, replace bool) bool
func Uninstall(o any)
func Installed() bool
func Global() *Recorder                           // renamed from Current; nil when none
```

#### `internal/telemetry/checkout.go`, `telemetry_hooks.go` — UPDATED

`NewCheckout(rec *Recorder, parent context.Context, pid int, hasPID, metered bool)`; `currentMetrics(rec)`, `traceContextHeaders(rec, ctx)`, `(*Pool).observe` read `p.inner.cfg.Recorder`.

### 6.10 Server info (row 11)

#### `server/src/http.rs` — UPDATED

```rust
.route("/info", get(info))
async fn info(State(state): State<AppState>) -> Response   // Json(ServerInfo)
```

#### `server/src/info.rs` — CREATED

```rust
#[derive(serde::Serialize)]
pub struct ServerInfo { pub version: &'static str, pub monty_rev: &'static str, pub protocol_version: u32, pub limits: Limits }
#[derive(serde::Serialize)]
pub struct Limits { idle_timeout_s: u64, keepalive_s: u64, session_timeout_s: u64, turn_timeout_s: u64,
                    max_duration_s: u64, max_memory_bytes: u64, max_recursion_depth: u64,
                    max_sessions: usize, max_sessions_per_client: usize }
impl ServerInfo { pub fn from_config(config: &Config) -> Self }
```

`server/Cargo.toml` adds `serde = { version = "1", features = ["derive"] }`, `serde_json = "1"`, and `axum` feature `json`.

#### `websocket.go` (root) — UPDATED

```go
type ServerInfo struct {
    Version         string
    MontyRev        string
    ProtocolVersion uint32
    Limits          ServerLimits
}
type ServerLimits struct {
    IdleTimeout, Keepalive, SessionTimeout, TurnTimeout, MaxDuration time.Duration   // 0 = disabled
    MaxMemory         uint64
    MaxRecursionDepth uint64
    MaxSessions, MaxSessionsPerClient int
}
// FetchServerInfo reads GET /info of the server behind opts.URL over the same transport as the dial.
func FetchServerInfo(ctx context.Context, opts WebSocketOptions) (*ServerInfo, error)
```

`internal/worker/websocket.go`: `func (d *WebSocketDialer) GetJSON(ctx context.Context, path string, headers [][2]string, out any) error` shared by `HealthCheck`.

### 6.11 Codec evidence (row 19)

#### `internal/wire/codec_bench_test.go` — CREATED

`BenchmarkDecodeEventPrint`, `BenchmarkDecodeEventDeepValue`, `BenchmarkDecodeEventComplete`, `BenchmarkEncodeRequestFeed16MiB`, each with a `montypb` counterpart (`proto.Unmarshal` / `proto.Marshal`) under sub-benchmarks `hand` and `generated`.

#### `internal/wire/codec_fuzz_test.go` — CREATED

`FuzzDecodeEvent`, `FuzzDecodeRequest`: seeds from the differential corpus, inputs capped at 1 MiB, invariants: no panic, an accepted event re-encodes and decodes to an equal value, a `montypb`-rejected input is rejected.

#### `internal/wire/fields_test.go` — CREATED

Parses `proto/monty/v1/monty.proto` messages and field numbers and asserts each `(message, number)` appears in a table exported from the codec (`fieldTable` in `codec.go`, generated by hand and checked by this test).

### 6.12 Release (rows 10, 20, 21)

#### `.github/workflows/ci.yml` — UPDATED

- `test` job: step `make check-pins`; Linux matrix leg adds `go test -race -count=1 -timeout 40m ./...`; step `govulncheck ./...` (`golang/govulncheck-action@v1`); step `make fuzz`.
- `docker` job: `cargo install cargo-audit --locked` + `cargo audit` in `server/`; `make docker-build ... VERSION=$(scripts/version.sh)`.
- `release` job (on tags `v*`): `make docker-push REGISTRY=ghcr.io/${{ github.repository_owner }}` with `--sbom --provenance`; requires `packages: write`.

#### `docker/Dockerfile` — UPDATED (licences)

Build stage: `cargo install cargo-about --locked --version <pin>`, `cargo about generate -o /out/THIRD_PARTY_RUST.html about.hbs` in `server/`; final stage copies it to `/usr/share/licenses/monty-server/THIRD_PARTY_RUST.html`. `server/about.toml` and `server/about.hbs` are CREATED.

#### `montypb/generate.go` — UPDATED

Package doc: "Generated wire types. They carry no stability promise beyond `monty.ProtocolVersion`; consumers speaking the raw protocol MAY import them and MUST expect regeneration on every protocol change."

#### `changelogs/v0.1.0.md` — CREATED

Structure of `changelogs/v0.0.23.md`: baseline, added, changed (breaking renames), parity, tests, limitations, build notes.

### 6.13 Documentation

| File | Change |
|---|---|
| `docs/architecture/versioning.md` | CREATED: rule, script, `BindingVersion`, server stamping, image tags, pins, `check-pins` |
| `docs/architecture/session.md` | UPDATED: lifecycle state, `Interrupt`, `CloseNow`, `Done`/`Err`, `Go`, abort on cancel, host registry, limits, `Lines`, futures |
| `docs/architecture/pool.md` | UPDATED: counters, reaper, `Shutdown`, frame bound, retirement latency |
| `docs/architecture/websocket.md` | UPDATED: close reason, `Done`, `FetchServerInfo` |
| `docs/architecture/telemetry.md` | UPDATED: per-pool recorder, new metrics |
| `docs/architecture/server.md` | UPDATED: `/info`, version stamping |
| `docs/architecture/docker.md` | UPDATED: `MONTY_SERVER_VERSION`, tags, SBOM, licences |
| `docs/architecture/overview.md` | UPDATED: version pins paragraph, key technologies |
| `docs/parity/api.md` | UPDATED: every new and renamed identifier |
| `docs/parity/tests.md` | UPDATED: cancel deviation, `DisconnectError` text, new tests |
| `docs/parity/server.md` | UPDATED: `/info` |
| `README.md` | UPDATED: versions, `Interrupt`/`Go`, `Host`, `Expose`, `Unlimited`, `Lines`, `Shutdown`, telemetry per pool, long-running recipe, `MaxSuspensions` per session |
| `CLAUDE.md` | UPDATED: commands (`make version`, `check-pins`, `fuzz`), conventions (versions come from `scripts/version.sh`; pins from `proto/PROTO_REV`), upgrade checklist (`check-pins` is the authority) |

---

## 7. Data flows

### 7.1 Version at build time

1. `make <target>` evaluates `VERSION := $(shell scripts/version.sh)`.
2. Go targets pass `-ldflags "-X github.com/asalimonov/montygo.buildVersion=$(VERSION)"`; `BindingVersion()` returns it.
3. `docker-build` passes `--build-arg MONTY_SERVER_VERSION=$(VERSION)`; `build.rs` emits `rustc-env`; `SERVER_VERSION` carries it into `/`, `/info` and `monty_server_build_info`.
4. The image is tagged `monty-server:$(VERSION)` and `:latest`.

| Failure | Result |
|---|---|
| not a git repository | script prints `0.0.0-unknown` and warns; build continues |
| `git` missing | same |
| tag with a non-semver name (`release-1`) | ignored by the `v*` filter |
| dirty tree in CI | `-dirty` appears; CI fails `check-pins`? No: `check-pins` checks pins, not the tag; the image tag carries `-dirty`, which the release job rejects (`test -z "$(git status --porcelain)"`) |

### 7.2 Version at run time in a consumer

1. `BindingVersion()` reads `buildVersion`; empty in a consumer.
2. `debug.ReadBuildInfo()`; find `Deps[i].Path == "github.com/asalimonov/montygo"`; if `Replace != nil` and version is the zero pseudo-version → `(devel)`; else strip `v`.
3. Not found (montygo is the main module) → `bi.Main.Version` if not `(devel)`, else `0.0.0-unknown`.

### 7.3 Feed with cancel during a host call (row 3)

1. `FeedRun` sends `Feed`; the worker suspends with `FunctionCall`.
2. `answerer.callHost` creates `cb, cancel := context.WithCancel(cbCtx)`, calls `life.beginCallback(cancel)`; if `life.interrupt` is already set, skips the call and aborts.
3. The host function runs; the caller cancels `ctx` → `cbCtx` (derived from `ctx`) is done.
4. The function returns (with `ctx.Err()` or a value). `callHost` sees `cb.Err() != nil` → `abort(ctx, KeyboardInterrupt)`.
5. `Checkout.Abort` sends `AbortFeed` under `WithoutCancel(ctx)` + 5 s; the worker replies `Error`; `dispatch` returns `KindRuntime`.
6. `drive` maps it to `*RuntimeError{TypeName: "KeyboardInterrupt"}`; `printTarget.finish` flushes; the session is usable.

| Failure | Result |
|---|---|
| worker answers the abort with a suspension | protocol violation, worker discarded, session lost (existing rule) |
| abort send fails (connection gone) | `DisconnectError`, session lost |
| abort turn exceeds 5 s | `CrashedError{TimedOut}`, session lost |
| host function ignores the context and never returns | `FeedRun` blocks; `Interrupt` kills after grace; the function's eventual return finds the worker gone |

### 7.4 `Interrupt` while Python executes

1. `Interrupt` sets `life.interrupt = reason`; no `cancelCb` registered.
2. Waits up to `InterruptGrace` for `beginCallback` (which aborts at once) or turn end.
3. Grace expires → `co.Abandon()` kills the worker; the in-flight `Recv` fails; `FeedRun` returns `CrashedError`/`DisconnectError`; `life.finish(err)`.
4. `Interrupt` returns nil once the feed has ended.

### 7.5 `CloseNow`

1. `life.closing = true`; `life.finish(ErrSessionClosed)`; `co.Abandon()`.
2. A running `FeedRun` fails on its next I/O; `drive` sees `life.closing` and returns `ErrSessionClosed` rather than the transport error.
3. `Close` afterwards is a no-op.

### 7.6 Frame backpressure (row 1)

1. The worker prints faster than `PrintTarget` consumes; `pumpFrames` pushes frames; `bytes` grows.
2. At `bytes + len(frame) > maxBytes`, `push` waits on `space`.
3. The OS pipe (native) or `io.Pipe` (wasm) fills; the worker blocks in its stdout write.
4. `pop` frees bytes and closes `space`; `push` continues.
5. `RequestTimeout` or `Kill` → `queue.close()` → `push` returns `ErrWorkerGone` → the pump exits.

| Failure | Result |
|---|---|
| one frame larger than the bound | accepted (the frame cap is 256 MiB); the next push blocks |
| consumer never pops and no timeout | worker stays blocked; documented — set `RequestTimeout` |

### 7.7 Pool retirement (row 4)

1. `release`: worker not reusable → `retiring++`, `active--`, slot sent to `reaper` (never blocks: capacity `MaxProcesses`).
2. `reap`: `shutdownWorker` (graceful for release) or `Wait` after `Kill` (discard); then `retiring--`, `wakeLocked`.
3. `acquire` computes `live()`; a waiter proceeds only when `live() < MaxProcesses`.

### 7.8 `Pool.Shutdown`

1. `closed = true`; waiters fail with `KindClosed`; idle workers are retired as in `Close`.
2. Wait for `active+retiring == 0` or `ctx.Done()`.
3. On `ctx.Done()`: `OnShutdown(force)` → root pool calls `CloseNow` on every session; wait again with a fixed 5 s bound; return `ctx.Err()` if still not zero.

### 7.9 WebSocket close reason (row 5)

1. Server closes with `1008 idle timeout of 60s exceeded`.
2. `read()` gets `websocket.CloseError`; stores code and reason; `drop()`; `close(done)`.
3. Next `Send`: `done` closed → `ErrWorkerGone` wrapping `ClosedError`.
4. `Checkout.poison("sending a request")` → `pool.Error{KindDisconnected, CloseCode: 1008, CloseReason}`.
5. `mapError` → `DisconnectError{Code: 1008, Reason}`; `life.finish`; `Session.Done()` closes; `errors.Is(err, ErrSessionLost)`.

Also without any call: `Session.Done()` is driven by `co.Done()` (the worker's `done`) through a goroutine started at checkout that calls `life.finish(&DisconnectError{...})` when the worker ends while the session is idle.

### 7.10 Host registry and restore (rows 12, 18)

1. `host.Object("records", table, ClassInstanceOptions{ID: fixedUUID, AllowedMethods: Expose[RecordsAPI]()})` validates at registration.
2. `Checkout` → `host.register(store)` puts every object under its ID.
3. `LoadSession(dump)`: the dump references `fixedUUID`; `answerMethodCall` finds it in the store; the call runs.
4. `Host.Restorable()` returns an error naming the first object without an ID; `LoadSession` with such a host returns that error before sending `Load`.

### 7.11 Telemetry resolution (row 17)

1. `New`: `rec := resolveRecorder(opts.Telemetry)`; nil `Telemetry` → `telemetry.Global()` (may be nil).
2. `pool.Config.Recorder = rec`; `metered = rec != nil && rec.Metering()`.
3. `observe` → `telemetry.NewCheckout(rec, ...)`; nil recorder → no observer.
4. `Instrument` after creation does not affect the pool.

### 7.12 Failure-mode summary

| Component | Failure | Detection | Effect |
|---|---|---|---|
| version script | no git | exit 0 with fallback | `0.0.0-unknown`, warning |
| check-pins | drift | grep mismatch | make/CI fails naming the file |
| frame queue | flood | `bytes` bound | worker blocks; deadline kills |
| abort | worker misbehaves | protocol rule | session lost |
| interrupt | callback ignores ctx | grace expiry | worker killed; `FeedRun` returns when the callback returns |
| reaper | worker never exits | `Wait` with 1 s after `Kill` | slot freed after kill |
| Shutdown | sessions never close | ctx | `CloseNow` all; returns `ctx.Err()` |
| store | too many objects | `put` | `RuntimeError` in the sandbox, session usable |
| futures | too many pending | `registerFuture` | `RuntimeError` in the sandbox |
| `/info` | old server | 404 | `FetchServerInfo` returns `ErrNoServerInfo` |
| fuzz | crash | CI | fails |

---

## 8. Pseudo-code

### 8.1 `scripts/version.sh` main

```bash
main() {
  git rev-parse --git-dir >/dev/null 2>&1 || { echo "version.sh: not a git repository" >&2; echo "0.0.0-unknown"; return 0; }
  hash=$(git rev-parse --short HEAD)
  at_head=$(pick_highest_at_head)              # loose semver from tags pointing at HEAD
  if [ -n "$at_head" ]; then
    version="$at_head"
  else
    base=$(pick_highest_reachable)             # loose semver from tags merged into HEAD
    if [ -n "$base" ]; then version="$base-$hash"
    else echo "WARNING: no v* tag reachable; using 0.0.0-$hash" >&2; version="0.0.0-$hash"; fi
  fi
  is_dirty && version="$version-dirty"
  printf '%s\n' "$version"
}
```

### 8.2 `BindingVersion`

```go
func BindingVersion() string {
    if buildVersion != "" {
        return buildVersion
    }
    if v, ok := moduleVersion(); ok {
        return v
    }
    return unknownVersion
}

func moduleVersion() (string, bool) {
    bi, ok := debug.ReadBuildInfo()
    if !ok {
        return "", false
    }
    const path = "github.com/asalimonov/montygo"
    for _, dep := range bi.Deps {
        if dep.Path != path {
            continue
        }
        if dep.Replace != nil && strings.HasPrefix(dep.Version, "v0.0.0-00010101000000") {
            return "(devel)", true
        }
        if dep.Replace != nil && dep.Replace.Version != "" {
            return strings.TrimPrefix(dep.Replace.Version, "v"), true
        }
        return strings.TrimPrefix(dep.Version, "v"), true
    }
    if bi.Main.Path == path && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
        return strings.TrimPrefix(bi.Main.Version, "v"), true
    }
    return "", false
}
```

### 8.3 `frameQueue.push`

```go
func (q *frameQueue) push(frame []byte) error {
    n := int64(len(frame))
    q.mu.Lock()
    for q.maxBytes > 0 && q.bytes > 0 && q.bytes+n > q.maxBytes {   // a single oversize frame passes when the queue is empty
        wait := q.space
        q.mu.Unlock()
        select {
        case <-wait:
        case <-q.closed:
            return ErrWorkerGone
        }
        q.mu.Lock()
    }
    select {
    case <-q.closed:
        q.mu.Unlock()
        return ErrWorkerGone
    default:
    }
    q.frames = append(q.frames, frame)
    q.bytes += n
    if q.observe != nil { q.observe(n) }
    close(q.notify); q.notify = make(chan struct{})
    q.mu.Unlock()
    return nil
}

func (q *frameQueue) pop(ctx context.Context) ([]byte, error) {
    for {
        q.mu.Lock()
        if len(q.frames) > 0 {
            f := q.frames[0]; q.frames[0] = nil; q.frames = q.frames[1:]
            q.bytes -= int64(len(f))
            if len(q.frames) == 0 { q.frames = nil }
            if q.observe != nil { q.observe(-int64(len(f))) }
            close(q.space); q.space = make(chan struct{})
            q.mu.Unlock()
            return f, nil
        }
        if q.err != nil { err := q.err; q.mu.Unlock(); return nil, err }
        ch := q.notify
        q.mu.Unlock()
        select { case <-ch: case <-ctx.Done(): return nil, ctx.Err() }
    }
}
```

### 8.4 `answerer.callHost`

```go
func (a *answerer) callHost(ctx, cbCtx context.Context, run func(context.Context) (any, error)) (any, *wire.Event, error) {
    cb, cancel := context.WithCancel(cbCtx)
    defer cancel()
    if reason := a.s.life.beginCallback(cancel); reason != nil {   // Interrupt arrived before the call
        ev, err := a.abort(ctx, reason)
        return nil, ev, err
    }
    result, err := run(cb)
    a.s.life.endCallback()
    if cb.Err() != nil {                                             // feed ctx cancelled or Interrupt
        reason := a.s.life.takeInterrupt()
        if reason == nil { reason = keyboardInterrupt }
        ev, aerr := a.abort(ctx, reason)
        return nil, ev, aerr
    }
    return result, nil, err
}

func (a *answerer) abort(ctx context.Context, reason error) (*wire.Event, error) {
    excType, msg := exceptionParts(reason)
    actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortDeadline(a.s.pool))
    defer cancel()
    err := a.s.co.Abort(actx, wire.NewException(excType, msg), a.pt.onPrint)
    return nil, err   // KindRuntime → *RuntimeError by drive
}
```

`answerFunctionCall` becomes:

```go
fn, err := asFunction(entry)
if err != nil { return a.resumeError(ctx, "TypeError", fc.FunctionName+": "+err.Error()) }
result, aborted, err := a.callHost(ctx, cbCtx, func(cb context.Context) (any, error) {
    return safeCall(cb, fn, a.restoreArgs(fc.Args), kwargsRecord(fc.Kwargs, a.s.store))
})
if aborted != nil || errors.Is(err, errAborted) { return aborted, err }
if err != nil { excType, msg := exceptionParts(err); return a.resumeError(ctx, excType, msg) }
return a.resumeOutcome(ctx, fc.CallID, fc.AllowEagerAwait, result)
```

### 8.5 `Checkout.Abort`

```go
func (c *Checkout) Abort(ctx context.Context, exc *wire.Exception, onPrint OnPrint) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.pending.kind == pendingNone { return protocolError("abort without a pending suspension") }
    if c.inFlight { return protocolError("abort while a turn is in flight") }
    c.abortFlight = true
    c.pending = pending{}
    _, err := c.turn(ctx, wire.AbortFeed{Exception: exc}, false, onPrint)   // dispatch: Error → KindRuntime, else protocol violation
    return err
}
```

### 8.6 `Session.Interrupt`

```go
func (s *Session) Interrupt(ctx context.Context, reason error) error {
    if reason == nil { reason = keyboardInterrupt }
    l := &s.life
    l.mu.Lock()
    if !l.inFeed && !s.pendingSnapshot() { l.mu.Unlock(); return nil }
    l.interrupt = reason
    cancel := l.cancelCb
    inFeed := l.inFeed
    l.mu.Unlock()
    if !inFeed {                          // suspended snapshot between steps
        s.mu.Lock(); defer s.mu.Unlock()
        return s.mapError(s.co.Abort(bounded(ctx), exception(reason), nil))
    }
    if cancel != nil { cancel(); return s.waitFeedEnd(ctx) }
    select {
    case <-s.feedEnded(): return nil
    case <-time.After(s.limits.interruptGrace):
    case <-ctx.Done(): return ctx.Err()
    }
    l.mu.Lock(); cancel = l.cancelCb; l.mu.Unlock()
    if cancel != nil { cancel(); return s.waitFeedEnd(ctx) }   // a suspension arrived during the grace
    s.co.Abandon()
    return s.waitFeedEnd(ctx)
}
```

### 8.7 `Session.CloseNow`

```go
func (s *Session) CloseNow() error {
    l := &s.life
    l.mu.Lock()
    if l.closing { l.mu.Unlock(); return nil }
    l.closing = true
    cancel := l.cancelCb
    l.mu.Unlock()
    l.finish(ErrSessionClosed)
    if cancel != nil { cancel() }
    s.co.Abandon()          // kills the worker; a running FeedRun fails and returns ErrSessionClosed
    s.pool.untrack(s)
    return nil
}
```

`drive` and `advance` check `s.life.closing` before mapping a transport error and return `ErrSessionClosed` instead.

### 8.8 `Pool.acquire`, `release`, `reap`

```go
func (p *Pool) live() int { return p.starting + p.active + len(p.idle) + p.retiring }

acquire:
    for {
        lock
        if closed → KindClosed
        pop idle alive → active++; return
        if live() < MaxProcesses { starting++; unlock; spawn; lock; starting--; on error wake+return; active++; unlock; return }
        wait notify / ctx / timer
    }

release(s):
    lock
    if reusable → idle = append(idle, s); active--; WorkersIdle(+1)
    else → active--; retiring++; WorkersRetiring(+1); reaper <- s (never blocks: cap MaxProcesses)
    wake; unlock; obs handling as today

discard(s, reason):
    s.w.Kill()
    lock; active--; retiring++; WorkerTerminated(reason); reaper <- s; wake; unlock; obs.close()

reap():
    for s := range p.reaper {
        if killed { s.w.Wait(1s) } else { shutdownWorker(s.w, s.obs) }
        lock; retiring--; WorkersRetiring(-1); wake; unlock
    }
```

### 8.9 `Pool.Shutdown`

```go
func (p *Pool) Shutdown(ctx context.Context) error {
    _ = p.Close(ctx)                                   // idle workers, waiters
    if p.waitDrained(ctx) == nil { return nil }
    if p.cfg.OnShutdown != nil { p.cfg.OnShutdown(func() { /* root: CloseNow every session */ }) }
    fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := p.waitDrained(fctx); err != nil { return ctx.Err() }
    return ctx.Err()
}
```

### 8.10 `wsWorker.read` and `Send`

```go
func (w *wsWorker) read() {
    defer close(w.done)
    for {
        typ, data, err := w.conn.Read(context.Background())
        if err != nil || typ != websocket.MessageBinary {
            var ce websocket.CloseError
            w.closeMu.Lock()
            switch {
            case errors.As(err, &ce): w.err = &ClosedError{Code: int(ce.Code), Reason: ce.Reason}
            case err != nil: w.err = err
            default: w.err = errors.New("non-binary message")
            }
            w.closeMu.Unlock()
            w.drop()
            return
        }
        select { case w.frames <- data: case <-w.stop: return }
    }
}

func (w *wsWorker) Send(ctx context.Context, payload []byte) error {
    if len(payload) > wire.MaxFrameLen { return &wire.FrameTooLargeError{...} }
    select {
    case <-w.done: return fmt.Errorf("%w: %w", ErrWorkerGone, w.Err())
    default:
    }
    if w.stopped() { return ErrWorkerGone }
    err := w.conn.Write(ctx, websocket.MessageBinary, payload)
    if err != nil && ctx.Err() != nil { return ctx.Err() }
    return err
}
```

### 8.11 `configure` limits

```go
case l.MaxRecursionDepth == Unlimited: return cfg, &OptionError{Message: "maxRecursionDepth cannot be unlimited"}
if l.MaxDuration > 0 && l.MaxDuration != UnlimitedDuration { set micros }
if l.MaxMemory > 0 && l.MaxMemory != Unlimited { set bytes }
suspensions := uint64(1000); if l.MaxSuspensions > 0 { suspensions = l.MaxSuspensions }   // Unlimited passes as MaxUint64
```

### 8.12 `Expose`

```go
func Expose[T any]() AttrPolicy {
    t := reflect.TypeFor[T]()
    if t.Kind() != reflect.Interface {
        panic(fmt.Sprintf("monty.Expose: %s is not an interface type", t))
    }
    names := make([]string, 0, t.NumMethod())
    for i := 0; i < t.NumMethod(); i++ {
        names = append(names, SandboxName(t.Method(i).Name))
    }
    return Names(names...)
}
```

### 8.13 `Host.Stubs`

```go
func (h *Host) Stubs() string {
    var b strings.Builder
    b.WriteString("from typing import Any, Awaitable\n\n")
    for _, name := range sortedKeys(h.funcs) {
        f := h.funcs[name]
        if f.sig == nil { fmt.Fprintf(&b, "def %s(*args: Any, **kwargs: Any) -> Any: ...\n", name); continue }
        params := []string{}
        for i := firstParam(f.sig); i < lastParam(f.sig); i++ { params = append(params, fmt.Sprintf("arg%d: %s", i, pyType(f.sig.In(i)))) }
        if hasKwargs(f.sig) { params = append(params, "**kwargs: Any") }
        fmt.Fprintf(&b, "def %s(%s) -> %s: ...\n", name, strings.Join(params, ", "), pyResult(f.sig))
    }
    for _, name := range sortedKeys(h.objects) {
        ci := h.objects[name]
        fmt.Fprintf(&b, "\nclass %s:\n", ci.Name())
        for _, m := range ci.exposedMethods() { fmt.Fprintf(&b, "    def %s(self, %s) -> %s: ...\n", m.name, m.params, m.result) }
        fmt.Fprintf(&b, "\n%s: %s\n", name, ci.Name())
    }
    return b.String()
}

pyType: int kinds → "int"; float → "float"; string → "str"; bool → "bool"; []byte → "bytes";
        slice → "list[" + elem + "]"; map[string]T → "dict[str, " + T + "]"; *Future → "Awaitable[Any]"; else "Any"
pyResult: no results or (error) → "None"; (T) or (T, error) → pyType(T)
```

### 8.14 `Lines`

```go
func (t *lineTarget) Print(stream Stream, text string) error {
    t.mu.Lock(); defer t.mu.Unlock()
    buf := t.partial[stream] + text
    for {
        i := strings.IndexByte(buf, '\n')
        if i < 0 { break }
        if err := t.fn(stream, buf[:i]); err != nil { t.partial[stream] = buf[i+1:]; return err }
        buf = buf[i+1:]
    }
    t.partial[stream] = buf
    return nil
}

func (t *lineTarget) Flush() error {
    t.mu.Lock(); defer t.mu.Unlock()
    for _, stream := range []Stream{Stdout, Stderr} {
        if rest := t.partial[stream]; rest != "" {
            t.partial[stream] = ""
            if err := t.fn(stream, rest); err != nil { return err }
        }
    }
    return nil
}
```

### 8.15 `instanceStore.put` with bound

```go
func (s *instanceStore) put(w wrapper, onlyIfAbsent bool) error {
    s.mu.Lock(); defer s.mu.Unlock()
    if existing, ok := s.m[w.wrapperID()]; ok {
        if !sameObject(existing.identity(), w.identity()) { return &ConversionError{...} }
        if onlyIfAbsent { return nil }
        s.m[w.wrapperID()] = w
        return nil
    }
    if s.limit != Unlimited && uint64(len(s.m)) >= s.limit {
        return &ResourceError{Resource: "host object", Limit: s.limit}
    }
    s.m[w.wrapperID()] = w
    if len(s.m) > s.peak { s.peak = len(s.m) }
    return nil
}
```

### 8.16 `check-pins.sh`

```bash
REV=$(tr -d '[:space:]' < proto/PROTO_REV); SHORT=${REV:0:8}
MV=$(sed -n 's/^[[:space:]]*MontyVersion *= *"\(.*\)".*/\1/p' monty.go)
fail=0
expect() { # file pattern description
  grep -qE "$2" "$1" || { echo "$1: expected $3" >&2; fail=1; }
}
expect monty.go "UpstreamRev *= *\"$SHORT\"" "UpstreamRev = \"$SHORT\""
expect worker-wasm/Cargo.toml "rev = \"$SHORT\"" "rev = \"$SHORT\""
expect server/Cargo.toml "rev = \"$SHORT\"" "rev = \"$SHORT\""
expect server/src/version.rs "MONTY_REV: &str = \"$REV\"" "MONTY_REV = \"$REV\""
expect docker/Dockerfile "ARG MONTY_REV=$REV" "ARG MONTY_REV=$REV"
expect docker/pyclient.Dockerfile "ARG MONTY_REV=$REV" "ARG MONTY_REV=$REV"
expect .github/workflows/ci.yml "MONTY_REV: $REV" "MONTY_REV: $REV"
expect internal/worker/websocket.go "monty-pool/$MV" "DefaultUserAgent monty-pool/$MV"
expect worker-wasm/Cargo.toml "^version = \"$MV\"" "version = \"$MV\""
[ "$(grep -c "rev = \"$SHORT\"" worker-wasm/Cargo.toml)" -eq 3 ] || { echo "worker-wasm/Cargo.toml: expected 3 revs" >&2; fail=1; }
[ "$(grep -c "rev = \"$SHORT\"" server/Cargo.toml)" -eq 3 ] || { echo "server/Cargo.toml: expected 3 revs" >&2; fail=1; }
exit $fail
```

### 8.17 `FetchServerInfo`

```go
func FetchServerInfo(ctx context.Context, opts WebSocketOptions) (*ServerInfo, error) {
    timeout := requestTimeout(opts)
    pairs, err := connectPairs(ctx, opts)
    if err != nil { return nil, err }
    var raw serverInfoJSON
    if err := opts.dialer(timeout).GetJSON(ctx, "/info", pairs, &raw); err != nil {
        var he *worker.HTTPStatusError
        if errors.As(err, &he) && he.Code == 404 { return nil, ErrNoServerInfo }
        return nil, err
    }
    return raw.toServerInfo(), nil
}
```

### 8.18 `resolveRecorder`

```go
func resolveRecorder(c *TelemetryComponents) *telemetry.Recorder {
    if c == nil { return telemetry.Global() }
    if c.Tracer == nil && c.Meter == nil && c.Logger == nil { return nil }
    return telemetry.NewRecorder(telemetry.Components(*c))
}
```

---

## 9. Tests to add or change

| Area | Tests |
|---|---|
| version | `version_test.go`: stamped value wins; `moduleVersion` parsing table (tag, pseudo, replace, main devel); `scripts/version_test.sh` (bats-free shell test creating a temp repo: no tag, tag at HEAD, tag behind, dirty) run by `make test-scripts` |
| frame queue | `internal/worker/worker_test.go`: bound blocks push, pop frees, close unblocks, oversize single frame passes; root `print_test.go`: flood with a blocking target and `RequestTimeout` → `CrashedError{TimedOut}`, RSS not asserted |
| abort/cancel | `cancel_test.go`: rewrite "cancelling while awaiting a host future" → `KeyboardInterrupt`, session usable; new `interrupt_test.go`: interrupt during callback, during Python (killed), during snapshot suspension, `CloseNow` mid-feed, `Go`/`Run.Wait`, `Done`/`Err` on server close (websocket backend) |
| pool | `internal/pool/pool_test.go`: retiring counted, reaper drains, `Shutdown` waits and forces; root `pool_test.go`: `Stats` |
| lost sessions | `internal/worker/websocket_test.go`: close reason captured, `Send` after close; `tests/network`: `TestTimeouts_IdleClosesSession` asserts `DisconnectError.Code == 1008` and `ErrSessionLost` |
| limits | `limits_test.go`: `Unlimited` suspensions run 1500 host calls; recursion `Unlimited` rejected; `MaxHostObjects` raises; `MaxPendingFutures` raises |
| exposure | `class_instance_test.go`: `Expose[T]`, non-interface panics; examples compile |
| host | `host_test.go`: invalid signature rejected at `Func`, `Stubs` golden, `Restorable`, restore after `Dump` on a fresh session with pinned IDs (native, wasm, websocket) |
| print | `print_test.go`: `Lines` splits and flushes at turn end and on error |
| futures | `async_test.go`: `AsyncContext` cancelled on interrupt |
| telemetry | `telemetry_test.go`: per-pool components without child process for the new cases; global fallback kept for the ported ones |
| server | `server/tests/session.rs`: `/info` JSON; `tests/network`: `TestAdmission_InfoEndpoint`, `FetchServerInfo` values match flags |
| codec | benchmarks, fuzz, field coverage |
| pins | `make check-pins` in CI; a test that mutates a copy and expects failure |

---

## 10. NOT CONSIDERED & TODO

| # | Item | Tags | Handling |
|---|---|---|---|
| N1 | Frame bound for the WebSocket transport | postponed | depth-1 channel and 256 MiB read limit already bound it |
| N2 | Host callback that ignores its context after `Interrupt` | too-complex | documented; worker killed after grace |
| N3 | Server memory ceiling on restored dumps | postponed | unchanged (server N6 stub) |
| N4 | Stubs for nested structs, generics, variadics | potential-improvement | `Any` |
| N5 | Backend sub-packages | postponed | root constructors stay |
| N6 | Signed tags, cosign, provenance verification | postponed | `--provenance=true` only |
| N7 | `scripts/version.sh` on Windows | postponed | Linux/macOS builds; `BindingVersion` needs no script |
| N8 | Minor/major auto-bump of dev versions | postponed | last tag as-is |
| N9 | Fuzz inputs above 1 MiB | postponed | capped |
| N10 | Cancellable `Run.Wait` | potential-improvement | use `Done()` |
| N11 | Explicit release of host objects | too-complex | rotation is the boundary |
| N12 | `Instrument` after a pool exists | postponed | resolved at creation |
| N13 | Go pseudo-version vs script version mismatch for untagged commits | postponed | documented in `versioning.md` |
| N14 | `Pool.Shutdown` and user goroutines from `Async` | too-complex | futures settled; goroutines continue |

---

## 11. Order of implementation

1. Versioning: `scripts/version.sh`, `version.go`, `monty.go`, Makefile, `build.rs`, Dockerfile, `check-pins.sh`, docs (`versioning.md`), CI `check-pins`.
2. Errors and lifecycle: `ErrSessionLost`, `DisconnectError` fields, worker `Done`/`Err`, `lifecycle`, `Interrupt`, `CloseNow`, `Done`, `Err`, `Checkout.Abort`, `callHost`, `Run`; tests; parity docs.
3. Frame queue bound and pool accounting/`Shutdown`; metrics; tests.
4. Limits, store bound, futures bound, `Unlimited`, README wording.
5. `All()`, `Expose`, `Host`, `Stubs`, restore, `AsNamedTuple`, `Lines`, `AsyncContext`; examples.
6. Telemetry per pool.
7. Server `/info`, `FetchServerInfo`.
8. Codec benchmarks, fuzz, field coverage; CI race, govulncheck, cargo audit, fuzz, SBOM, licences; changelog; golden file; `docs/parity`.
9. Full verification: vet, lint, both backends, race, `make server-check`, `make docker-build`, `make test-docker`, `make test-network`, `make examples`, `go mod tidy -diff` in all modules, `make check-pins`.
