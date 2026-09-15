# Pool and turn engine

## Pool

- `MinProcesses` workers are spawned at creation (default 1); `MaxProcesses` caps live workers.
- Acquiring pops the most recently idle worker, skipping dead ones; otherwise it spawns while under the cap, otherwise it waits (bounded by `CheckoutTimeout`).
- A finished checkout sends `Reset`. A worker that answers `Ok` returns to the idle list unless `MaxCheckoutsPerWorker` is reached.
- WebSocket workers are single-use: no prewarming, no `Reset`, a close frame on finish.
- `Close` sends `Shutdown` to idle workers and kills any that do not exit within 500 ms. Checked-out workers finish with their sessions.

### Accounting

- Every worker is in one of four states, reported by `Stats()`: `Starting` (spawn in flight), `Active` (checked out), `Idle`, `Retiring` (handed to a reaper, not yet exited). All four count toward `MaxProcesses`, so a waiter proceeds only once a retiring worker has exited.
- Retirement is asynchronous. A worker leaving `release` or `discard` goes onto a channel with `MaxProcesses` capacity, served by at most 8 reaper goroutines. A reaper sends `Shutdown` and waits 500 ms before killing, or, for a killed worker, waits up to 1 s for exit; then it decrements `Retiring` and wakes waiters. A recycled local worker's telemetry observer closes after the reaper has shut it down; a discarded worker's, and a WebSocket worker's, closes at once.
- `discard` retires a worker whose session is lost. A local worker is killed at once; a WebSocket worker gets its close frame from the reaper.
- `Shutdown(ctx)` is `Close` followed by a wait for the live count to reach zero. When `ctx` ends first, the owner's `OnShutdown` hook runs (the root pool calls `CloseNow` on every open session), the wait continues for a fixed 5 s force grace, and `ctx.Err()` is returned when workers still remain. `Checkout` after `Shutdown` returns `ErrPoolClosed`.

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

## Suspension budget

The parent counts suspensions per checkout, that is per session, not per feed. The first one past `max_suspensions` is answered with `AbortFeed(RuntimeError("suspension limit N exceeded"))`; the worker MUST reply with `Error`. A restored dump resets the count but never raises the configured limit. `montygo.Unlimited` disables the count; a lower limit the worker reports still tightens it.

## Abort

`Checkout.Abort` answers the pending suspension with `AbortFeed` carrying an exception. The worker MUST reply with `Error`, which is returned as a runtime error; any other reply is a protocol violation and the worker is discarded. The session uses it for cancelled host calls, `Interrupt` and interrupted snapshots (see `session.md`).

## Failure classification

| Signal | Result | Worker |
|---|---|---|
| `Error` / `TypingError` event | runtime / typing error | kept |
| `FatalError` event | crash with the worker's reason | reaped |
| stream end, exit code 65 | `MemoryError: the worker exceeded its memory limit and was terminated` | gone |
| stream end, other status | crash with the exit status | gone |
| deadline | timeout | killed |
| caller context cancelled mid-turn (Python executing) | context error now, protocol error on the next call | killed |
| caller context cancelled while a host call is pending | `AbortFeed`; runtime error `KeyboardInterrupt` | kept |
| `Session.Interrupt` with Python executing | crash after `InterruptGrace` | killed |
| `Session.CloseNow` | `ErrSessionClosed` | killed |
| malformed frame | protocol error | discarded |
| WebSocket stream end | disconnect, with the close frame's code and reason when one was received | gone |
| `ShutdownDump` (WebSocket only) | shutdown with the dump | gone |

## Mount servicing

An `OsCall` is first offered to the feed's mount table. A covered call is answered from the host filesystem between turns, outside any deadline. An uncovered call goes to the OS handler, then to `not_handled`.
