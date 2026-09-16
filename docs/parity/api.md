# API parity with `@pydantic/monty`

| TypeScript | Go | Notes |
|---|---|---|
| `Monty.create(options)` | `montygo.New(ctx, montygo.Options{...})` | `Backend` selects native, wasm (the `/wasm` entry) or auto |
| `MontyOptions.binaryPath` / `minProcesses` / `maxProcesses` | `BinaryPath` / `MinProcesses` / `MaxProcesses` | `MinProcesses` 0 means 1, negative means none |
| `checkoutTimeout` / `requestTimeout` (seconds) | `CheckoutTimeout` / `RequestTimeout` (`time.Duration`) | |
| `durationLimitGrace` (`null` disables) | `DurationLimitGrace` (`montygo.NoDurationLimitGrace` disables) | |
| `maxCheckoutsPerWorker` | `MaxCheckoutsPerWorker` | |
| `pool.checkout(options)` | `pool.Checkout(ctx, montygo.CheckoutOptions{...})` | |
| `pool.close()` / `await using` | `pool.Close(ctx)` / `defer` | |
| `CheckoutOptions.scriptName`, `limits`, `typeCheck`, `typeCheckStubs`, `typeCheckFormat`, `typeCheckColor` | same fields | |
| `assertMessageAnnotations` (`boolean \| number`) | `AssertMessageAnnotations *uint32` | `montygo.Uint32(0)` disables |
| `printFlushInterval` (seconds) | `PrintFlushInterval *time.Duration` | |
| `ResourceLimits.maxDurationSecs`, `maxMemory`, `gcInterval`, `maxRecursionDepth`, `maxSuspensions` | `MaxDuration`, `MaxMemory`, `GCInterval`, `MaxRecursionDepth`, `MaxSuspensions` | zero means the default; `montygo.Unlimited` / `montygo.UnlimitedDuration` disable a limit; `MaxRecursionDepth` cannot be unlimited; `MaxSuspensions` counts per session, as upstream counts per checkout |
| `session.feedRun(code, options)` | `session.FeedRun(ctx, code, &montygo.FeedOptions{...})` | |
| `FeedOptions.inputs`, `externalLookup`, `printCallback`, `mount`, `cwd`, `os`, `skipTypeCheck` | `Inputs`, `ExternalLookup`, `Print`, `Mount`, `Cwd`, `OS`, `SkipTypeCheck` | |
| `session.feedStart` | `session.FeedStart` | |
| `session.loadSession` / `loadSnapshot` / `dump` | `LoadSession` / `LoadSnapshot` / `Dump` | |
| `session.installDependencies` | `session.InstallDependencies` | |
| `session.workerPid` | `session.WorkerPID() (int, bool)` | |
| `session.close()` | `session.Close(ctx[, policy])` | a running feed is stopped with the policy first; `Close(ctx, montygo.KillNow)` ends it at once |
| `FunctionSnapshot` (`resume`, `resumeAuto`, `resumeError`, `resumeNotFound`, `resumeFuture`, `resumeNotHandled`, `dump`) | `*montygo.FunctionSnapshot` (`Resume`, `ResumeAuto`, `ResumeError`, `ResumeNotFound`, `ResumeFuture`, `ResumeNotHandled`, `Dump`) | |
| `NameLookupSnapshot.resume(name?)` / `resumeValue` / `resumeAuto` | `ResumeFunction(name)`, `ResumeUnresolved()` / `ResumeValue` / `ResumeAuto` | |
| `FutureSnapshot.resume([{callId, value \| error}])` | `Resume([]montygo.FutureResolution{CallID, Value, Err})` | |
| `MontyComplete.output` | `*montygo.Complete.Output` | |
| `NOT_HANDLED` | `montygo.NotHandled` | |
| external function (sync / async) | Go func, `montygo.Function`, `montygo.FunctionFunc`; async via `*montygo.Future` (`montygo.Async`, `montygo.AsyncContext`) | keyword args arrive in a trailing `montygo.Kwargs`; `AsyncContext` follows the callback context |
| thrown `Error` with `name` | `montygo.Raise(excType, msg)` | other errors raise `RuntimeError` |
| `ClassInstance(obj, {eagerAttrs, lazyAttrs, allowedMethods, name, convertValue, id, classType})` | `montygo.NewClassInstance(obj, montygo.ClassInstanceOptions{...})` | policies are `montygo.All()` / `montygo.Names(...)` / `montygo.Expose[T]()` |
| `ClassType(cls, {..., init, instanceEagerAttrs, instanceLazyAttrs, instanceAllowedMethods})` | `montygo.NewClassType[T](montygo.ClassTypeOptions{...})` | class members go in `Statics`; `Constructor` builds instances |
| `ClassType.construct` / `instanceWrapper` override | `ClassType.Construct` / `InstanceWrapper` option | |
| `MontyClassProxy` | `*montygo.ClassProxy` | |
| `CollectString` / `CollectStreams` / `DEFAULT_MAX_PRINT_COLLECT_BYTES` | `montygo.NewCollectString` / `montygo.NewCollectStreams` / `montygo.DefaultMaxPrintCollectBytes` | `montygo.UnlimitedPrintCollect` is `null` |
| `PrintCallback` | `montygo.PrintTarget`, `montygo.PrintFunc`, `montygo.ContextPrintTarget`, `montygo.FlushingPrintTarget` | `montygo.Lines` delivers whole lines |
| `MountDir({hostPath, virtualPath, mode, writeBytesLimit, memoryUsageLimit})` | `montygo.NewMountDir(montygo.MountDirOptions{...})` | |
| `MontyFileHandle(path, mode, {position})` | `montygo.NewFileHandle(path, mode, position)` | |
| `MontyError`, `MontySyntaxError`, `MontyRuntimeError`, `MontyTypingError`, `MontyCrashedError`, `ProtocolError` | `montygo.Error`, `*SyntaxError`, `*RuntimeError`, `*TypingError`, `*CrashedError`, `*ProtocolError` | every session-loss error matches `montygo.ErrSessionLost` |
| `montyVersion()` (native binding) | `montygo.MontyVersion` | the upstream release; `montygo.BindingVersion()` is this module's release; `Version` is a deprecated alias |
| `Frame`, `ExceptionInfo`, `display(format)` | `montygo.Frame`, `montygo.ExceptionInfo`, `Display(montygo.DisplayFormat)` | |
| `MAX_VALUE_DEPTH` | `montygo.MaxValueDepth` | |
| `findMontyBinary` | `montygo.FindMontyBinary` | |
| `instrumentTelemetry` / `flushTelemetry` / `MontyInstrumentation` | `montygo.Instrument` / `montygo.Flush` / `montygo.NewInstrumentation` | process-wide; `Options.Telemetry` and `WebSocketOptions.Telemetry` select components per pool |
| Value markers (`MontyDate`, `MontyDateTime`, `MontyTime`, `MontyTimeDelta`, `MontyTimeZone`, `MontyException`) | `montygo.Date`, `DateTime`, `Time`, `TimeDelta`, `TimeZone`, `Exception` | |
| named tuple values | `montygo.NamedTuple`, `montygo.AsNamedTuple(struct)`, `montygo.NewNamedTuple(typeName, pairs...)` | |
| `Map` / `Set` / tuple arrays / `BigInt` / `Buffer` | `*montygo.Dict` / `*montygo.Set`, `*montygo.FrozenSet` / `montygo.Tuple` / `*big.Int` / `[]byte` | |

## Beyond the TypeScript package

| Feature | Go | Origin |
|---|---|---|
| WebSocket workers | `montygo.NewWebSocket`, `*DisconnectError`, `*ShutdownError` | Python `AsyncMontyWebsocket` |
| TLS settings for WebSocket dials | `WebSocketOptions.TLSConfig` | montygo; Python has no equivalent |
| Custom TCP dialer for WebSocket dials | `WebSocketOptions.DialContext` | montygo; Python has no equivalent |
| Server health probe | `montygo.CheckWebSocketHealth` (`GET <path>/health`) | montygo; Python has no equivalent |
| In-memory OS helpers | package `osaccess` | Python `AbstractOS`, `OSAccess`, `MemoryFile`, `CallbackFile`, `StatResult` |
| Mounts on the wasm backend | host-side mount servicing | the TS browser entry has no mounts |
| Memory-limit classification on wasm | exit code 65 → `MemoryError` | the TS browser worker reports a crash |
| Server info | `montygo.FetchServerInfo` (`GET <path>/info`), `ServerInfo`, `ServerLimits`, `ErrNoServerInfo` | montygo; `monty-server` addition, see `server.md` |
| Binding version | `montygo.BindingVersion()`, `montygo.MontyVersion` | montygo; see `docs/architecture/versioning.md` |
| Session lifecycle | `Session.Stop`, `Session.State`, `SessionState` (`SessionIdle`, `SessionRunning`, `SessionPaused`, `SessionClosed`), `Session.Done`, `Session.Err`, `Session.Stats`, `SessionStats`, `Session.Go`, `*Run` (`Wait`, `WaitContext`, `Done`, `Stop`) | montygo; synchronous execution admission, `ErrSessionBusy`, generation-bound snapshots |
| Lost-session classification | `ErrSessionLost`; `Is` on `*CrashedError`, `*DisconnectError`, `*ShutdownError`, `*ProtocolError`, `*SessionKilledError` and fatal memory `*RuntimeError`; `DisconnectError.Code`, `Reason` | `ErrSessionClosed` deliberately does not match loss |
| Stop policy | `StopPolicy` (`Drain`, `Timeout`, `Join`, `Reason`, `Catchable`), `DefaultStopPolicy`, `KillNow`, `Options.Stop`, `WebSocketOptions.Stop`, `CheckoutOptions.Stop`, `Stopped` (`How`, `Err`, `SessionErr`, `SessionKept`), `StopKind` (`StopPending`, `StopNotRunning`, `StopAborted`, `StopKilled`, `StopFinished`), `ErrCallbackDetached` | montygo; one library-owned timeline (request, kill at `Timeout`, join) for `Stop`, `Close`, `Shutdown` and feed-context cancellation; caller context is wait-only |
| Catchable stop | `StopPolicy.Catchable` | montygo; the reason is raised in the sandbox at the next host call or await; no upstream counterpart |
| One-shot run | `Pool.Run`, `RunOptions` | montygo; checkout, `FeedRun` and close in one call |
| Session holder | `Pool.Slot`, `*Slot` (`Session`, `State`, `Go`, `FeedRun`, `FeedStart`, `Stop`, `Close`) | montygo; re-checks out after a loss, one execution at a time |
| Host-side bounds | `CheckoutOptions.MaxHostObjects`, `MaxPendingFutures`, `*ResourceError`; `Options.MaxPendingBytes`, `UnlimitedPendingBytes` | montygo; TS keeps every wrapper and buffers every frame |
| Explicit unlimited | `montygo.Unlimited`, `montygo.UnlimitedDuration` | montygo; TS has no unlimited suspensions |
| Host registry | `montygo.NewHost`, `*Host` (`Func`, `Object`, `Names`, `Stubs`, `Restorable`), `HostFuncOptions`, `ClassInstanceOptions.ParameterNames`, `ClassTypeOptions.ParameterNames`, `CheckoutOptions.Host`, `ErrHostObjectNotRestorable` | fixed reflected parameters are positional-only; optional labels do not enable keyword binding; `/` follows a fixed parameter only |
| Interface-driven exposure | `montygo.Expose[T]()` | montygo |
| Pool shutdown and accounting | `Pool.Shutdown(ctx[, policy])`, `Pool.Stats`, `PoolStats` | montygo; TS `close()` does not wait for sessions; `Shutdown` closes every open session with the stop policy and waits for the workers |
| Per-pool telemetry | `Options.Telemetry`, `WebSocketOptions.Telemetry` | montygo; TS installs process-wide only |
| Line-oriented print | `montygo.Lines`, `montygo.FlushingPrintTarget` | montygo |
| Cancellable async host work | `montygo.AsyncContext` | montygo |
| Struct to named tuple | `montygo.AsNamedTuple`, `montygo.NewNamedTuple` | montygo |
| Known exception names | `montygo.KnownExceptionNames()` | montygo; a sorted copy of the names `Raise` maps to their own type |

## Deviations

| Behaviour | `@pydantic/monty` | montygo |
|---|---|---|
| Cancelling the feed context | the worker is killed and the session is poisoned, whether Python executes or waits on a host call | the run ends through the session's stop policy. Where Python yields (a host call, an `await`, a paused snapshot), `AbortFeed(KeyboardInterrupt)` ends the feed: `FeedRun` returns a `*RuntimeError` with `TypeName` `KeyboardInterrupt` and the session stays usable. Where Python never yields, the worker is killed when the policy's `Timeout` expires and `FeedRun` returns a `*SessionKilledError`. `Run.Stop` and `Session.Stop` use the same path with a chosen reason. |
| Catching the stop in the sandbox | not applicable | by default the sandbox cannot catch it, because `AbortFeed` ends the feed. `StopPolicy.Catchable` raises the reason as an ordinary exception at the next host call or await instead, so `except KeyboardInterrupt` can run cleanup with further host calls; the kill at `Timeout` still applies. A catchable stop of a paused snapshot falls back to `AbortFeed`. |
| `session.close()` during a running feed | the caller awaits the feed first | `Session.Close(ctx[, policy])` stops the running feed with the policy, then finishes the session; the context bounds only the caller's wait and the close continues in the background |
| Pool shutdown | `close()` returns without waiting for checked-out sessions | `Pool.Shutdown(ctx[, policy])` closes every open session with the policy concurrently, running feeds stopped at once unless `Drain` is set, and waits for every worker within `ctx`; there is no separate force grace |
| `MaxSuspensions` | counted per checkout, no unlimited | counted per checkout (session), reset by `LoadSession` and `LoadSnapshot`; `Unlimited` disables it |
| `All` | a value | a function, `montygo.All()` |

The v0.3.0 lifecycle is a deliberate Go-only design. One `StopPolicy` timeline
(`Drain`, request, kill at `Timeout`, join) is owned by the library and resolved
through `DefaultStopPolicy`, `Options.Stop` / `WebSocketOptions.Stop`,
`CheckoutOptions.Stop` and the call. Future subscriptions are per execution/call
ID, not per shared Future object. Terminal cleanup cancels callback work and
drops subscriptions without settling caller-owned Futures. A kill can retire a
worker while a Go callback still runs; `Stop` then reports `ErrCallbackDetached`
after `Join`. No wire or server protocol change is required. The README lists
the v0.2.0 → v0.3.0 renames.

## Go additions: Docker, supervisors and rotation

Upstream has no analogue. `@pydantic/monty` is subprocess-only, and
`AsyncMontyWebsocket` dials one fixed URL, never reconnects and never rotates a
session. These additions change no wire or server protocol.

| Go | Purpose |
|---|---|
| `montygo.NewDocker(ctx, DockerOptions)` | starts a `monty-server` container through the local `docker` CLI and returns a pool that owns it; `Pool.Backend()` is `BackendDocker` |
| `montygo.NewDockerSupervisor`, `*DockerSupervisor` | the same container management as a value, for use with `NewWebSocket` |
| `DockerOptions` | image, version, CLI command, server variables, `docker run` arguments, timeouts, reaper, and the pool's own options |
| `DefaultDockerImage`, `MONTYGO_DOCKER_IMAGE`, `MONTYGO_DOCKER_VERSION` | the default repository and the overrides; the tag derives from `BindingVersion()` |
| `ServerEndpoint`, `ServerSupervisor` | where a pool dials, resolved per attempt, and how a server is restarted |
| `RecoveryPolicy` | attempts, per-attempt timeout and the opt-in server restart |
| `WebSocketOptions.Supervisor`, `Recovery`, `RotateSessions`, `RotationMargin` | supervised dials and session rotation on a plain WebSocket pool |
| `OrphanReaper` | removes containers left by a process that died; the default reaps nothing |
| `*RotationError` | a session that could not move to a fresh connection; `Dump` restores it elsewhere; matches `ErrSessionLost` |
| `ErrSupervisorClosed` | a supervisor that no longer serves endpoints |
| `montygo/supervisor/native` (`New`, `NewPool`, `Options`, `Supervisor`) | runs `monty-server` as a child process on an ephemeral loopback port; the same limit policy as the container supervisor |
| `montygo/supervisor/docker` (`New`, `NewPool`, `Options`, `Supervisor`) | the container supervisor; `montygo.NewDocker` and `montygo.DockerOptions` remain as aliases |

The implementation is split by concern: `runtime` and `runtime/host` hold the
Python value model and host objects, `supervisor` the server contract,
`telemetry` the OpenTelemetry surface, and `internal/engine` the pool and
session machinery. The root package is a facade over them, so a program that
imports only `montygo` sees the same API as before.

Rotation is off unless it is asked for: `NewWebSocket` keeps upstream behaviour
until `RotateSessions` is set, and `BackendAuto` never contacts Docker.

## Not provided

| TypeScript | Reason |
|---|---|
| Browser `Worker` pool, `WorkerTransport`, `WasmHost`, `WorkerChannel` factories | browser-only plumbing; the wasm backend covers the use case |
| JS prototype hardening (`constructor`, `__proto__`, `Function.prototype`) | no Go analogue; unexported members are unreachable |
| Windows native backend | not supported in this release; the wasm backend works on Windows |

## montypb

`montypb` holds the protobuf types generated from `proto/monty/v1/monty.proto`. The package is public because `tests/network` and consumers speaking the raw protocol import it, but it carries no stability promise beyond `montygo.ProtocolVersion`: it is regenerated on every protocol change, and names, fields and enum values follow upstream `monty.proto` without deprecation. The runtime codec in `internal/wire` does not use it; it is the oracle for the differential, benchmark and fuzz tests.
