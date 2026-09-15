# Telemetry

- `monty.Instrument` or a `monty.Instrumentation` installs OpenTelemetry components process-wide. Pools created afterwards record into them. Supplying components opts in to recording fed code, inputs, call arguments, results, exceptions and print output.
- Each signal fails independently: a tracer, meter or logger that panics or errors turns off its own signal only. Uninstalling drops open spans without ending them.

## Protocol mirror

- Telemetry mirrors the protocol inside the pool. Every checkout gets an observer before `Configure` is sent. The observer sees each request once it has reached the wire and each event once it is fully decoded, `Print` included. It is closed exactly once, when the worker is released or discarded.
- The span mirror and the turn-metrics mirror are two independent state machines ported from `monty-pool`. The span mirror exists while spans or logs are recorded. Turn metrics exist for every checkout of a metered pool, traced or not.
- Attribute values are logfire-style: booleans, integers, finite floats and strings keep their type, bytes use their repr, everything else is compact JSON. Each value is capped at 64 KB, and a cut sets `length_limit_exceeded` once per span or record.
- `Dump` and `Load` payloads are recorded by size only.

## Span tree

- `session {script_name}` opens at `Configure` and closes at `Reset`. It is a child of the span in the checkout context, and carries the configuration, limits and `worker_pid`.
- `run code` opens at `Feed` and is held across suspensions. It closes at `Complete`, `Error` or `TypingError`, carrying `output` and the execution budget.
- Each suspension opens a child span that closes when its answer is sent, so its duration is the host round trip:
  - `call {function_name}` and `os call {function}` gain `return_value` from the answer.
  - `name lookup {name}` gains `value`.
  - `resolve futures` holds the pending call ids.
  - An abort records `aborted_with` on whichever suspension it answers.
- An eager `ResumeFutures` answering the pending call's id records `return_value` on the call span. Any other `ResumeFutures` emits a `future results` record under the waiting span.
- `load`, `dump` and `install dependencies` are housekeeping spans:
  - A restored suspended feed keeps its `load` span as the run span, and its completion becomes a `complete` record.
  - A failed `Dump` closes only the `dump` span and leaves the feed resumable.
- Log records: one `print {stream}` per output segment, and `error {exc_type}` with `exc_data.*`. The other records are `typing error`, `fatal error` and `shutdown`.

## Metrics

| Instrument | Kind | Unit | Attributes |
|---|---|---|---|
| `monty.pool.workers.live` | up/down counter | `{worker}` | none |
| `monty.pool.workers.idle` | up/down counter | `{worker}` | none |
| `monty.pool.workers.suspended` | up/down counter | `{worker}` | none |
| `monty.pool.checkout.wait` | histogram | `s` | `outcome`: idle, spawned, waited, exhausted, error |
| `monty.pool.worker.terminated` | counter | `{worker}` | `reason` |
| `monty.pool.session.duration` | histogram | `s` | `outcome`: ok, error, abandoned |
| `monty.run.duration` | histogram | `s` | `outcome`: complete, error, typing_error, fatal_error, shutdown |
| `monty.run.execution_time` | histogram | `s` | none; per-run delta of the cumulative clock, re-based by `Load` |
| `monty.turn.duration` | histogram | `s` | `turn`, `outcome` |
| `monty.run.suspensions` | counter | `{suspension}` | `kind` |
| `monty.ext.call.duration` | histogram | `s` | `kind`, `outcome`, `function` for OS calls |
| `monty.snapshot.bytes` | histogram | `By` | `op` |
| `monty.print.bytes` | counter | `By` | `stream` |
| `monty.wire.frame.bytes` | histogram | `By` | `direction` |

- Pool-level instruments come from the pool, per-turn instruments from the mirror.
- Attribute values are closed sets. Sandbox-chosen names never become metric attributes; only the protocol's own OS call names do.

## Callback context

- Host callbacks receive the caller's context with the checkout's innermost open span attached. That context reaches `Function.Call`, `OSHandler`, futures started from them, and `ContextPrintTarget`.
- Caller values survive, so request-scoped values and baggage reach callbacks.
- A snapshot resumed with another context delivers that context's values.
- When spans are not recorded, callbacks receive the caller's context unchanged.

## Trace context propagation

- Requests never carry `trace_parent`, as in `monty-pool`; a worker starts its own trace.
- While spans are recorded, a WebSocket dial whose checkout context carries a valid span context sends `traceparent`, and `tracestate` when non-empty. These headers precede the caller's connect headers, so a caller header of the same name wins.
