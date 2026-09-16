# Session, drive loop and host objects

## Session state

- A short `life.mu` protects admission, execution identity, snapshot sequence, stop requests, and the canonical terminal cause. It MUST NOT be held across I/O or user callbacks.
- One durable execution owns the session, including paused snapshots. A separate owner channel covers each active protocol operation. Conflicting requests return `ErrSessionBusy`; there is no implicit queue.
- Callbacks MAY observe state or call `Close(ctx, KillNow)`. They MUST NOT wait on their own execution's completion or use an unbounded `Close` or `Stop` from inside that execution.
- `driven` makes `LoadSession` and `LoadSnapshot` valid only on a fresh session.
- Snapshots carry execution identity, sequence and single-use state. Claiming a cursor validates all three and admission in one lock acquisition. Failed busy/cancelled claims do not consume it.
- `State()` reports a coarse `SessionState` from one lock acquisition: `SessionClosed` when a terminal cause is set; `SessionRunning` while a close attempt or a control operation is in flight, or an execution is not paused; `SessionPaused` while a `FeedStart` snapshot is pending; `SessionIdle` otherwise. `Slot.State` maps a missing or closed session to `SessionIdle` and a closed slot to `SessionClosed`.

## Lifecycle

- `Done()` is closed once and `Err()` becomes non-nil when the session is closed, lost or its worker ended. A goroutine started at checkout watches the worker, so a worker that dies while the session is idle is reported too: `*CrashedError` for local workers, `*DisconnectError` with the close frame's `Code` and `Reason` for WebSocket workers.
- Loss errors (`*CrashedError`, `*DisconnectError`, `*ShutdownError`, `*ProtocolError`, `*SessionKilledError`, and fatal memory `*RuntimeError`) match `ErrSessionLost`. Deliberate `ErrSessionClosed` does not. The first terminal cause is immutable; `Err()` and late driver results agree.
- The idle watcher waits for an in-flight protocol owner's reply classification, preserving queued `ShutdownDump` payloads. A turn-completion channel hands classification back to the watcher at callbacks, pauses, and completion.
- Futures are execution-owned subscriptions keyed by wire call ID. All terminal paths clear them without settling shared caller-owned Futures. The callback context is cancelled before Run.Done is published.
- `Close(ctx, policy...)` resolves the policy over the session's, then: a negative `Timeout` (`KillNow`) fences terminal state, cancels callbacks and retires the lease without joining Go work; a terminal session is an immediate no-op, even with an expired context; concurrent close attempts coalesce; a running execution is stopped through `Run.Stop` with the policy; a paused snapshot is reclaimed and its token invalidated; then one `Finish` turn ends the session with `ErrSessionClosed`. When the caller's context ends during the stop, `Close` returns `ctx.Err()` and a background goroutine finishes the close after the run ends, bounded by `Timeout + Join`.
- `Go` reserves execution synchronously, copies FeedOptions and its top-level maps/slice, and starts the driver. Admission failure returns a completed Run. The caller MUST NOT mutate referenced nested values while a run uses them. `WaitContext` is wait-only; a Run can never stop a later execution.

### Feed context and host calls

- Each execution owns a cancellable callback root independent of successful snapshot step returns. Turns run under `context.WithoutCancel` of the step context; a `context.AfterFunc` on the step context requests a stop with the session policy instead, and a stale step is ignored. Callbacks take values from their current turn so caller values, baggage and Monty spans reach them (see `telemetry.md`).
- `AsyncContext(ctx, fn)` derives from the context the host function received, so its goroutine observes the same cancellation. `Async` cannot be cancelled.
- When a host call returns and a stop has been requested, the answerer does not resume the worker with the result. Without `Catchable` it sends `AbortFeed` with the stop reason under `context.WithoutCancel`, bounded by the kill deadline. The worker MUST reply with `Error`; the feed returns a `*RuntimeError` with the reason's type and the session stays usable. A `ResolveFutures` wait is ended the same way.
- The default stop cannot be caught by `except KeyboardInterrupt` in the sandbox, because `AbortFeed` ends the feed. `@pydantic/monty` kills the worker on a cancelled context instead; see `docs/parity/tests.md`.
- An uncooperative callback can outlive worker termination. Its Run.Done MUST remain open until the synchronous driver returns. Independent Async work is not joined.

### Stop policy

`StopPolicy{Drain, Timeout, Join, Reason, Catchable}` describes how an execution ends. Zero fields inherit: call → `CheckoutOptions.Stop` → `PoolOptions.Stop` → the built-in policy (`Timeout` 3 s, `Join` 3 s), a constant rather than a variable. The pool resolves its policy at `NewPool`, the session at `Checkout`, and each call over the session's. A nil `Reason` is `KeyboardInterrupt`. `Drain` and `Join` MUST be non-negative; a negative `Timeout` (`KillNow`) kills at the request instant. `Catchable` and `Drain` inherit like every other field, so a call cannot switch them off.

One timeline serves `Run.Stop`, `Session.Stop`, `Session.Close`, `Pool.Shutdown` and feed-context cancellation:

```
t0                  Stop called; with Drain > 0 the run may end on its own
t0+Drain            request: callbacks cancelled; Reason delivered at the next host call, await or suspension
                    Catchable=false → AbortFeed (uncatchable, session kept)
                    Catchable=true  → resume with the exception; callback context rotated
t0+Drain+Timeout    run not ended → worker killed (SessionKilledError, session lost)
+Join               Go callback still running → Stop returns ErrCallbackDetached
```

An execution has at most one `stopRequest`. The first caller creates it and starts one watchdog; later callers, including the feed-context hook, join it and can only move `requestAt` and `killAt` earlier. The first `Reason` and `Catchable` win. The watchdog enters the request phase at `requestAt` and calls `forceExecution` at `killAt`, which fences terminal state under `life.mu` and kills the worker outside the lock. Old timers MUST verify identity and terminal state before acting.

| Phase | Request, uncatchable | Request, catchable | Kill |
|---|---|---|---|
| Starting / Preparing | the run ends with the reason before the first send (`StopAborted`) | same | n/a |
| Executing (Python runs, no suspension) | wait for the next suspension | wait | at `killAt` |
| Callback (host call running) | callback context cancelled; at return → `AbortFeed` | callback context cancelled; at return → resume with the exception, rotate the callback context | at `killAt` |
| Paused (`FeedStart` snapshot) | `AbortFeed` now; the next `Resume*` returns the error | falls back to `AbortFeed` | at `killAt` |
| Control (`Close` / `Dump` in flight) | after the operation | after the operation | at `killAt` |
| Aborting | already in progress | — | at `killAt` |
| Finished | `StopFinished` with the run's own result | same | — |

`Stop` returns `Stopped{How, Err, SessionErr}` once the kind is final and the run has ended: `StopAborted` when the reason ended it and the session is kept; `StopKilled` after a kill, with `Err` and `SessionErr` the same `*SessionKilledError`; `StopFinished` when the run ended on its own, including a caught catchable stop that completed and a worker or server that ended the run first (`Err` is then the loss error and `SessionErr` is set); `StopNotRunning` from `Session.Stop` on an idle session. The caller's context bounds only its wait: on expiry `Stop` returns `StopPending` with `ctx.Err()` and the request keeps its deadlines. After a kill, `Stop` waits up to `Join` for the driver and returns `ErrCallbackDetached` when a Go callback still runs. A stop never returns `ErrSnapshotStale`; a `Run` whose execution is no longer current reports `StopFinished`.

Catchable delivery: at the boundary the answerer marks the reason delivered, replaces the execution's callback context with a live one (`context.WithCancel(context.WithoutCancel(stepCtx))`) and resumes the worker with the exception. Pending futures registered before the delivery stay cancelled with the old context. Python that catches the exception can make further host calls until it completes (`StopFinished`) or until `killAt`. An uncaught exception ends the run as `StopAborted`. A catchable request on a paused snapshot falls back to `AbortFeed`, because the application owns the snapshot chain (N10 in the design record).

### Bounds

- `MaxHostObjects` (default 10 000, `Unlimited` disables) bounds the instance store. Instances and their class types both count. The first wrapper past the bound fails with `*ResourceError`, which raises `RuntimeError: host object limit N exceeded` inside the sandbox; the session stays usable.
- `MaxPendingFutures` (default 1000, `Unlimited` disables) bounds unresolved wire call IDs per execution as `pending future limit N exceeded`. Shared Future pointers count separately. Duplicate call IDs are protocol errors.
- `Session.Stats` reports `HostObjects`, `PeakHostObjects` and `PendingFutures`.

## Slot

## Rotation

A session of a rotating pool (`Remote` with `RotateSessions`) moves to a fresh connection before the server's session timeout closes it. `FeedRun`, `FeedStart`, `Go`, `LoadSession`, `LoadSnapshot`, `Dump` and `InstallDependencies` call `rotateIfDue` first; an idle session is rotated by a timer. The rotation takes a dump, hands off the capacity slot, dials again, loads the dump, and keeps the same `*Session`, its runtime and its stop policy. An operation that arrives while a rotation runs waits for it instead of reporting `ErrSessionBusy`. A failed rotation ends the session with `*RotationError`, whose `Dump` restores the state on a new session. See `supervisor.md`.

## Slots

`Pool.Slot(opts)` returns a `Slot` that owns at most one session checked out with `opts`. Under its own mutex, `Go`, `FeedRun` and `FeedStart` acquire the session: a closed slot returns `ErrSessionClosed`; a session whose state is not `SessionClosed` is reused; otherwise a new checkout replaces it, and sandbox state is not restored. A session that is not `SessionIdle` returns `ErrSessionBusy` (or its terminal error). `Slot.Stop` delegates to `Session.Stop`, or reports `StopNotRunning` without a session. `Slot.Close` marks the slot closed, drops the session and closes it with the policy; it is idempotent. A terminal old session needs no close, because `terminateSession` already retired its worker.

## Host registry

- `NewHost()` builds a `Host`. `Func(name, fn, ...HostFuncOptions)` and `Object(name, v, opts)` validate at registration: identifiers MUST be unique and not hard Python keywords; an object class and exposed method names are validated too. Configure the host before checkout. Runtime resolution performs one O(1) lookup, not a registry copy.
- `RuntimeOptions.Host` exposes the registry to every session of the runtime. Its objects are put into the instance store at checkout. `FeedOptions.ExternalLookup` is consulted first, so its entries override host names.
- `Stubs()` renders Python stubs for `TypeCheckStubs`: one `def` per function from its Go signature (a leading `context.Context` and a trailing `Kwargs` are mapped, unknown types are `Any`), one class per object with its allowed methods, and one typed name per object.
- Fixed reflected arguments, including method self, are positional-only. The `/` marker is written only after at least one fixed parameter; `self` does not count. Optional labels are copied, exclude context/Kwargs and include variadic arguments: `HostFuncOptions.ParameterNames` for functions, `ClassInstanceOptions.ParameterNames` and `ClassTypeOptions.ParameterNames` by sandbox method name for instance methods and statics. Every key MUST name an exposed method and every list MUST match its signature, checked at `NewClassInstance`, `NewClassType` and `Host.Object`; instance names override class names. Direct Function implementations retain generic args/kwargs stubs. Labels MUST NOT imply keyword binding that runtime does not support.
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
without sending; the owner checks the stop request before Resume. A stopped old
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
