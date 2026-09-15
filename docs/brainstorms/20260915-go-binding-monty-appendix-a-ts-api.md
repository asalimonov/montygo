# Appendix A — `@pydantic/monty` TypeScript public API inventory (HEAD `f8acf4fa`)

Parity checklist for the Go port. Source root: `../monty/crates/monty-js/`. Package `@pydantic/monty` v0.0.23, ESM, Node ≥ 20.

## A.1 Entry points

| Subpath | Condition | File |
|---|---|---|
| `.` | types | `ts/index.ts` |
| `.` | browser | `ts/worker/index.browser.ts` |
| `.` | node / default | `ts/node.ts` |
| `./node` | all | `ts/node.ts` |
| `./wasm` | node / browser / default | `ts/worker/index.{node,browser}.ts`, `ts/worker/index.ts` |

`index.ts` exports: `Monty`, `CheckoutOptions`, `MontyOptions`, `ResourceLimits`, `ClassInstance`, `ClassType`, `MontyClassProxy`, `AttrPolicy`, `BaseWrapperOptions`, `ClassInstanceOptions`, `ClassTypeOptions`, `AssertMessageAnnotations`, `TypeCheckFormat`, `FunctionSnapshot`, `FutureSnapshot`, `MontyComplete`, `MontySession`, `NameLookupSnapshot`, `NOT_HANDLED`, `ExternalFunction`, `FeedOptions`, `FeedStartOptions`, `FutureResolution`, `LoadSnapshotOptions`, `OsCallback`, `PrintCallback`, `PrintTargetInput`, `Snapshot`, `CollectString`, `CollectStreams`, `DEFAULT_MAX_PRINT_COLLECT_BYTES`, `CollectedStreamEntry`, `MontyCrashedError`, `MontyError`, `MontyRuntimeError`, `MontySyntaxError`, `MontyTypingError`, `ProtocolError`, `ExceptionInfo`, `Frame`, `MontyDate`, `MontyDateTime`, `MontyException`, `MontyFileHandle`, `MontyFileHandleOptions`, `MontyTime`, `MontyTimeDelta`, `MontyTimeZone`, `MAX_VALUE_DEPTH`.

`node.ts` adds: `MountDirMode`, `MountDirOptions`, `MountDir`, `findMontyBinary`, `flushTelemetry`, `instrumentTelemetry`, `MontyInstrumentation`, `MontyInstrumentationConfig`, `TelemetryComponents`.

`worker/index.ts` (wasm) adds: `WasmPoolOptions`, `createWorkerPool`, `WorkerPool`, `inProcessFactory`, `PooledWorker`, `WorkerFactory`, `WorkerPoolOptions`, `WorkerTransport`, `WorkerSessionConfig`, `WasmHost`, `inProcessDispatcher`, `ComponentModules`, `Dispatcher`, `WorkerChannel`, `WorkerChannelOptions`, `WorkerLike`, `browserWorkerFactory`.

## A.2 Constants

```ts
MAX_VALUE_DEPTH: number                     // from napi addon (wire nesting bound)
DEFAULT_MAX_PRINT_COLLECT_BYTES = 10 * 1024 * 1024
NOT_HANDLED: unique symbol
// internal but parity-relevant
COLLECT_STREAMS_ENTRY_OVERHEAD = 64
DEFAULT_MEMORY_USAGE_LIMIT = 100_000_000    // mount, decimal 100 MB
MAX_INPUT_DEPTH = 48                        // outbound prepare() recursion guard
DENIED_NAMES = {constructor, __proto__, prototype, arguments, caller}
TYPE_CHECK_FORMATS = {full:1, concise:2, azure:3, json:4, jsonlines:5, rdjson:6, pylint:7, gitlab:8, github:9}
UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
```

## A.3 Pool

```ts
interface MontyOptions {
  binaryPath?: string                 // default findMontyBinary()
  minProcesses?: number               // 1
  maxProcesses?: number               // os.availableParallelism()
  checkoutTimeout?: number            // seconds; default wait forever
  requestTimeout?: number             // seconds; default off
  durationLimitGrace?: number | null  // seconds; default 1; null disables
  maxCheckoutsPerWorker?: number      // unlimited
}
interface CheckoutOptions {
  scriptName?: string                                  // 'main.py'
  limits?: ResourceLimits
  typeCheck?: boolean                                  // false
  typeCheckStubs?: string
  typeCheckFormat?: TypeCheckFormat                    // 'full'
  typeCheckColor?: boolean                             // false
  assertMessageAnnotations?: boolean | number          // true (120)
  printFlushInterval?: number                          // seconds; 0.005; 0 = line buffering
}
interface ResourceLimits { maxDurationSecs?; maxMemory?; gcInterval?; maxRecursionDepth? /*1000*/; maxSuspensions? /*1000*/ }
class Monty {
  static create(options?: MontyOptions): Promise<Monty>
  checkout(options?: CheckoutOptions): Promise<MontySession>   // throws 'the pool is closed — create a new Monty pool'
  close(): Promise<void>                                       // idempotent; checked-out sessions keep workers
  [Symbol.asyncDispose]()
}
```
Encoding: `checkoutTimeoutMs = s*1000`, `requestTimeoutMs = s*1000`, `durationLimitGraceMs = (grace ?? 1)*1000`, omitted when `null`. `assertMessageAnnotations`: `undefined|true → absent`, `false → 0`, int in `[1, 2^32-1] → itself`, else `RangeError`.

## A.4 Session

```ts
class MontySession {
  feedRun(code: string, options?: FeedOptions): Promise<unknown>
  feedStart(code: string, options?: FeedStartOptions): Promise<Snapshot>
  loadSession(state: Uint8Array): Promise<void>
  loadSnapshot(state: Uint8Array, options?: LoadSnapshotOptions): Promise<Snapshot>
  dump(): Promise<Buffer>
  installDependencies(requirements: string[]): Promise<void>
  get workerPid(): number | undefined
  close(): Promise<void>
  [Symbol.asyncDispose]()
}
interface FeedOptions {        // FeedStartOptions structurally identical (externalLookup/os only used by resumeAuto)
  inputs?: Record<string, unknown>
  externalLookup?: Record<string, unknown>
  printCallback?: PrintCallback | CollectString | CollectStreams
  mount?: MountDir | MountDir[]
  cwd?: string                 // absolute virtual path; persists; default first mount's virtualPath else '/'
  os?: OsCallback
  skipTypeCheck?: boolean
}
interface LoadSnapshotOptions { printCallback?; mount?; externalLookup?; os? }
type ExternalFunction = (...args: never[]) => unknown
type OsCallback = (name: string, args: unknown[], kwargs: Record<string, unknown>) => unknown
type PrintCallback = (stream: 'stdout' | 'stderr', text: string) => void
type Snapshot = FunctionSnapshot | NameLookupSnapshot | FutureSnapshot | MontyComplete
type FutureResolution = { callId: number; value: unknown } | { callId: number; error: unknown }
```
Session state: `broken: Error|null`, `closed`, `driven`, `instances: InstanceStore`. Errors: `'the session is closed — check out a new one'`; `'loadSession / loadSnapshot is only valid on a fresh session, before any feedRun / feedStart / loadSession / loadSnapshot'`; `'this dump is a suspended snapshot — use loadSnapshot() to resume it'`; `'this dump is an idle session — use loadSession() to restore it'`; `'snapshot has already been resumed'`; `'resumeNotHandled is only valid for OS-call snapshots'`.

### Snapshots (single-use cursors)

```ts
class FunctionSnapshot {
  functionName: string; args: unknown[]; kwargs: Record<string, unknown>; callId: number
  isOsFunction: boolean; allowEagerAwait: boolean; objectId: string | null
  resume(value): Promise<Snapshot>; resumeAuto(); resumeError(error); resumeNotFound(); resumeFuture(); resumeNotHandled(); dump(): Promise<Buffer>
}
class NameLookupSnapshot { variableName: string; objectId: string | null; resume(functionName?: string); resumeValue(value); resumeAuto(); dump() }
class FutureSnapshot { pendingCallIds: number[]; resume(results: FutureResolution[]); resumeAuto(); dump() }
class MontyComplete { readonly output: unknown }
```

### Drive loop (`feedRun`)

1. `ensureUsable()`; `driven = true`; fresh `PrintTarget`; fresh per-feed `TurnAnswerer(native, instances, externalLookup, os)`.
2. `turn = native.feed(code, prepareInputs(inputs), mountsToNative(mount), {cwd, skipTypeCheck}, onPrint)`.
3. Loop: `complete` → `throwIfFailed()`, return `restore(value)`; `error` → `MontySyntaxError` if `excType==='SyntaxError'` else `MontyRuntimeError`; `typingError` → `MontyTypingError(diagnostics)`; `crashed` → poison `MontyCrashedError`; `protocol` → poison `ProtocolError`; suspension → `throwIfFailed()` (print failure on a suspension poisons the session) then `answerer.answer(turn)`; answerer throw → `broken ??= err`, rethrow.

### `TurnAnswerer`

- **functionCall**: `objectId` → method call on `instances.get(objectId)` (names starting `_` except `__call__` → `AttributeError`; missing wrapper → `RuntimeError("no host object registered for method call '<name>' (id <id>) — the instance store is empty after loading a dump into a fresh session")`). Plain: own-property lookup in `externalLookup`; absent → `resumeNotFound` (NameError); non-function → `resumeError('TypeError', "'<pytype>' object is not callable")`; function → call with `args...` + trailing kwargs bag only if non-empty; throw → `resumeError(jsErrorParts)`; thenable → if `allowEagerAwait` await then `resolveFutures([{callId, ok, value|excType/message}])`, else `registerFuture` + `resumeFuture`; value → `prepare` + `resumeReturn` (prepare throw → `resumeError`).
- **nameLookup**: `objectId` → lazy attr via wrapper (`AttrNotExposed`/`_`-prefix/missing wrapper → unresolved = AttributeError; other error → `resumeNameLookupError`; ok → `resumeLazyAttr(value)`). Plain: absent → unresolved (NameError); function → `resumeNameLookup(fn.name || '<anonymous>')`; value → `resumeNameLookup(null, {value: prepare(v)})`.
- **osCall**: (1) `native.resumeFromMounts` — if not `notMounted` that is the answer; (2) no `os` → `resumeNotHandled`; (3) `os(name, args, kwargsRecord)`; await thenable; throw → `resumeError`; `NOT_HANDLED` → `resumeNotHandled`; else `resumeReturn`.
- **resolveFutures**: unknown id → `ProtocolError("worker reported unknown pending call id <id>")`; empty → `ProtocolError('worker reported ResolveFutures with no pending call ids')`; `Promise.race` on pending; deliver every settled future; `native.resolveFutures(results)`.
- `jsErrorParts(err)`: `MontyError` → `{typeName, message}`; `Error` → `{PYTHON_EXC_NAMES.has(name) ? name : 'RuntimeError', message}`; other → `{'RuntimeError', String(err)}`.

### `PrintTarget`

Per feed / per snapshot chain. Adapts collectors; no callback → process stdout/stderr. Callback throws are captured into `failure`, later writes dropped; `throwIfFailed()` at turn boundary takes precedence over the turn outcome.

### Persistence

| Method | Native | Accepts | Produces |
|---|---|---|---|
| `session.dump()` | `dump()` | — | bytes; session usable |
| `snapshot.dump()` | `dump()` | — | bytes of a suspended worker |
| `loadSession(state)` | `restore(bytes, [], print)` | idle dump | `void` |
| `loadSnapshot(state, opts)` | `restore(bytes, mounts, print)` | suspended dump | `Snapshot` |

`claimFresh()` rejects after any drive. Failed load → poison + `finish()`; not retryable. `installDependencies` sets `driven`; `ok` → return; `error` → `MontyRuntimeError` (session usable); `crashed|protocol` → poison.

### `InstanceStore` (one per session)

`map: Map<uuid, BaseWrapper>` (shared namespace for instances and class types); `register(ClassInstance)`, `registerClass(ClassType)`, `registerClassIfAbsent(ClassType)`, `get(id)`; re-sending same object overwrites; same id for a *different* object → `TypeError("wrapper id '<id>' already identifies a different object in this session")`. Retained until session close (host memory, not `maxMemory`).

## A.5 Errors

```ts
interface Frame { filename; line; column; endLine; endColumn; functionName?; sourceLine? }
interface ExceptionInfo { typeName: string; message: string }
class MontyError extends Error { get exception(): ExceptionInfo; display(format: 'type-msg'|'msg' = 'msg') }   // message = `${type}: ${msg}` or type
class MontySyntaxError extends MontyError { constructor(message, tracebackText=''); display('traceback'|'type-msg'|'msg' = 'msg') }
class MontyRuntimeError extends MontyError { constructor(typeName, message, frames=[], tracebackText=''); traceback(): Frame[]; display(format = 'traceback') }
class MontyTypingError extends MontyError { constructor(diagnostics); display(): string }  // typeName 'TypeError', message = first line
class MontyCrashedError extends MontyError { timedOut: boolean; exitStatus: string|null }  // typeName 'RuntimeError'
class ProtocolError extends Error {}   // NOT a MontyError
```
Traceback fallback renderer: `Traceback (most recent call last):`, per frame `  File "<f>", line <n>, in <name|<module>>`, preview line, `~` caret row (skipped when preview starts with `raise`, `column<=0`, `endColumn<=column`), then summary.

`PYTHON_EXC_NAMES` (39): Exception, BaseException, SystemExit, KeyboardInterrupt, ArithmeticError, OverflowError, ZeroDivisionError, LookupError, IndexError, KeyError, RuntimeError, NotImplementedError, RecursionError, AttributeError, FrozenInstanceError, NameError, UnboundLocalError, ValueError, UnicodeDecodeError, UnicodeEncodeError, json.JSONDecodeError, ImportError, ModuleNotFoundError, OSError, FileNotFoundError, FileExistsError, IsADirectoryError, NotADirectoryError, PermissionError, io.UnsupportedOperation, AssertionError, MemoryError, StopIteration, SyntaxError, TimeoutError, TypeError, re.PatternError, binascii.Error, binascii.Incomplete.

`pyTypeName` (for "not callable" messages): null/undefined→NoneType, boolean→bool, number→int|float, bigint→int, string→str, function→function, Uint8Array→bytes, Map→dict, Set→set, array→tuple|list, ClassInstance→class name, markers per `MARKED_TYPE_NAMES`, else object.

## A.6 Print collectors

```ts
class CollectString  { constructor(maxBytes: number|null = 10MiB); get output(): string; write(stream, text) }
class CollectStreams { constructor(maxBytes = 10MiB); get output(): {stream, text}[]; write(stream, text) }
```
UTF-8 byte accounting; `CollectStreams` charges +64 per entry, never merges. Over cap → `MontyRuntimeError('MemoryError', 'memory limit exceeded: <used> bytes > <max> bytes')`, check-before-append (prior content kept). Invalid `maxBytes` → `TypeError('maxBytes must be a finite non-negative number or null')`.

## A.7 Mounts (native only)

```ts
type MountDirMode = 'read-only' | 'read-write' | 'overlay'
interface MountDirOptions { hostPath: string; virtualPath: string; mode?: MountDirMode /*overlay*/; writeBytesLimit?: number /*null*/; memoryUsageLimit?: number /*100_000_000*/ }
class MountDir { hostPath; virtualPath; mode; writeBytesLimit: number|null; memoryUsageLimit; constructor(o); close(); [Symbol.dispose](); repr(): "MountDir(host_path='…', virtual_path='…', mode='…')" }
```
Validation order: mode (`"invalid mount mode: '<m>'. Expected 'read-only', 'read-write' or 'overlay'"`), `memoryUsageLimit` non-negative safe integer, then host dir opened immediately (descriptor-bound).

## A.8 Class wrappers

```ts
type AttrPolicy = readonly string[] | ReadonlySet<string> | 'all'
interface BaseWrapperOptions { eagerAttrs?; lazyAttrs?; allowedMethods?; name?; convertValue?: (name, value) => unknown }
interface ClassInstanceOptions extends BaseWrapperOptions { id?: string; classType?: ClassType }
interface ClassTypeOptions extends BaseWrapperOptions { id?; init?: boolean; instanceEagerAttrs?; instanceLazyAttrs?; instanceAllowedMethods? }
class ClassInstance { id; classType; constructor(instance: object, o?) ; getName() }
class ClassType { id; get classType(); constructor(cls, o?); getName(); callMethod(); construct(args, kwargs): ClassInstance; instanceWrapper(instance): ClassInstance }
class MontyClassProxy { name; isDataclass; id; attributes: Record<string, unknown> }
```
Rules: policy `undefined` → nothing; `'all'` → non-`_` names (instance: own enumerable keys for eager; prototype-chain methods below Object.prototype for `allowedMethods`; ClassType: own static functions); string policy ≠ `'all'` → `TypeError("<field> must be 'all', undefined or a list/Set of names, got '<p>'")`; `DENIED_NAMES` always refused; `'__call__'` on an instance → `AttrNotExposed`; `ClassType.callMethod('__call__')` → `construct` (requires `init: true`, else `TypeError("cannot instantiate host class '<name>'")`); kwargs → trailing null-prototype options bag (`__proto__` dropped) only when non-empty; thenable method results are awaited then `convertValue`d; default `ClassType.id` is process-wide per class (WeakMap), explicit id bypasses; explicit id must match `UUID_PATTERN` and is lowercased; `classType` + `name` together → `TypeError('pass name on the ClassType wrapper, not alongside classType')`; mismatched `classType` → `TypeError("classType does not match the instance's class")`; null-prototype object → `TypeError('ClassInstance expects an instance of a class, not a null-prototype object')`.

`prepare(value)` (outbound, depth 48 → `TypeError('Max input depth exceeded')`): primitives pass; `ClassType` → Type marker `{name,id,hostDefined:true,isDataclass:false,attrs}` (registers); `ClassInstance` → ClassInstance marker (registers class if absent); `MontyClassProxy` → marker with `instanceId`; arrays (keep `__tuple__`), `Map`, `Set`, `Uint8Array`; raw ClassInstance marker → `TypeError('raw ClassInstance markers are not accepted — wrap the object in ClassInstance(...)')`; raw Type marker with classType → `TypeError('raw Type markers are not accepted — pass the class through ClassType(...)')`; plain object → dict; other → `TypeError('Cannot convert <Ctor> instance to a Monty value — wrap it in ClassInstance(...)')`.

`restore(value)` (inbound): ClassInstance marker → registered wrapper's original object, else `MontyClassProxy`; Type marker → registered host class object if known, else marker unchanged; plain objects → null-prototype dict.

## A.9 Marker value types

```ts
MontyDate { year, month, day }
MontyDateTime { year, month, day, hour, minute, second, microsecond, offsetSeconds?, timezoneName? }
MontyTime { hour, minute, second, microsecond, offsetSeconds?, timezoneName?, fold? }
MontyTimeDelta { days, seconds, microseconds }
MontyTimeZone { offsetSeconds, name? }
MontyException { excType, message }
class MontyFileHandle { path; mode /*canonical*/; position; constructor(path, mode, {position?}); get binary(); get readable(); get writable() }
```
`canonicalFileMode`: exactly one of `r|w|a`, optional `b`, optional `t`; `x` → `'exclusive creation mode is not supported'`; `+` → `"update modes ('+') are not yet supported"`; `b`+`t` → `"can't have text and binary mode at once"`; duplicates → `'invalid mode: binary mode specified twice'` / text; empty/no action → `'Must have exactly one of create/read/write/append mode and at most one plus'`; other → `` `invalid mode: '<c>'` ``.

Value mapping table (README): None↔null, bool, int↔number|BigInt, float↔number, str, bytes↔Buffer, list↔Array, tuple↔Array+`__tuple__`, dict↔Map, set/frozenset↔Set, datetime types↔markers, file handles↔MontyFileHandle, class instances↔wrappers/proxies; plain objects accepted as string-keyed dict inputs.

## A.10 Binary resolution (`findMontyBinary(explicit?)`)

explicit (must exist: `` `monty binary not found at binaryPath: ${p}` ``) → `MONTY_BIN` → `@pydantic/monty-<triple>/monty[.exe]` → `PATH` → cargo workspace walk (6 levels) `target/debug/monty` then `target/release/monty` → `` Error(`could not locate the monty binary (tried: …). Install the platform package, set MONTY_BIN, or pass binaryPath.`) ``. Triples: darwin-x64, darwin-arm64, linux-x64-gnu, linux-arm64-gnu, win32-x64-msvc.

## A.11 Telemetry (node only)

```ts
interface TelemetryComponents { tracer?: Tracer; meter?: Meter; logger?: Logger }
interface MontyInstrumentationConfig { enabled?; traces?; metrics?; logs? }   // all default true
function instrumentTelemetry(c: TelemetryComponents): void   // 'Monty telemetry is already configured' / 'at least one OpenTelemetry component is required'
function flushTelemetry(): Promise<void>
class MontyInstrumentation { instrumentationName '@pydantic/monty'; instrumentationVersion; enable(); disable(); setTracerProvider(); setMeterProvider(); getConfig(); setConfig(); forceFlush() }
```
Process-wide, before pool creation. Print callbacks bound via `AsyncResource`; `captureTelemetryContext()` at checkout → `{traceId, spanId, traceFlags, traceState}` passed to `native.enter`. Metrics (from monty-pool README): `monty.pool.workers.live|idle|suspended`, `monty.pool.checkout.wait`, `monty.pool.worker.terminated`, `monty.pool.session.duration` (`ok|error|abandoned`), `monty.run.duration`, `monty.run.execution_time`, `monty.turn.duration`, `monty.run.suspensions`, `monty.ext.call.duration`, `monty.snapshot.bytes`, `monty.print.bytes`, `monty.wire.frame.bytes`. Spans: one per checkout (session), nested feed span across suspensions, child span per suspension; values encoded logfire-style capped at 64 KB; dump/load recorded by size only.

## A.12 Wasm entry differences (for the parity decision table)

| Area | Native | Wasm |
|---|---|---|
| `maxProcesses` default | CPU count | 4 |
| `checkoutTimeout`, `durationLimitGrace`, `binaryPath` | honoured | accepted, ignored |
| `requestTimeout` | kills worker | `Worker.terminate()`; no preemption in-process |
| Mounts | supported | `Error('the wasm worker does not support filesystem mounts (browser has no host filesystem)')` |
| Telemetry, `findMontyBinary`, `MAX_VALUE_DEPTH` | exported | absent |
| `workerPid` | pid | `undefined` |
| `installDependencies` | real (CPython worker) / `MontyRuntimeError` | empty ok; else `RuntimeError: dependency installation is only supported by the CPython worker` |
| `maxSuspensions` | in Rust pool | in TS transport: `abort-feed` with `RuntimeError: suspension limit <N> exceeded` |
| `cwd` validation | Rust | TS: NUL → `ValueError: cwd must not contain NUL bytes: "<debug>"`; not absolute → `cwd must be an absolute POSIX path: …`; trailing slashes trimmed |
| Print | timed flush in worker | all frames per turn at once |
