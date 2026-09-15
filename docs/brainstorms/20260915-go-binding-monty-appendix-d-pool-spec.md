# Appendix D — `monty-pool` parent-side semantic specification (for the Go re-implementation)

Derived from `monty-pool/src/{lib,pool,worker,checkout}.rs`, `monty-proto/src/{lib,frame,requirement}.rs`, `monty-runtime/src/{main,subprocess}.rs`, `monty-types`, `monty-fs`, `monty-alloc` at HEAD `f8acf4fa`.

## D.1 Configuration

### `PoolConfig`

| Field | Default (subprocess) | Semantics |
|---|---|---|
| `min_processes` | 1 (websocket: forced 0) | eagerly spawned and kept warm |
| `max_processes` | `available_parallelism()` else 4 | cap on live workers (idle + checked out + spawning) |
| `transport` | `Subprocess(path)` / `Websocket(url)` | |
| `checkout_timeout` | None (wait forever) | waiting for capacity only → `Exhausted` |
| `request_timeout` | None | per protocol turn; expiry kills worker → `Timeout`; also bounds ws dial |
| `duration_limit_grace` | 1 s | added to remaining `max_duration` for the backstop |
| `max_checkouts_per_worker` | None | recycle after N finished checkouts |
| `metrics` | None | telemetry |

Validation: `min > max || max == 0` → `Spawn("invalid pool size: min_processes=… max_processes=…")`.

### `ReplConfig` → `Configure`

| Field | Default | Wire |
|---|---|---|
| `script_name` | `"main.py"` | 1 |
| `limits` | None | 2 |
| `type_check` | false | 3 |
| `type_check_stubs` | None | 4 |
| `type_check_config.format` / `.color` | FULL / false | 7 / 8 |
| `assert_message_annotations` | on @120 | 6 (absent=default, 0=off, n=bytes) |
| `print_flush_interval` | None (child default 5 ms) | 10; `0` line buffering |

Always: `protocol_version = 3`, `monty_version = <version>`. Flush encoding: `zero → 0; else clamp(ms, 1, u32::MAX)`.

`ResourceLimits`: `max_duration_micros?`, `max_memory_bytes?`, `gc_interval?`, `max_recursion_depth` (always sent, default 1000), `max_suspensions` (always sent, default 1000; parent enforces).

### `CheckoutOptions`: `telemetry` (traceparent/tracestate spliced at index 0 of headers), `connect_headers` (ws only, last-wins).

### `MountSpec`: opened host dir + normalized virtual path (opened once), `mode` ReadOnly/ReadWrite/Overlay, `write_bytes_limit?`, `memory_usage_limit = 100_000_000`. Non-absolute virtual path or unopenable host dir → `Runtime` error at spec build.

## D.2 Worker lifecycle

**Spawn:** `Command::new(binary).arg("subprocess").env_clear().stdin(piped).stdout(piped).kill_on_drop(true)`; stderr inherited; Windows re-adds `SystemRoot`. No spawn handshake; a broken binary surfaces on the first `Configure`.

**Framing:** LE u32 length + protobuf. Constants: `MAX_FRAME_LEN = 256 MiB`, `DEFAULT_MAX_DECODE_BYTES = 1 GiB` per frame (reset before each decode), `READ_CHUNK = 8 KiB`, `RETAIN_BUF_MAX = 64 KiB`, `SEND_BUF_CAPACITY = 1024`, `DECODE_OFFLOAD_MIN = 1 MiB`. Reader: if ≥4 bytes, `len = LE(buf[0..4])`; `len > MAX` → `FrameTooLarge` (fatal); retain trailing bytes; EOF with empty buffer = clean EOF, EOF mid-frame = `Truncated`; inside a checkout both are errors (child must never close first). Write: check encoded length before encoding so an oversize frame never desyncs.

**Acquire (loop):** arm wakeup before inspecting state; under lock `idle.pop()` (LIFO), discarding workers whose process already exited (`total -= 1`); else if `total < max` reserve slot (`total += 1`), unlock, spawn (failed/cancelled spawn releases slot); else wait on notify bounded by `checkout_timeout` → `Exhausted`. Websocket: always dial fresh, never pooled.

**Release (clean finish):** recycle if websocket (`single_use`) or `checkouts_served >= max_checkouts_per_worker` (`recycled`); else push to idle and notify one waiter. `checkouts_served` increments only on a successful `Reset`.

**Pool close:** drain idle; send `Shutdown` to each; concurrently `reap_or_kill(SHUTDOWN_EXIT_GRACE = 500 ms)`. Drop without close kills idle workers.

**Exit codes:**

| Code | Meaning | Classification |
|---|---|---|
| 0 | Shutdown handled or clean stdin EOF | normal; mid-checkout → `Crashed{Vanished}` |
| 1 | CLI misuse | `Crashed{Vanished, status 1}` on first Configure |
| 3 | stdout write failed | `Crashed{Vanished}` |
| 4 | after `FatalError` (e.g. version skew) | `Crashed{Announced{reason}, status 4}` |
| 65 (`OOM_EXIT_CODE`) | allocator refusal | `Runtime(MemoryError "the worker exceeded its memory limit and was terminated")`; worker gone; later calls → `Finished` |
| 76 (`EX_PROTOCOL`) | desync / oversize response | `Crashed{Announced}` if FatalError arrived, else `Vanished, status 76` |
| signal/none | segfault/abort/kill | `Crashed{Vanished{context}}` |

`poison(context)`: take worker, clear `pending` and `feed_mounts`; ws → `Disconnected{context}`; subprocess → `reap_or_kill(FATAL_EXIT_GRACE = 100 ms)` then classify (65 → MemoryError; else Crashed). `FatalError` event → reap with 100 ms grace → `Crashed{Announced}`. Deadline expiry → drop turn I/O, kill immediately, reap → `Timeout{timeout}`.

**Finish:** subprocess sends `Reset` (deadline `request_timeout`), expects `Ok` → `checkouts_served += 1`, return to idle; other reply → `Protocol("unexpected reply to Reset: …")`, discard. Websocket: no Reset; close frame (≤1 s) then drop. Dropped checkout without finish → kill (`abandoned`).

## D.3 Turn loop

**Checkout creation:** splice telemetry headers; acquire worker; send `Configure` (deadline `request_timeout`); only `Ok` accepted; else `Protocol("unexpected reply to Configure: …")`, discard.

**Generic turn:** `ensure_ready()`; `turn_in_flight = true`; `armed_deadline = deadline`; send; loop recv: decode error → `Protocol("invalid payload from worker: …")` (discard); I/O/EOF → `poison("waiting for a reply")`; `abort_if_over_budget(event)` → continue; remember `restored_script_name`; dispatch on kind. `Print` segments delivered in order to `on_print` (awaited, inside deadline); `UNSPECIFIED|STDOUT` → stdout, `STDERR` → stderr.

| Event | Action |
|---|---|
| FunctionCall | `pending = Call{call_id, name, os_call: None, allow_eager_await}`; return event |
| OsCall | decode typed oneof (missing → `Protocol("OsCall event with no call")`; bad → `Protocol("invalid OS call payload: …")`); `pending = Call{…, os_call: Some}`; return |
| NameLookup | `object_id` must be 16 bytes else `Protocol("NameLookup.object_id is not a 16-byte uuid")`; `pending = NameLookup`; return |
| ResolveFutures | `pending = Futures`; return |
| Complete | `pending = None`, `feed_mounts = None`; missing value → protocol violation; return value |
| Error | unless request was Dump: clear pending/feed_mounts; missing exc → `Protocol("error event with no exception")`; `Runtime(exc)` (session alive) |
| TypingError | clear; `Typing(diagnostics)` |
| Ok / DumpResult | control acks |
| FatalError | reap 100 ms grace → `Crashed{Announced}` |
| ShutdownDump | subprocess: `Protocol("subprocess worker sent a ShutdownDump")`; ws: `Shutdown{dump}` |
| none | `Protocol("unexpected event")` |

**Deadlines:**
```
backstop = duration_budget && grace ? (duration_budget - reported_execution).saturating + grace : None
execution turns (feed, resume*, turn_raw): deadline = min(request_timeout, backstop)
control turns (Configure, Load, Dump, Reset, InstallDependencies): deadline = request_timeout
```
`duration_budget` from `Configure.limits.max_duration`, else adopted from the first event reporting `max_duration_micros` (after Load). `reported_execution` = monotonic max of `total_execution_micros`.

**Feed:** `ensure_ready`; `pending.is_some()` → `Protocol("feed called while a suspension is awaiting an answer")`; `ensure_sendable(inputs)` (depth); `cwd = explicit ? validate_cwd : (cwd_set ? "" : first_mount.virtual_path or "/")`; `feed_mounts = build(mounts)` (None when empty; no I/O); send `Feed{code, inputs, skip_type_check, cwd}`; expect a TurnEvent; if request was sent and result is not `Typing` → `cwd_set = true`. `validate_cwd`: NUL → `"cwd must not contain NUL bytes: {:?}"`; not starting with `/` → `"cwd must be an absolute POSIX path: {:?}"`; strip trailing `/` (empty → `/`).

**Resume family:**

| Op | Precondition | Request |
|---|---|---|
| `resume(v)` | `pending = Call` else `Protocol("no suspended call to resume")`; `NotHandled` only with `os_call` else `Protocol("NotHandled is only valid answering an OS call")` | `ResumeCall{call_id, result}` |
| `resume_name_lookup(r)` | `pending = NameLookup` else `Protocol("no suspended name lookup to resume")` | `ResumeNameLookup{value|undefined|error}` |
| `resume_futures(rs)` | `pending = Futures`, or `Call{allow_eager_await}` with exactly one result matching call_id (else `Protocol("eager result must match the suspended call id")`); else `Protocol("no suspended futures to resume")`; each result Return/Error else `Protocol("future {id} must resolve to Return or Error")` | `ResumeFutures{results}` |

`ResumeValue` → `ExtFunctionResult`: `Return(obj)`→`return_value`; `Error(exc)`→`error`; `Future`→`future(call_id of the suspension)`; `NotFound`→`not_found(function name)`; `NotHandled`→`not_handled`. `pending` is not cleared on send (oversize answer rejected pre-send keeps suspension answerable).

**`max_suspensions` (parent-only):** `suspension_limit = limits.max_suspensions or 1000`; `suspensions_seen = 0`. On every non-Print event: ratchet `reported_execution`; adopt budget; `suspension_limit = min(limit, event.max_suspensions)` (tighten only); if suspension → `seen += 1`. Trip when `is_suspension && seen > limit`: validate OsCall payload first; send `AbortFeed{RuntimeError("suspension limit {limit} exceeded")}`; the next non-Print must be `Error|FatalError|ShutdownDump` else `Protocol("worker answered AbortFeed with something other than an Error")`; failed send → `poison("aborting a feed")`.

**Pre-send validation (session-preserving):** depth (`exceeds_max_value_depth`) → `Runtime(RuntimeError("Max input depth exceeded"))`; frame > 256 MiB → `Runtime(RuntimeError("request frame of {len} bytes exceeds the maximum of {max} bytes"))`; bad cwd → `Runtime(ValueError)`; bad requirement → `Runtime(ValueError("invalid requirement {req:?}: …"))`. Depth costs: scalar 1, list-like 2, dict 3, class instance 4; `MAX_PROTO_VALUE_DEPTH = 97`; `MAX_VALUE_DEPTH = 48`.

**Dump:** `Dump{}` (deadline `request_timeout`) → `DumpResult{state}`; else `Protocol("unexpected reply to Dump: …")`. `Error` reply to Dump keeps `pending`/`feed_mounts` (suspended session stays resumable).

**Restore:** `pending = None`; save budget into `pending_load_budget`; reset `duration_budget`, `reported_execution`, `suspensions_seen` (keep `suspension_limit` as ceiling); install `feed_mounts`; send `Load{state}`; `Ok` → idle (None); re-announced suspension → `Some(event)`; `DumpResult` → `Protocol("unexpected reply to Load: …")`. First non-Print reply settles the budget (Ok/suspension adopt; otherwise restore saved). `cwd_set = true`. Returns `(Option<TurnEvent>, Option<restored_script_name>)`.

**InstallDependencies:** `ensure_ready`; `pending.is_some()` → `Protocol("install_dependencies called while a suspension is awaiting an answer")`; empty list → immediate `Ok(())` with no frame; validate each (`trim` empty → "must not be empty"; starts with `-` → "must not start with '-' (it would be parsed as a uv option)"); send; reply must be `Ok`; `Error` → `Runtime` (session usable).

**Dropped turns:** `turn_in_flight` set across send+read (and mount servicing). A later call seeing it set: discard worker, `Protocol("a previous protocol turn was cancelled mid-flight; the worker was discarded")`. `ensure_ready` → `Finished` when worker gone. Every public op calls `ensure_ready` before its own validation.

## D.4 Mount servicing

Order: receive OsCall → `resume_from_mounts()` (`Some(event)` serviced incl. into an error; `None` uncovered, suspension intact) → host `os` handler → `NotHandled`.

Mount-routable arms (have a filesystem primary path): exists, is_file, is_dir, is_symlink, read_text, read_bytes, stat, iterdir, resolve, absolute, unlink, rmdir (2–13); write_text, append_text (14,15); write_bytes, append_bytes (16,17); open (18); mkdir (19); rename (20, primary src, secondary dst). Never mount-serviced: getenv (21), get_environ (22), date_today (23), date_time_now (24).

Routing: no primary path → NotHandled; length checks first (`PATH_MAX 4096`, `NAME_MAX 255`, `DEPTH_MAX 64` → `ENAMETOOLONG`, path elided); NUL → embedded-null error; on existence checks rejected paths return `False`; longest virtual-prefix match; rename: both endpoints same mount else `CrossMountRename` error (never handed on); `(None, None)` → NotHandled; mount executes enforcing mode/limits.

Per feed: `feed_mounts` built from specs (no I/O); Overlay → fresh in-memory overlay per feed, discarded on Complete/Error/TypingError (except Error to Dump) and on every worker-loss path. ReadWrite writes persist and are untrusted. `write_bytes_limit` cumulative; `memory_usage_limit` covers retained overlay data + transient results.

`resume_from_mounts` mechanics: requires `pending = Call{os_call: Some}` else `Protocol("no suspended call to resume")` / `Protocol("resume_from_mounts is only valid answering an OS call")`; typed call moved into the table and returned when uncovered; covered calls do host I/O on a blocking pool, outside the deadline but inside `turn_in_flight`; result sent as `Return`/`Error`; if the answer is rejected pre-send while pending is set, re-answer with `Error(that exception)`.

## D.5 `PoolError`

| Variant | Worker | Session |
|---|---|---|
| `Crashed{status, cause: Vanished{context} | Announced{reason}}` | lost | dead; later → `Finished` |
| `Timeout{timeout}` | lost (killed, no grace) | dead |
| `Protocol(msg)` | lost for worker violations / cancelled turns; kept for caller misuse | poisoned only when worker lost |
| `Runtime(exc)` | kept (except OOM MemoryError) | usable |
| `Typing(diag)` | kept | usable |
| `Exhausted` | — | none |
| `Spawn(msg)` | never created | none |
| `Finished` | gone | terminal |
| `Disconnected{context}` | lost (ws) | dead |
| `Shutdown{dump}` | lost (ws) | request did not run |

Display strings: `"monty worker crashed: {reason}"` / `"monty worker crashed while {context}"` [+ `" ({status})"`]; `"monty worker killed after exceeding request timeout of {timeout:?}"`; `"monty worker protocol error: {msg}"`; `"type checking failed:\n{diagnostics}"`; `"no monty worker became available within the checkout timeout"`; `"failed to spawn monty worker: {msg}"`; `"this checkout has already been finished"`; `"monty worker connection closed while {context}"`; `"monty server is shutting down; the request did not run (session dump attached)"`. `io::Error` → `Crashed{None, Vanished{"performing I/O: {err}"}}`.

Termination reasons (metrics): closed, died_idle, recycled, single_use, crash, disconnected, oom, turn_timeout, fatal, discarded, abandoned.

## D.6 Constants

| Constant | Value |
|---|---|
| `PROTOCOL_VERSION` / `MIN_SUPPORTED` | 3 / 3 |
| `DEFAULT_PRINT_FLUSH_INTERVAL` | 5 ms |
| `MAX_FRAME_LEN` | 268 435 456 |
| `DEFAULT_MAX_DECODE_BYTES` | 1 GiB |
| `MAX_VALUE_DEPTH` | 48 |
| `DEFAULT_MAX_SUSPENSIONS` / `DEFAULT_MAX_RECURSION_DEPTH` | 1000 / 1000 |
| `OOM_EXIT_CODE` | 65 |
| `EX_PROTOCOL` | 76 |
| alloc headroom | 4 MiB base, 32 MiB with type checking |
| `DEFAULT_MEMORY_USAGE_LIMIT` (mount) | 100 000 000 |
| `SHUTDOWN_EXIT_GRACE` / `FATAL_EXIT_GRACE` | 500 ms / 100 ms |
| ws `CLOSE_WRITE_TIMEOUT` / `DEFAULT_DIAL_TIMEOUT` / channel depth | 1 s / 30 s / 1 |
| path policy | PATH_MAX 4096, NAME_MAX 255, DEPTH_MAX 64 |

## D.7 WebSocket transport (optional for Go)

URL dialed verbatim; one binary message per frame (no length prefix); max message = 256 MiB; decode budget reset per message; `User-Agent: monty-pool/<version>` before caller headers; telemetry headers first; dial budget `request_timeout` else 30 s; single-use; reader polled continuously; non-binary/close/end → `Truncated` → `Disconnected`; teardown close frame ≤ 1 s; no Reset; `ShutdownDump` accepted only here; `pid()` None.

## D.8 Implementation checklist

1. Frame codec with LE u32 prefix, 256 MiB cap before allocation, clean-EOF vs mid-frame EOF, retained trailing bytes, partial state outside the read call.
2. Spawn `monty subprocess` with empty env (+`SystemRoot` on Windows), piped stdin/stdout, inherited stderr, guaranteed kill on abandonment.
3. `Configure` → `Ok` on checkout; `Reset` → `Ok` on finish; `Shutdown` + 500 ms grace + kill on close.
4. One turn = one request, consume Prints, stop at first non-Print. Never pipeline.
5. Deadlines `min(request_timeout, remaining_max_duration + grace)` for execution turns; `request_timeout` alone for control turns; kill on expiry.
6. Count suspensions; `AbortFeed(RuntimeError("suspension limit {n} exceeded"))` past the limit; require Error/FatalError/ShutdownDump next.
7. Classify death: FatalError → announced; exit 65 → MemoryError; else vanished; deadline → timeout.
8. Reject oversize requests before writing any bytes.
