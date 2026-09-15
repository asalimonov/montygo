# Feedback analysis: montygo as a third-party library

Date: 2026-09-15. Sources: `../montygo-feedback-codex.md`, `../montygo-feedback-claude.md`. Reviewed revision: `cf99bdd` on `feat/dockerized-runtime`.

Every claim was checked against the code before it was marked confirmed. Items that both reviews raise are merged into one row. Rows tagged `[montygo-test]` concern the consumer project, not this library; they are listed for completeness, and a fix is given only where the library is the right place for it.

## 1. Summary table

| # | Priority | Who | Title | Problem | Confirmed | Comment |
|---|---|---|---|---|---|---|
| 1 | critical | codex | Native and wasm frame queue is unbounded | `internal/worker/worker.go` `frameQueue` appends every frame the worker emits with no byte or count bound; a print flood grows parent memory outside `MaxMemory`. | yes | `pumpFrames` never waits for the consumer. WebSocket uses a depth-1 channel, so only native and wasm are affected. |
| 2 | critical | codex, claude | No out-of-band abort of an active feed | `Session.FeedRun` holds `s.mu` for the whole turn including host callbacks; `Session.Close` needs the same mutex, so nothing can interrupt a running feed except the feed's own context, which kills the worker. | yes | `session.go:118-139`, `:301-313`. The protocol's `AbortFeed` is used only for the suspension limit (`internal/pool/checkout.go:221`). |
| 3 | high | claude | Cancelling a feed that waits on a host call kills the worker | Cancelling the feed context during a host `sleep` returns bare `context.Canceled` and poisons the session, although the worker is idle and `AbortFeed` could end the feed gracefully. | yes | `cancel_test.go:37` documents it. Changing it alters observable behaviour: MUST be listed in `docs/parity`. |
| 4 | high | codex | Retiring workers are not counted in pool capacity | `internal/pool/pool.go` `release` and `discard` decrement `p.total` before the old worker exits; `retire` runs in an unbounded goroutine. Under churn, live processes and open connections exceed `MaxProcesses`. | yes | `pool.go:218-263`. |
| 5 | high | claude | Lost sessions are detected late and the close reason is discarded | `wsWorker.Send` checks only `w.stop`, not `w.done`; `read()` drops the close frame's code and reason. A server timeout surfaces as "connection closed while sending a request" on the next feed. | yes | `internal/worker/websocket.go:273-303`. No public classifier for "session lost" either. |
| 6 | high | codex, claude | `MaxSuspensions` is documented per feed but counted per session | `README.md:244` says "per feed"; `internal/pool/budget.go` counts across the checkout and resets only on `Load`. `ResourceLimits` has no "unlimited": `0` means the 1000 default (`options.go:118-123`). | yes | Long-lived sessions die after 1000 host calls with no supported way to lift the limit. |
| 7 | high | codex, claude | `monty.All()` exposes every exported method | Any exported method added to a host type later becomes callable from Python with no signal in review. Library examples use `All` by default. | yes | `classinstance.go:26`; `examples/classes/*` use `EagerAttrs: monty.All()`. |
| 8 | high | codex | Host wrapper retention is unbounded | `instanceStore` (`classinstance.go:568-614`) keeps every wrapper until the session closes; no limit, no metric. | yes | Required for identity restore, but a long session receiving unique objects grows without bound. |
| 9 | medium | codex | Public mutable globals are read internally | `PythonExceptionNames` is a writable map read without synchronization at `errors.go:328`; `NotHandled` is compared by pointer identity at `answer.go:251`; `All` is a writable `var`. | yes | A consumer mutating `PythonExceptionNames` concurrently is a data race. |
| 10 | medium | codex, claude | Version constants conflate binding, upstream and server | `monty.Version` is the upstream Monty version, not a montygo release; `IMAGE_TAG` derives from it, so two different server builds share one tag. Pins live in many files. | yes | `monty.go:8-12`, `Makefile:63`, `docs/architecture/docker.md:74` already admits the tag problem. No git tags exist. |
| 11 | medium | claude | Server timeouts are invisible to the client | `Ok` echoes only `max_duration_micros` and `max_suspensions`; the client cannot learn idle, turn or session timeouts and cannot plan around them. | yes | Needs a server change; new texts go through `server/src/texts.rs`, deviations into `docs/parity/server.md`. |
| 12 | medium | claude | Host function errors are hidden until the sandbox calls them | `asFunction` (`answer.go:60-63`) swallows the `Func` error; the sandbox sees `TypeError: object is not callable`. Name typos are `NameError` at runtime. | yes | A registry validated at build time would catch both. |
| 13 | medium | claude | Plain structs are rejected as return values | `convert.go:203` returns a wrap hint for any struct; consumers build `NamedTuple{FieldNames, Values}` by hand with no length check. | yes | `monty:"name"` tags already exist for wrappers and could drive a struct-to-named-tuple conversion. |
| 14 | medium | claude | Print targets receive chunks, not lines | With the default 5 ms flush interval a chunk can hold several lines or part of one; every consumer rebuilds line buffering. | yes | `print.go` has `PrintFunc`, `CollectString`, `CollectStreams`, no line adapter. |
| 15 | medium | codex | `Async` host work has no lifecycle | `future.go:25-33` starts a goroutine with no context; user work continues after the feed or session ends; no bound on unresolved futures. | yes | Additive fix. |
| 16 | medium | codex | `Pool.Close` does not wait for checked-out sessions | `internal/pool/pool.go:301` shuts idle workers only. Service shutdown that expects "all resources gone" is surprised. | yes | Ownership model is defensible; the gap is the missing `Shutdown`/`Wait` and docs. |
| 17 | medium | claude | Telemetry is process-global | `monty.Instrument` installs one `atomic.Pointer[Recorder]` (`internal/telemetry/recorder.go:41`); two components or parallel tests cannot configure telemetry independently. | yes | Mirrors upstream's global installation, which is why it was built this way. |
| 18 | medium | claude | Restore after drain loses host objects | A snapshot loaded into a fresh session has an empty `instanceStore`; method calls fail with the message at `answer.go:155`. | yes | `ClassInstanceOptions.ID` already allows pinning ids; re-registration is the missing piece. |
| 19 | medium | codex | Hand-written codec has no benchmarks or fuzzing | No `Benchmark*` or `Fuzz*` functions exist; the differential tests are the only guard. Every new field is edited in two representations. | yes | The codec's value (budgeted decode, validation) is real; the missing part is evidence and fuzz coverage. |
| 20 | medium | codex | `montypb` is public but documented as a test oracle | It is importable by consumers (and `tests/network/wire.go` already imports it). Its stability is undefined. | yes | Cannot move to `internal/` without breaking `tests/network`; see solution. |
| 21 | medium | codex | Release engineering is incomplete | No git tags, no changelog for this branch, CI runs no `-race`, no `govulncheck`, no fuzzing, no SBOM or provenance for the image. | yes | `ci.yml` has no `-race` step. |
| 22 | low | claude | Transport errors read like Python exceptions | `CrashedError.Error()` and `DisconnectError.Error()` prefix `RuntimeError:` (`errors.go:124`, `:139`). | yes | Deliberate parity with the TypeScript binding's error text; changing `Error()` is a parity deviation. |
| 23 | low | claude | No helper to run the server image | Consumers copy about 220 lines of `docker run`, port parsing, health wait and cleanup. | yes | `tests/network` has the same logic behind testcontainers. |
| 24 | low | codex | UUID generation ignores `crypto/rand.Read` errors | `classinstance.go:98-103` discards the error. | no | Since Go 1.24 `crypto/rand.Read` "never returns an error, and always fills b entirely"; it crashes the program on the legacy failure path. The module requires Go 1.25. Nothing to fix. |
| 25 | critical | codex | `[montygo-test]` Dependency and image are not reproducible | `go.mod` replaces montygo with `../montygo`; image is `monty-server:latest`; montygo has no tags. | yes | The library-side part is row 10. |
| 26 | critical | codex | `[montygo-test]` Continuous execution disables every liveness bound | Server turn, session and idle timeouts set to 0, `NoRequestTimeout`, `MaxSuspensions: 1 << 40`, endless feed. | yes | Root causes in the library are rows 2, 3, 6, 11. |
| 27 | high | codex, claude | `[montygo-test]` `monty.All()` on the records object | `extensions.go:53-57`. | yes | Same as row 7. |
| 28 | high | codex | `[montygo-test]` Session semantics contradict the README | README says globals are not kept between runs, but the session is reused, so module globals persist. | yes | Consumer docs; library behaviour is correct. |
| 29 | high | codex | `[montygo-test]` Forced stop is not context-bounded | `runner.go:109-112` cancels then waits unconditionally on `run.finished`. | yes | Library cause is row 2. |
| 30 | high | codex | `[montygo-test]` Stale-container cleanup is unsafe on shared daemons | `parseStale` treats malformed labels as stale and trusts the local PID table; cleanup uses `context.WithoutCancel` with no timeout. | yes | Consumer only; row 23 would remove the code. |
| 31 | medium | codex | `[montygo-test]` Client, server and cgroup capacity are not aligned | `MaxProcesses: 2` for one session, server default 64 sessions, 512 MiB, 64 PIDs, loopback port open to other local processes. | yes | Consumer only. |
| 32 | medium | codex | `[montygo-test]` No end-to-end test of the deployed topology | Runner tests use wasm; no test of the image, drain, mismatch or cleanup. | yes | `tests/network` in montygo covers the server; the consumer's own lifecycle is untested. |
| 33 | medium | codex | `[montygo-test]` REPL scanner goroutine and unflushed partial output | `repl.go` scanner cannot be interrupted; `Output.partial` is never flushed at feed end. | yes | Library part is row 14. |
| 34 | medium | codex | `[montygo-test]` SQLite URI built by concatenation; `last`+`insert` is not atomic | `store.go:39-40`. | yes | Consumer only. |
| 35 | low | codex | `[montygo-test]` Repository hygiene | Not a git repository, no `.gitignore` or license, `print("blah")` in `script.py`, `db.sqlite` committed alongside code. | yes | Consumer only. |

Priority legend: critical = memory or liveness hole for untrusted code; high = blocks a real embedding pattern; medium = ergonomics, observability, release quality; low = cosmetics or convenience.

## 2. Solutions for confirmed library problems

All solutions keep the Monty wire protocol version 3 unchanged. Where a solution changes behaviour that upstream defines, the deviation MUST be recorded in `docs/parity/`.

### 1. Bound the frame queue

Replace `frameQueue`'s append-only slice with a byte-bounded queue and make the reader wait.

```go
type frameQueue struct {
    mu       sync.Mutex
    frames   [][]byte
    bytes    int
    maxBytes int           // Options.MaxPendingFrameBytes, default 64 MiB
    err      error
    notify   chan struct{} // consumer wake
    space    chan struct{} // producer wake
}

func (q *frameQueue) push(ctx context.Context, frame []byte) error // blocks while bytes+len(frame) > maxBytes
```

- `pumpFrames` calls `push` with a context cancelled by `Kill`/`Close`, so a blocked reader never outlives the worker.
- One frame larger than `maxBytes` is still accepted (the frame limit is already 256 MiB); the bound is on the backlog, not on a single frame.
- Backpressure is safe because the protocol is strictly alternating: the worker blocks on its stdout pipe, which is the same effect the OS pipe buffer already has today for the native backend once the Go side stops reading.
- Release the backing array when the queue drains (`q.frames = nil`), and report depth and bytes through the existing `Metrics` interface (`internal/pool` `Metrics` gains `PendingFrames(bytes int64)`).
- Test: a feed that prints in a tight loop against a `PrintTarget` that blocks; assert the process RSS stays flat and the worker is killed on deadline.

### 2 and 3. Out-of-band abort, and graceful cancel while the worker is paused

Split session state into two locks: `s.mu` keeps serializing turns; a new `s.life` (mutex plus fields) holds the abort state so `Abort` never waits on a turn.

```go
// Interrupt ends the running feed. When the worker is paused on a host call
// it sends AbortFeed(reason) and the session stays usable; otherwise it kills
// the worker after grace and poisons the session.
func (s *Session) Interrupt(ctx context.Context, reason error) error

// CloseNow kills the worker without waiting for a turn and marks the session closed.
func (s *Session) CloseNow() error
```

Mechanics:

- `answerer` records the active host callback's `cancel` and the turn's `cancel` in `s.life` before it invokes host code, and clears them after.
- `Interrupt` cancels the callback context, then waits up to `grace` (default 100 ms) for the answerer to observe the cancellation and send `AbortFeed` with `KeyboardInterrupt` (or `reason` mapped through `exceptionParts`). `FeedRun` returns a `*RuntimeError` and the session is not poisoned.
- If no callback is active (the worker is executing Python), `Interrupt` calls `co.Abandon()` after `grace`, which kills the worker; the session is poisoned as today.
- `CloseNow` sets `closed` under `s.life`, calls `co.Abandon()`, and returns without touching `s.mu`; the running `FeedRun` returns `ErrSessionClosed`.
- Row 3: the same path is used when the feed context is cancelled while a callback is pending: send `AbortFeed` instead of killing. This changes the outcome of `cancel_test.go` "cancelling while awaiting a host future poisons the session": the session now survives and the error is a `KeyboardInterrupt` `*RuntimeError`. Record it in `docs/parity/tests.md` as a deliberate deviation from `@pydantic/monty`, and keep the old behaviour behind `FeedOptions.KillOnCancel bool` for parity tests.
- Tests: abort during Python execution, during a host callback, during a print, during WebSocket read and write; `CloseNow` from another goroutine while `FeedRun` is blocked.

A run handle (claude item 7) becomes a thin wrapper once `Interrupt` exists:

```go
func (s *Session) Go(ctx context.Context, code string, opts *FeedOptions) *Run
func (r *Run) Done() <-chan struct{}
func (r *Run) Wait() (any, error)
func (r *Run) Interrupt(reason error) error
```

### 4. Count retiring workers

Add explicit counters to `internal/pool.Pool`: `starting`, `active`, `idle`, `retiring`. `MaxProcesses` bounds `starting+active+idle+retiring`. `release` moves the slot to `retiring` and decrements only when `shutdownWorker` returns; `acquire` waits on the same condition it waits on today. Replace one-goroutine-per-retirement with a bounded reaper (`min(MaxProcesses, 8)` goroutines fed by a channel). For discarded workers use `Kill` (native, wasm) or `CloseNow`-style drop (WebSocket) instead of a graceful close, because the session is already poisoned. Expose `retiring` through `Pool.Size()` and a `WorkersRetiring` gauge.

### 5. Detect lost sessions early and keep the close reason

- `wsWorker.read`: on error, extract `websocket.CloseError` and store `code` and `reason` in the worker before closing `done`.
- `wsWorker.Send`: check `w.done` as well as `w.stop`; return an error carrying the stored close reason.
- `DisconnectError` gains `Code int` and `Reason string`; `Error()` renders `closed by server (1008): idle timeout of 60s exceeded` when a close frame was seen.
- Add `Session.Done() <-chan struct{}` and `Session.Err() error`, driven by a transport-level `Worker.Done()`; the pool's `slot` forwards it.
- Add one classifier: `var ErrSessionLost = errors.New("monty: session lost")`, and make `CrashedError`, `DisconnectError`, `ShutdownError`, `ProtocolError` and the closed-session error implement `Is(ErrSessionLost)`. Also wrap the bare `context.Canceled` of a cancelled feed as `&ProtocolError{cause: ErrTurnCancelled}` so `errors.Is(err, ErrSessionLost)` holds there too (row 2 removes most of that path anyway).

### 6. Suspension budget: fix the docs, add unlimited

- `README.md:244` and the `ResourceLimits` doc comment: "per session, reset by `LoadSession`/`LoadSnapshot`".
- Add `const Unlimited uint64 = math.MaxUint64` accepted by `MaxRecursionDepth` and `MaxSuspensions`. In `checkoutConfig`, `Unlimited` sends `MaxSuspensions = math.MaxUint64` on the wire (the parent counts, so no worker change), and for recursion sends the value unchanged. The server ceiling still clamps recursion.
- Change the abort text to `suspension limit N exceeded for this session` only if upstream's text is not asserted by parity tests; it is (`internal/pool/websocket_test.go`), so keep the text and fix only the documentation.
- Document a "long-running scripts" recipe: `MaxSuspensions: monty.Unlimited`, finite `RequestTimeout` sized to one work unit, server `--turn-timeout` above it, and session rotation on `ErrSessionLost`.

### 7. Explicit method exposure

- Change every example under `examples/classes` and the README to `monty.Names(...)`.
- Add `func Expose[T any](v T) ClassInstanceOptions` that builds `AllowedMethods` from the exported methods of the interface type `T` at compile time (via reflection on `reflect.TypeFor[T]()`), so widening the surface requires editing the interface.
- Keep `All`; document in `docs/architecture/session.md` that it is for prototypes.

### 8. Bound the instance store

Add `CheckoutOptions.MaxHostObjects int` (default 10 000). `instanceStore.put` returns `&ResourceError{Kind: "host objects", Limit: n}` when exceeded, which the answerer raises inside the sandbox as `RuntimeError: host object limit N exceeded`. Expose current and peak counts through `Session.Stats()`. Document session rotation as the reclamation boundary. Explicit release is not attempted: Python may still hold the id.

### 9. Freeze the globals

- `PythonExceptionNames`: make the internal read use a private `knownExceptions` set built in `init`; keep the exported map as a copy for compatibility and document it as read-only. Add `func KnownExceptionNames() []string`.
- `NotHandled`: compare against a private sentinel; keep the exported variable pointing to it and document that reassigning it has no effect.
- `All`: change to `func All() AttrPolicy` in the next breaking release; until then keep the variable, since `AttrPolicy` is a value type and copies do not affect the library.

### 10. Separate the version dimensions

```go
const (
    BindingVersion  = "0.1.0"      // this module's release
    MontyVersion    = "0.0.23"     // upstream release tracked (alias: Version, deprecated)
    UpstreamRev     = "f8acf4fa"
    ProtocolVersion = 3
)
```

- `IMAGE_TAG` becomes `$(BindingVersion)` and the image label `io.montygo.monty-rev` keeps the upstream pin; `docker-push` also tags `$(BindingVersion)-$(git rev-parse --short HEAD)`.
- `server/src/version.rs` reads `SERVER_VERSION` from `Cargo.toml`, which MUST equal `BindingVersion`; add a Go test that compares the two, like `monty_rev_matches_lockfile`.
- Create `versions.json` at the root as the single machine-readable pin source (`monty_version`, `upstream_rev`, `protocol_version`, `binding_version`); add a `make check-pins` target that greps every file in the upgrade checklist against it and fails on drift; run it in CI.
- Tag the repository with `v0.1.0` and publish the compatibility matrix in the README.

### 11. Publish server timeouts

Add `GET /info` to `monty-server` returning JSON: `{"version", "monty_rev", "protocol_version", "limits": {"idle_timeout_s", "keepalive_s", "session_timeout_s", "turn_timeout_s", "max_duration_s", "max_memory_bytes", "max_recursion_depth"}}`. Keep `GET /` as the text page. Add `monty.ServerInfo` and `func FetchServerInfo(ctx, WebSocketOptions) (*ServerInfo, error)` next to `CheckWebSocketHealth`. Document `/info` in `docs/architecture/server.md` and list it as an extension in `docs/parity/server.md`.

### 12. A host registry validated at build time

```go
type Host struct{ /* names → Function | wrapper */ }
func NewHost() *Host
func (h *Host) Func(name string, fn any) error      // runs monty.Func now
func (h *Host) Object(name string, v any, opts ClassInstanceOptions) error
func (h *Host) Stubs() string                       // "def sleep(ms: int) -> None: ..."
```

`CheckoutOptions.Host *Host` registers the objects once per session; `FeedOptions.ExternalLookup` entries override by name. `asFunction` returns the `Func` error to the sandbox as `TypeError: <name>: <reason>` instead of swallowing it. `Stubs()` feeds `TypeCheckStubs` so typos fail at type-check time.

### 13. Structs as named tuples

Add `func AsNamedTuple(v any) NamedTuple` using the existing `monty:"name"` tag rules (exported fields in declaration order, tag overrides the snake_case name), and `func NewNamedTuple(typeName string, pairs ...Pair) (NamedTuple, error)` that validates duplicate and empty names. Keep the wrap hint for untagged structs to avoid silently changing conversions.

### 14. Line-based print target

```go
func Lines(fn func(stream Stream, line string) error) PrintTarget
```

Buffers per stream, emits complete lines, and flushes the remainder when the feed ends. The flush hook is `printTarget.finish()`, which `Session.drive` already calls at the turn boundary; add an optional `Flush() error` interface that `Lines` implements.

### 15. Async with lifecycle

Add `func AsyncContext(ctx context.Context, fn func(context.Context) (any, error)) *Future`; the answerer derives `ctx` from the feed context so cancelling the feed cancels the work. Add `CheckoutOptions.MaxPendingFutures` (default 1000) enforced in `answerer.answerFunctionCall`; on session abort, settle every pending future with `ErrSessionLost`. Document that `Async` cannot be cancelled.

### 16. Pool shutdown that waits

Add `func (p *Pool) Shutdown(ctx context.Context) error`: marks the pool closed, rejects new checkouts, calls `CloseNow` on every checked-out session when `ctx` expires, and waits for `total == 0`. Keep `Close` as is and document both in `docs/architecture/pool.md`.

### 17. Per-pool telemetry

Add `Options.Telemetry *TelemetryComponents`. `internal/telemetry.Recorder` becomes a value carried in `pool.Config` instead of `current atomic.Pointer`; `Instrument` sets the default used when `Options.Telemetry` is nil. Tests in `telemetry_test.go` stop needing a child process once the recorder is per pool.

### 18. Re-register host objects on restore

With row 12's `Host` and pinned `ClassInstanceOptions.ID`, `LoadSnapshot` and `LoadSession` accept `Host`; the wrappers are put into the fresh session's `instanceStore` under their pinned ids before `Load` is sent. Without pinned ids the current error stays. A `monty.Reconnect(ctx, pool, dump, host)` helper then implements the drain recovery loop.

### 19. Codec evidence

Add `BenchmarkDecodeEvent`/`BenchmarkEncodeRequest` against `montypb` for a large print event, a deep value and a 16 MiB feed; add `FuzzDecodeEvent` and `FuzzDecodeRequest` seeded from the differential test corpus; add a test that every field number in `proto/monty/v1/monty.proto` appears in `internal/wire` (parse the proto, grep the codec). Run fuzzing for 30 s in CI.

### 20. Decide `montypb`'s status

Keep it public but document it in `docs/parity/api.md` as "generated wire types, no stability promise beyond `ProtocolVersion`", because `tests/network` and any raw-protocol consumer need it. Move `generate.go` and the `montypb` package doc to say so.

### 21. Release engineering

- CI: add `go test -race` for the root module (Linux only, native and wasm), `govulncheck ./...`, `cargo audit` in `server/`, the fuzz step from row 19, and `make check-pins` from row 10.
- Tag `v0.1.0`; add `changelogs/v0.1.0.md`.
- Image: attach an SBOM and provenance with `docker buildx build --sbom=true --provenance=true` in `docker-push`; copy the license files of the Rust dependency tree into the image with `cargo about` at build time so recipients of the `scratch` image receive them.

### 22. Error text of transport errors

Keep `Error()` as is: the `RuntimeError:` prefix is asserted by ported parity tests and matches `@pydantic/monty`. Add `func (e *DisconnectError) Unwrap() error` returning `ErrSessionLost` (row 5) so Go code classifies without parsing text, and log the `Message` field rather than `Error()` in examples.

### 23. Server runner module

Create `montyserver/` as a separate module (`github.com/asalimonov/montygo/montyserver`) depending on `github.com/moby/moby/client` only: `Run(ctx, Options{Image, Env, Limits}) (*Server, error)` with `URL()`, `Info()`, `Logs()`, `Close(ctx)`. It reuses the health wait and port discovery from `tests/network/pool.go`, which then imports it. Container ownership uses a random instance id label, and cleanup is opt-in.

## 3. Consumer-only findings

Rows 25 to 35 belong to `montygo-test`. The library changes that remove the underlying workarounds are rows 2, 3, 5, 6, 7, 11, 12, 14 and 23. The remaining consumer items (session semantics in its README, forced-stop deadline, stale-container ownership, capacity alignment, SQLite URI, hygiene) need changes in that repository and are not repeated here.

## 4. Suggested order

1. Rows 1, 2, 3, 4, 5: safety and durability blockers; every embedding application works around them today.
2. Rows 6, 7, 8, 9, 10: limits, exposure, retention, globals, versions.
3. Rows 11, 12, 13, 14, 15, 16, 17, 18: ergonomics and long-session support.
4. Rows 19, 20, 21, 22, 23: verification, release and convenience.
