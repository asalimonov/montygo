# Session, drive loop and host objects

## Session state

- A short `life.mu` protects admission, execution identity, snapshot sequence, interrupt requests, and the canonical terminal cause. It MUST NOT be held across I/O or user callbacks.
- One durable execution owns the session, including paused snapshots. A separate owner channel covers each active protocol operation. Conflicting requests return `ErrSessionBusy`; there is no implicit queue.
- Callbacks MAY observe state or call `CloseNow`. They MUST NOT wait on their own execution's completion or use an unbounded `Close` or `Interrupt` from inside that execution.
- `driven` makes `LoadSession` and `LoadSnapshot` valid only on a fresh session.
- Snapshots carry execution identity, sequence and single-use state. Claiming a cursor validates all three and admission in one lock acquisition. Failed busy/cancelled claims do not consume it.

## Lifecycle

- `Done()` is closed once and `Err()` becomes non-nil when the session is closed, lost or its worker ended. A goroutine started at checkout watches the worker, so a worker that dies while the session is idle is reported too: `*CrashedError` for local workers, `*DisconnectError` with the close frame's `Code` and `Reason` for WebSocket workers.
- Loss errors (`*CrashedError`, `*DisconnectError`, `*ShutdownError`, `*ProtocolError`, `*SessionKilledError`, and fatal memory `*RuntimeError`) match `ErrSessionLost`. Deliberate `ErrSessionClosed` does not. The first terminal cause is immutable; `Err()` and late driver results agree.
- The idle watcher waits for an in-flight protocol owner's reply classification, preserving queued `ShutdownDump` payloads. A turn-completion channel hands classification back to the watcher at callbacks, pauses, and completion.
- Futures are execution-owned subscriptions keyed by wire call ID. All terminal paths clear them without settling shared caller-owned Futures. The callback context is cancelled before Run.Done is published.
- `Close(ctx)` coalesces concurrent close attempts and bounds waiting on owner channels. A wait timeout leaves the session open. `CloseNow` fences terminal state first, cancels callbacks and retires the lease without joining Go work. `Close` after terminal state is an immediate no-op, even with an expired context.
- `Go` reserves execution synchronously, copies FeedOptions and its top-level maps/slice, and starts the driver. Admission failure returns a completed Run. The caller MUST NOT mutate referenced nested values while a run uses them. `WaitContext` is wait-only; a Run can never interrupt a later execution.

### Feed context and host calls

- Each execution owns a cancellable callback root independent of successful snapshot step returns. Step-context watchers are generation-checked atomically with interruption admission. Callbacks take values from their current turn so caller values, baggage and Monty spans reach them (see `telemetry.md`).
- `AsyncContext(ctx, fn)` derives from the context the host function received, so its goroutine observes the same cancellation. `Async` cannot be cancelled.
- When a host call returns and its context is cancelled, the answerer does not resume the worker. It sends `AbortFeed` with `KeyboardInterrupt`, or the interrupt reason, under `context.WithoutCancel` bounded by 5 s. The worker MUST reply with `Error`; the feed returns a `*RuntimeError` with `TypeName` `KeyboardInterrupt` and the session stays usable. A `ResolveFutures` wait is cancelled the same way.
- The interrupt cannot be caught by `except KeyboardInterrupt` in the sandbox, because `AbortFeed` ends the feed. This deviates from `@pydantic/monty`, which kills the worker; see `docs/parity/tests.md`.
- An uncooperative callback can outlive worker termination. Its Run.Done MUST remain open until the synchronous driver returns. Independent Async work is not joined.

### Interrupt

`Session.Interrupt(ctx, InterruptOptions)` captures the current execution once;
`Run.Interrupt` uses its stored identity. Both return `(InterruptResult, error)`.
Nil Reason means KeyboardInterrupt. Grace nil inherits the checkout default;
zero forces immediately; a negative grace is rejected. The first reason wins and
later callers can only shorten the absolute deadline. One watchdog continues
after any caller times out. The waiter receives InterruptPending and ctx.Err().

| State | Effect |
|---|---|
| before first send | `InterruptBeforeStart`, no wire effect, session reusable |
| suspended or cooperative callback | cancel callback context, then one `AbortFeed`; `InterruptAborted` preserves the session |
| grace expires in Python, print, mount or host callback | fence terminal state under life.mu, then kill/retire outside the lock; `InterruptKilled` and `SessionKilledError` |
| old completed Run | `InterruptAlreadyFinished`, no effect on a newer run |
| nothing is running | `InterruptNotRunning` |
| natural completion wins | `InterruptFinished`; inspect `SessionErr` for reusability |

`RunDone` reports actual driver completion, independently of the stop outcome.
`InterruptUnknown` is reserved for a zero/unaccepted result. A force owner retains
the right to publish Killed even if its driver exits before termination dispatch
returns. Old timers MUST verify identity and terminal state before acting.

### Bounds

- `MaxHostObjects` (default 10 000, `Unlimited` disables) bounds the instance store. Instances and their class types both count. The first wrapper past the bound fails with `*ResourceError`, which raises `RuntimeError: host object limit N exceeded` inside the sandbox; the session stays usable.
- `MaxPendingFutures` (default 1000, `Unlimited` disables) bounds unresolved wire call IDs per execution as `pending future limit N exceeded`. Shared Future pointers count separately. Duplicate call IDs are protocol errors.
- `Session.Stats` reports `HostObjects`, `PeakHostObjects` and `PendingFutures`.

## Host registry

- `NewHost()` builds a `Host`. `Func(name, fn, ...HostFuncOptions)` and `Object(name, v, opts)` validate at registration: identifiers MUST be unique and not hard Python keywords; an object class and exposed method names are validated too. Configure the host before checkout. Runtime resolution performs one O(1) lookup, not a registry copy.
- `CheckoutOptions.Host` exposes the registry to every feed of the session. Its objects are put into the instance store at checkout. `FeedOptions.ExternalLookup` is consulted first, so its entries override host names.
- `Stubs()` renders Python stubs for `TypeCheckStubs`: one `def` per function from its Go signature (a leading `context.Context` and a trailing `Kwargs` are mapped, unknown types are `Any`), one class per object with its allowed methods, and one typed name per object.
- Fixed reflected arguments, including method self, are positional-only (`/`). Optional ParameterNames are copied, exclude context/Kwargs and include variadic arguments. Direct Function implementations retain generic args/kwargs stubs. Labels MUST NOT imply keyword binding that runtime does not support.
- `Restorable()` reports the first object registered without a pinned `ClassInstanceOptions.ID` as `ErrHostObjectNotRestorable`. Pinned ids are what let a dump reference the same objects after `LoadSession` on another checkout with the same `Host`; `LoadSession` on a session with a `Host` returns that error before sending `Load`.

## Drive loop

`FeedRun` answers suspensions until the turn completes (`answer.go`):

- **FunctionCall**: a host function from `ExternalLookup`, or a method on a registered wrapper when `ObjectID` is set. A returned `*Future` becomes a sandbox future, or is awaited directly when the worker allows an eager await.
- **NameLookup**: a function entry resolves to an external function, a value entry to the value, an absent name stays undefined. With `ObjectID` set it is a lazy attribute of a wrapper.
- **OsCall**: mounts first, then the OS handler, then the worker's default.
- **ResolveFutures**: waits for at least one pending future and delivers every settled one.

`FeedStart` surfaces each suspension as a single-use snapshot; `ResumeAuto` runs the same answerer for one step.

Every suspension and every Resume variant checks stop/terminal state, including
name lookups, OS calls and manual snapshots. Mount handling computes a result
without sending; the owner checks cancellation before Resume. A stopped old
snapshot returns its saved execution error and MUST NOT poison a later feed.
Future-resolved class-method conversion uses a cancellable derived future and
recovers conversion panics; it never settles its source Future.

## Print

Print segments stream to the feed's print target during the turn. A target error is kept and returned at the turn boundary; on a suspension it poisons the session.

- A `FlushingPrintTarget` has its `Flush` called when a feed or turn ends (`Complete`, `Error`, `TypingError`), so buffered output is delivered before the result. A `Flush` error fails the feed like a `Print` error.
- `Lines(fn)` is a `FlushingPrintTarget` that delivers complete lines per stream without their newline and flushes an unterminated remainder at the end of the turn.

## Value conversion

- `prepareValue` normalizes Go values into `internal/value`: Go ints become `int64`, slices become lists, maps become dicts with sorted keys, wrappers become wire markers and are registered.
- `restoreValue` maps instance markers back to the original host object, or to `*ClassProxy`.
- Conversion failures of inputs fail host-side; failures of host return values raise inside the sandbox.
- Struct tags do not enable automatic record conversion. A per-object ConvertValue SHOULD explicitly switch on the intended Record and nullable *Record types and call AsNamedTuple. Nested collections require their own conversion. The same converter handles future-resolved method results; see `record_conversion_test.go` and the README.

## Host objects

- `ClassInstance` and `ClassType` are wrappers with attribute and method policies. `All()` exposes every public name, `Names(...)` an explicit list, and `Expose[T]()` the methods of the interface `T` under their sandbox names, so widening the surface means editing the interface. `Expose` panics for a non-interface type.
- Every wrapper sent into a session is stored by id in the session's instance store until the session closes, bounded by `MaxHostObjects`. Reusing an id for a different object is rejected.
- Reflection exposes exported fields and methods under snake_case names or a `monty` field tag. Unexported members are never reachable.
- `AsNamedTuple(struct)` converts a struct's exported fields, in declaration order and under the same names, into a `NamedTuple`; `NewNamedTuple(typeName, pairs...)` builds one from unique, non-empty field names. The sandbox reports the type of either as `namedtuple`.
