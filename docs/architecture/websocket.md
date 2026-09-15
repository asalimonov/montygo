# WebSocket workers

## Pool and dial

- `monty.NewWebSocket` builds a pool whose workers are single-use remote children: no prewarming, one dial per checkout, a close frame when the checkout ends.
- `WebSocketOptions.ConnectHeaders` runs once per checkout, before waiting for capacity and before dialing. Its headers travel to the dialer through the checkout context.
- The upgrade request carries `User-Agent: monty-pool/<version>` first; caller headers win case-insensitively. Header names and values are validated before any I/O.
- `TLSConfig` configures `wss://` dials. Each dial clones it into a clone of `http.DefaultTransport`, so the caller's value is never mutated. `nil` keeps the system roots.
- `DialContext` opens the TCP connection in place of `net.Dialer`. The dialer keeps the raw TCP connection beneath TLS, so `Kill` closes it and unblocks a pending read.
- The dial (DNS, TCP, TLS and upgrade) is bounded by `RequestTimeout`. With `NoRequestTimeout`, it falls back to 30 s.
- Each protocol message is one binary WebSocket message, with no length prefix. A reader goroutine keeps reading, so pings are answered while a session is idle; it buffers at most one frame.
- The remote is untrusted and exit codes do not travel: a dropped connection is a `DisconnectError`, and a draining server's `ShutdownDump` becomes a `ShutdownError` carrying the dump. A `ShutdownDump` from a local worker is a protocol violation.
- Finishing, abandoning or timing out a checkout sends a close frame bounded to one second, then drops the socket.
- `RequestTimeout` defaults to 10 seconds, as in the Python binding; `NoRequestTimeout` disables it.
- Releasing a WebSocket worker closes its telemetry observer before `release` returns. Nothing is sent to retire a remote worker, so the checkout's spans end before `Session.Close` returns. Local workers retire asynchronously.

## Health check

`monty.CheckWebSocketHealth(ctx, opts)` returns nil when the server answers `GET <path>/health` with 200.

- The URL maps `ws` to `http` and `wss` to `https`, appends `/health` to the path, and drops the query and fragment. Another scheme fails with `<url>: unsupported URL scheme "<scheme>"`.
- `ConnectHeaders` runs once. Its error is returned unchanged. The request carries the same `User-Agent` and validated headers as an upgrade.
- The request uses `TLSConfig` and `DialContext`, like a dial.
- `RequestTimeout` bounds it: 0 means 10 s, and `NoRequestTimeout` leaves only `ctx`.
- Errors are prefixed with the health URL: `health check returned <status>`, `health check timed out after <duration>`, or the transport error. A cancelled `ctx` returns `ctx.Err()`.

## Observed from monty-server

`monty-server` is described in `server.md`. A montygo client observes it as follows.

| Server behaviour | montygo result |
|---|---|
| 503 or 429 at upgrade (capacity, quota, draining) | `Pool.Checkout` fails with a `*SpawnError` whose text carries the status |
| listener closed during drain | `Pool.Checkout` fails with a `*SpawnError`; `CheckWebSocketHealth` fails |
| a close frame with any code, or a dropped connection (policy violation, idle, turn or session timeout, keepalive, drain grace expiry) | `*DisconnectError`; the session is lost |
| `FatalError` (protocol version refusal, worker crash, pool timeout, spawn failure, oversized dump) | `*CrashedError` with the server's reason |
| `Error` with `MemoryError` after a memory kill | `*RuntimeError`; the server then closes, so the next call is a `*DisconnectError` |
| `ShutdownDump` answering the next request during a drain | `*ShutdownError`; `Dump` holds a signed envelope, or is nil when no dump was captured |
| an invalid envelope in `LoadSession` or `LoadSnapshot` | `*RuntimeError` `ValueError: invalid session dump signature`; the session stays usable |

- Dumps from the server are `MTYD` envelopes. They load only into a server with the same dump key, or with it as the previous key, built at the same upstream revision.
- Dumps from local workers are rejected by the server, and envelopes are rejected by local workers.
- The server clamps limits, and its `Ok` echoes the clamped `max_duration_micros` and `max_suspensions`. The pool adopts the echoed duration as its backstop only when the checkout set no `MaxDuration`, and adopts the echoed suspension limit only when it is lower than its own. The worker enforces the clamped values either way.
