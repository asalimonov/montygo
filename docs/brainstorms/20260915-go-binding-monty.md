# Brainstorm: Go binding for Monty (`montygo`) with parity to `@pydantic/monty`

Date: 2026-09-15
Status: DONE (target: `20260915-go-binding-monty-target.md`)

## 1. Initial input (verbatim)

> you need to design and implement golang binding to monthy with 100% functional parity of official binding to TypeScript
> You need to design a golang packge which implements binding to pydantic monthy (checkouted there ../monthy).
> It should containt semantically the same tests as for typescript. You can use any 3rd party libraries for better and simpler port, you can use WASM technologies for some cases.
> You can also find python bindings in ../monthy
> After successfull porting you need to write ./changelogs/v0.0.23.md (as the latest release of monthy) where describe the current state and what was added.
> monthy has MIT license and gomonthy is empty dir (project).

Corrections to the input discovered during research:

- The upstream checkout is `../monty` (not `../monthy`). Project name upstream: **Monty** (pydantic/monty).
- `montygo` is an empty directory, not a git repository. No `project.txt`, no `./architecture`, no README.
- The brainstorm skill is read-only for the codebase. Implementation is a follow-up (`/go-plan` then `/exec-plan`). The changelog `./changelogs/v0.0.23.md` is an implementation deliverable and is specified in the target document.

## 2. Upstream facts (derived)

| Fact | Value | Source |
|---|---|---|
| Upstream version | `0.0.23` (tag `v0.0.23`, HEAD `f8acf4fa`) | `Cargo.toml`, `git tag` |
| License | MIT | `LICENSE` |
| Rust MSRV | 1.95 (local rustc is 1.93.0) | `Cargo.toml` `rust-version` |
| Local toolchain | Go 1.25.5 darwin/arm64, cargo 1.93, node 24.13, uv 0.10 | shell |
| Official bindings | Python (`pydantic-monty`, PyO3 over `monty-pool`), JS (`@pydantic/monty`, napi-rs over `monty-pool` + TS drive loop; wasm/browser path over a WIT component) | `CLAUDE.md` |
| Community Go binding | `github.com/ewhauser/gomonty` — purego + bundled Rust C-ABI shared library, in-process `Runner` API (compile/run, dump/load, os callbacks). No pool/session/snapshot-cursor/mount/class-instance/type-check surface. Experimental, 10 stars. | README via web |
| Wire protocol | protobuf `monty.v1` (`crates/monty-proto/proto/monty/v1/monty.proto`, 824 lines), 4-byte LE length prefix, strict request/reply alternation, designed so "a parent or child can be implemented in any language" | `monty-proto/README.md`, proto header |
| Worker | `monty subprocess` (framed protobuf on stdin/stdout), spawned with an empty environment; crash = EOF without `FatalError`; OOM exit code classified as `MemoryError` | `pool-architecture.md` |
| Execution model | REPL-only: pool → checkout (dedicated worker) → feed/resume turns; no in-process execution API in either official binding | `pool-architecture.md` |
| WASM path | `monty-wasm-runtime`: WASI 0.2 **component** (wasm32-wasip1 core + jco preview1 adapter), WIT interface with flat node arenas; used only by the browser/JS entry | `CLAUDE.md`, `wit/runtime.wit` |

### 2.1 Architecture of `@pydantic/monty` (the parity target)

```
                         @pydantic/monty (TypeScript)
   ┌───────────────────────────────────────────────────────────────┐
   │ pool.ts (Monty)  session.ts (MontySession + drive loop)       │
   │ classInstance.ts print.ts mount.ts/mountDir.ts errors.ts      │
   │ telemetry.ts (OTel bridge)   binary.ts (monty bin resolution) │
   └───────────────┬───────────────────────────────┬───────────────┘
                   │ napi (NativePool/NativeSession)│ WorkerTransport (wasm)
   ┌───────────────▼───────────────┐   ┌───────────▼─────────────────┐
   │ crates/monty-js/src (Rust)    │   │ ts/worker/* (TS pool over   │
   │  → monty-pool (tokio)         │   │   WIT component in Worker)  │
   │  → monty-proto (protobuf)     │   │  → monty-wasm-runtime       │
   └───────────────┬───────────────┘   └───────────┬─────────────────┘
                   │ stdin/stdout frames            │ WIT dispatch
          ┌────────▼────────┐               ┌───────▼────────┐
          │ monty subprocess│               │ Child state    │
          │ (monty binary)  │               │ machine (wasm) │
          └─────────────────┘               └────────────────┘
```

The TS layer owns: the drive loop (answering FunctionCall/OsCall/NameLookup/ResolveFutures events), value conversion JS↔MontyObject, the class-instance store (uuid → wrapper), print collectors, mount policy objects, error classes, snapshot cursor objects (`FunctionSnapshot`/`NameLookupSnapshot`/`FutureSnapshot`/`MontyComplete`). The Rust layer owns: worker lifecycle, framing, timeouts, `max_suspensions`, duration backstop, mount servicing (`monty-fs`), telemetry recording.

For Go there is no napi. The Go binding must therefore re-implement **both** layers: the pool/protocol parent (monty-pool equivalent) and the drive loop / conversion layer (session.ts equivalent).

## 3. Thoughts (running log)

- The protocol is explicitly language-agnostic protobuf over stdio. A pure-Go parent is the natural design and matches how both official bindings work (subprocess isolation, crash recovery). This gives Go crash isolation for free and needs no cgo.
- The WASM path upstream is a WASI 0.2 *component*. Go runtimes (`wazero`) execute core wasm modules, not components; driving the component would require either a component→core transpile step or re-targeting the `monty-wasm-runtime` crate. This is not required for parity of *behaviour* (the wasm entry exposes the same API) and is a large detour. Candidate for "NOT CONSIDERED / postponed".
- The existing `ewhauser/gomonty` is an in-process FFI design that upstream itself deprecated for its own bindings (no crash isolation). It is not a parity base; it is prior art only.
- Parity scope will be defined by the TS public API + `__test__/*.spec.ts` inventories (delegated). JS-only tests (napi/wasm word size, entrypoint exports, docs snippets, OpenTelemetry Node SDK wiring) need a per-item decision: port semantically, replace with a Go-idiomatic equivalent, or mark as JS-only.
- `monty` worker binary provisioning is a design fork for Go: (a) require `MONTY_BIN`/PATH, (b) download platform binary from npm/PyPI at build/test time, (c) `go:embed` a binary per platform in sub-modules (npm-style optional deps have no Go analog), (d) build from `../monty` with cargo. This is the Go analog of the "binary resolution is part of the trust boundary" rule.

## 4. Baseline fork: `v0.0.23` tag vs local `main` (HEAD `f8acf4fa`)

The local checkout is 25 commits ahead of the `v0.0.23` tag (released 2026-09-05). Relevant deltas:

| Area | `v0.0.23` tag | HEAD (`main`) |
|---|---|---|
| `PROTOCOL_VERSION` / `MIN_SUPPORTED` | 2 / 2 | 3 / 3 (a v2 parent is rejected with `FatalError`) |
| `Print` event | `stream` + `text` (one run) | `repeated PrintSegment segments` (tags 1,2 reserved) |
| `Feed.cwd` | absent | present (CWD support #828) |
| `FunctionCall.allow_eager_await` | absent | present (Eager awaits #833) |
| TS surface | — | `+ cwd` feed option, eager-await in `resumeAuto`, `callbackContext.ts` (Node `AsyncLocalStorage` context for callbacks), telemetry deltas, `os_paths.ts` test helper, ~+400 test lines |
| Prebuilt worker | `@pydantic/monty-<platform>@0.0.23` (npm, 5 targets), `pydantic-monty-runtime==0.0.23` (PyPI) | none; must `cargo build -p monty-runtime` with rustc ≥ 1.95 (local 1.93; `rustup` offers 1.98.1) |

Upstream v0.0.23 release notes ("What's Changed" bullet list, "Full Changelog" link, "Contributors") — model for `./changelogs/v0.0.23.md`:
- Debounce print output in the worker (#809)
- Docs improvements (#813), snapshotting on alternatives page (#817), doc links (#820)
- `itertools` adaptors `accumulate`, `batched`, `zip_longest` (#779)
- `str.format` (#775)
- refactor telemetry bridge to receive language-specific otel components (#815)
- bump to 0.0.23 (#823)

### Q1 (asked): which baseline is the parity target?

**A1 (user):** Local `main` (HEAD `f8acf4fa`). Consequences: protocol version **3**; worker must be built from `../monty` (`cargo build -p monty-runtime`, rustc ≥ 1.95); TS API + `__test__` at HEAD are the parity checklist; the changelog `v0.0.23.md` describes the Go port state aligned with upstream `0.0.23` + the post-tag protocol-3 changes (must say so explicitly).

Appendices written from delegated research: `-appendix-a-ts-api.md` (TS public API), `-appendix-b-python-and-protocol.md` (Python surface + protocol rules), `-appendix-c-ts-tests.md` (489 TS tests, portability), `-appendix-d-pool-spec.md` (monty-pool parent semantics).

## 5. Transport fork: how Go reaches the interpreter

Facts:
- `crates/monty-runtime` has a `standalone` cargo feature (default on) that owns the REPL/CLI stack (`rustyline`, `anstream`). With `--no-default-features` the `monty` binary is *only* `monty subprocess`: `std::io` stdin/stdout, `std::process::ExitCode`, no tokio, no threads, no process spawning. Same deps as `monty-wasm-runtime` (`monty-proto` worker feature + `monty-alloc`), which CI already builds for `wasm32-wasip1`.
- Therefore `cargo build -p monty-runtime --no-default-features --target wasm32-wasip1` is very likely to produce a **core WASI preview-1 module** whose stdin/stdout speak the same protobuf frames. Unverified locally (rustc 1.93 < MSRV 1.95, no wasip1 target installed). Must be spiked in the plan phase.
- Go's `wazero` (v1.12.0, pure Go, no cgo) runs wasip1 core modules with configurable stdin/stdout pipes, per-module memory limits (`WithMemoryLimitPages`), and `context` cancellation (`WithCloseOnContextDone`). It does **not** run WASI 0.2 components, so upstream's `monty-wasm-runtime` WIT component is not directly usable.

Options for the Go "worker backend":

| # | Backend | Crash isolation | Distribution | Perf | Parity with TS |
|---|---|---|---|---|---|
| A | Native `monty subprocess` (os/exec) | process boundary (same as node/python) | needs a platform binary: `MONTY_BIN`, `PATH`, cargo `target/`, or download | native | = `@pydantic/monty` node entry |
| B | `monty subprocess` compiled to wasip1, run in-process by wazero with piped stdio | wasm sandbox; trap/OOM = module close, classified like a crash; `requestTimeout` via ctx close | **one `go:embed` `.wasm` for all OS/arch**, zero install | ~2–5× slower, ~100–300 ms compile once per process (cacheable) | = `@pydantic/monty/wasm` entry (no mounts? — no: mounts are host-side in Go, so they *can* work here) |
| C | cgo/purego FFI to a Rust C-ABI crate (ewhauser/gomonty style) | none (in-process) | platform shared libs | fastest | none of the official bindings do this; rejected |

Recommendation: **A as the primary, B as a second `Transport` implementation behind the same pool**, sharing 100 % of the Go protocol/drive-loop code (only "spawn / read / write / kill / exit-status" differ). B is the Go analog of the TS wasm entry and gives `go get` users a zero-install path; it is gated on the wasip1 spike. C rejected.

**A2 (user):** Subprocess + wazero wasm. Native `monty subprocess` is the primary backend; an embedded wasip1 worker under wazero is the secondary backend behind the same pool and drive loop. The wasm backend is gated on a build spike (`cargo build -p monty-runtime --no-default-features --target wasm32-wasip1`).

### Q3 (asked): may the toolchain be updated locally (rustup stable → 1.98.1, add `wasm32-wasip1`) to run the spike and build a protocol-3 worker during the brainstorm?

**A3 (user):** Yes — update stable and add `wasm32-wasip1`; spike running in the background (log: scratchpad `spike.log`).

## 6. Worker provisioning fork (native backend)

TS chain: explicit `binaryPath` → `MONTY_BIN` → bundled `@pydantic/monty-<triple>` package → `PATH` → cargo `target/{debug,release}/monty` walking up 6 levels. Python: explicit → `MONTY_BIN` → sysconfig scripts dir → `PATH` → `<repo>/target/{debug,release}/monty`.

Go has no analog of npm optional platform packages or PyPI wheels. Options:

| # | Option | Notes |
|---|---|---|
| P1 | Mirror the chain minus the package step: explicit `BinaryPath` → `MONTY_BIN` → `PATH` → cargo workspace walk (dev) | zero magic; users install `monty` via `uv tool install pydantic-monty-runtime` / `npm` / cargo |
| P2 | P1 **plus** automatic fallback to the embedded wasm backend when no native binary is found | `monty.New()` always works after `go get`; native used when present. Needs a `Backend` option (`Auto`, `Native`, `Wasm`) so tests can pin one |
| P3 | Downloader for the npm platform tarball at first use | needs network at runtime, unsigned artefacts, and no protocol-3 release exists yet; rejected for now |
| P4 | `go:embed` native binaries per GOOS/GOARCH | 5 × 10–20 MB in the module; rejected |

Recommendation: **P2** with the resolution order `BinaryPath` → `MONTY_BIN` → `PATH` → cargo walk → embedded wasm; `Backend: Native` makes a missing binary an error whose text mirrors TS: `could not locate the monty binary (tried: …). Install pydantic-monty-runtime, set MONTY_BIN, or pass BinaryPath.`

**A4 (user):** Chain + wasm fallback (P2). Resolution: `BinaryPath` → `MONTY_BIN` → `PATH` → cargo target walk → embedded wasm; `Backend: Auto | Native | Wasm`.

Environment note (build spike): rustup stable is now 1.98.1 with `wasm32-wasip1`. Host linking fails with the default `MacOSX27.0.sdk` (beta; `ld` reports `unknown architecture arm64e.x1-macos` in its `.tbd` stubs). `SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk` links fine. Any Makefile/CI step in `montygo` that builds the worker on macOS MUST document this (`SDKROOT` override), or the implementer will hit it again.

## 7. Value mapping fork (Go ↔ `MontyObject`)

TS maps dict→`Map`, tuple→`Array+__tuple__`, set/frozenset→`Set`, int→`number|BigInt`, bytes→`Buffer`, datetime→marker objects. Go has no ordered map, no tuple, no set, no bigint literal and a strongly typed `time.Time`. Choices:

| Python | Go output (proposed) | Go inputs accepted |
|---|---|---|
| `None` | `nil` | `nil`, typed nil pointers |
| `bool` | `bool` | `bool` |
| `int` (≤ i64) | `int64` | any Go int/uint kind (uint64 > MaxInt64 → big) |
| `int` (big) | `*big.Int` | `*big.Int`, `big.Int` |
| `float` | `float64` | `float32`, `float64` |
| `str` | `string` | `string` |
| `bytes` | `[]byte` | `[]byte` |
| `list` | `[]any` | any slice/array via reflect |
| `tuple` | `monty.Tuple` (`[]any` named type) | `monty.Tuple` |
| `dict` | `*monty.Dict` (insertion-ordered, any hashable key) | `*monty.Dict`, `map[string]any` and other `map[K]V` (Go map order undefined → sorted by key for determinism) , struct? (no) |
| `set` / `frozenset` | `*monty.Set` / `*monty.FrozenSet` (ordered) | same types; `map[K]struct{}` (sorted) |
| `date`/`datetime`/`time`/`timedelta`/`timezone` | `monty.Date`, `monty.DateTime`, `monty.Time`, `monty.TimeDelta`, `monty.TimeZone` structs mirroring the TS markers, with `time.Time`/`time.Duration` helpers | the structs; `time.Time` → `DateTime` (aware when `Location` has a fixed offset ≠ Local? — decide), `time.Duration` → `TimeDelta` |
| `Ellipsis` / `NotImplemented` | `monty.Ellipsis` / `monty.NotImplemented` sentinels | same |
| exception value | `monty.Exception{ExcType, Message}` | same; also Go `error` values? (only when explicitly raised via callbacks) |
| `type` | `monty.Type{Name, ID, Origin, IsDataclass, Attrs}` | builtin `monty.Type{Name}` only |
| builtin function | `monty.BuiltinFunction(name)` | same |
| `pathlib.Path` | `monty.Path(string)` | same |
| file handle | `*monty.FileHandle` | same |
| named tuple | `monty.NamedTuple{TypeName, Fields, Values}` | same |
| class instance | original Go object (identity via store) or `*monty.ClassProxy` | `*monty.ClassInstance` / `*monty.ClassType` wrappers, `*monty.ClassProxy` |
| repr fallback / cycle | `string` (as TS/Python) / `monty.Cycle` | rejected as input |

Alternative (V2): output `map[string]any` when all dict keys are strings — rejected: return type would depend on data.

**A5 (user):** `*monty.Dict` always for outputs; inputs accept `*monty.Dict`, `map[string]any`, other Go maps (sorted keys). Sets likewise `*monty.Set`/`*monty.FrozenSet`.

## 8. Host-function calling convention fork

TS: any entry in `externalLookup` that is a JS function is called as `fn(...args, kwargsBag?)`; a returned Promise becomes a sandbox future (or is eagerly awaited when `allowEagerAwait`); a thrown `Error` crosses as the Python exception named by `error.name` (else `RuntimeError`). Go has no Promises, no untyped variadic calls, and errors are values. Options:

| # | Shape | Sync call | Async (future) | Errors |
|---|---|---|---|---|
| F1 | Interface only: `type Function interface { Call(ctx, args []any, kwargs *Dict) (any, error) }` | implement `Call` | return `*monty.Future` from `Call` | return `error`; `*monty.RaisedError{ExcType, Message}` picks the Python type, else `RuntimeError` |
| F2 | Reflect adapter: plain Go `func` values in `ExternalLookup` are invoked via `reflect`, args converted to parameter types (trailing `monty.Kwargs` param receives kwargs; trailing `error` return crosses as exception; a `*monty.Future` return registers a future) | `func(a int, b string) (int, error)` | `func(url string) *monty.Future` | `error` |
| F3 | F1 + F2 (interface is the primitive; `monty.Func(fn any) Function` is the adapter and `ExternalLookup` applies it automatically to `reflect.Func` values) | both | both | both |

Async execution model: `monty.Async(func(ctx) (any, error)) *Future` runs the body in a goroutine; `resumeAuto`/`FeedRun` register `future(call_id)` when a `*Future` is returned (or await it directly when `allow_eager_await`). `ResolveFutures` waits on the first settled pending future, delivers every settled one (TS `Promise.race` + drain).

Recommendation: **F3**. Only-interface (F1) is verbose for tests that port 60+ TS lambdas; only-reflect (F2) hides the primitive that snapshots and `ClassInstance` methods need.

**A6 (user):** Interface + reflect adapter (F3).

### Spike results so far
- Native worker: `cargo build -p monty-runtime` (SDKROOT=26.5) OK → `../monty/target/debug/monty` (140 MB debug), protocol 3.
- wasip1: `cargo build -p monty-runtime --no-default-features --target wasm32-wasip1` FAILS: `monty-runtime` depends on `monty-fs` unconditionally → `fs-set-times` needs nightly `wasi_ext`. Upstream's `monty-wasm-runtime` deliberately depends only on `monty-proto` (worker) + `monty-alloc` + `monty-types`. Consequence: montygo must own a small Rust crate (`worker-wasm/`, MIT, ~150 lines, a port of `crates/monty-runtime/src/subprocess.rs`) that builds the stdio worker for wasip1. Spike continues with that crate.

## 9. Host-object (`ClassInstance` / `ClassType`) fork

TS policies are defined over JS object machinery (own enumerable props, prototype chain, statics, `constructor`). Go analogs:

| TS concept | Go analog (proposed) |
|---|---|
| instance | any Go value; `'all'` eager attrs = exported struct fields (pointer auto-deref) — names kept verbatim (`Balance`), no case conversion |
| lazy attrs | exported fields read on demand; plus optional `monty.AttrProvider` interface (`GetAttr(name string) (any, error)`) for computed attrs |
| methods, `'all'` | exported methods in the value's method set (pointer receiver when the instance is a pointer); called via reflect with converted args, trailing `monty.Kwargs` param, optional `error` return, `*monty.Future` return = async; optional `monty.MethodProvider` interface (`CallMethod(ctx, name, args, kwargs) (any, error)`) bypasses reflect |
| `_`-prefixed never exposed | unexported members are unreachable anyway; `_` rule kept for parity on explicit lists |
| `DENIED_NAMES`, `Object.prototype` members | no analog; not needed (documented as JS-only) |
| `convertValue(name, value)` | same hook, `func(name string, value any) (any, error)` |
| class (`ClassType`) | Go has no class object: `monty.NewClassType(name string, opts ClassTypeOptions)` with `Constructor any` (a Go func, called for `__call__` when `Init: true`), `Statics map[string]any` (static methods / class constants for `eagerAttrs`/`lazyAttrs`/`allowedMethods` policies), instance policies; default id per `reflect.Type` of the constructor's result (process-wide map, parity with the TS WeakMap) |
| `type(a) is type(b)` | class id derived from `reflect.Type` of the instance (pointer type normalised) |
| `MontyClassProxy` | `*monty.ClassProxy{Name, ID, IsDataclass, Attributes *Dict}`; Type marker → `monty.Type` value |

Options: **H1** reflect-based defaults + provider interfaces (above); **H2** interfaces only (every host object implements `AttrProvider`/`MethodProvider`); **H3** reflect only. Recommendation **H1** (mirrors F3).

**A7 (user):** Reflect + provider interfaces (H1).

## 10. Telemetry fork

TS `@pydantic/monty/node`: `instrumentTelemetry({tracer, meter, logger})`, `MontyInstrumentation` (OTel `Instrumentation` lifecycle), `flushTelemetry()`; recording happens in Rust (`monty-pool` `telemetry` feature) and is bridged to the host SDK. Span model: `session {script_name}` → `run code` → per-suspension `call {function_name}` / `os call {function}` / `name lookup {name}`; attributes include code, inputs, outputs (64 KB cap, `length_limit_exceeded`), exceptions, print text as log records; 13 metrics (`monty.pool.workers.live|idle|suspended`, `monty.pool.checkout.wait`, `monty.pool.worker.terminated`, `monty.pool.session.duration`, `monty.run.duration`, `monty.run.execution_time`, `monty.turn.duration`, `monty.run.suspensions`, `monty.ext.call.duration`, `monty.snapshot.bytes`, `monty.print.bytes`, `monty.wire.frame.bytes`). In Go the pool itself is Go code, so recording must be re-implemented directly against `go.opentelemetry.io/otel` (trace + metric + log APIs, v1.47).

Options:
| # | Scope | Effort |
|---|---|---|
| T1 | Full: spans + metrics + logs with the upstream names/attributes, `Instrument(TelemetryComponents{Tracer, Meter, Logger})` process-wide, `Flush()`, `context.Context` propagation into callbacks (Go analog of `AsyncLocalStorage`: the ctx passed to host functions carries the Monty span) | high |
| T2 | Spans + metrics only (logs for prints omitted) | medium |
| T3 | Hook interface only (`Observer` callbacks per event), no OTel dependency; OTel adapter later | low |
| T4 | Out of scope for the first release (documented in changelog) | none |

Recommendation: **T1**, because the user asked for 100 % functional parity and the span/metric contract is precisely specified; OTel-go is a pure-Go dependency. The 17 Node telemetry tests port semantically using the OTel-go SDK in-memory exporters (`sdktrace/tracetest`, `sdkmetric/metricdata`, `sdklog/logtest`).

**A8 (user):** Full telemetry parity (T1): spans + metrics + logs via OTel-go, `Instrument`, `Flush`, ctx propagation into callbacks.

## 11. Test strategy fork

The TS suite (Appendix C): 489 tests; ~380 portable, ~110 JS/Node-specific. The TS suite runs the *same* spec files under two backends (node napi, browser wasm) via `env.ts` skips. Go options:

| # | Strategy |
|---|---|
| S1 | Port each `*.spec.ts` to `<name>_test.go` 1:1 (same order, same titles as `t.Run` names), run the whole portable suite against **both** backends (`MONTY_TEST_BACKEND=native|wasm`, or a `t.Run("native")/t.Run("wasm")` matrix). JS-only tests get a Go-idiomatic replacement when one exists (`await using`→`defer Close`; `Buffer`→`[]byte`; `BigInt`→`*big.Int`; `Map`→`*Dict`; OTel Node SDK→OTel-go in-memory exporters; `AsyncLocalStorage`→`context.Context` values) and are otherwise listed in `docs/parity/tests.md` with a reason (packaging, docs runner, JS prototype hardening, 32-bit word size). `wasm_*.spec.ts` port against the wazero backend. |
| S2 | Port only the portable ~380 against the native backend; wasm backend gets the 18 `wasm_*` tests only. |
| S3 | Port the Python suite instead (950 tests, closer to Go's sync model). |

Framework: std `testing` + `github.com/stretchr/testify` (`require`) for readability of 489 ports; exact-string assertions instead of snapshot libraries (TS uses literal expectations). Tests need a worker: `MONTY_BIN` or the cargo `target/debug/monty` walk (`../monty` sibling) for native; the embedded wasm for wasm. The per-file "one pool per spec file" fixture becomes a `TestMain`/package-level pool with `t.Cleanup`.

Recommendation: **S1**. The task says "semantically the same tests as for TypeScript"; a two-backend matrix is what the TS suite itself does.

**A9 (user):** 1:1 port of the TS suite against both backends (S1).

### Spike result: Go ↔ native worker protocol round-trip (PASS)
Harness (scratchpad `harness/`): `protoc 34.1` + `protoc-gen-go v1.36.11` generated `monty.pb.go` (6126 lines) from `monty/v1/monty.proto` with no edits (`--go_opt=M…=<module>/montypb`). Against `../monty/target/debug/monty subprocess` (empty env), the sequence `Configure{protocol_version:3}` → `Ok`; `Feed("print('hi'); x = 21")` → `Print[stdout "hi\n"]`, `Complete(None)`; `Feed("x * 2 + fetch(1)")` → `FunctionCall{fetch, [1], call_id 0}`; `ResumeCall{0, Return(100)}` → `Complete(142)`; `Dump` → 188 bytes; `Shutdown` → `Ok`, exit 0. Whole run 60 ms (debug binary). `total_execution_micros` reported on turn-enders (0 on control acks).

## 12. Module identity and repository layout fork

| # | Module path | Root package | Notes |
|---|---|---|---|
| L1 | `github.com/speckzzz/montygo` | `monty` (`import "github.com/speckzzz/montygo"` → `monty.New(...)`) | directory name stays `montygo`; package name mirrors upstream (`@pydantic/monty`, `pydantic_monty`) |
| L2 | `github.com/speckzzz/montygo` | `montygo` | package name = dir name, longer identifiers (`montygo.New`) |
| L3 | `github.com/speckzzz/montygo/monty` | `monty` | root module with sub-package; leaves the module root for docs/tooling |

Proposed layout (for L1):
```
montygo/
  go.mod                      module github.com/speckzzz/montygo   (go 1.25)
  monty.go pool.go session.go snapshot.go value.go dict.go classinstance.go print.go mount.go errors.go options.go telemetry.go binary.go   (package monty)
  montypb/                    generated protobuf (package montypb), go:generate from ../monty proto (vendored copy under proto/)
  proto/monty/v1/monty.proto  vendored schema (MIT, with upstream commit hash)
  internal/wire/              framing (LE u32), decode budget, pre-send checks
  internal/worker/            Transport interface, subprocess backend, wazero backend
  internal/mountfs/           host-side mount table (port of monty-fs routing + overlay)
  internal/wasm/monty.wasm    embedded wasip1 worker (go:embed)
  worker-wasm/                Rust crate building monty.wasm (path deps on ../monty or git pin)
  docs/architecture/ docs/brainstorms/ docs/parity/
  changelogs/v0.0.23.md
  Makefile                    build-worker (native, SDKROOT note), build-wasm, generate, test-native, test-wasm
```

**A10 (user):** module path `github.com/asalimonov/montygo`. Root package name not stated — **assumed `monty`** (L1 layout otherwise unchanged); protobuf sub-package `montypb`.

### Spike result: Go ↔ wasip1 worker under wazero (PASS)
Crate `monty-wasi-worker` (scratchpad; `main.rs` = global `LimitedAllocator` + verbatim copy of `crates/monty-runtime/src/subprocess.rs`; deps `monty-proto[worker]`, `monty-alloc[exit-code]`, `monty-types`) builds with `cargo build --release --target wasm32-wasip1` in 3 m 12 s → `monty-wasi-worker.wasm` **18 MB** (includes the `ty` type checker). Under wazero v1.12 (compiler engine, WASI preview 1, stdin from a byte reader, stdout to an `io.Pipe`, empty env, args `monty subprocess`): identical event sequence to native (`Ok`, `Print`, `Complete(None)`, `FunctionCall fetch`, `Complete(142)`, `DumpResult 189 B`, `Ok`), exit 0. Timings: `CompileModule` **4.55 s** (one-off per process; wazero `CompilationCache` with a directory brings later starts to ~100 ms — to be measured), run 6.7 ms. `total_execution_micros` is **millisecond-granular** on wasm (4000/5000/6000 µs), matching upstream's note about the wasm clock.

Design consequences:
- The wazero backend = "virtual subprocess": same framing/drive loop; `Transport` differs only in spawn (instantiate module with pipes), kill (`module.Close` / ctx cancel via `WithCloseOnContextDone`), exit status (`sys.ExitError.ExitCode()` → the same classification incl. 65), pid (none).
- `max_memory` inside the worker is the same allocator; additionally `wazero.NewRuntimeConfig().WithMemoryLimitPages` caps linear memory.
- Startup cost must be amortised: compile once per process (`sync.Once`), enable the on-disk compilation cache (`os.UserCacheDir()/montygo/wazero`), prewarm `MinProcesses` instances.

## 13. Wasm artifact distribution fork

| # | Option | `go get` UX | Repo impact |
|---|---|---|---|
| W1 | Commit `internal/wasm/monty.wasm` (18 MB; or `.wasm.zst`/`.gz` ≈ 4–5 MB, decompressed on first use) via `go:embed` | zero-install | binary in git history each rebuild (~5 MB compressed per version; Git LFS optional) |
| W2 | `go generate` builds it locally (needs Rust + wasip1 target) | user must have Rust toolchain; breaks zero-install | clean repo |
| W3 | Download from a montygo GitHub release at first use into the cache dir | network at runtime; checksum pinned in code | clean repo |
| W4 | Separate Go module `github.com/asalimonov/montygo-wasm` containing only the embedded blob, imported by the main module | zero-install; main repo stays small | second repo; version lockstep |

Recommendation: **W1 with compression** (gzip/zstd via `klauspost/compress`), because the whole point of the wazero backend is `go get` and run. W4 is the fallback if repo size becomes a problem.

**A11 (user):** Commit compressed blob + `go:embed` (W1).

**A12 (user):** Git rev pin (`rev = "f8acf4fa"`) + documented local path override.

### Measurements (wasm blob)
- `monty-wasi-worker.wasm`: 19 367 319 bytes (release, fat LTO, stripped)
- gzip -9: 6 104 413 bytes; zstd -19: 4 064 031 bytes; xz -9: 3 617 500 bytes
- wazero compile: cold 4.53 s; with `wazero.NewCompilationCacheWithDir` warm **132 ms** (cache dir 77 MB). Run of the 6-request sequence: 5–7 ms.
- Decision inputs: ship `internal/wasm/monty.wasm.zst` (pure-Go `github.com/klauspost/compress/zstd` decoder; xz saves 0.4 MB more but pure-Go xz is slower and less common); enable the on-disk compilation cache by default under `os.UserCacheDir()/montygo/wazero/<blob-sha256>/`, overridable via `WasmCacheDir` and disable-able with an empty string.

**A13 (user):** Windows unsupported in v0.0.23: native backend build-tagged `unix`; Windows users get the wazero backend only; documented as a parity gap.

## 14. Modelled Go API (first cut, for review)

Package `monty` (`github.com/asalimonov/montygo`). Everything blocking, `context.Context` first. One API (no sync/async split): concurrency comes from goroutines; sandbox "async" host calls are `*Future` values.

### 14.1 Pool

```go
type Backend int
const (
    BackendAuto   Backend = iota // native if a binary resolves, else embedded wasm
    BackendNative
    BackendWasm
)

type Options struct {
    Backend               Backend
    BinaryPath            string        // explicit worker; TS binaryPath
    MinProcesses          int           // 1
    MaxProcesses          int           // runtime.NumCPU()
    CheckoutTimeout       time.Duration // 0 = wait forever
    RequestTimeout        time.Duration // 0 = off (set it for untrusted code)
    DurationLimitGrace    time.Duration // 0 = default 1 s; NoDurationLimitGrace (-1) disables
    MaxCheckoutsPerWorker int           // 0 = unlimited
    WasmCacheDir          string        // "" = os.UserCacheDir()/montygo/wazero; DisableWasmCache to turn off
    DisableWasmCache      bool
}

func New(ctx context.Context, opts Options) (*Pool, error)                 // TS Monty.create
func (p *Pool) Checkout(ctx context.Context, opts CheckoutOptions) (*Session, error)
func (p *Pool) Close(ctx context.Context) error                          // idempotent; live sessions keep their workers
func (p *Pool) Backend() Backend

type CheckoutOptions struct {
    ScriptName               string            // "main.py"
    Limits                   *ResourceLimits
    TypeCheck                bool
    TypeCheckStubs           string
    TypeCheckFormat          TypeCheckFormat   // "" = "full"
    TypeCheckColor           bool
    AssertMessageAnnotations *uint32           // nil = default (120); 0 = off; n = truncation; >2^32-1 impossible by type
    PrintFlushInterval       *time.Duration    // nil = 5 ms; 0 = line buffering; negative → error
}

type ResourceLimits struct {
    MaxDuration       time.Duration // 0 = unlimited
    MaxMemory         uint64        // 0 = unlimited
    GCInterval        uint64        // 0 = unlimited
    MaxRecursionDepth uint64        // 0 = 1000
    MaxSuspensions    uint64        // 0 = 1000
}
```

### 14.2 Session and snapshots

```go
func (s *Session) FeedRun(ctx context.Context, code string, opts *FeedOptions) (any, error)
func (s *Session) FeedStart(ctx context.Context, code string, opts *FeedOptions) (Snapshot, error)
func (s *Session) LoadSession(ctx context.Context, state []byte) error
func (s *Session) LoadSnapshot(ctx context.Context, state []byte, opts *LoadSnapshotOptions) (Snapshot, error)
func (s *Session) Dump(ctx context.Context) ([]byte, error)
func (s *Session) InstallDependencies(ctx context.Context, requirements []string) error
func (s *Session) WorkerPID() (pid int, ok bool)   // ok=false on wasm or mid-turn
func (s *Session) Close(ctx context.Context) error // returns worker to the pool (Reset); idempotent

type FeedOptions struct {
    Inputs         map[string]any
    ExternalLookup map[string]any // Function | plain Go func (reflect-adapted) | any value
    Print          PrintTarget    // nil = host os.Stdout / os.Stderr
    Mount          []*MountDir
    Cwd            string         // absolute virtual path; "" keeps the session cwd
    OS             OSHandler
    SkipTypeCheck  bool
}
type LoadSnapshotOptions struct { Print PrintTarget; Mount []*MountDir; ExternalLookup map[string]any; OS OSHandler }

type Snapshot interface{ snapshot() }            // *FunctionSnapshot | *NameLookupSnapshot | *FutureSnapshot | *Complete
type Complete struct{ Output any }

type FunctionSnapshot struct {
    FunctionName    string
    Args            []any
    Kwargs          Kwargs
    CallID          uint32
    IsOSFunction    bool
    AllowEagerAwait bool
    ObjectID        string // "" for plain external calls
}
func (f *FunctionSnapshot) Resume(ctx, value any) (Snapshot, error)
func (f *FunctionSnapshot) ResumeAuto(ctx) (Snapshot, error)
func (f *FunctionSnapshot) ResumeError(ctx, err error) (Snapshot, error)
func (f *FunctionSnapshot) ResumeNotFound(ctx) (Snapshot, error)
func (f *FunctionSnapshot) ResumeFuture(ctx) (Snapshot, error)
func (f *FunctionSnapshot) ResumeNotHandled(ctx) (Snapshot, error)   // ErrNotOSCall unless IsOSFunction
func (f *FunctionSnapshot) Dump(ctx) ([]byte, error)

type NameLookupSnapshot struct { VariableName string; ObjectID string }
func (n *NameLookupSnapshot) ResumeUnresolved(ctx) (Snapshot, error)          // TS resume()  → NameError / AttributeError
func (n *NameLookupSnapshot) ResumeFunction(ctx, functionName string) (Snapshot, error) // TS resume(name)
func (n *NameLookupSnapshot) ResumeValue(ctx, value any) (Snapshot, error)
func (n *NameLookupSnapshot) ResumeAuto(ctx) (Snapshot, error)
func (n *NameLookupSnapshot) Dump(ctx) ([]byte, error)

type FutureSnapshot struct { PendingCallIDs []uint32 }
type FutureResolution struct { CallID uint32; Value any; Err error }  // Err != nil → error result
func (f *FutureSnapshot) Resume(ctx, results []FutureResolution) (Snapshot, error)
func (f *FutureSnapshot) ResumeAuto(ctx) (Snapshot, error)
func (f *FutureSnapshot) Dump(ctx) ([]byte, error)
```
Single-use: a second resume returns `ErrSnapshotResumed` ("snapshot has already been resumed").

### 14.3 Host functions, futures, OS handler

```go
type Kwargs map[string]any

type Function interface {
    Call(ctx context.Context, args []any, kwargs Kwargs) (any, error)
}
type FunctionFunc func(ctx context.Context, args []any, kwargs Kwargs) (any, error)  // implements Function
func Func(fn any) (Function, error)   // reflect adapter: params converted from sandbox values; optional leading ctx;
                                      // optional trailing Kwargs; returns (T) | (T, error) | (error) | (*Future) | (*Future, error)
type Future struct{ /* settled chan */ }
func Async(fn func(ctx context.Context) (any, error)) *Future   // runs fn in a goroutine
func NewFuture() (*Future, func(value any, err error))          // manual settle

type RaisedError struct { ExcType string; Message string }      // Error() = "ExcType: Message"; ExcType must be a Python exception name
// any other error returned by a host function → RuntimeError(err.Error()); *RuntimeError/*SyntaxError from a nested session keep their type

var NotHandled = &notHandled{}                                  // OS handler return value
type OSHandler func(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error)
```
Resolution in `FeedRun`/`ResumeAuto` (parity with `TurnAnswerer`): entry absent → `not_found`; entry is `Function` or a func → call; entry any other value on a *call* → `TypeError: '<pytype>' object is not callable`; on a *name lookup* → value. Returned `*Future` → `future(call_id)` (or awaited directly when `AllowEagerAwait`). `ResolveFutures` → wait for the first settled pending future, deliver every settled one.

### 14.4 Values

```go
type Dict struct{ /* ordered keys []any, values []any, index map[hashKey]int */ }
func NewDict(pairs ...Pair) *Dict; func (d *Dict) Get(k any) (any, bool); Set(k, v any); Delete(k any) bool; Len() int
func (d *Dict) Keys() []any; Values() []any; Pairs() []Pair; ToStringMap() (map[string]any, error); Equal(*Dict) bool
type Pair struct{ Key, Value any }
type Tuple []any
type Set struct{ /* ordered */ }; type FrozenSet struct{ /* ordered */ }
var Ellipsis = ellipsis{}; var NotImplemented = notImplemented{}
type Date struct{ Year int32; Month, Day uint8 }
type DateTime struct{ Year int32; Month, Day, Hour, Minute, Second uint8; Microsecond uint32; OffsetSeconds *int32; TimezoneName *string }
type Time struct{ Hour, Minute, Second uint8; Microsecond uint32; OffsetSeconds *int32; TimezoneName *string; Fold uint8 }
type TimeDelta struct{ Days, Seconds, Microseconds int32 }
type TimeZone struct{ OffsetSeconds int32; Name *string }
func DateTimeFromTime(t time.Time) DateTime; func (d DateTime) Time() (time.Time, error)  // helpers, no implicit conversion of time.Time
type Exception struct{ ExcType string; Message string }     // exception *value* (no traceback)
type Type struct{ Name string; ID string; Origin TypeOrigin; IsDataclass bool; Attrs *Dict }
type BuiltinFunction string; type Path string
type NamedTuple struct{ TypeName string; FieldNames []string; Values []any }
type FileHandle struct{ Path, Mode string; Position uint64 }; func NewFileHandle(path, mode string, position uint64) (*FileHandle, error); Binary()/Readable()/Writable()
type Cycle struct{ Identity uint64; Placeholder string }
const MaxValueDepth = 48
```
Inbound ints: `int64` when it fits, else `*big.Int`. Outbound accepted: all Go integer kinds, `*big.Int`, `float32/64`, `string`, `[]byte`, `bool`, `nil`, slices/arrays (→ list), `Tuple`, maps (→ dict, keys sorted for determinism), `*Dict`, `*Set`, `*FrozenSet`, the structs above, `*ClassInstance`, `*ClassType`, `*ClassProxy`, `*FileHandle`. Anything else → `TypeError: Cannot convert <GoType> instance to a Monty value — wrap it in ClassInstance(...)` (for struct values) / `Cannot convert <GoType> to Monty value` (others).

### 14.5 Host objects

```go
type AttrPolicy struct{ /* none | all | names */ }
var All = AttrPolicy{all: true}
func Names(names ...string) AttrPolicy

type ClassInstanceOptions struct {
    EagerAttrs, LazyAttrs, AllowedMethods AttrPolicy
    Name         string
    ConvertValue func(name string, value any) (any, error)
    ID           string      // canonical uuid, lowercased
    ClassType    *ClassType
}
func NewClassInstance(instance any, opts ClassInstanceOptions) (*ClassInstance, error)
func (c *ClassInstance) ID() string; Instance() any; ClassType() *ClassType; Name() string

type ClassTypeOptions struct {
    EagerAttrs, LazyAttrs, AllowedMethods AttrPolicy   // over Statics
    Statics      map[string]any                        // class constants and static "methods" (funcs / Function)
    Name         string
    ConvertValue func(name string, value any) (any, error)
    ID           string
    Init         bool
    Constructor  any                                   // Go func (reflect-adapted) called for __call__
    InstanceEagerAttrs, InstanceLazyAttrs, InstanceAllowedMethods AttrPolicy
    InstanceWrapper func(instance any) (*ClassInstance, error)
}
func NewClassType[T any](opts ClassTypeOptions) (*ClassType, error)   // class identity = reflect.Type of T (pointer-normalised)
func (c *ClassType) ID() string; Name() string; Construct(ctx, args []any, kwargs Kwargs) (*ClassInstance, error)

type AttrProvider interface{ GetAttr(name string) (any, error) }                                 // optional override of reflection
type MethodProvider interface{ CallMethod(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error) }
var ErrAttrNotExposed = errors.New("attribute not exposed")   // providers return it to mean "absent" (AttributeError)

type ClassProxy struct{ Name string; ID string; IsDataclass bool; Attributes *Dict }
```
Reflection defaults: `All` eager/lazy attrs = exported struct fields (value or pointer); `All` methods = exported methods of the method set; explicit `Names` may also name unexported? No — unexported members are never reachable (Go analog of `_`-prefix). Class id per `reflect.Type` (process-wide map, like the TS WeakMap); explicit `ID` bypasses.

### 14.6 Errors

```go
type Error interface { error; Exception() ExceptionInfo; Display(format DisplayFormat) string }   // implemented by the four below
type ExceptionInfo struct{ TypeName, Message string }
type Frame struct{ Filename string; Line, Column, EndLine, EndColumn uint32; FunctionName, SourceLine *string }
type RuntimeError struct{ TypeName, Message string; Frames []Frame; tracebackText string }   // Traceback() []Frame; Display(default "traceback")
type SyntaxError struct{ Message string; tracebackText string }
type TypingError struct{ Diagnostics string }          // Display() ignores format
type CrashedError struct{ Message string; TimedOut bool; ExitStatus string /* "" = unknown */ }
type ProtocolError struct{ Message string }            // not a monty.Error (parity)
var ErrPoolClosed, ErrSessionClosed, ErrNotFresh, ErrSnapshotResumed, ErrNotOSCall, ErrWrongDumpKindSuspended, ErrWrongDumpKindIdle error  // messages verbatim from TS
```
All are caught with `errors.As`.

### 14.7 Print, mounts, telemetry, binary

```go
type Stream string; const (Stdout Stream = "stdout"; Stderr Stream = "stderr")
type PrintTarget interface{ Print(stream Stream, text string) error }
type PrintFunc func(stream Stream, text string) error            // implements PrintTarget; a returned error aborts the feed (TS throw)
const DefaultMaxPrintCollectBytes = 10 << 20; const UnlimitedPrintCollect = -1
func NewCollectString(maxBytes int64) (*CollectString, error); func (c *CollectString) Output() string
func NewCollectStreams(maxBytes int64) (*CollectStreams, error); func (c *CollectStreams) Output() []CollectedStreamEntry

type MountMode string; const (MountReadOnly MountMode = "read-only"; MountReadWrite = "read-write"; MountOverlay = "overlay")
type MountDirOptions struct{ HostPath, VirtualPath string; Mode MountMode; WriteBytesLimit *uint64; MemoryUsageLimit *uint64 }
func NewMountDir(opts MountDirOptions) (*MountDir, error); func (m *MountDir) Close() error; String() string  // "MountDir(host_path='…', virtual_path='…', mode='…')"

type TelemetryComponents struct{ Tracer trace.Tracer; Meter metric.Meter; Logger log.Logger }
func Instrument(c TelemetryComponents) error; func Flush(ctx context.Context) error
type Instrumentation struct{...}; func NewInstrumentation(cfg InstrumentationConfig) *Instrumentation  // Enable/Disable/SetTracerProvider/SetMeterProvider/SetLoggerProvider/ForceFlush

func FindMontyBinary(explicit string) (string, error)
```

### 14.8 Concurrency contract
- `Session` methods are mutually exclusive (mutex); a call cancelled while waiting for the mutex leaves the session usable; a ctx cancelled **during** a protocol turn kills the worker and poisons the session with `ErrTurnCancelled` (parity with Python cancellation semantics).
- Host callbacks (`Function.Call`, `OSHandler`, `PrintTarget`, providers) run on the goroutine that drives the turn; `*Future` bodies run in their own goroutines. Re-entering the *same* session from its own callback deadlocks (documented, as upstream).
- `Pool` is goroutine-safe.

**A14 (user):** API model accepted as the basis for the target document.

## 15. Verification: inconsistencies found and NOT CONSIDERED & TODO (draft, to be resolved one by one)

Re-read of the source ask against the research:
- "100 % functional parity with the TS binding": achievable for the node entry except Windows (A13) and the Node-specific telemetry *delivery* mechanics (bounded queues); the browser/wasm entry is matched by the wazero backend (which additionally supports mounts, live print streaming and a real exit code — improvements over upstream wasm).
- "semantically the same tests": S1 (A9). ~110 JS-only tests need a documented mapping (Appendix C).
- The changelog name `v0.0.23.md` vs. baseline HEAD (protocol 3): the changelog MUST state "upstream 0.0.23 + main@f8acf4fa (protocol 3)".

| # | Item | Tags | Proposed resolution |
|---|---|---|---|
| V1 | **Per-frame decode budget (1 GiB resident)**: `google.golang.org/protobuf` decodes a whole frame before any host-size accounting is possible; upstream's hand-written `WireObject` charges the budget *during* decode. | porting:simplification / potential-improvement | Options: (a) frame-length cap only (256 MiB) + post-decode host-size check (allocation already happened; worst case ≈ 22× blow-up), documented divergence; (b) custom `protowire` streaming decoder for `MontyObject` (charges budget while building Go values directly, no intermediate pb tree — also faster) — ~500 lines, `differential` tests against the generated codec. |
| V2 | `node_docs.spec.ts` (README/docs snippets executed) | potential-improvement | Go analog: `example_test.go` with `Example…` functions for every README snippet (compiled and run by `go test`, output-checked). |
| V3 | `node_entrypoint_exports.spec.ts` | porting:deffered (N/A) | Single package; replaced by a `public_api_test.go` that asserts the exported identifier list (parity checklist) — cheap and catches accidental API drift. |
| V4 | WebSocket transport (`AsyncMontyWebsocket`, Python-only) | porting:postponned-to-the-next-task / porting:improvement-over-upstream | Not in TS; a `Transport` seam makes it a later addition. |
| V5 | `AbstractOS` / `OSAccess` / `MemoryFile` / `CallbackFile` / `StatResult` helpers (Python-only) | porting:postponned-to-the-next-task | Not in TS; the TS suite hand-rolls a `Map` in the `os` callback, which ports as-is. |
| V6 | Node telemetry delivery mechanics (bounded queues, overflow disables a signal, `AsyncResource` binding) | porting:simplification | Go records synchronously into the OTel SDK (Python's model); span/metric/log *contracts* kept. |
| V7 | wasm backend: `RequestTimeout` kill via wazero `WithCloseOnContextDone` and `MaxMemory` exit-code 65 classification | too-complex for this session (needs a spike) | First plan task: spike `while True: pass` kill and `' ' * (1 << 60)` OOM under wazero; fall back to "module close = crash" classification if exit code does not propagate. |
| V8 | wasm `total_execution_micros` is ms-granular | postponed (upstream property) | `wasm_type_check` 15-attempt tests port unchanged. |
| V9 | `wasm_word_size` tests | none — portable | wazero runs wasm32, so both tests port to the wazero backend. |
| V10 | Memory-figure expectations (89 113 B native / 75 047 B wasm etc.) | postponed | Same worker code → expected to match node/browser figures; ±1 KiB tolerance kept; re-derive if not. |
| V11 | JS prototype-hardening tests (`constructor`, `__proto__`, `Function.prototype`, cross-realm Set) | porting:deffered (JS-only) | Listed in `docs/parity/tests.md` as not applicable; Go's equivalent guarantee (unexported members unreachable) gets 2 tests. |
| V12 | Windows | porting:postponned-to-the-next-task | A13. |
| V13 | Upstream `monty-fs` semantics (path security, overlay, stat shapes, error strings) | in progress | Appendix E from the delegated read; the Go `internal/mountfs` package ports it 1:1. |
| V14 | Value-codec messages and traceback rendering (`convert.rs`, `exceptions.rs`) | in progress | Appendix F from the delegated read. |
| V15 | Go min version / CI | postponed to plan | `go 1.25`; GitHub Actions: build worker from the git pin, run suite on both backends (linux + macos). |
| V16 | `Session` re-entrancy from its own callback deadlocks | documented (as upstream) | no change. |

**A15 (user, V1):** custom `protowire` streaming codec for `MontyObject` with the 1 GiB resident budget; generated pb for the rest; differential tests against the generated codec. Appendix F (`-appendix-f-codec-and-errors.md`) records the conversion/error contract.

**A16 (user, V2/V3):** `Example…` functions for README snippets + `public_api_test.go` export-list check. Appendix E (`-appendix-e-mountfs.md`) records the mount-table contract (V13 resolved).

**A17 (user, V4/V5):** in scope for v0.0.23: **WebSocket transport** and **in-memory OS helpers** (Python-only features; tagged `porting:improvement-over-upstream` relative to TS). `ClassTypeProxy` not selected → postponed (TS-style `Type` value kept). Consequence: the TS test corpus does not cover these; the Python tests `test_websocket.py` (10) and `test_os_access*.py` (139 + 41 + 27) are the semantic reference — scope of their port asked as Q18. Appendix G (delegated) will record the Python contract for both.

**A18 (user):** port the Python tests for the extras 1:1 (`test_websocket.py` 10, `test_os_access.py` 139, `test_os_access_compat.py` 41, `test_os_access_raw.py` 27) with a Go WebSocket relay/server fixture.

### Spike result: wazero kill and memory limits (V7)
- **Kill:** `Feed("while True: pass")` then `cancel()` on the instantiate ctx (`WithCloseOnContextDone(true)`) stops the spinning module within the cancel latency; wazero returns `sys.ExitError` with code `4294967295` (`sys.ExitCodeContextCanceled`). → the wasm backend can enforce `RequestTimeout` and the `max_duration` backstop exactly like the native kill; classify that code as `timed_out` / crash, never as 65.
- **Soft limit:** `[0] * 10_000_000` with 1 MiB → `Error{MemoryError: "memory limit exceeded: 160024198 bytes > 1048576 bytes"}`; `' ' * (1 << 30)` with 50 MiB → `MemoryError: memory limit exceeded: 1073765841 bytes > 52428800 bytes` (native: `1073772418` — figures differ by ~6.5 KB between targets, confirming "re-derive per backend"). Session survives.
- **Word size:** `' ' * (1 << 60)` on wasm32 → `OverflowError: cannot fit 'int' into an index-sized integer` (the `wasm_word_size` behaviour), so the TS "refused allocation" test must use a 32-bit-sized payload on the wasm backend, as the browser variant does.
- Harness note: the worker blocks on stdin after a turn; a parent must close stdin (or send `Shutdown`) to let it exit 0.
- **Hard limit (allocator refusal):** a 16 MiB source snippet with a 1 KiB limit → worker stderr `monty worker: allocation of 16777236 bytes exceeds the memory limit`, EOF without a turn-ending event, **exit code 65** on both native and wazero (`sys.ExitError.ExitCode() == 65`). → the wazero backend gets the same `MemoryError("the worker exceeded its memory limit and was terminated")` classification as native — an improvement over upstream's browser worker (which can only trap → `MontyCrashedError`). The `wasm_memory_limit` test #3 expectation therefore changes for Go (documented).

### Resolution of the NOT CONSIDERED & TODO list (final)
| # | Resolution | Tags |
|---|---|---|
| V1 | custom protowire codec with budget (A15) | porting:improvement-over-upstream |
| V2/V3 | Example funcs + export-list test (A16) | potential-improvement |
| V4 | WebSocket transport in scope (A17); Python tests ported (A18) | porting:improvement-over-upstream (vs TS) |
| V5 | in-memory OS helpers in scope (A17/A18) | porting:improvement-over-upstream (vs TS) |
| V6 | synchronous OTel recording | porting:simplification |
| V7 | verified by spike: ctx-kill and exit 65 both work under wazero | resolved |
| V8 | ms-granular wasm clock accepted | postponed (upstream property) |
| V9 | word-size tests port to wazero backend | resolved |
| V10 | memory figures re-derived per backend (native = node figures; wasm ≈ browser figures) | postponed to implementation |
| V11 | JS prototype hardening: N/A, 2 Go-specific tests instead | porting:deffered |
| V12 | Windows unsupported (A13) | porting:postponned-to-the-next-task |
| V13 | Appendix E | resolved |
| V14 | Appendix F | resolved |
| V15 | go 1.25; CI on linux+macos, both backends | postponed to plan |
| V16 | re-entrancy documented | resolved |
| V17 (new) | `ClassTypeProxy` not adopted; TS-style `Type` value | postponed |
| V18 (new) | `installDependencies` parity only (sandbox worker rejects; CPython worker out of scope) | porting:deffered |
| V19 (new) | Telemetry `Instrumentation` lifecycle object: Go has no OTel `Instrumentation` base class; provided as a plain struct with the same methods | porting:simplification |

## 16. Summary

**Findings.** Monty's official bindings never embed the interpreter: they drive `monty subprocess` children over a language-agnostic, 4-byte-LE-framed protobuf protocol (v3 on the chosen `main` baseline). A Go binding therefore needs no cgo: it re-implements the parent (monty-pool: spawning, framing, turn deadlines, suspension budget, crash classification, mounts) and the TS drive loop (external lookup, futures, class wrappers, snapshots, print, errors). The wasm entry upstream is a WASI 0.2 component that Go runtimes cannot load, but the same stdio worker compiles to a plain wasip1 module once `monty-fs` is left out; wazero runs it as a virtual subprocess with a real exit code (65 on OOM) and context-based kill — an improvement over the browser worker. Two spikes verified both backends end to end from Go with generated protobuf types.

**Resolutions.** Baseline main@f8acf4fa (A1); subprocess + wazero backends (A2); toolchain updated, macOS 27 beta SDK needs `SDKROOT=MacOSX26.5.sdk` (A3); resolution chain with wasm fallback (A4); `*Dict` always (A5); `Function` + reflect adapter + `*Future` (A6); reflect host objects + providers, generic `NewClassType[T]` (A7); full OTel-go telemetry (A8); 1:1 TS test port on both backends (A9); module `github.com/asalimonov/montygo`, package `monty` (A10); committed `.wasm.zst` + go:embed + compilation cache (A11); git-rev pin + local override (A12); Windows unsupported (A13); API model accepted (A14); custom protowire codec with decode budget (A15); Example + export-list tests (A16); WebSocket transport and OS helpers in scope (A17) with the Python tests ported (A18).

**Deliverables of this session.** This log; appendices A–G (TS API, Python/protocol rules, TS tests, pool spec, mount spec, codec/errors, Python extras); the target architecture `20260915-go-binding-monty-target.md` (layout, all signatures per file, data flows, failure modes, test mapping, tooling, changelog spec, NOT CONSIDERED list, pseudo-code, milestones). Scratchpad artefacts (not in repo): `monty-wasi-worker` crate and the Go harness proving the protocol.

BRAINSTORM DONE
