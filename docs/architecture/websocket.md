# WebSocket workers

- `monty.NewWebSocket` builds a pool whose workers are single-use remote children: no prewarming, one dial per checkout, a close frame when the checkout ends.
- `WebSocketOptions.ConnectHeaders` runs once per checkout, before waiting for capacity and before dialing. Its headers travel to the dialer through the checkout context.
- The upgrade request carries `User-Agent: monty-pool/<version>` first; caller headers win case-insensitively. Header names and values are validated before any I/O.
- Each protocol message is one binary WebSocket message, with no length prefix. A reader goroutine keeps reading, so pings are answered while a session is idle; it buffers at most one frame.
- The remote is untrusted and exit codes do not travel: a dropped connection is a `DisconnectError`, and a draining server's `ShutdownDump` becomes a `ShutdownError` carrying the dump. A `ShutdownDump` from a local worker is a protocol violation.
- Finishing, abandoning or timing out a checkout sends a close frame bounded to one second, then drops the socket.
- `RequestTimeout` defaults to 10 seconds, as in the Python binding; `NoRequestTimeout` disables it.
