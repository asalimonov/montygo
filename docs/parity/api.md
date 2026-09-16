# API parity with `@pydantic/monty`

The Go API splits upstream's `MontyOptions` and `CheckoutOptions` along a
different seam: a `Runtime` is the sandbox configuration a session runs in
(host objects, OS handler, mounts, print, limits, type checking), a `Pool` is
the worker management, and `Checkout` joins the two. Value types live in
`sandbox`, host objects in `sandbox/host` and errors in `monterr`.

| TypeScript | Go | Notes |
|---|---|---|
| `Monty.create(options)` | `montygo.NewPool(ctx, montygo.PoolOptions{...})` | `Workers` selects native, wasm (the `/wasm` entry) or auto through `Native`, `Wasm`, `Auto` |
| `MontyOptions.binaryPath` / `minProcesses` / `maxProcesses` | `NativeOptions.BinaryPath` / `PoolOptions.MinWorkers` / `MaxWorkers` | `MinWorkers` 0 means 1, negative means none |
| `checkoutTimeout` / `requestTimeout` (seconds) | `CheckoutTimeout` / `RequestTimeout` (`time.Duration`) | `NoRequestTimeout` disables the per-turn deadline |
| `durationLimitGrace` (`null` disables) | `DurationLimitGrace` (`montygo.NoDurationLimitGrace` disables) | |
| `maxCheckoutsPerWorker` | `MaxCheckoutsPerWorker` | |
| `pool.checkout(options)` | `pool.Checkout(ctx, rt, montygo.CheckoutOptions{...})` | `rt` is a `*Runtime` from `NewRuntime(RuntimeOptions{...})` |
| `pool.close()` / `await using` | `pool.Close(ctx)` / `defer` | |
| `CheckoutOptions.scriptName`, `limits` | `CheckoutOptions.ScriptName`, `Limits` | `Limits` replace `RuntimeOptions.Limits` for one session |
| `CheckoutOptions.typeCheck`, `typeCheckStubs`, `typeCheckFormat`, `typeCheckColor` | `RuntimeOptions.TypeCheck`, `TypeCheckStubs`, `TypeCheckFormat`, `TypeCheckColor` | validated by `NewRuntime` |
| `assertMessageAnnotations` (`boolean \| number`) | `RuntimeOptions.AssertMessageAnnotations *uint32` | `montygo.Uint32(0)` disables |
| `printFlushInterval` (seconds) | `RuntimeOptions.PrintFlushInterval *time.Duration` | |
| `ResourceLimits.maxDurationSecs`, `maxMemory`, `gcInterval`, `maxRecursionDepth`, `maxSuspensions` | `MaxDuration`, `MaxMemory`, `GCInterval`, `MaxRecursionDepth`, `MaxSuspensions` | zero means the default; `montygo.Unlimited` / `montygo.UnlimitedDuration` disable a limit; `MaxRecursionDepth` cannot be unlimited; `MaxSuspensions` counts per session, as upstream counts per checkout |
| `session.feedRun(code, options)` | `session.FeedRun(ctx, code, &montygo.FeedOptions{...})` | |
| `FeedOptions.inputs`, `externalLookup`, `printCallback`, `mount`, `cwd`, `skipTypeCheck` | `Inputs`, `ExternalLookup`, `Print`, `Mount`, `Cwd`, `SkipTypeCheck` | `Mount` adds to `RuntimeOptions.Mounts`; `Print` overrides `RuntimeOptions.Print` |
| `FeedOptions.os` | `RuntimeOptions.OS` | the handler belongs to the runtime, not the feed |
| `session.feedStart` | `session.FeedStart` | |
| `session.loadSession` / `loadSnapshot` / `dump` | `LoadSession` / `LoadSnapshot` / `Dump` | |
| `session.installDependencies` | `session.InstallDependencies` | |
| `session.workerPid` | `session.WorkerPID() (int, bool)` | |
| `session.close()` | `session.Close(ctx[, policy])` | a running feed is stopped with the policy first; `Close(ctx, montygo.KillNow)` ends it at once |
| `FunctionSnapshot` (`resume`, `resumeAuto`, `resumeError`, `resumeNotFound`, `resumeFuture`, `resumeNotHandled`, `dump`) | `*montygo.FunctionSnapshot` (`Resume`, `ResumeAuto`, `ResumeError`, `ResumeNotFound`, `ResumeFuture`, `ResumeNotHandled`, `Dump`) | |
| `NameLookupSnapshot.resume(name?)` / `resumeValue` / `resumeAuto` | `ResumeFunction(name)`, `ResumeUnresolved()` / `ResumeValue` / `ResumeAuto` | |
| `FutureSnapshot.resume([{callId, value \| error}])` | `Resume([]montygo.FutureResolution{CallID, Value, Err})` | |
| `MontyComplete.output` | `*montygo.Complete.Output` | |
| `NOT_HANDLED` | `host.NotHandled` | |
| external function (sync / async) | Go func, `host.Function`, `host.FunctionFunc`; async via `*host.Future` (`host.Async`, `host.AsyncContext`) | keyword args arrive in a trailing `host.Kwargs`; `AsyncContext` follows the callback context |
| thrown `Error` with `name` | `monterr.Raise(excType, msg)` | other errors raise `RuntimeError` |
| `ClassInstance(obj, {eagerAttrs, lazyAttrs, allowedMethods, name, convertValue, id, classType})` | `host.NewClassInstance(obj, host.ClassInstanceOptions{...})` | policies are `host.All()` / `host.Names(...)` / `host.Expose[T]()` |
| `ClassType(cls, {..., init, instanceEagerAttrs, instanceLazyAttrs, instanceAllowedMethods})` | `host.NewClassType[T](host.ClassTypeOptions{...})` | class members go in `Statics`; `Constructor` builds instances |
| `ClassType.construct` / `instanceWrapper` override | `ClassType.Construct` / `InstanceWrapper` option | |
| `MontyClassProxy` | `*host.ClassProxy` | |
| `CollectString` / `CollectStreams` / `DEFAULT_MAX_PRINT_COLLECT_BYTES` | `sandbox.NewCollectString` / `sandbox.NewCollectStreams` / `sandbox.DefaultMaxPrintCollectBytes` | `sandbox.UnlimitedPrintCollect` is `null` |
| `PrintCallback` | `sandbox.PrintTarget`, `sandbox.PrintFunc`, `sandbox.ContextPrintTarget`, `sandbox.FlushingPrintTarget` | `sandbox.Lines` delivers whole lines |
| `MountDir({hostPath, virtualPath, mode, writeBytesLimit, memoryUsageLimit})` | `sandbox.NewMountDir(sandbox.MountDirOptions{...})` | |
| `MontyFileHandle(path, mode, {position})` | `sandbox.NewFileHandle(path, mode, position)` | |
| `MontyError`, `MontySyntaxError`, `MontyRuntimeError`, `MontyTypingError`, `MontyCrashedError`, `ProtocolError` | `monterr.Error`, `*SyntaxError`, `*RuntimeError`, `*TypingError`, `*CrashedError`, `*ProtocolError` | every session-loss error matches `monterr.ErrSessionLost` |
| `montyVersion()` (native binding) | `montygo.MontyVersion` | the upstream release; `montygo.BindingVersion()` is this module's release; `Version` is a deprecated alias |
| `Frame`, `ExceptionInfo`, `display(format)` | `monterr.Frame`, `monterr.ExceptionInfo`, `Display(monterr.DisplayFormat)` | |
| `MAX_VALUE_DEPTH` | `montygo.MaxValueDepth` | |
| `findMontyBinary` | `montygo.FindMontyBinary` | |
| `instrumentTelemetry` / `flushTelemetry` / `MontyInstrumentation` | `PoolOptions.Telemetry *telemetry.Components` / provider flushing / `telemetry.NewInstrumentation` | per pool, never process-wide; `Instrumentation.Components()` builds the components |
| Value markers (`MontyDate`, `MontyDateTime`, `MontyTime`, `MontyTimeDelta`, `MontyTimeZone`, `MontyException`) | `sandbox.Date`, `DateTime`, `Time`, `TimeDelta`, `TimeZone`, `Exception` | |
| named tuple values | `sandbox.NamedTuple`, `host.AsNamedTuple(struct)`, `host.NewNamedTuple(typeName, pairs...)` | |
| `Map` / `Set` / tuple arrays / `BigInt` / `Buffer` | `*sandbox.Dict` / `*sandbox.Set`, `*sandbox.FrozenSet` / `sandbox.Tuple` / `*big.Int` / `[]byte` | |

## Beyond the TypeScript package

| Feature | Go | Origin |
|---|---|---|
| Runtime | `montygo.Runtime`, `RuntimeOptions`, `NewRuntime`, `Runtime.Options` | montygo; an immutable sandbox configuration shared by sessions of any pool |
| Worker sources | `montygo.WorkerSource`, `Native`, `Wasm`, `Remote`, `Auto`, `NativeOptions`, `WasmOptions`, `RemoteOptions`, `WorkerKind`, `Pool.Workers` | montygo; a sealed interface, constructors only |
| Remote workers | `montygo.Remote`, `*monterr.DisconnectError`, `*monterr.ShutdownError` | Python `AsyncMontyWebsocket` |
| Fixed server URL | `montygo.StaticServer(url, tlsConfig, headers)` | montygo; the supervisor of a URL |
| TLS settings for remote dials | `RemoteOptions.TLSConfig`, `ServerEndpoint.TLSConfig` | montygo; Python has no equivalent |
| Custom TCP dialer for remote dials | `RemoteOptions.DialContext` | montygo; Python has no equivalent |
| Server health probe | `montygo.CheckServerHealth(ctx, sup, RemoteOptions)` (`GET <path>/health`) | montygo; Python has no equivalent |
| In-memory OS helpers | package `sandbox/osaccess` | Python `AbstractOS`, `OSAccess`, `MemoryFile`, `CallbackFile`, `StatResult` |
| Mounts on the wasm backend | host-side mount servicing | the TS browser entry has no mounts |
| Memory-limit classification on wasm | exit code 65 → `MemoryError` | the TS browser worker reports a crash |
| Server info | `montygo.FetchServerInfo(ctx, sup, RemoteOptions)` (`GET <path>/info`), `ServerInfo`, `ServerLimits`, `monterr.ErrNoServerInfo` | montygo; `monty-server` addition, see `server.md` |
| Binding version | `montygo.BindingVersion()`, `montygo.MontyVersion` | montygo; see `docs/architecture/versioning.md` |
| Session lifecycle | `Session.Stop`, `Session.State`, `SessionState` (`SessionIdle`, `SessionRunning`, `SessionPaused`, `SessionClosed`), `Session.Done`, `Session.Err`, `Session.Stats`, `SessionStats`, `Session.Go`, `*Run` (`Wait`, `WaitContext`, `Done`, `Stop`) | montygo; synchronous execution admission, `ErrSessionBusy`, generation-bound snapshots |
| Lost-session classification | `monterr.ErrSessionLost`; `Is` on `*CrashedError`, `*DisconnectError`, `*ShutdownError`, `*ProtocolError`, `*SessionKilledError`, `*RotationError` and fatal memory `*RuntimeError`; `DisconnectError.Code`, `Reason` | `ErrSessionClosed` deliberately does not match loss |
| Stop policy | `StopPolicy` (`Drain`, `Timeout`, `Join`, `Reason`, `Catchable`), `KillNow`, `PoolOptions.Stop`, `CheckoutOptions.Stop`, `Stopped` (`How`, `Err`, `SessionErr`, `SessionKept`), `StopKind` (`StopPending`, `StopNotRunning`, `StopAborted`, `StopKilled`, `StopFinished`), `monterr.ErrCallbackDetached` | montygo; one library-owned timeline (request, kill at `Timeout`, join) for `Stop`, `Close`, `Shutdown` and feed-context cancellation; caller context is wait-only; the built-in default is not a variable |
| Catchable stop | `StopPolicy.Catchable` | montygo; the reason is raised in the sandbox at the next host call or await; no upstream counterpart |
| One-shot run | `Pool.Run(ctx, rt, code, opts)`, `RunOptions` | montygo; checkout, `FeedRun` and close in one call |
| Session holder | `Pool.Slot(rt, opts)`, `*Slot` (`Session`, `State`, `Go`, `FeedRun`, `FeedStart`, `Stop`, `Close`) | montygo; re-checks out after a loss, one execution at a time |
| Host-side bounds | `RuntimeOptions.MaxHostObjects`, `MaxPendingFutures`, `*monterr.ResourceError`; `PoolOptions.MaxPendingBytes`, `UnlimitedPendingBytes` | montygo; TS keeps every wrapper and buffers every frame |
| Explicit unlimited | `montygo.Unlimited`, `montygo.UnlimitedDuration` | montygo; TS has no unlimited suspensions |
| Host registry | `host.NewHost`, `*host.Host` (`Func`, `Object`, `Names`, `Stubs`, `Restorable`), `host.HostFuncOptions`, `ClassInstanceOptions.ParameterNames`, `ClassTypeOptions.ParameterNames`, `RuntimeOptions.Host`, `monterr.ErrHostObjectNotRestorable` | fixed reflected parameters are positional-only; optional labels do not enable keyword binding; `/` follows a fixed parameter only |
| Runtime mounts and print | `RuntimeOptions.Mounts`, `RuntimeOptions.Print` | montygo; feed options add to or override them |
| Interface-driven exposure | `host.Expose[T]()` | montygo |
| Pool shutdown and accounting | `Pool.Shutdown(ctx[, policy])`, `Pool.Stats`, `PoolStats` | montygo; TS `close()` does not wait for sessions; `Shutdown` closes every open session with the stop policy and waits for the workers |
| Per-pool telemetry | `PoolOptions.Telemetry`, `telemetry.Components`, `telemetry.Instrumentation.Components` | montygo; TS installs process-wide only |
| Line-oriented print | `sandbox.Lines`, `sandbox.FlushingPrintTarget` | montygo |
| Cancellable async host work | `host.AsyncContext` | montygo |
| Struct to named tuple | `host.AsNamedTuple`, `host.NewNamedTuple` | montygo |
| Known exception names | `monterr.KnownExceptionNames()` | montygo; a sorted copy of the names `Raise` maps to their own type |

## Deviations

| Behaviour | `@pydantic/monty` | montygo |
|---|---|---|
| Sandbox configuration | per checkout and per feed (`os`) | a `Runtime` built once and shared; `CheckoutOptions` keep `ScriptName`, `Limits` and `Stop`; the OS handler, host objects, mounts, print and type checking come from the runtime |
| Cancelling the feed context | the worker is killed and the session is poisoned, whether Python executes or waits on a host call | the run ends through the session's stop policy. Where Python yields (a host call, an `await`, a paused snapshot), `AbortFeed(KeyboardInterrupt)` ends the feed: `FeedRun` returns a `*RuntimeError` with `TypeName` `KeyboardInterrupt` and the session stays usable. Where Python never yields, the worker is killed when the policy's `Timeout` expires and `FeedRun` returns a `*SessionKilledError`. `Run.Stop` and `Session.Stop` use the same path with a chosen reason. |
| Catching the stop in the sandbox | not applicable | by default the sandbox cannot catch it, because `AbortFeed` ends the feed. `StopPolicy.Catchable` raises the reason as an ordinary exception at the next host call or await instead, so `except KeyboardInterrupt` can run cleanup with further host calls; the kill at `Timeout` still applies. A catchable stop of a paused snapshot falls back to `AbortFeed`. |
| `session.close()` during a running feed | the caller awaits the feed first | `Session.Close(ctx[, policy])` stops the running feed with the policy, then finishes the session; the context bounds only the caller's wait and the close continues in the background |
| Pool shutdown | `close()` returns without waiting for checked-out sessions | `Pool.Shutdown(ctx[, policy])` closes every open session with the policy concurrently, running feeds stopped at once unless `Drain` is set, and waits for every worker within `ctx`; there is no separate force grace |
| `MaxSuspensions` | counted per checkout, no unlimited | counted per checkout (session), reset by `LoadSession` and `LoadSnapshot`; `Unlimited` disables it |
| `All` | a value | a function, `host.All()` |
| Telemetry installation | process-wide `instrumentTelemetry` | a pool parameter; nothing is global |

The v0.3.0 lifecycle is a deliberate Go-only design. One `StopPolicy` timeline
(`Drain`, request, kill at `Timeout`, join) is owned by the library and resolved
through the built-in default, `PoolOptions.Stop`, `CheckoutOptions.Stop` and the
call. Future subscriptions are per execution/call ID, not per shared Future
object. Terminal cleanup cancels callback work and drops subscriptions without
settling caller-owned Futures. A kill can retire a worker while a Go callback
still runs; `Stop` then reports `ErrCallbackDetached` after `Join`. No wire or
server protocol change is required. The README lists the v0.2.0 → v0.3.0
renames.

## Go additions: supervisors and rotation

Upstream has no analogue. `@pydantic/monty` is subprocess-only, and
`AsyncMontyWebsocket` dials one fixed URL, never reconnects and never rotates a
session. These additions change no wire or server protocol.

| Go | Purpose |
|---|---|
| `ServerEndpoint`, `ServerSupervisor` | where a pool dials, resolved per attempt, and how a server is restarted; declared in the root package so an application can implement one |
| `StaticServer` | the supervisor of a fixed URL: the endpoint never moves and `Restart` fails |
| `RecoveryPolicy`, `RemoteOptions.Recovery` | attempts, per-attempt timeout and the opt-in server restart |
| `RemoteOptions.RotateSessions`, `RotationMargin` | session rotation before the server's session timeout |
| `OrphanReaper` | removes servers left by a process that died; nil reaps nothing |
| `*monterr.RotationError` | a session that could not move to a fresh connection; `Dump` restores it elsewhere; matches `ErrSessionLost` |
| `monterr.ErrSupervisorClosed` | a supervisor that no longer serves endpoints |
| `supervisor/docker` (`New`, `Options`, `Supervisor`, `DefaultImage`, `ImageEnv`, `VersionEnv`) | runs the `monty-server` image through the local `docker` CLI; the tag derives from `BindingVersion()`; `MaxSessions` sizes the server |
| `supervisor/native` (`New`, `Options`, `Supervisor`, `BinaryEnv`, `DefaultBinary`) | runs `monty-server` as a child process on an ephemeral loopback port; the same limit policy as the container supervisor |

A supervisor is never owned by a pool: the application closes it after the
pools that dial it. Rotation is off unless `RotateSessions` asks for it, and
`Auto()` never contacts Docker.

## Not provided

| TypeScript | Reason |
|---|---|
| Browser `Worker` pool, `WorkerTransport`, `WasmHost`, `WorkerChannel` factories | browser-only plumbing; the wasm backend covers the use case |
| JS prototype hardening (`constructor`, `__proto__`, `Function.prototype`) | no Go analogue; unexported members are unreachable |
| Windows native backend | not supported in this release; the wasm backend works on Windows |

## montypb

`montypb` holds the protobuf types generated from `proto/monty/v1/monty.proto`. The package is public because `tests/network` and consumers speaking the raw protocol import it, but it carries no stability promise beyond `montygo.ProtocolVersion`: it is regenerated on every protocol change, and names, fields and enum values follow upstream `monty.proto` without deprecation. The runtime codec in `internal/wire` does not use it; it is the oracle for the differential, benchmark and fuzz tests.
