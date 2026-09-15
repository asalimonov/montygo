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
| `session.close()` | `session.Close(ctx)` | `CloseNow` ends a running feed at once |
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
| Session lifecycle | `Session.Interrupt`, `Session.CloseNow`, `Session.Done`, `Session.Err`, `Session.Stats`, `SessionStats`, `Session.Go`, `*Run` (`Wait`, `Done`, `Interrupt`) | montygo; TS has only `close()` |
| Lost-session classification | `ErrSessionLost`; `Is` on `*CrashedError`, `*DisconnectError`, `*ShutdownError`, `*ProtocolError` and `ErrSessionClosed`; `DisconnectError.Code`, `Reason` | montygo |
| Interrupt grace | `CheckoutOptions.InterruptGrace` | montygo |
| Host-side bounds | `CheckoutOptions.MaxHostObjects`, `MaxPendingFutures`, `*ResourceError`; `Options.MaxPendingBytes`, `UnlimitedPendingBytes` | montygo; TS keeps every wrapper and buffers every frame |
| Explicit unlimited | `montygo.Unlimited`, `montygo.UnlimitedDuration` | montygo; TS has no unlimited suspensions |
| Host registry | `montygo.NewHost`, `*Host` (`Func`, `Object`, `Names`, `Stubs`, `Restorable`), `CheckoutOptions.Host`, `ErrHostObjectNotRestorable` | montygo |
| Interface-driven exposure | `montygo.Expose[T]()` | montygo |
| Pool shutdown and accounting | `Pool.Shutdown`, `Pool.Stats`, `PoolStats` | montygo; TS `close()` does not wait for sessions |
| Per-pool telemetry | `Options.Telemetry`, `WebSocketOptions.Telemetry` | montygo; TS installs process-wide only |
| Line-oriented print | `montygo.Lines`, `montygo.FlushingPrintTarget` | montygo |
| Cancellable async host work | `montygo.AsyncContext` | montygo |
| Struct to named tuple | `montygo.AsNamedTuple`, `montygo.NewNamedTuple` | montygo |
| Known exception names | `montygo.KnownExceptionNames()` | montygo; a sorted copy of the names `Raise` maps to their own type |

## Deviations

| Behaviour | `@pydantic/monty` | montygo |
|---|---|---|
| Cancelling the feed context while the worker waits on a host call | the worker is killed and the session is poisoned | `AbortFeed(KeyboardInterrupt)` ends the feed; `FeedRun` returns a `*RuntimeError` with `TypeName` `KeyboardInterrupt` and the session stays usable. `Session.Interrupt` uses the same path with a chosen reason. The sandbox cannot catch it with `except KeyboardInterrupt`. Cancelling while Python executes still kills the worker. |
| `MaxSuspensions` | counted per checkout, no unlimited | counted per checkout (session), reset by `LoadSession` and `LoadSnapshot`; `Unlimited` disables it |
| `All` | a value | a function, `montygo.All()` |

## Not provided

| TypeScript | Reason |
|---|---|
| Browser `Worker` pool, `WorkerTransport`, `WasmHost`, `WorkerChannel` factories | browser-only plumbing; the wasm backend covers the use case |
| JS prototype hardening (`constructor`, `__proto__`, `Function.prototype`) | no Go analogue; unexported members are unreachable |
| Windows native backend | not supported in this release; the wasm backend works on Windows |

## montypb

`montypb` holds the protobuf types generated from `proto/monty/v1/monty.proto`. The package is public because `tests/network` and consumers speaking the raw protocol import it, but it carries no stability promise beyond `montygo.ProtocolVersion`: it is regenerated on every protocol change, and names, fields and enum values follow upstream `monty.proto` without deprecation. The runtime codec in `internal/wire` does not use it; it is the oracle for the differential, benchmark and fuzz tests.
