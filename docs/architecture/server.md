# monty-server

`monty-server` is an open WebSocket server for Monty workers. It follows Full Monty, specified by upstream `docs/server.md`: the same flags, admission status codes, limit ceilings, timeouts, drain protocol and signed dumps. Any Monty WebSocket client can use it, including montygo's `NewWebSocket` and Python's `AsyncMontyWebsocket`. Deviations are listed in `docs/parity/server.md`.

The crate lives in `server/`. It is built on `monty_pool`: every request after `Configure` goes through `Checkout::turn_raw`, which relays decoded `monty.v1` protobuf messages to the worker without converting them to Monty values.

## Process model

```
client ── ws:// ──▶ axum router (tokio)
                     ├─ GET /          info page
                     ├─ GET /health    readiness
                     ├─ GET /metrics   OpenMetrics
                     └─ WS  /          admission ─▶ session task ─┬─ reader task
                                                                   ├─ writer task
                                                                   └─ monty_pool::Checkout
                                                                        │ turn_raw over framed stdio
                                                                        ▼
                                                                   `monty subprocess`, one per session
```

- One `monty_pool::Pool` uses the subprocess transport with `min_processes = 0`, `max_processes = --max-sessions`, `max_checkouts_per_worker = 1`, a 5 s checkout timeout and `request_timeout = --turn-timeout`.
- No worker starts with the server. Each session checks out a fresh `monty subprocess`, which exits when the session ends. No sandbox state crosses sessions.
- Each upgraded connection runs a session task, a reader task and a writer task. The writer owns the socket and drains a queue of 16 outbound messages.
- stdout carries one line, `ws://<bound address>/`, once the listener is bound. stderr carries logfmt lines `ts_ms=<ms> level=<level> event=<event> key=value …`. No tracing subscriber is installed.
- Startup order: parse flags, validate them, install OTLP exporters, build the tokio runtime, build the pool, bind, print the URL, watch signals, serve.
- A configuration or startup failure exits 2 with one stderr line `monty-server: <reason>`. A serve error after startup exits 1. A completed drain exits 0.

Log events: `listening`, `session_start`, `session_end` (with `outcome`, `detail` and `duration_ms`), `rejected`, `dump_rejected`, `worker_failed`, `shutdown_dump_too_large`, `drain_started`, `drain_forced`, `drain_finished`, `otlp_metrics_export_failed`, `otlp_metrics_client_failed`, `signal_handler_failed`.

## Configuration

Every flag has an environment variable. A flag wins over its variable. Durations are whole seconds.

| Flag | Variable | Default | Rule |
|---|---|---|---|
| `--host` | `MONTY_SERVER_HOST` | `127.0.0.1`; the image `CMD` passes `0.0.0.0` | an address containing `:` binds as `[host]:port` |
| `--port` | `MONTY_SERVER_PORT` | `8000` | `0` selects an ephemeral port |
| `--monty-bin` | `MONTY_BIN` | `monty`; the image sets `/usr/local/bin/monty` | a path MUST name an executable file; a bare name is searched on `PATH` |
| `--max-sessions` | `MONTY_SERVER_MAX_SESSIONS` | `64` | at least 1 |
| `--max-sessions-per-client` | `MONTY_SERVER_MAX_SESSIONS_PER_CLIENT` | `10` | `0` disables |
| `--idle-timeout` | `MONTY_SERVER_IDLE_TIMEOUT` | `60` | `0` disables |
| `--keepalive` | `MONTY_SERVER_KEEPALIVE` | `5` | `0` disables |
| `--session-timeout` | `MONTY_SERVER_SESSION_TIMEOUT` | `3600` | `0` disables |
| `--turn-timeout` | `MONTY_SERVER_TURN_TIMEOUT` | `300` | `0` disables |
| `--drain-grace` | `MONTY_SERVER_DRAIN_GRACE` | `30` | any value |
| `--max-memory-mib` | `MONTY_SERVER_MAX_MEMORY_MIB` | `64` | `0` disables |
| `--max-duration` | `MONTY_SERVER_MAX_DURATION` | `60` | `0` disables |
| `--max-recursion-depth` | `MONTY_SERVER_MAX_RECURSION_DEPTH` | `1000` | `0` is rejected |
| `--trust-forwarded-for` | `MONTY_SERVER_TRUST_FORWARDED_FOR` | off | |
| `--dump-key` | `MONTY_SERVER_DUMP_KEY` | required | at least 16 bytes of the UTF-8 value |
| `--dump-key-previous` | `MONTY_SERVER_DUMP_KEY_PREVIOUS` | none | at least 16 bytes, different from `--dump-key` |
| `--otlp-endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` | none; an empty value disables export | MUST start with `http://` or `https://`; a trailing `/` is trimmed |
| `--otlp-protocol` | `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` | `grpc` is rejected when an endpoint is set |
| — | `OTEL_EXPORTER_OTLP_HEADERS` | none | comma-separated `key=value` pairs sent with every export |
| — | `OTEL_SERVICE_NAME` | `monty-server` | resource `service.name` |

`--help` hides the values of the dump key variables.

`monty-server probe [--url URL]` sends `GET http://127.0.0.1:${MONTY_SERVER_PORT:-8000}/health` by default, with 2 s connect, read and write timeouts. It accepts only `http://` URLs. It exits 0 on status 200 and 1 otherwise.

## HTTP endpoints

| Request | Response |
|---|---|
| `GET /` without `Upgrade` | 200 `text/plain` info page: `monty-server <version> (monty <MONTY_REV>)` and `WebSocket endpoint: ws://<bound>/` |
| `GET /` with invalid upgrade headers | axum's upgrade rejection, not counted in metrics |
| `GET /` WebSocket upgrade | admission, then a session |
| `GET /health` | 200 with an empty body; 503 `monty server is shutting down` while draining |
| `GET /metrics` | 200 `application/openmetrics-text; version=1.0.0; charset=utf-8` |

## Admission

Admission runs before the upgrade, in this order:

| Check | Status | Body | `reason` label |
|---|---|---|---|
| draining | 503 | `monty server is shutting down` | `draining` |
| open sessions ≥ `--max-sessions` | 503 | `monty server at capacity` | `capacity` |
| the caller's sessions ≥ `--max-sessions-per-client` | 429 | `too many sessions for this client` | `client_quota` |

- The caller identity is the peer IP. With `--trust-forwarded-for`, it is the last entry of the last `X-Forwarded-For` header when that entry parses as an IP, else the peer IP. IPv4-mapped IPv6 addresses become IPv4.
- An admitted request holds a permit for the session's lifetime. `monty_server_sessions_active` counts permits.
- The upgrade accepts messages and frames up to `monty_proto::MAX_FRAME_LEN` (256 MiB).
- The `traceparent` and `User-Agent` headers are kept for the connection span.
- There is no upgrade handshake timeout.

## Session

### Requests

Every binary message decodes into one `ParentRequest`. The session checks, in order:

1. An undecodable frame closes 1008 `malformed request frame: <error>`.
2. A request without a kind closes 1008 `request has no kind`.
3. While draining, the request does not run. The session sends `ShutdownDump`; see Drain.
4. A second `Configure` closes 1008 `session already configured`.
5. `Configure` configures the session.
6. `Reset` or `Shutdown` closes 1008 `lifecycle requests (Reset/Shutdown) are not accepted`.
7. Any other request before `Configure` closes 1008 `expected Configure as the first request`.
8. `Load` verifies its envelope first; see Dump envelope.
9. The request runs as a turn.

A text message closes 1008 `text messages are not part of the protocol`.

### Configure

1. `monty_proto::check_protocol_version` refuses a mismatched version. The session sends `FatalError` with the upstream text, for example `unsupported protocol version 2 (server supports protocol version 3, try updating to a newer client version)`, and closes 1000.
2. The client limits are clamped to the ceilings; see Limit ceilings.
3. The pool's `ReplConfig` takes the script name (empty keeps the pool default), the clamped limits, type checking, stubs, format and colour, assert message annotations and the print flush interval.
4. `Pool::checkout_with` runs under the turn deadline and the hard drain signal. With telemetry, the checkout's spans are parented to the connection span.
5. On success, `monty_server_workers_spawned_total` increments. The session synthesizes the `Ok` a worker would send, carrying the clamped `max_duration_micros` and `max_suspensions` (the client value, else the upstream default). The idle deadline is armed.
6. A pool error follows Failure forwarding.

### Turns

- A non-resume request that arrives outside a suspension arms the turn deadline. Resume requests are `ResumeCall`, `ResumeNameLookup`, `ResumeFutures` and `AbortFeed`.
- `turn_raw` streams each `Print` event to the client as it arrives.
- While a turn runs, the session watches the turn and session deadlines, keepalive, the client socket and the hard drain signal. A request that arrives mid-turn is held and runs next. The first drain signal does not interrupt a turn.
- An interrupted turn drops the checkout, which kills the worker.

The turn-ending event is handled before it is forwarded:

| Event | Handling |
|---|---|
| `DumpResult` | the state is signed into an envelope; `dumps_total{op="signed"}` |
| `DumpResult` whose envelope exceeds `MAX_FRAME_LEN` | `FatalError` `response frame of <n> bytes exceeds maximum of <max> bytes`, close 1011 |
| `FatalError` | forwarded, close 1000 |
| `Ok` or a suspension answering `Load` | the echoed budget MUST pass the restore check, else close 1008 `restored session exceeds server limits` |
| a suspension | forwarded; the session is suspended; after `Load`, the turn deadline is re-armed |
| any other event | forwarded; the turn deadline is cleared, except after a `Dump` inside a suspension |

Every forwarded event re-arms the idle deadline.

### Client close

- A client close with a checkout outside a suspension calls `Checkout::finish`, bounded to 5 s. Otherwise the checkout is dropped and the worker killed.
- The writer gets 2 s to flush. The reader is aborted, and the permit is released.
- A failed write ends the session with outcome `error`.

## Timeouts

| Deadline | Armed | Cleared | On expiry |
|---|---|---|---|
| idle | at upgrade, and after every event that awaits a request: `Ok`, a suspension, a turn-ender, an invalid-dump `ValueError` | when a request arrives | close 1008 `idle timeout of <n>s exceeded` |
| turn | when a non-resume request arrives outside a suspension; it also bounds the `Configure` checkout | on a turn-ender that is not a suspension | close 1008 `turn timeout of <n>s exceeded` |
| session | at upgrade | never | close 1008 `session timeout of <n>s exceeded` |
| keepalive | each interval, a ping when none is outstanding | on a pong | a ping unanswered for a full interval ends the session without a close reason |

- Every expiry increments `monty_server_timeouts_total{kind}`, drops the checkout and ends the session with outcome `timeout`.
- The turn deadline keeps running through suspensions, so it includes host callback time. `--max-duration` counts only interpreter execution.
- The pool's `request_timeout` equals `--turn-timeout` and backstops each exchange inside a turn. It surfaces as a pool error.

## Failure forwarding

| Server-side outcome | Sent to the client | Close | Outcome |
|---|---|---|---|
| `turn_raw` returns an event | the event, handled as above | — | — |
| a worker `FatalError` event | the event | 1000 | `error` |
| `PoolError::Runtime(exception)`, such as the memory kill `MemoryError: the worker exceeded its memory limit and was terminated` | `Error` with that exception | 1011 | `error` |
| any other `PoolError`, such as a crash, a timeout or a spawn failure | `FatalError` with the `PoolError` display text | 1011 | `error` |
| protocol version refused | `FatalError` with the `check_protocol_version` text | 1000 | `error` |
| oversized signed `DumpResult` | `FatalError` `response frame of …` | 1011 | `error` |
| invalid envelope on `Load` | `Error` `ValueError: invalid session dump signature` | — | the session continues |
| protocol violation | — | 1008 with the reason | `error` |
| idle, turn or session timeout | — | 1008 with the reason | `timeout` |
| keepalive timeout | — | no reason | `timeout` |
| drain, next request | `ShutdownDump` | 1001 `server is shutting down` | `drained` |
| drain grace expired, or a second signal | — | 1001 `server is shutting down` | `drain_dropped` |
| capacity, quota or draining at upgrade | HTTP 503, 429 or 503 | — | no session |

Pool failures are logged as `worker_failed`.

## Close codes

| Code | Used for |
|---|---|
| 1000 | protocol version refusal; a worker `FatalError` |
| 1001 | drain: after `ShutdownDump`, and for sessions still open when the drain turns hard |
| 1008 | protocol violations, restored limits over the ceilings, idle, turn and session timeouts |
| 1011 | pool failures; an oversized signed dump |

A close frame gets 1 s to be written. Close reasons are truncated to 123 bytes on a UTF-8 boundary.

## Texts

`server/src/texts.rs` defines every server text.

| Name | Text |
|---|---|
| `INVALID_DUMP` | `invalid session dump signature`, raised as `ValueError` |
| `HTTP_CAPACITY` | `monty server at capacity` |
| `HTTP_CLIENT_QUOTA` | `too many sessions for this client` |
| `HTTP_DRAINING` | `monty server is shutting down` |
| `CLOSE_EXPECTED_CONFIGURE` | `expected Configure as the first request` |
| `CLOSE_ALREADY_CONFIGURED` | `session already configured` |
| `CLOSE_LIFECYCLE` | `lifecycle requests (Reset/Shutdown) are not accepted` |
| `CLOSE_TEXT_MESSAGE` | `text messages are not part of the protocol` |
| `CLOSE_EMPTY_REQUEST` | `request has no kind` |
| `CLOSE_RESTORED_OVER_LIMITS` | `restored session exceeds server limits` |
| `CLOSE_SHUTTING_DOWN` | `server is shutting down` |
| `close_malformed` | `malformed request frame: <error>` |
| `close_idle`, `close_session`, `close_turn` | `idle timeout of <n>s exceeded`, `session timeout of <n>s exceeded`, `turn timeout of <n>s exceeded` |
| `frame_too_large` | `response frame of <n> bytes exceeds maximum of <max> bytes` |
| `info_page` | `monty-server <version> (monty <MONTY_REV>)\nWebSocket endpoint: ws://<bound>/\n` |

Every other text comes from upstream verbatim: the protocol version refusal from `monty-proto`, and `PoolError` display text, including the memory kill message, from `monty-pool`.

## Dump envelope

Every dump that leaves the server, in `DumpResult.state` or `ShutdownDump.dump`, is wrapped in an `MTYD` v1 envelope.

```
offset  size  field     value
0       4     magic     "MTYD" (0x4D 0x54 0x59 0x44)
4       1     version   0x01
5       4     key_id    SHA-256(key)[0..4]
9       32    mac       HMAC-SHA256(key, magic ‖ version ‖ key_id ‖ MONTY_REV ‖ state)
41      n     state     raw worker dump
```

- `MONTY_REV` is the 40-character ASCII SHA in `server/src/version.rs`, so a dump loads only on a server built at the same upstream revision. The test `monty_rev_matches_lockfile` keeps it equal to the `monty-pool` revision in `server/Cargo.lock`.
- The key is the UTF-8 bytes of `--dump-key`. `--dump-key-previous` only verifies; signing always uses the current key.
- `key_id` selects the current or the previous key. An unknown id fails.
- The MAC is compared in constant time.
- Verification fails for a short input, a bad magic, another version, an unknown key or a bad MAC. Every failure sends the same `ValueError`, increments `dumps_total{op="rejected"}`, logs `dump_rejected` with the cause, and keeps the session. The worker never sees the bytes.
- A verified envelope increments `dumps_total{op="verified"}`, and the worker receives the raw state.
- Signing is refused when the envelope would exceed `MAX_FRAME_LEN`.
- A dump from a local worker, or from a server with another key, is rejected.

## Limit ceilings

| Ceiling | Flag | Client field |
|---|---|---|
| duration | `--max-duration`, seconds | `max_duration_micros` |
| memory | `--max-memory-mib`, MiB | `max_memory_bytes` |
| recursion depth | `--max-recursion-depth` | `max_recursion_depth` |

- An absent client value takes the ceiling.
- A client value above the ceiling is lowered to it. A lower client value wins.
- A disabled ceiling passes the client value through. Recursion depth cannot be disabled.
- `gc_interval` and `max_suspensions` pass through. `monty-pool` enforces the suspension limit.
- After `Load`, an enabled duration ceiling requires the reply's echoed `max_duration_micros` to be present and within the ceiling. The worker does not echo a restored memory limit, so a dump signed under a higher memory ceiling restores with that limit.
- `--max-memory-mib` limits allocator bytes per session, not RSS. Container sizing is in `docker.md`.

## Drain

```
first SIGTERM or SIGINT
  ├─ monty_server_draining = 1; grace deadline = now + --drain-grace; log drain_started
  ├─ the listener closes; /health answers 503 on HTTP connections that were already open
  └─ per session:
       turn in flight           ─▶ finishes; its result is forwarded
       next request             ─▶ does not run:
            no checkout         ─▶ ShutdownDump{}
            checkout            ─▶ turn_raw(Dump), bounded by min(turn timeout, grace left)
                                    ├─ DumpResult signed   ─▶ ShutdownDump{envelope}
                                    ├─ envelope too large  ─▶ ShutdownDump{}, log shutdown_dump_too_large
                                    └─ error or timeout    ─▶ ShutdownDump{}
                                ─▶ close 1001, outcome drained
       silent through the grace ─▶ close 1001, outcome drain_dropped
second signal or grace expiry   ─▶ every remaining session closes 1001 now, outcome drain_dropped
listener stopped ─▶ wait for sessions, at most 3 s after the hard signal ─▶ close the pool
                 ─▶ flush telemetry ─▶ log drain_finished ─▶ exit 0
```

- Any request that arrives while draining, `Dump` and `Load` included, is answered with `ShutdownDump`. It did not run, and the client can resend it after restoring the dump.
- A dump captured during a suspension restores into a session that re-announces the pending call.
- Idle, turn and session deadlines keep running during the drain.
- A second signal logs `drain_forced`.

## Metrics

`GET /metrics` renders the prometheus-client registry. A labelled series appears after its first increment.

| Series | Type | Labels | Changes when |
|---|---|---|---|
| `monty_server_sessions_active` | gauge | — | +1 at admission, −1 when the session releases its permit |
| `monty_server_sessions_total` | counter | `outcome`: `closed`, `error`, `timeout`, `drained`, `drain_dropped` | a session ends |
| `monty_server_rejections_total` | counter | `reason`: `capacity`, `client_quota`, `draining` | an upgrade is refused |
| `monty_server_timeouts_total` | counter | `kind`: `idle`, `keepalive`, `session`, `turn` | a deadline ends a session |
| `monty_server_dumps_total` | counter | `op`: `signed`, `verified`, `rejected` | an envelope is signed, verified or rejected |
| `monty_server_workers_spawned_total` | counter | — | a `Configure` checkout succeeds |
| `monty_server_draining` | gauge | — | 0 → 1 at the first signal |
| `monty_server_build_info` | gauge, always 1 | `version`, `monty_rev` | startup |

- Outcomes: a client close is `closed`; a violation, a worker failure, a protocol refusal or a failed write is `error`; a deadline is `timeout`; a `ShutdownDump` is `drained`; a hard drain is `drain_dropped`.
- The `reason` value `bad_request` is declared but never recorded.

## Telemetry

- `--otlp-endpoint` enables export. Without it, the server exports nothing and writes only its log lines.
- Only OTLP over HTTP with protobuf bodies exists. `--otlp-protocol grpc` fails at startup with `OTLP gRPC export is not supported; use --otlp-protocol http/protobuf`.
- Spans go to `<endpoint>/v1/traces`, logs to `<endpoint>/v1/logs`, and `monty-pool` metrics to `<endpoint>/v1/metrics`. Every export carries `OTEL_EXPORTER_OTLP_HEADERS`.
- The batch processors export from plain threads, so the exporters use blocking `reqwest` clients with a 10 s timeout. They are built before the tokio runtime, because a blocking client cannot be created inside it.
- Metric payloads pass through a queue of 16 to a dedicated thread. A full queue drops the payload. Export failures are logged at most once a minute.
- Each connection records a server span `monty-server connection`. The upgrade's `traceparent` header sets its parent. It carries `client.address`, `user_agent.original` when present, an event `timeout` or `violation` with attribute `detail` when policy ends the session, and `monty.server.outcome` at the end.
- `monty-pool` records its session, run and host-call spans and log records beneath the connection span, with the names and attributes other Monty clients use. They include source code, inputs, results and printed output.
- The drain flushes and shuts down every exporter before exit.

## Security

- There is no authentication, and the listener speaks plain `ws://`. TLS MUST terminate at an ingress or reverse proxy, and the listener SHOULD stay on a private network.
- `--trust-forwarded-for` MUST be enabled only behind a proxy that sanitizes `X-Forwarded-For`. On a directly exposed listener, callers can forge an identity per connection and bypass the quota. Without the flag, callers behind one NAT or proxy share a quota.
- Dumps are signed, so `Load` cannot bring in a forged session or its limits. Replicas MUST share the dump key. A rotation MUST keep the old key as `--dump-key-previous` while old dumps are still in use.
- The dump key SHOULD come from `MONTY_SERVER_DUMP_KEY`, because a command line is visible in `ps`.
- Each session may send frames up to 256 MiB, and there is no handshake timeout. `--max-sessions`, the container memory limit and the ingress SHOULD bound slow or oversized clients.
- The image runs as uid 65532 on `scratch`. Hardening flags are in `docker.md`.
