# Session, drive loop and host objects

## Session state

- A session is used by one goroutine at a time; its mutex is held for a whole call, including host callbacks. A callback MUST NOT call back into the same session.
- `broken` poisons the session after a crash, a protocol error or a host failure that left the worker suspended.
- `driven` makes `LoadSession` and `LoadSnapshot` valid only on a fresh session.
- `lifecycle` is the state `Interrupt`, `CloseNow`, `Done` and `Err` share under their own mutex, so they work while a feed holds the session mutex.

## Lifecycle

- `Done()` is closed once and `Err()` becomes non-nil when the session is closed, lost or its worker ended. A goroutine started at checkout watches the worker, so a worker that dies while the session is idle is reported too: `*CrashedError` for local workers, `*DisconnectError` with the close frame's `Code` and `Reason` for WebSocket workers.
- Every loss error (`*CrashedError`, `*DisconnectError`, `*ShutdownError`, `*ProtocolError`, `ErrSessionClosed`) matches `errors.Is(err, ErrSessionLost)`. Poisoning the session records the same error in the lifecycle, so `Err()` and the failing call agree.
- Futures registered by the feed are settled with `ErrSessionLost` when the session ends.
- `Close` waits for a running call; `CloseNow` does not. `CloseNow` marks the session closing, cancels the feed context, records `ErrSessionClosed`, kills the worker and untracks the session from the pool. A running feed then returns `ErrSessionClosed` instead of the transport error. `Close` afterwards is a no-op.
- `Go` runs `FeedRun` on its own goroutine and returns a `*Run` with `Done`, `Wait` and `Interrupt`.

### Feed context and host calls

- Each feed derives a feed context from the caller's context. Host calls run under a context that follows the feed context for cancellation and reads values from the turn context, so caller values, baggage and the Monty span reach callbacks (see `telemetry.md`).
- `AsyncContext(ctx, fn)` derives from the context the host function received, so its goroutine observes the same cancellation. `Async` cannot be cancelled.
- When a host call returns and its context is cancelled, the answerer does not resume the worker. It sends `AbortFeed` with `KeyboardInterrupt`, or the interrupt reason, under `context.WithoutCancel` bounded by 5 s. The worker MUST reply with `Error`; the feed returns a `*RuntimeError` with `TypeName` `KeyboardInterrupt` and the session stays usable. A `ResolveFutures` wait is cancelled the same way.
- The interrupt cannot be caught by `except KeyboardInterrupt` in the sandbox, because `AbortFeed` ends the feed. This deviates from `@pydantic/monty`, which kills the worker; see `docs/parity/tests.md`.
- A host function that ignores its context keeps the feed blocked until it returns.

### Interrupt

`Session.Interrupt(ctx, reason)` and `Run.Interrupt` stop the running feed. A nil reason means `KeyboardInterrupt`; any other error maps through the same rules as a host error (`Raise` picks the exception type).

| State | Effect |
|---|---|
| a host call is running | the feed context is cancelled; the call's return is answered with `AbortFeed`; `Interrupt` returns when the feed ends |
| Python is executing | `Interrupt` waits `InterruptGrace` (default 100 ms) for a suspension; then it kills the worker and the feed fails with `*CrashedError` (`ErrSessionLost`) |
| a `FeedStart` snapshot is suspended | `AbortFeed` is sent at once; the next `Resume*` returns the `KeyboardInterrupt` `*RuntimeError` and the session stays usable |
| nothing is running | no-op |

### Bounds

- `MaxHostObjects` (default 10 000, `Unlimited` disables) bounds the instance store. Instances and their class types both count. The first wrapper past the bound fails with `*ResourceError`, which raises `RuntimeError: host object limit N exceeded` inside the sandbox; the session stays usable.
- `MaxPendingFutures` (default 1000, `Unlimited` disables) bounds unresolved futures per feed the same way, as `pending future limit N exceeded`.
- `Session.Stats` reports `HostObjects`, `PeakHostObjects` and `PendingFutures`.

## Host registry

- `NewHost()` builds a `Host`. `Func(name, fn)` and `Object(name, v, opts)` validate at registration: the name MUST be a Python identifier and unique in the host; a function's signature MUST be one `Func` accepts; an object MUST be a valid `ClassInstance`.
- `CheckoutOptions.Host` exposes the registry to every feed of the session. Its objects are put into the instance store at checkout. `FeedOptions.ExternalLookup` is consulted first, so its entries override host names.
- `Stubs()` renders Python stubs for `TypeCheckStubs`: one `def` per function from its Go signature (a leading `context.Context` and a trailing `Kwargs` are mapped, unknown types are `Any`), one class per object with its allowed methods, and one typed name per object.
- `Restorable()` reports the first object registered without a pinned `ClassInstanceOptions.ID` as `ErrHostObjectNotRestorable`. Pinned ids are what let a dump reference the same objects after `LoadSession` on another checkout with the same `Host`; `LoadSession` on a session with a `Host` returns that error before sending `Load`.

## Drive loop

`FeedRun` answers suspensions until the turn completes (`answer.go`):

- **FunctionCall**: a host function from `ExternalLookup`, or a method on a registered wrapper when `ObjectID` is set. A returned `*Future` becomes a sandbox future, or is awaited directly when the worker allows an eager await.
- **NameLookup**: a function entry resolves to an external function, a value entry to the value, an absent name stays undefined. With `ObjectID` set it is a lazy attribute of a wrapper.
- **OsCall**: mounts first, then the OS handler, then the worker's default.
- **ResolveFutures**: waits for at least one pending future and delivers every settled one.

`FeedStart` surfaces each suspension as a single-use snapshot; `ResumeAuto` runs the same answerer for one step.

## Print

Print segments stream to the feed's print target during the turn. A target error is kept and returned at the turn boundary; on a suspension it poisons the session.

- A `FlushingPrintTarget` has its `Flush` called when a feed or turn ends (`Complete`, `Error`, `TypingError`), so buffered output is delivered before the result. A `Flush` error fails the feed like a `Print` error.
- `Lines(fn)` is a `FlushingPrintTarget` that delivers complete lines per stream without their newline and flushes an unterminated remainder at the end of the turn.

## Value conversion

- `prepareValue` normalizes Go values into `internal/value`: Go ints become `int64`, slices become lists, maps become dicts with sorted keys, wrappers become wire markers and are registered.
- `restoreValue` maps instance markers back to the original host object, or to `*ClassProxy`.
- Conversion failures of inputs fail host-side; failures of host return values raise inside the sandbox.

## Host objects

- `ClassInstance` and `ClassType` are wrappers with attribute and method policies. `All()` exposes every public name, `Names(...)` an explicit list, and `Expose[T]()` the methods of the interface `T` under their sandbox names, so widening the surface means editing the interface. `Expose` panics for a non-interface type.
- Every wrapper sent into a session is stored by id in the session's instance store until the session closes, bounded by `MaxHostObjects`. Reusing an id for a different object is rejected.
- Reflection exposes exported fields and methods under snake_case names or a `monty` field tag. Unexported members are never reachable.
- `AsNamedTuple(struct)` converts a struct's exported fields, in declaration order and under the same names, into a `NamedTuple`; `NewNamedTuple(typeName, pairs...)` builds one from unique, non-empty field names. The sandbox reports the type of either as `namedtuple`.
