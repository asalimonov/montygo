# API parity with `@pydantic/monty`

| TypeScript | Go | Notes |
|---|---|---|
| `Monty.create(options)` | `monty.New(ctx, monty.Options{...})` | `Backend` selects native, wasm (the `/wasm` entry) or auto |
| `MontyOptions.binaryPath` / `minProcesses` / `maxProcesses` | `BinaryPath` / `MinProcesses` / `MaxProcesses` | `MinProcesses` 0 means 1, negative means none |
| `checkoutTimeout` / `requestTimeout` (seconds) | `CheckoutTimeout` / `RequestTimeout` (`time.Duration`) | |
| `durationLimitGrace` (`null` disables) | `DurationLimitGrace` (`monty.NoDurationLimitGrace` disables) | |
| `maxCheckoutsPerWorker` | `MaxCheckoutsPerWorker` | |
| `pool.checkout(options)` | `pool.Checkout(ctx, monty.CheckoutOptions{...})` | |
| `pool.close()` / `await using` | `pool.Close(ctx)` / `defer` | |
| `CheckoutOptions.scriptName`, `limits`, `typeCheck`, `typeCheckStubs`, `typeCheckFormat`, `typeCheckColor` | same fields | |
| `assertMessageAnnotations` (`boolean \| number`) | `AssertMessageAnnotations *uint32` | `monty.Uint32(0)` disables |
| `printFlushInterval` (seconds) | `PrintFlushInterval *time.Duration` | |
| `ResourceLimits.maxDurationSecs`, `maxMemory`, `gcInterval`, `maxRecursionDepth`, `maxSuspensions` | `MaxDuration`, `MaxMemory`, `GCInterval`, `MaxRecursionDepth`, `MaxSuspensions` | zero means unlimited or the default |
| `session.feedRun(code, options)` | `session.FeedRun(ctx, code, &monty.FeedOptions{...})` | |
| `FeedOptions.inputs`, `externalLookup`, `printCallback`, `mount`, `cwd`, `os`, `skipTypeCheck` | `Inputs`, `ExternalLookup`, `Print`, `Mount`, `Cwd`, `OS`, `SkipTypeCheck` | |
| `session.feedStart` | `session.FeedStart` | |
| `session.loadSession` / `loadSnapshot` / `dump` | `LoadSession` / `LoadSnapshot` / `Dump` | |
| `session.installDependencies` | `session.InstallDependencies` | |
| `session.workerPid` | `session.WorkerPID() (int, bool)` | |
| `session.close()` | `session.Close(ctx)` | |
| `FunctionSnapshot` (`resume`, `resumeAuto`, `resumeError`, `resumeNotFound`, `resumeFuture`, `resumeNotHandled`, `dump`) | `*monty.FunctionSnapshot` (`Resume`, `ResumeAuto`, `ResumeError`, `ResumeNotFound`, `ResumeFuture`, `ResumeNotHandled`, `Dump`) | |
| `NameLookupSnapshot.resume(name?)` / `resumeValue` / `resumeAuto` | `ResumeFunction(name)`, `ResumeUnresolved()` / `ResumeValue` / `ResumeAuto` | |
| `FutureSnapshot.resume([{callId, value \| error}])` | `Resume([]monty.FutureResolution{CallID, Value, Err})` | |
| `MontyComplete.output` | `*monty.Complete.Output` | |
| `NOT_HANDLED` | `monty.NotHandled` | |
| external function (sync / async) | Go func, `monty.Function`, `monty.FunctionFunc`; async via `*monty.Future` | keyword args arrive in a trailing `monty.Kwargs` |
| thrown `Error` with `name` | `monty.Raise(excType, msg)` | other errors raise `RuntimeError` |
| `ClassInstance(obj, {eagerAttrs, lazyAttrs, allowedMethods, name, convertValue, id, classType})` | `monty.NewClassInstance(obj, monty.ClassInstanceOptions{...})` | policies are `monty.All` / `monty.Names(...)` |
| `ClassType(cls, {..., init, instanceEagerAttrs, instanceLazyAttrs, instanceAllowedMethods})` | `monty.NewClassType[T](monty.ClassTypeOptions{...})` | class members go in `Statics`; `Constructor` builds instances |
| `ClassType.construct` / `instanceWrapper` override | `ClassType.Construct` / `InstanceWrapper` option | |
| `MontyClassProxy` | `*monty.ClassProxy` | |
| `CollectString` / `CollectStreams` / `DEFAULT_MAX_PRINT_COLLECT_BYTES` | `monty.NewCollectString` / `monty.NewCollectStreams` / `monty.DefaultMaxPrintCollectBytes` | `monty.UnlimitedPrintCollect` is `null` |
| `PrintCallback` | `monty.PrintTarget`, `monty.PrintFunc`, `monty.ContextPrintTarget` | |
| `MountDir({hostPath, virtualPath, mode, writeBytesLimit, memoryUsageLimit})` | `monty.NewMountDir(monty.MountDirOptions{...})` | |
| `MontyFileHandle(path, mode, {position})` | `monty.NewFileHandle(path, mode, position)` | |
| `MontyError`, `MontySyntaxError`, `MontyRuntimeError`, `MontyTypingError`, `MontyCrashedError`, `ProtocolError` | `monty.Error`, `*SyntaxError`, `*RuntimeError`, `*TypingError`, `*CrashedError`, `*ProtocolError` | |
| `Frame`, `ExceptionInfo`, `display(format)` | `monty.Frame`, `monty.ExceptionInfo`, `Display(monty.DisplayFormat)` | |
| `MAX_VALUE_DEPTH` | `monty.MaxValueDepth` | |
| `findMontyBinary` | `monty.FindMontyBinary` | |
| `instrumentTelemetry` / `flushTelemetry` / `MontyInstrumentation` | `monty.Instrument` / `monty.Flush` / `monty.NewInstrumentation` | |
| Value markers (`MontyDate`, `MontyDateTime`, `MontyTime`, `MontyTimeDelta`, `MontyTimeZone`, `MontyException`) | `monty.Date`, `DateTime`, `Time`, `TimeDelta`, `TimeZone`, `Exception` | |
| `Map` / `Set` / tuple arrays / `BigInt` / `Buffer` | `*monty.Dict` / `*monty.Set`, `*monty.FrozenSet` / `monty.Tuple` / `*big.Int` / `[]byte` | |

## Beyond the TypeScript package

| Feature | Go | Origin |
|---|---|---|
| WebSocket workers | `monty.NewWebSocket`, `*DisconnectError`, `*ShutdownError` | Python `AsyncMontyWebsocket` |
| TLS settings for WebSocket dials | `WebSocketOptions.TLSConfig` | montygo; Python has no equivalent |
| Custom TCP dialer for WebSocket dials | `WebSocketOptions.DialContext` | montygo; Python has no equivalent |
| Server health probe | `monty.CheckWebSocketHealth` (`GET <path>/health`) | montygo; Python has no equivalent |
| In-memory OS helpers | package `osaccess` | Python `AbstractOS`, `OSAccess`, `MemoryFile`, `CallbackFile`, `StatResult` |
| Mounts on the wasm backend | host-side mount servicing | the TS browser entry has no mounts |
| Memory-limit classification on wasm | exit code 65 → `MemoryError` | the TS browser worker reports a crash |

## Not provided

| TypeScript | Reason |
|---|---|
| Browser `Worker` pool, `WorkerTransport`, `WasmHost`, `WorkerChannel` factories | browser-only plumbing; the wasm backend covers the use case |
| JS prototype hardening (`constructor`, `__proto__`, `Function.prototype`) | no Go analogue; unexported members are unreachable |
| Windows native backend | not supported in this release; the wasm backend works on Windows |
