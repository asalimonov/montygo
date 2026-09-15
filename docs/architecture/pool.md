# Pool and turn engine

## Pool

- `MinProcesses` workers are spawned at creation (default 1); `MaxProcesses` caps live workers.
- Acquiring pops the most recently idle worker, skipping dead ones; otherwise it spawns while under the cap, otherwise it waits (bounded by `CheckoutTimeout`).
- A finished checkout sends `Reset`. A worker that answers `Ok` returns to the idle list unless `MaxCheckoutsPerWorker` is reached.
- WebSocket workers are single-use: no prewarming, no `Reset`, a close frame on finish.
- `Close` sends `Shutdown` to idle workers and kills any that do not exit within 500 ms. Checked-out workers finish with their sessions.

### Accounting

- Every worker is in one of four states, reported by `Stats()`: `Starting` (spawn in flight), `Active` (checked out), `Idle`, `Retiring` (handed to a reaper, not yet exited). All four count toward `MaxProcesses`, so a waiter proceeds only once a retiring worker has exited.
- Retirement is asynchronous. Jobs contain immutable worker references, not mutable checkout slots. The queue has MaxProcesses capacity and at most 8 reapers. A graceful job sends Shutdown and waits 500 ms before killing. Reapers MUST keep Retiring capacity charged until Wait observes exit; repeated 1 s timeouts do not free capacity.
- A checkout lease arbitrates normal release versus termination exactly once under its own short mutex. `Terminate` works while the protocol mutex is held by Print or mount servicing. A stop's kill and `Close(ctx, KillNow)` kill both local and remote workers immediately; ordinary remote abandonment retains its graceful close-frame path. A stale checkout MUST NOT kill a normally released worker reused by another session.
- Observer lifetime is reference-counted across the full root operation and each turn. Requested closure runs once, outside locks, after active callbacks return. Accounting retirement MUST NOT wait for those callbacks.
- Close moves idle workers to Retiring and waits within ctx for their observed exits. It MUST NOT remove them from live accounting at dispatch time.
- Root shutdown publication and final checkout tracking share sessionsMu. A checkout that loses this race is terminated, not returned as an untracked live session.
- `internal/pool.Shutdown(ctx)` is `Close` followed by a wait for the live count to reach zero within `ctx`; it returns `ctx.Err()` when workers remain and stops the reapers otherwise. It has no hook and no force grace.

### Root shutdown

`montygo.Pool.Shutdown(ctx, policy...)`:

1. Marks the pool closed under `sessionsMu`, so `Checkout` returns `ErrPoolClosed` and a checkout that loses the race is terminated.
2. Snapshots the open sessions and calls `Session.Close(ctx, policy...)` on each concurrently. Each session resolves the policy over its own, so a running feed is stopped at once (`Drain` in the policy lets it end first), a paused snapshot is invalidated, and an idle session gets its `Finish` turn.
3. Calls `internal/pool.Shutdown(ctx)`.
4. Returns the first session close error other than a context error, else `ctx.Err()` when the wait was cut short, else nil. The context bounds only the caller's wait; stops and closes that were started continue to their deadlines.

### `Pool.Run`

`Pool.Run(ctx, code, opts)` checks out with `opts.CheckoutOptions`, calls `FeedRun` with `opts.FeedOptions` and closes the session under a context derived with `context.WithoutCancel` and bounded by the session policy's `Timeout + Join`, so a cancelled caller context still returns the worker. A checkout failure is returned as is; the feed result is returned after the close.

### Frame byte bound

- Each subprocess and wasm worker has a reader goroutine that pumps frames into a queue bounded by `Options.MaxPendingBytes` (default 64 MiB; `UnlimitedPendingBytes`, -1, disables). Past the bound the reader blocks, so the worker's stdout pipe fills and the worker blocks in its write instead of growing the parent. A single frame larger than the bound is accepted when the queue is empty.
- A kill, a deadline or stream end closes the queue and releases a blocked reader.
- The queue reports its byte delta to the pool's metrics as `monty.pool.pending_frame_bytes`.
- WebSocket workers buffer at most one frame and are not subject to the bound.

## Checkout

A checkout owns one worker and tracks:

- `pending`: the suspension awaiting an answer. Resumes are validated against it.
- the feed's mount table, dropped when the feed ends.
- the session budget: reported execution time, the duration budget and the suspension count.
- `cwdSet`: the first feed sends the first mount's virtual path (or `/`) as the working directory.

## Deadlines

- Control turns (`Configure`, `Load`, `Dump`, `Reset`, `InstallDependencies`) use `RequestTimeout`.
- Execution turns use the smaller of `RequestTimeout` and `remaining max_duration + DurationLimitGrace`.
- The worker's `total_execution_micros` is the only clock; the parent keeps no second one.
- On expiry the worker is killed and the checkout fails with a timeout.
- The session sends every turn under `context.WithoutCancel` of the caller's context; a cancelled feed context ends the run through the stop policy (see `session.md`), not through the turn engine.

## Suspension budget

The parent counts suspensions per checkout, that is per session, not per feed. The first one past `max_suspensions` is answered with `AbortFeed(RuntimeError("suspension limit N exceeded"))`; the worker MUST reply with `Error`. A restored dump resets the count but never raises the configured limit. `montygo.Unlimited` disables the count; a lower limit the worker reports still tightens it.

## Abort

`Checkout.Abort` answers the pending suspension with `AbortFeed` carrying an exception. The worker MUST reply with `Error`, which is returned as a runtime error; any other reply is a protocol violation and the worker is discarded. The session uses it for the uncatchable stop request: cancelled host calls, `Stop` and stopped snapshots (see `session.md`).

## Failure classification

| Signal | Result | Worker |
|---|---|---|
| `Error` / `TypingError` event | runtime / typing error | kept |
| `FatalError` event | crash with the worker's reason | reaped |
| stream end, exit code 65 | `MemoryError: the worker exceeded its memory limit and was terminated` | gone |
| stream end, other status | crash with the exit status | gone |
| deadline | timeout | killed |
| turn context cancelled mid-turn (Python executing) | canonical protocol error wrapping the context error and `ErrTurnCancelled` | killed |
| stop requested while a host call is pending | `AbortFeed`; runtime error with the stop reason (`KeyboardInterrupt`) | kept |
| stop `Timeout` expires in Python or a Go callback | `SessionKilledError`, `StopKilled`; Go driver may still be running | killed and retired |
| `Session.Close(ctx, KillNow)` | `ErrSessionClosed` | killed |
| malformed frame | protocol error | discarded |
| WebSocket stream end | disconnect, with the close frame's code and reason when one was received | gone |
| `ShutdownDump` (WebSocket only) | shutdown with the dump | gone |

## Mount servicing

An `OsCall` is first offered to the feed's mount table. A covered call is answered from the host filesystem between turns, outside any deadline. An uncovered call goes to the OS handler, then to `not_handled`.
