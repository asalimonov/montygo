# Pool and turn engine

## Pool

- `MinProcesses` workers are spawned at creation; `MaxProcesses` caps live workers.
- Acquiring pops the most recently idle worker, skipping dead ones; otherwise it spawns while under the cap, otherwise it waits (bounded by `CheckoutTimeout`).
- A finished checkout sends `Reset`. A worker that answers `Ok` returns to the idle list unless `MaxCheckoutsPerWorker` is reached.
- WebSocket workers are single-use: no prewarming, no `Reset`, a close frame on finish.
- `Close` sends `Shutdown` to idle workers and kills any that do not exit within 500 ms. Checked-out workers finish with their sessions.

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

The parent counts suspensions per checkout. The first one past `max_suspensions` is answered with `AbortFeed(RuntimeError("suspension limit N exceeded"))`; the worker MUST reply with `Error`. A restored dump resets the count but never raises the configured limit.

## Failure classification

| Signal | Result | Worker |
|---|---|---|
| `Error` / `TypingError` event | runtime / typing error | kept |
| `FatalError` event | crash with the worker's reason | reaped |
| stream end, exit code 65 | `MemoryError: the worker exceeded its memory limit and was terminated` | gone |
| stream end, other status | crash with the exit status | gone |
| deadline | timeout | killed |
| caller context cancelled mid-turn | context error now, protocol error on the next call | killed |
| malformed frame | protocol error | discarded |
| WebSocket stream end | disconnect | gone |
| `ShutdownDump` (WebSocket only) | shutdown with the dump | gone |

## Mount servicing

An `OsCall` is first offered to the feed's mount table. A covered call is answered from the host filesystem between turns, outside any deadline. An uncovered call goes to the OS handler, then to `not_handled`.
