# Appendix B — Python binding inventory and protocol rules for a new parent

## B.1 Python public surface (checklist, `pydantic_monty`)

- Pools: `Monty(binary_path, min_processes=1, max_processes, checkout_timeout, request_timeout, max_checkouts_per_worker)`, `AsyncMonty(...)`, `AsyncMontyWebsocket(url, max_processes, checkout_timeout, request_timeout=10.0, connect_headers)`.
- `checkout(script_name='main.py', limits, type_check=False, type_check_stubs, type_check_format, type_check_color=False, assert_message_annotations, print_flush_interval)`.
- Sessions: `feed_run`, `feed_start`, `load_session`, `load_snapshot(state, mount, print_callback, external_lookup, os)`, `dump`, `install_dependencies`, `worker_pid`.
- Snapshots: `MontyComplete.output`; `FunctionSnapshot{allow_eager_await, script_name, is_os_function, object_id, function_name, call_id, args, kwargs; resume(result), resume_not_handled(), resume_auto(), dump()}`; `NameLookupSnapshot{script_name, variable_name, object_id; resume(value=), resume_auto(), dump()}`; `FutureSnapshot{pending_call_ids; resume(results), resume_auto(), dump()}`.
- Resume result dicts: `{'return_value'}`, `{'exception'}`, `{'exc_type','message'}`, `{'future': ...}`.
- `ResourceLimits` TypedDict; errors `MontyError.exception()`, `MontySyntaxError`, `MontyRuntimeError`, `MontyTypingError`, `MontyConversionError`, `MontyCrashedError{timed_out, exit_status}`, `MontyDisconnectError`, `MontyShutdown{dump}`, `Frame`.
- Print: `CollectStreams`, `CollectString`.
- FS: `MountDir(host_path, virtual_path, mode, write_bytes_limit, memory_usage_limit)`, `NOT_HANDLED`, `AbstractOS` (24 ops), `OSAccess(files, environ)`, `MemoryFile`, `CallbackFile`, `StatResult`, `MontyFileHandle`.
- Host objects: `ClassInstance`, `ClassType`, `MontyClassProxy`, `MontyClassTypeProxy`.
- `instrument_telemetry(tracer, meter, logger)`, `__version__`, `TypeCheckFormat`.

## B.2 Python layering

Three distributions: `pydantic-monty-client` (PyO3 over `monty-pool` with `telemetry`; the parent), `pydantic-monty-runtime` (the `monty` worker binary), `pydantic-monty` (metapackage pinning both). No in-process API. Binary resolution `find_monty_binary`: explicit → `MONTY_BIN` → sysconfig scripts dirs → `PATH` → `<repo>/target/{debug,release}/monty` (newest mtime). Workers spawned with an empty environment.

## B.3 Python test files (950 test functions, ~14k lines)

test_async 50, test_basic 6, test_callback_context 5, test_class_instance 103, test_exceptions 46, test_external 42, test_external_async 6, test_external_function_identity 8, test_feed_start 51, test_inputs 19, test_install_dependencies 5, test_limits 22, test_mount_table 56, test_os_access 139, test_os_access_compat 41, test_os_access_raw 27, test_os_calls 34, test_pool 20, test_print 38, test_re 9, test_readme_examples 1, test_repl 48, test_telemetry 12, test_threading 4, test_type_check 46, test_types 102, test_websocket 10.

## B.4 Python vs TypeScript deltas

Python-only: WebSocket transport (`AsyncMontyWebsocket`, `MontyDisconnectError`, `MontyShutdown`); ready-made virtual FS (`AbstractOS`, `OSAccess`, `MemoryFile`, `CallbackFile`, `StatResult`); `MontyClassTypeProxy`; name-lookup resume to arbitrary values via `resume(value=)`; `ExternalExceptionData` resume by type name; separate sync/async pools.

TS-only: `durationLimitGrace` option (Python hard-wired 1 s); browser/wasm target; explicit resume verbs (`resume/resumeError/resumeNotFound/resumeFuture/resumeNotHandled`) and caller-driven futures; `MontyInstrumentation` OTel SDK lifecycle + non-blocking queued delivery; `ProtocolError`, `MAX_VALUE_DEPTH`; `await using`; JS prototype hardening rules.

## B.5 Protocol rules for a Go parent (from `monty.proto`, `monty-proto/README.md`, `pool-architecture.md`)

- Framing: 4-byte unsigned LE length + protobuf. Parent → child stdin `ParentRequest`; child → parent stdout `ChildEvent`; stderr free-form. `MAX_FRAME_LEN = 256 MiB`; decode budget `DEFAULT_MAX_DECODE_BYTES = 4 × MAX_FRAME_LEN = 1 GiB` of resident decoded bytes, charged incrementally.
- Strict alternation: one request → zero or more `Print` → exactly one turn-ending event. Single blocking read loop.
- EOF without `FatalError` = crash. Spawn with empty env (Windows keeps `SystemRoot`).
- `ParentRequest.kind`: Configure=1, InstallDependencies=2, Feed=3, ResumeCall=4, ResumeNameLookup=5, ResumeFutures=6, Dump=7, Load=8, Reset=9, Shutdown=10, AbortFeed=11; `trace_parent = 20`.
- `ChildEvent.kind`: Print=1, FunctionCall=2, OsCall=3, NameLookup=4, ResolveFutures=5, Complete=6, Error=7, TypingError=8, DumpResult=9, Ok=10, FatalError=11, ShutdownDump=12; `total_execution_micros=20`, `max_duration_micros=21`, `restored_script_name=22`, `max_suspensions=23`.
- Lifecycle: spawn → `Configure` (once; only with no session) or `Load` → feeds/resumes → `Reset` (→ no-session, reusable) / `Shutdown` (→ `Ok` then exit 0). `InstallDependencies` only after a session exists. A checkout dropped mid-turn must kill its worker.
- Version: send `protocol_version = 3` (HEAD). `0` always rejected; out-of-range → `FatalError` naming the range, child exits code 4. `monty_version` informational. Dumps versioned separately (`MONTY\0` magic) — same dump version required.
- `Configure`: `script_name`, `limits`, `type_check`, `type_check_stubs?`, `monty_version`, `assert_message_annotations?` (absent=on@120, 0=off), `type_check_format` (0→FULL), `type_check_color`, `protocol_version`, `print_flush_interval_ms?` (absent=child default 5 ms, 0=line buffering).
- `ResourceLimits`: all optional uint64; absent = unlimited except `max_recursion_depth` and `max_suspensions` (1000).
- Suspensions: `FunctionCall{function_name,args,kwargs,call_id,object_id?,allow_eager_await}` → `ResumeCall{call_id, ExtFunctionResult}`; `OsCall{call_id, oneof 24 arms}` → `ResumeCall` (no handler → `not_handled`); `NameLookup{name, object_id?}` → `ResumeNameLookup{value|undefined|error}` (`undefined` → NameError / AttributeError with object_id); `ResolveFutures{pending_call_ids}` → `ResumeFutures{results}`; any suspension → `AbortFeed{exception}`.
- `ExtFunctionResult`: `return_value | error | future(call_id) | not_found(name) | not_handled`.
- Eager await: `allow_eager_await=true` lets the parent answer a coroutine with `ResumeFutures` carrying exactly one result for that call_id.
- Turn-ending: `Complete`, `Error` (session survives), `TypingError` (pre-rendered), `DumpResult`, `Ok`, `FatalError` (child exits), `ShutdownDump` (relay only; from a local child = protocol violation).
- Parent-enforced policy: `max_suspensions` counting + `AbortFeed` (restore resets count; re-adopted limit capped by checkout config); `max_duration` backstop from `total_execution_micros` + grace (default 1 s) → kill; `request_timeout` per turn (each resume restarts it) → kill, `timed_out=true`; exit code 65 (`EX_DATAERR`) = allocator refusal → `MontyRuntimeError/MemoryError` (worker already dead); exit code 4 = fatal after `FatalError`; 76 (`EX_PROTOCOL`) = desync; 3 = stdout write failure.
- Mounts are host-side: parent answers `OsCall` from its mount table (first refusal), then `os` callback, then `not_handled`. `Feed.cwd` resolved by parent: explicit, or first mount's virtual path on the session's first feed, else empty (`/`). Overlay writes are per-feed and discarded.
- Print: `Print{segments[]{stream,text}}` batched (~8 KiB or flush interval); always flushed before turn end.
- Values: `repr`/`cycle` output-only (reject as input); nesting ~48 lists / 32 dicts / 24 instances; `BigInt` sign+magnitude BE; `Dict` as ordered `Pair` list; `Uuid` 16 bytes; `Type.origin` BUILTIN (no id, valid input) / SANDBOX (id, decoded, rejected as input) / HOST (id); `Type.attrs` non-empty replaces, empty leaves unchanged; semantic validation while decoding — invalid frame from child → discard worker with protocol error; unrepresentable sandbox values degrade to repr strings; host return values the wire cannot carry fail *inside* the sandbox (`TypeError: Cannot convert X to Monty value`, `RuntimeError: Max input depth exceeded`); only `inputs`/`external_lookup` fail host-side.
- Dump/restore: `Dump{}` works idle or suspended; >256 MiB fails call, session untouched. `Load{state}` only from no session; suspended state re-emits the suspension; `restored_script_name` on the reply; mounts never in dumps; overlay writes lost; failed load poisons the session; instance store not in dump → proxies after restore.
- Invariants: workers never fork (kill single PID is sufficient); mount I/O has no deadline and is not cancellable; special files rejected with `PermissionError`; `max_duration` exhaustion terminal for the session; cancelled in-flight turn loses the session.
- WebSocket (optional): single-use dialed workers, `User-Agent: monty-pool/<version>`, `traceparent` header when tracing; exit codes don't travel.
