# Brainstorm: a designed API surface instead of alias files

Date: 2026-09-16

## Initial input

> Now the library looks absurd due to abusing of aliases instead of proper design of a surface of its API. Need to design surface of this library which can be in the root of it and import dependencies from child packages.
> Like when I want to use the library I expect:
> - runtime struct/interface which allows to construct a new runtime with my extensions, for instance for host file system or my `sleep` method which is not available from `time.sleep` in monty
> - it can use telemetry w/o aliases and move runtime/errors to independent errors package to avoid dependency of telemetry on runtime/errors
> - supervisor/supervisor.go can be moved to the root instead of alias_supervisor.go
> - alias_docker.go I guess can be eliminated after moving supervisor/supervisor.go to the root
> - etc
>
> Design surface and eliminate alias files from the root; need to design proper API surface instead of tuning KPI without achieving the real goal of the library: be usable and clear. Some test files can be moved to the root back if it improves the design.

## Current state (after the package split, commit 33de74f)

| Package | Lines | Holds |
|---|---|---|
| root `montygo` | 368 | 7 generated alias files, `montygo.go` (pins), `version.go` (ldflags stamp) |
| `internal/engine` | 4599 | `Pool`, `Session`, `Run`, `Slot`, snapshots, `Options`, `CheckoutOptions`, `FeedOptions`, `StopPolicy`, transports, rotation, recovery wiring |
| `runtime` | 504 | value model, conversion helpers, print targets, mounts, **and all error types** |
| `runtime/host` | 1728 | `Host`, `ClassInstance`, `ClassType`, `Function`, `Future`, `OSHandler`, `Kwargs` |
| `runtime/osaccess` | | in-memory OS helpers |
| `supervisor` | 237 | `ServerSupervisor`, `ServerEndpoint`, `RecoveryPolicy`, `OrphanReaper`, `Recoverer` |
| `supervisor/docker`, `supervisor/native` | 661, 340 | implementations |
| `telemetry` | 218 | `Components`, `Instrument`, `Instrumentation` |
| `conformance` | 48 files | ported suites and API tests against the facade |

Measured facts:

- `telemetry` depends on `runtime` for one symbol, `ErrTelemetryPresent`.
- `internal/engine` is one cohesive unit: `session ↔ pool` is an irreducible cycle, and `lifecycle`, `answer`, `stop`, `snapshot`, `rotation`, `slot`, `run` are all methods on `*Session` or `*Pool` (see `docs/reports/20260916-package-layout.md`).
- Consumers (`examples/`, `../montygo-test`) use, through root aliases: `Repr` 29×, `Pair` 26×, `Kwargs` 26×, `Dict` 26×, `Raise` 18×, `NewDict` 18×, `FunctionFunc` 16×, `Names` 10×, `ClassInstanceOptions` 10×, `Path` 9×, `NewClassInstance` 8×, `Async` 8×, `Stream` 7×, `RuntimeError` 7×, `Host` 6×, `Future` 6×, `Equal` 6×, `DisplayTraceback` 6×, `All` 6×, `Lines` 5×, `Stderr` 5×, `Stdout` 4× — the value model and host objects are the vocabulary of every program, not an occasional import.
- Root's own vocabulary in consumers: `FeedOptions` 21×, `New` 19×, `CheckoutOptions` 17×, `Stopped` 10×, `Pool` 10×, `StopPolicy` 9×, `Session` 9×, `WebSocketOptions` 5×, `Options` 5×.

## Thoughts

### What went wrong

The previous goal was stated as a file and line budget for the root. The split moved the engine out and re-exported everything through generated aliases. The numbers were met; the design was not: a user reading `montygo` sees `type Dict = rt.Dict` a hundred times and learns nothing, godoc for the package is empty, functions became `var` bindings, and the `Example` tests moved away from the package they document.

### What a real surface is

A root package that **declares** the types a program composes: the runtime (pool) and its options, sessions and runs, the supervisor contract, and the error contract, importing the value model, host objects and telemetry from child packages by their own names. Nothing in root exists only to rename something else.

### The forks

1. **Where `Session` and `Pool` live.** They are the surface, and the machinery is their methods. Either they come back to root as real types, or root wraps them.
2. **What "runtime" means.** A configured environment (host functions, OS handler, mounts, telemetry, supervisor) that sessions inherit, versus today's `Pool` plus per-checkout and per-feed options.
3. **The error package.** Its name (`errors` collides with the standard library in every file that uses both), and what it holds: sandbox exceptions only, or also the pool's infrastructure errors.
4. **The supervisor contract in root.** Consumer-side interface, implementations in `supervisor/docker` and `supervisor/native` importing root; `montygo.NewDocker` cannot exist then.
5. **Which vocabulary root should re-export, if any.** `Dict`, `Kwargs`, `Raise` are used by every program.

## Questions and answers

### Q1. Where `Session` and `Pool` live

Options: engine back in root as real types; root wraps `internal/engine` with forwarding methods; thin root over an internal engine (not viable, `internal/` cannot be named by users).

**Answer: engine in root.** The API-bearing types and their machinery return to the root package. The file and line budget from the previous task is dropped; the surface is judged by what it declares, not by its size. Wrappers and aliases are out.

Consequences:

- Root imports child packages; child packages other than the supervisor implementations do not import root.
- `supervisor/docker` and `supervisor/native` import root, so `montygo.NewDocker` cannot exist; a program calls `docker.NewPool(ctx, …)`.
- `internal/engine` disappears. `internal/pool`, `internal/worker`, `internal/wire`, `internal/value`, `internal/mountfs`, `internal/telemetry` stay internal.

### Q2. What "runtime" and "pool" mean

The question offered: a `Runtime` type replacing `Pool`; a `Runtime` layered above `Pool`; no runtime type. The user answered in free form instead:

> From my point of view runtime is configuration and/or extensions of runtime of monty. Pool is for management of supervisors and workers behind supervisors. It is like pool of resources which can be busy, which are not, and pool manages resources in accordance of limits which are default + optionally configured by the app who uses this library.
> Management of several pools and runtimes is out of the scope. The app which uses this library should do it itself; the library just shouldn't rely on global values and be managed via parameters and context to maintain isolated and self-sustainable scope of entities in memory.

Resolution:

- **`Runtime`** is configuration and extensions of the sandbox: host functions and classes (`sleep`), the OS handler (a host file system), mounts, print defaults, type-check stubs, resource limits. It is a value; it owns no workers.
- **`Pool`** manages resources: workers behind a backend or a supervisor, busy and idle, within limits that have defaults the application MAY override. It knows nothing about Python.
- A `Session` is one pool resource configured with one runtime.
- No package-level state. Today's violations: `DefaultStopPolicy` (a package variable read at pool creation) and the process-wide telemetry installation (`telemetry.Instrument`, `internal/telemetry.Global()`, used when `Options.Telemetry` is nil). Both become parameters.
- Several pools or runtimes in one process are the application's concern; the library only guarantees that none of them share hidden state.

### Q3. How a runtime and a pool produce a session

Options: `pool.Checkout(ctx, rt, opts)`; `rt.Checkout(ctx, pool)`; binding one runtime to a pool at construction.

**Answer: `pool.Checkout(ctx, rt, opts)`.** The pool is the factory; the runtime is an immutable parameter of every checkout, so one pool of workers serves any number of runtimes. A worker is `Reset` between checkouts, so no sandbox state crosses runtimes. `CheckoutOptions` keeps the per-session overrides (script name, limits, stop policy); `FeedOptions` the per-feed extras (inputs, extra mounts, print target, external lookup).

### Q4. The error package

Options: `monterr` with the whole contract; `errors` with the whole contract; `monterr` for sandbox exceptions only, infrastructure errors in root.

**Answer: `monterr`, whole contract.** One package holds the sandbox exceptions and the infrastructure failures, including `ErrSessionLost`, which both kinds match. `telemetry` depends on it instead of on the value model. No standard-library name clash.

### Q5. The sandbox packages

Options: a `sandbox` family; flat `pyvalue`, `host`, `osaccess`; keep `runtime/…`.

**Answer: the `sandbox` family.** `montygo/sandbox` holds the value model, print targets and mounts; `montygo/sandbox/host` the host objects, functions and futures; `montygo/sandbox/osaccess` the OS helpers. `runtime/` as a directory goes, so the word means only `montygo.Runtime`.

### Q6. Telemetry without process-wide state

Options: parameters only; keep `Instrument` as a convenience returning components; keep the global fallback.

**Answer: parameters only.** `telemetry.Instrument`, `telemetry.Flush` and `internal/telemetry.Global()` are removed. A pool records only into `PoolOptions.Telemetry`; nil records nothing. `telemetry.Instrumentation` stays as a builder over otel providers and exposes `Components()`; it installs nothing. `docs/parity/api.md` records the deviation from upstream's global `instrument()`.

### Q7. Worker sources and the supervisor contract

Options: `WorkerSource` values from three constructors; one flat `PoolOptions` with `Backend` or `Supervisor`; three pool constructors.

**Answer: `WorkerSource` values.** `PoolOptions.Workers` is one of `montygo.Native(NativeOptions)`, `montygo.Wasm(WasmOptions)` or `montygo.Remote(ServerSupervisor, RemoteOptions)`; each carries only the fields that apply. The contract (`ServerSupervisor`, `ServerEndpoint`, `RecoveryPolicy`, `OrphanReaper`) and the recovery machinery are declared in root. `montygo.StaticServer(url, tls, headers)` is the fixed-URL supervisor. `supervisor/docker` and `supervisor/native` import root and return implementations; the `supervisor` package itself goes.

### Q8. What a runtime holds

Options: the runtime owns the environment and is the only host source; the runtime holds defaults and a checkout may add a host; the host is the runtime.

**Answer: the runtime owns the environment.** `NewRuntime(RuntimeOptions{Host, OS, Mounts, Print, TypeCheck, TypeCheckStubs, Limits})` validates once and returns an immutable `*Runtime`. `CheckoutOptions` loses `Host` and keeps `ScriptName`, a `Limits` override and `Stop`. `FeedOptions` keeps `Inputs`, `ExternalLookup`, extra `Mounts`, `Print`, `Cwd`, `SkipTypeCheck`. A different host means another runtime on the same pool.

### Q9. Where the tests live

Options: back to root as `montygo_test`; keep `conformance/`; split by upstream origin.

**Answer: back to root.** The ported suites, the montygo-only lifecycle, stop and rotation tests, and `example_test.go` return to the root package as external tests. `conformance/` goes. Package tests stay with `sandbox`, `sandbox/host`, `monterr`, `telemetry`, `supervisor/docker` and `supervisor/native`.

### Q10. Supervisor ownership

Options: the application closes it; opt-in ownership through `RemoteOptions`; owning pool helpers in `docker` and `native`.

**Answer: the application closes it.** `montygo.Remote(sup, opts)` never takes ownership; the program closes the supervisor after `pool.Shutdown`. `Pool.OwnSupervisor` and the owned-supervisor paths in `Close`/`Shutdown` are deleted. `docker.NewPool` and `native.NewPool` go; `docker.New` and `native.New` remain.

## Migration surface (measured)

Files that mention a name this design removes or renames:

| Name | `docs/` | README | `examples/` | `../montygo-test` | `tests/network` |
|---|---|---|---|---|---|
| `NewWebSocket` | 5 | 4 | 2 | 1 | 2 |
| `NewDocker` | 9 | 4 | 0 | 2 | 2 |
| `Instrument(` | 0 | 1 | 0 | 0 | 0 |
| `DefaultStopPolicy` | 2 | 2 | 0 | 0 | 0 |
| `CheckoutOptions{Host` | 0 | 1 | 1 | 1 (`Host:` field) | 0 |
| `Backend` values | 2 | 3 | 1 | 1 | 1 |
| `osaccess` import path | 4 | 2 | 3 | 0 | 0 |

The `eachBackend` harness references `montygo.Backend` in 44 test files; it becomes a harness-level name (`native`, `wasm`, `websocket`, `docker`) mapped to a `WorkerSource`, so those files change in one helper.

## Verification

Re-reading the ask, the decisions and the current documents:

| # | Finding | Proposed resolution |
|---|---|---|
| V1 | `docs/parity/api.md` maps upstream `MontyOptions.minProcesses` / `maxProcesses` to `MinProcesses` / `MaxProcesses`. A pool of remote workers has no processes, and the user calls them workers. | keep the upstream names `MinProcesses`/`MaxProcesses` in `PoolOptions` for parity, or rename to `MinWorkers`/`MaxWorkers` and record the deviation — decided below |
| V2 | `Pool.Run(ctx, code, opts)` and `Pool.Slot(opts)` have no runtime parameter. | `Pool.Run(ctx, rt, code, opts)`, `Pool.Slot(rt, opts)`; `RunOptions` keeps `CheckoutOptions` and `FeedOptions` |
| V3 | `Pool.Backend()` reports an enum that no longer exists as an option. | `Pool.Workers() WorkerKind` with `WorkerNative`, `WorkerWasm`, `WorkerRemote`; the string names stay `native`, `wasm`, `websocket` for the test harness and telemetry attributes |
| V4 | `internal/telemetryhooks` exists only because the engine and the supervisors lived apart. | folds back into root |
| V5 | `internal/buildinfo` carries the pins so packages that cannot import root could read them; now only `telemetry` needs the version. | pins return to `montygo.go` and `scripts/check-pins.sh` reads root again; `internal/buildinfo` keeps only `Set`/`Version` for `telemetry` |
| V6 | CLAUDE.md: "Backends MUST only implement `worker.Worker`" and "pool, session, mount and telemetry code MUST NOT branch on the transport". | unchanged; `WorkerSource` constructors build spawners, the pool still sees `worker.Worker` |
| V7 | CLAUDE.md: "Upstream behaviour is the specification … a deliberate deviation MUST be listed in `docs/parity/`". | new deviations to list: no global `instrument()`; `Runtime` as a Go concept; `Pool.Checkout` taking a runtime |
| V8 | `docs/architecture/versioning.md` says `BindingVersion()` feeds the WebSocket `User-Agent`; root keeps `buildVersion`, so `userAgent()` returns to root. | unchanged contract, documented as-is |
| V9 | The v0.3.0 changelog already describes the alias facade and `NewDocker`. | rewritten: this design ships as the v0.3.0 API; nothing has been released |

## NOT CONSIDERED & TODO

| # | Item | Tags |
|---|---|---|
| N1 | Merging two hosts for one session (runtime host plus a per-checkout host) | postponed — a second runtime covers it |
| N2 | A `Runtime` that carries default `FeedOptions` (inputs, external lookup) rather than only environment | potential-improvement |
| N3 | `host.Host` gaining a fluent `Func(name, fn)` registration beside `NewHost` options; today functions register through the options struct | potential-improvement, to be settled in the target doc's `sandbox/host` section |
| N4 | Sharing one `Runtime` across pools with different worker kinds while type-check stubs differ per worker build | too-complex |
| N5 | A pool metric attribute naming the runtime | potential-improvement |
| N6 | Upstream's global `instrument()` compatibility shim for programs written against `@pydantic/monty` conventions | postponed; deviation recorded |
| N7 | Removing `internal/buildinfo` entirely by giving `telemetry` a `Version` field in `Components` | potential-improvement |

**Verification answer: `MinWorkers`/`MaxWorkers`; V2–V9 and N1–N7 accepted as proposed.** The sizing rename is recorded in `docs/parity/api.md`.

## Summary

### Findings

- The alias facade met a file budget and produced no design: root declared nothing of its own, godoc was empty, functions became variables, and the Examples left the package they document.
- The engine (4,599 lines) is the API and cannot be split from it without a cycle; it belongs in root.
- `telemetry` depended on the value model for one error sentinel; the error contract is its own leaf.
- Two globals contradicted the "parameters and context only" rule: `DefaultStopPolicy` and the process-wide telemetry installation.

### Resolutions

| # | Decision |
|---|---|
| Q1 | the engine returns to root as real types; no aliases, no wrappers; the file budget is dropped |
| Q2 | `Runtime` = sandbox configuration and extensions; `Pool` = worker resources within limits; no package-level state |
| Q3 | `pool.Checkout(ctx, rt, opts)`; one pool serves any number of runtimes |
| Q4 | `montygo/monterr` holds the whole error contract |
| Q5 | `montygo/sandbox`, `sandbox/host`, `sandbox/osaccess` |
| Q6 | telemetry by parameters only; `Instrument`, `Flush`, `Global()` removed; `Instrumentation` builds `Components` |
| Q7 | `PoolOptions.Workers` is `Native(...)`, `Wasm(...)` or `Remote(sup, ...)`; the supervisor contract and recovery live in root; `supervisor/docker` and `supervisor/native` import root |
| Q8 | `NewRuntime(RuntimeOptions{Host, OS, Mounts, Print, TypeCheck, TypeCheckStubs, Limits})`; `CheckoutOptions` loses `Host` |
| Q9 | tests and Examples return to root as `montygo_test`; `conformance/` goes |
| Q10 | the application closes supervisors; no pool ownership |
| V1 | `MinWorkers` / `MaxWorkers`, deviation recorded |

The target architecture is in `docs/brainstorms/20260916-api-surface-target.md`.
