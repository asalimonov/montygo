# Brainstorm: feedback fixes and tag-based versioning

Date: 2026-09-15. Skill: go-brainstorm. Status: done. Target: `20260915-feedback-fixes-and-versioning-target.md`.

## 1. Initial input

> fix all confirmed issues with medium, high and critical priority from docs/reports/feedback.md
> montygo should have versioning based on tags, when version built not from tagged commit it should use `{verion}-{short-hash}` version and it should be resolved during build time. Example of it can be found in ../neria/meridian-overlay project

Scope from the report: rows 1–21 (critical 1–2, high 3–8, medium 9–21). Rows 22–23 (low) and 25–35 (`montygo-test`) are out of scope.

## 2. Research

### 2.1 meridian-overlay versioning

- `scripts/version.sh` (bash, about 200 lines): tags `transports/v*`.
  - Tag at HEAD (loose semver, prerelease allowed) → `X.Y.Z[-pre]`.
  - Else highest strict tag reachable from HEAD → `X.Y.(Z+1)-<short-hash>` (patch bump, dev build).
  - Else `transports/version` file → `<file>-<hash>`; else `0.0.0-<hash>` with a warning.
  - Dirty tree → `-dirty` suffix.
- `Makefile`: `VERSION ?= $(shell git describe --tags --always --dirty)` for test images, `IMAGE_VERSION ?= $(shell bash scripts/version.sh)` for release images; `--build-arg VERSION=`.
- `transports/Dockerfile`: `-ldflags="-X main.Version=v${VERSION}"`; `main.go`: `var Version = "dev"`.

### 2.2 montygo version wiring today

| Use | Source |
|---|---|
| `monty.Version = "0.0.23"` (`monty.go`) | hand-written constant, equals the upstream Monty release |
| `Configure.monty_version` (`pool.go:118`, informational on the wire) | `monty.Version` |
| OpenTelemetry instrumentation scope version (`telemetry.go:203-209`) | `monty.Version` |
| `worker.DefaultUserAgent = "monty-pool/0.0.23"` | hand-written |
| `server/Cargo.toml` `version = "0.0.23"`, `SERVER_VERSION = CARGO_PKG_VERSION` | hand-written |
| `IMAGE_TAG = $(VERSION)-$(UPSTREAM_REV)` (`Makefile:63`) | sed over `monty.go` |
| `MONTY_REV` (`server/src/version.rs`, Dockerfiles, `ci.yml`, `worker-wasm/Cargo.toml`, `proto/PROTO_REV`) | hand-written in each file |

No git tags exist. Commits: `890f0f9 Initial commit`, `cf99bdd Intermediate commit` on `feat/dockerized-runtime`.

### 2.3 Library constraint

`-ldflags -X` stamps a variable in a binary the consumer links; a consumer of the module does not run montygo's Makefile. For a library the runtime source is `runtime/debug.ReadBuildInfo()`: the main module's dependency entry for `github.com/asalimonov/montygo` carries the module version (`v0.1.0`, or a pseudo-version `v0.1.1-0.20260915120000-abcdef123456` for an untagged commit, or `(devel)` when built from a `replace`). So:

- Go library: `BindingVersion()` reads the module version from build info, falling back to a `-X`-stamped variable, then to a committed default.
- Binaries built in this repository (the server image, `examples/repl`): `-X` from `scripts/version.sh`.
- Rust server: the version is passed by the Dockerfile as a build argument and baked with `env!("MONTY_SERVER_VERSION")` through `build.rs`, falling back to `CARGO_PKG_VERSION`.

### 2.4 Code facts for the fixes

- `internal/worker/worker.go` `frameQueue`: `push` appends without bound; `pumpFrames` never blocks. Used by `subprocess_unix.go:45-47` and `wasm.go:90-113`. `wsWorker` uses `frames chan []byte` of depth 1.
- `session.go`: `Session{mu, closed, driven, broken, store, scriptName}`; `FeedRun` and `Close` both take `s.mu`. `answerer{s, lookup, os, pt, futures}`; host calls run under `cbCtx := a.s.co.CallbackContext(ctx)` with no cancel handle stored anywhere.
- `internal/pool/checkout.go:221`: `AbortFeed` is sent only for the suspension limit; `c.abortFlight` tracks the reply.
- `internal/pool/pool.go`: `Pool{idle, total, notify, closed}`; `release` decrements `total` before `go p.retire`; `discard` likewise; `Close` shuts idle workers only.
- `internal/worker/websocket.go`: `read()` drops the close frame's code and reason; `Send` checks `w.stop` only.
- `options.go:100-135`: `0` for a limit selects the default; `MaxRecursionDepth` and `MaxSuspensions` default to 1000.
- `classinstance.go`: `All` is `var`; `instanceStore{mu, m}` has no bound; `ClassInstanceOptions.ID` pins a uuid.
- `errors.go:221-243`: `PythonExceptionNames` is a writable map read at `:328`; `answer.go:251` compares `result == NotHandled`.
- `answer.go:60-63`: `asFunction` drops the `Func` error.
- `convert.go:203`: structs return a wrap hint.
- `print.go`: `PrintTarget`, `ContextPrintTarget`, `PrintFunc`, collectors; `session.go:316-370` `printTarget.onPrint` has no end-of-feed hook.
- `future.go`: `Async(fn func() (any, error))`, no context, no bound.
- `telemetry.go` + `internal/telemetry/recorder.go`: one process-wide `current atomic.Pointer[Recorder]`; `Instrument` installs it; `pool.go:118` passes `Version`.
- `server/src/http.rs`: routes `/`, `/health`, `/metrics`.
- No `Benchmark*`/`Fuzz*` in the repository; CI has no `-race`, `govulncheck` or fuzz step. `montypb` is imported by `tests/network/wire.go` and internal tests.

## 3. Questions and answers

### Q1. Version derivation rule

Options: last reachable tag as-is plus hash; meridian rule (patch bump plus hash); `git describe`.

**Answer:** last tag as-is + hash.

**Resolution R1.** `scripts/version.sh` (a trimmed port of meridian's script, tag prefix `v`):

| State | Output |
|---|---|
| a `v*` tag points at HEAD | `X.Y.Z[-pre]` |
| no tag at HEAD, a `v*` tag reachable | `X.Y.Z-<short-hash>` (highest reachable, no bump) |
| no `v*` tag reachable | `0.0.0-<short-hash>` and a warning on stderr |
| uncommitted changes | `-dirty` appended |

The difference from Go pseudo-versions (`v0.1.1-0.<time>-<hash>`, which bump the patch) is documented in `docs/architecture/versioning.md`.

### Q2. Runtime version API

Options: build info + ldflags fallback; generated `version_gen.go`; ldflags only.

**Answer:** build info + ldflags fallback.

**Resolution R2.**

```go
// monty.go
const (
    MontyVersion    = "0.0.23"   // upstream release tracked
    Version         = MontyVersion // Deprecated: use MontyVersion.
    UpstreamRev     = "f8acf4fa"
    ProtocolVersion uint32 = 3
)

// version.go
var buildVersion string // -ldflags "-X github.com/asalimonov/montygo.buildVersion=0.1.0-3f2a9c1"

func BindingVersion() string // buildVersion → module version from debug.ReadBuildInfo → "0.0.0-unknown"
```

Consumers:

| Caller | Source used |
|---|---|
| `Configure.monty_version` (informational) | stays `MontyVersion`: the worker diagnoses skew against the upstream release |
| telemetry instrumentation scope version | `BindingVersion()` |
| `DefaultUserAgent` | `monty-pool/<MontyVersion> montygo/<BindingVersion()>` |
| image tag | `scripts/version.sh` output (`IMAGE_TAG ?= $(shell scripts/version.sh)`) |
| server `SERVER_VERSION` | `MONTY_SERVER_VERSION` build argument via `build.rs`, else `CARGO_PKG_VERSION` |
| `examples/repl` banner | `BindingVersion()` |

### Q3. Cancel policy while a host call is pending (row 3)

Options: `AbortFeed` by default; opt-in `FeedOptions.InterruptOnCancel`; `Interrupt` only.

**Answer:** `AbortFeed` by default.

**Resolution R3.**

- The answerer records the running host callback's cancel function in the session's lifecycle state before it calls host code. When the feed context is cancelled, or `Session.Interrupt` is called, the callback context is cancelled.
- When the callback returns and its context was cancelled, the answerer sends `AbortFeed(KeyboardInterrupt)` under a fresh context (`context.WithoutCancel(ctx)` plus `abortDeadline`, 5 s, or the pool `RequestTimeout` when smaller). The worker answers `Error`; `FeedRun` returns `*RuntimeError{TypeName: "KeyboardInterrupt"}` and the session stays usable. `Interrupt(reason)` maps `reason` through `exceptionParts` instead of `KeyboardInterrupt`.
- When no host callback is pending (the worker is executing Python), a cancelled context still kills the worker and poisons the session; `Interrupt` waits `InterruptGrace` (default 100 ms) for a suspension, then kills.
- A host callback that ignores its context blocks `FeedRun`; `Interrupt` kills the worker after the grace and `FeedRun` returns once the callback returns.
- `cancel_test.go` "cancelling while awaiting a host future poisons the session" is rewritten; the deviation goes into `docs/parity/tests.md`. Snapshots (`FeedStart`) follow the same rule inside `ResumeAuto`; a snapshot whose owner cancels between steps is unaffected, because no turn is in flight.

### Q4. API compatibility policy

Options: breaks allowed before `v0.1.0`; additive only.

**Answer:** breaks allowed.

**Resolution R4.** `v0.1.0` is the first tag and the first stable exported surface. Renames in this change: `All` → `All()`, `PythonExceptionNames` → `KnownExceptionNames()`. `Version` stays as a deprecated alias of `MontyVersion`. `NotHandled` keeps its name; the internal comparison uses a private sentinel. The golden file, README, `docs/parity/api.md` and `changelogs/v0.1.0.md` record the changes.

### Q5. Telemetry ownership (row 17)

Options: `Options.Telemetry` with the global installation as default; per-pool only; keep global.

**Answer:** `Options.Telemetry`, global as default.

**Resolution R5.** `Options.Telemetry *TelemetryComponents` and `WebSocketOptions.Telemetry` build a `*telemetry.Recorder` owned by the pool and stored in `pool.Config.Recorder`. A nil field uses the recorder installed by `Instrument`/`NewInstrumentation`, resolved once at pool creation, so a later `Instrument` does not affect an existing pool. `internal/telemetry.Current()` is removed; `Checkout` observers take the recorder from `pool.Config`.

## 4. Modelled decisions for the remaining rows

Defaults chosen by the author; presented for confirmation in Q6.

| Row | Decision |
|---|---|
| 1 frame queue | `frameQueue` becomes byte-bounded with producer blocking. `Options.MaxPendingBytes int64`, default 64 MiB, applies to native and wasm; `0` means the default, `UnlimitedPendingBytes = -1` restores today's behaviour. `pumpFrames` blocks in `push` until space frees or the worker is killed. A single frame larger than the bound is still accepted (the frame cap stays 256 MiB). Metrics: `monty.pool.pending_frame_bytes` gauge. |
| 4 retirement | `Pool` gains `starting`, `active`, `idle`, `retiring` counts; `MaxProcesses` bounds their sum. `retire` decrements `retiring` after the worker exited; a bounded reaper (`min(MaxProcesses, 8)` goroutines, buffered channel) replaces one goroutine per retirement. `discard` uses `Kill` for every transport (a poisoned WebSocket session drops without the 1 s close handshake). `Pool.Size()` returns all four counts through a new `PoolStats` struct. |
| 5 lost sessions | `wsWorker` records the close frame (`websocket.CloseError`) before closing `done`; `Send` returns `ErrWorkerGone` wrapping the close reason when `done` is closed. `worker.Worker` gains `Done() <-chan struct{}` and `Err() error`. `DisconnectError` gains `Code int`, `Reason string`; message `closed by server (1008): idle timeout of 60s exceeded`. `Session.Done()`, `Session.Err()`. `var ErrSessionLost`; `CrashedError`, `DisconnectError`, `ShutdownError`, `ProtocolError` and `ErrSessionClosed` match it through `Is`. |
| 6 limits | README and doc comment say "per session, reset by `LoadSession`/`LoadSnapshot`". `const Unlimited uint64 = math.MaxUint64` accepted by `MaxSuspensions`, `MaxMemory` and (as `time.Duration`) `MaxDuration`; `MaxRecursionDepth: Unlimited` is an `OptionError`, because the worker's stack is finite. On the wire `Unlimited` omits the field for memory and duration and sends `math.MaxUint64` for suspensions (parent-enforced). Error text stays upstream's. |
| 7 `All` | `func All() AttrPolicy`; `func Expose[T any](v any) AttrPolicy` builds `Names` from the exported methods of interface `T` (compile-time contract, `T` MUST be an interface, else panic at construction). Examples, README and `example_test.go` switch to `Names`/`Expose`. |
| 8 host objects | `CheckoutOptions.MaxHostObjects int`, default 10 000, `Unlimited` allowed. `instanceStore.put` returns `*ResourceError{Resource: "host objects", Limit}` which the answerer raises in the sandbox as `RuntimeError: host object limit 10000 exceeded`. `Session.Stats()` returns `SessionStats{HostObjects, PeakHostObjects, PendingFutures, Suspensions}`. |
| 9 globals | Internal reads use private `knownExceptions` and `notHandledSentinel`. `KnownExceptionNames() []string` returns a sorted copy. |
| 10 versions | R1/R2 plus: `proto/PROTO_REV` stays the single source of the upstream revision; `make check-pins` (script `scripts/check-pins.sh`) verifies `monty.go` `UpstreamRev`, `worker-wasm/Cargo.toml`, `server/Cargo.toml`, `server/src/version.rs`, both Dockerfiles, `ci.yml` and `internal/worker/websocket.go` against it and `MontyVersion`; CI runs it. Image tag `monty-server:<BindingVersion>`, plus `:latest`. Labels: `org.opencontainers.image.version=<BindingVersion>`, `io.montygo.monty-rev`. |
| 11 server info | `GET /info` JSON (`version`, `monty_rev`, `protocol_version`, `limits{...}`); `monty.ServerInfo`, `FetchServerInfo(ctx, WebSocketOptions)`. `/` stays text. Listed as an extension in `docs/parity/server.md`. |
| 12 host registry | `type Host struct`; `NewHost()`, `Func(name, fn) error`, `Object(name, v, ClassInstanceOptions) error`, `Stubs() string`, `Names() []string`. `CheckoutOptions.Host *Host`; `FeedOptions.ExternalLookup` overrides by name. `asFunction` errors surface as `TypeError: <name>: <reason>`. |
| 13 structs | `AsNamedTuple(v any) (NamedTuple, error)` using `monty` tags; `NewNamedTuple(typeName string, pairs ...Pair) (NamedTuple, error)`; untagged structs keep the wrap hint. |
| 14 print | `Lines(fn func(Stream, string) error) PrintTarget` with per-stream buffers; new optional `FlushingPrintTarget{ Flush() error }` called by `printTarget.finish()` at every turn end (complete, error, snapshot). |
| 15 futures | `AsyncContext(ctx, fn func(context.Context) (any, error)) *Future`; the answerer passes the callback context so `Interrupt`/cancel propagate. `CheckoutOptions.MaxPendingFutures`, default 1000; excess raises `RuntimeError: pending future limit 1000 exceeded`. Session end settles pending futures with `ErrSessionLost`. |
| 16 shutdown | `Pool.Shutdown(ctx)`: closes the pool, calls `CloseNow` on checked-out sessions when `ctx` ends, waits for `active+retiring == 0`. `Close` unchanged. |
| 18 restore | `LoadSession`/`LoadSnapshot` register `CheckoutOptions.Host` objects under their pinned `ID`s before `Load`; `Host.Object` requires a pinned `ID` for restore to work and `Host.Restorable() error` reports objects without one. |
| 19 codec | `BenchmarkDecodeEvent*`, `BenchmarkEncodeRequest*` vs `montypb`; `FuzzDecodeEvent`, `FuzzDecodeRequest` seeded from the differential corpus; a test that every field number in `monty.proto` appears in `internal/wire`. CI: `go test -run=^$ -fuzz=Fuzz -fuzztime=30s` per fuzz target. |
| 20 `montypb` | Stays public; package doc and `docs/parity/api.md` state "generated wire types; no stability promise beyond `ProtocolVersion`". |
| 21 release | CI: `go test -race` on Linux, `govulncheck ./...`, `cargo audit`, fuzz step, `make check-pins`; `docker-push` adds `--sbom=true --provenance=true`; Rust licences bundled into the image with `cargo about`; `changelogs/v0.1.0.md`; the first tag `v0.1.0` is created by the maintainer after merge. |

**Q6 answer:** accept all.

## 5. Verification

### 5.1 Findings against conventions and documents

| # | Finding | Proposed resolution |
|---|---|---|
| F1 | `CLAUDE.md`: error messages MUST match upstream. `DisconnectError` gains the close code and reason; a cancelled host call now yields `KeyboardInterrupt` instead of a poisoned session. | Both are deliberate deviations recorded in `docs/parity/tests.md` and `docs/parity/api.md`. Upstream's texts are kept where they exist (`suspension limit N exceeded`, `PoolError` display). |
| F2 | `CLAUDE.md`: pool code MUST NOT branch on the transport. `release` already branches for the observer; row 4 removes the `discard` branch (`Kill` everywhere) but keeps the graceful `Close` on normal release so the server records `closed` rather than `error`. | Keep one branch in `release`, documented in `pool.md`; `discard` becomes transport-neutral. |
| F3 | The frame-queue bound must not deadlock a kill: `pumpFrames` blocked in `push` must wake when the worker is killed. | `push` selects on a `closed` channel that `Kill`/`Close` close; the queue then fails with `ErrWorkerGone`. |
| F4 | `Interrupt` while a snapshot (`FeedStart`) is suspended between steps: no callback is pending and no turn is in flight, yet the worker is suspended. | `Interrupt` sends `AbortFeed` itself under `s.mu` when `co.Pending()` reports a suspension; the next `Resume*` on the snapshot returns the `KeyboardInterrupt` error. |
| F5 | `Interrupt` racing a callback that has just returned: cancelling its context is a no-op and the feed completes normally. | `Interrupt` reports `nil`; the answerer checks the interrupt flag before every host call and aborts at the next suspension, so a feed that keeps making host calls still stops. |
| F6 | Retirement counting delays bursty WebSocket checkouts by up to `closeWriteBudget` (1 s) per slot. | Accepted; `docs/architecture/pool.md` states it. `discard` uses `Kill`, so failure paths are not delayed. |
| F7 | `BindingVersion()` inside this repository's own tests: the main module reports `(devel)` and no `-X` is set by `go test`. | Makefile targets pass `-ldflags -X …buildVersion=$(shell scripts/version.sh)`; a bare `go test` sees `0.0.0-unknown`, which no test asserts. Under a directory `replace` (consumer development) the module version is the zero pseudo-version and `BindingVersion` reports `(devel)`. |
| F8 | `server/Cargo.toml` `version = "0.0.23"` is the upstream version; `SERVER_VERSION` is used by `/`, `/info`, `/metrics` `build_info`. | Cargo version becomes `0.1.0` (binding line); `build.rs` prefers `MONTY_SERVER_VERSION` from the Docker build argument, so images carry `0.1.0-3f2a9c1`. `check-pins` compares Cargo's version base with the highest `v*` tag only when a tag exists. |
| F9 | The upgrade checklist in `CLAUDE.md` lists every file that holds the revision; `check-pins` automates it but the list must stay in step. | The checklist references `scripts/check-pins.sh` as the authority and keeps the file list as documentation. |
| F10 | `Host.Stubs()` must map Go types to Python annotations; the mapping is open-ended. | Scope: `int*`/`uint*` → `int`, `float*` → `float`, `string` → `str`, `bool` → `bool`, `[]byte` → `bytes`, `[]T` → `list[T]`, `map[string]T` → `dict[str, T]`, `error`-only result → `None`, `*Future` → `Awaitable[Any]`, everything else → `Any`; objects become `class <Name>:` with method stubs. |
| F11 | `Expose[T]` cannot constrain `T` to an interface type in Go generics. | Runtime check: non-interface `T` panics with a clear message; `MustExpose` is not needed since construction is at init time. |
| F12 | `Pool.Shutdown` cannot stop user goroutines started by `Async`. | Pending futures are settled with `ErrSessionLost`; the goroutine keeps running; documented. |

### 5.2 NOT CONSIDERED & TODO

| # | Item | Tags | Proposed handling |
|---|---|---|---|
| N1 | Frame bound for the WebSocket transport | postponed | The depth-1 channel and the 256 MiB read limit already bound it; no change. |
| N2 | A host callback that ignores its context keeps `FeedRun` blocked after `Interrupt` | too-complex | Documented; `Interrupt` kills the worker after the grace so the callback's eventual return fails fast. |
| N3 | Restored dump memory ceiling on the server (server-side N6 stub) | postponed | Unchanged. |
| N4 | Type stubs for nested Go structs, generics, variadics and `Kwargs` | potential-improvement | Emit `Any`; `**kwargs: Any` for `Kwargs`. |
| N5 | Backend split into sub-packages (`monty/backend/...`) | postponed | Not in the confirmed rows; root constructors stay. |
| N6 | Signed tags, cosign image signatures, provenance verification | postponed | `--provenance=true` only. |
| N7 | `scripts/version.sh` on Windows | postponed | Docker and CI run on Linux; `BindingVersion()` needs no script. |
| N8 | Minor/major auto-bump of dev versions | postponed | R1 uses the last tag as-is. |
| N9 | Fuzz inputs above a few MiB | postponed | Fuzz targets cap input at 1 MiB; the 256 MiB frame path is covered by the existing limit tests. |
| N10 | `Session.Go` run handle cancellation of `Wait` callers | potential-improvement | `Wait()` blocks until the run ends; `Done()` is the non-blocking path. |
| N11 | Explicit release of host objects from the instance store | too-complex | Not implemented; rotation is the reclamation boundary. |
| N12 | `Instrument` after a pool exists | postponed | The pool keeps the recorder resolved at creation; documented. |

**Q7 answer:** accept all F and N items as proposed.

## 6. Summary

### 6.1 Resolutions

| # | Topic | Resolution |
|---|---|---|
| R1 | Version rule | tag at HEAD → `X.Y.Z`; else `X.Y.Z-<hash>` from the highest reachable `v*` tag; `-dirty`; `0.0.0-<hash>` without tags. `scripts/version.sh`. |
| R2 | Version API | `BindingVersion()` = `-X buildVersion` → module version from build info → `0.0.0-unknown`. `MontyVersion` constant; `Version` deprecated alias. Server gets `MONTY_SERVER_VERSION` through `build.rs`. |
| R3 | Cancel policy | a cancelled feed context or `Session.Interrupt` while a host call is pending sends `AbortFeed(KeyboardInterrupt)`; the session survives. Kill only while Python executes. Parity deviation recorded. |
| R4 | API policy | breaking renames allowed before `v0.1.0`: `All()`, `KnownExceptionNames()`. |
| R5 | Telemetry | `Options.Telemetry`/`WebSocketOptions.Telemetry` per pool; nil falls back to the global installation resolved at creation. |
| Q6 | Rows 1, 4–16, 18–21 | defaults of §4 accepted. |
| Q7 | Verification | F1–F12 and N1–N12 accepted. |

### 6.2 Evidence gathered

- meridian-overlay's `scripts/version.sh` and `-ldflags -X main.Version` pattern; montygo is a library, so the runtime source is `debug.ReadBuildInfo`.
- `monty.Version` feeds the `Configure` frame, the OTel scope version, the user agent and the image tag; these now split between `MontyVersion` and `BindingVersion()`.
- The frame queue, session mutex, retirement path, WebSocket close handling, limit defaults, instance store, mutable globals, `asFunction`, struct conversion, print chunks, `Async`, `Pool.Close` and global telemetry were each read and confirmed as the report describes.

### 6.3 Deviations to record in `docs/parity/`

- `KeyboardInterrupt` on a cancelled host call instead of a poisoned session.
- `DisconnectError` message carrying the WebSocket close code and reason.
- `GET /info` on `monty-server`.
- `MaxSuspensions` documented per session (upstream's own semantics; the README was wrong).

BRAINSTORM DONE

