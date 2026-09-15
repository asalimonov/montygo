# Test parity

Upstream tests are ported file by file. Subtest names keep the upstream titles, so `go test -run 'TestMount/native/overlay_write_does_not_modify_host'` finds a TS test by its title. Root tests run on the native and wasm backends, and on the websocket backend against the server image.

- **ported**: same scenario and assertions.
- **adapted**: same intent, expressed with Go types or APIs.
- **skipped**: no meaningful Go analogue; the subtest exists and calls `t.Skip` with the reason.

## `@pydantic/monty` (`crates/monty-js/__test__`)

| Spec | Go | Tests | Ported | Adapted | Skipped |
|---|---|---|---|---|---|
| basic.spec.ts | basic_test.go | 9 | 8 | 1 | 0 |
| public_api.spec.ts | public_api_conformance_test.go | 3 | 0 | 3 | 0 |
| repl.spec.ts | repl_test.go | 3 | 2 | 1 | 0 |
| inputs.spec.ts | inputs_test.go | 16 | 16 | 0 | 0 |
| types.spec.ts | types_test.go | 45 | 29 | 16 | 0 |
| value_codec.spec.ts | value_codec_test.go | 2 | 0 | 2 | 0 |
| install_dependencies.spec.ts | install_dependencies_test.go | 2 | 2 | 0 | 0 |
| external.spec.ts | external_test.go | 62 | 50 | 11 | 1 |
| async.spec.ts | async_test.go | 20 | 20 | 0 | 0 |
| exceptions.spec.ts | exceptions_test.go | 49 | 33 | 16 | 0 |
| type_check.spec.ts | type_check_test.go | 20 | 17 | 3 | 0 |
| class_instance.spec.ts | class_instance_test.go | 72 | 61 | 9 | 2 |
| wasm_class_instance.spec.ts | wasm_class_instance_test.go | 5 | 5 | 0 | 0 |
| feed_start.spec.ts | feed_start_test.go, feed_start_mount_test.go | 22 | 22 | 0 | 0 |
| limits.spec.ts | limits_test.go, internal/pool/abort_test.go | 17 | 15 | 2 | 0 |
| print.spec.ts | print_test.go | 35 | 24 | 11 | 0 |
| mount.spec.ts | mount_test.go | 54 | 44 | 9 | 1 |
| pool.spec.ts | pool_test.go, pool_mount_test.go, internal/worker | 19 | 15 | 4 | 0 |
| wasm_print.spec.ts | wasm_print_test.go | 1 | 1 | 0 | 0 |
| wasm_memory_limit.spec.ts | wasm_memory_limit_test.go | 3 | 2 | 1 | 0 |
| wasm_word_size.spec.ts | wasm_word_size_test.go | 2 | 2 | 0 | 0 |
| wasm_type_check.spec.ts | wasm_type_check_test.go | 2 | 2 | 0 | 0 |
| wasm_value_codec.spec.ts | wasm_value_codec_test.go | 5 | 3 | 2 | 0 |
| node_entrypoint_exports.spec.ts | public_api_test.go | 2 | 0 | 2 | 0 |
| node_docs.spec.ts | example_test.go | 2 + snippets | 0 | all | 0 |
| node_telemetry.spec.ts | telemetry_test.go | 9 | 5 | 4 | 0 |
| node_telemetry_components.spec.ts | telemetry_test.go | 1 | 0 | 1 | 0 |
| node_callback_context.spec.ts | callback_context_test.go | 7 | 5 | 2 | 0 |

### Adaptations

- **Disposal**: `await using` becomes `defer Close`, asserting `ErrSessionClosed` / `ErrPoolClosed` afterwards.
- **Values**: `Map` → `*montygo.Dict`, `Set` → `*montygo.Set` / `*montygo.FrozenSet`, `__tuple__` arrays → `montygo.Tuple`, `BigInt` → `*big.Int`, `Buffer` → `[]byte`, marker objects → value structs.
- **Keyword arguments**: the trailing options bag becomes a `montygo.Kwargs` parameter.
- **Exception names**: JS `Error.name` becomes `montygo.Raise(excType, msg)`.
- **Unconvertible values**: a JS `Symbol` becomes a Go `chan int`.
- **Error classes**: `instanceof` checks become `errors.As` against `montygo.Error` and the concrete types; class constructor tests build Go error values directly.
- **Option validation**: values Go's types cannot express (negative or fractional numbers, NaN, `2**32` for `uint32` options, `null` vs `-1` for collector caps) are replaced by the representable boundary cases.
- **Codec unit tests**: tests of the TS wasm value codec run as round trips through a real session.
- **Transport unit tests**: tests of the TS wasm transport run against `internal/pool` with a fake worker, or against `internal/worker` directly.
- **Wasm backend differences**: mounts work on the Go wasm backend; a hard memory breach is classified as `MemoryError` instead of a crash; worker identity tests that need a process id skip on wasm; `' ' * (1 << 60)` overflows the 32-bit index on wasm, so the refused-allocation test uses a 32-bit size there.
- **Class wrappers**: JS prototype hardening (`constructor`, `__proto__`, `Function.prototype`) becomes checks that unexported members, `monty:"-"` fields and wrapper internals stay unreachable.
- **External lookup**: prototype-inherited names become absent map keys; getter-counting tests count name-lookup snapshots.
- **Mounts**: `Object.keys(MountDir)` becomes a reflection check of the exported fields; an empty `Cwd` means "not set" in Go.
- **Worker environment**: on darwin the test inspects `ps eww` instead of `/proc`.
- **Telemetry**: each scenario runs in a child process of the test binary, like `runTelemetryChild`. `AsyncLocalStorage` becomes `context.WithValue`, async callbacks return `*montygo.Future`, and spans are compared by span context. Broken components panic from `Start`, `Emit`, `Add` or `Record` instead of throwing. A broken context becomes a tracer that returns no span, because attaching a span to a Go context cannot fail. Name lookups run no host getter, so the concurrent callback test asserts the `name lookup {name}` span position instead.

### Skipped

| Test | Reason |
|---|---|
| external: stale proxy TypeError survives a throwing getter on the entry | Go map entries have no getters |
| class_instance: a set-like policy from another realm works | `AttrPolicy` is a typed value; there are no realms |
| class_instance: a string policy other than "all" is rejected at construction | an invalid policy string cannot be expressed |
| mount: browser wasm reports mounts as unsupported | the Go wasm backend services mounts host-side |

### Deviations

| Test | Upstream | montygo |
|---|---|---|
| `cancel_test.go`: cancelling while awaiting a host future interrupts the feed and keeps the session | the worker is killed and the session is poisoned | `AbortFeed(KeyboardInterrupt)` ends the feed; `FeedRun` returns a `*RuntimeError` with `TypeName` `KeyboardInterrupt`, `errors.Is(err, ErrSessionLost)` is false and the next feed runs |
| `cancel_test.go`: a gathered future wait honours cancellation | same | a cancelled `ResolveFutures` wait is aborted the same way |

The interrupt cannot be caught by `except KeyboardInterrupt` inside the sandbox, because `AbortFeed` ends the feed (`lifecycle_test.go`: interrupting a host call raises KeyboardInterrupt and keeps the session). Cancelling the context while Python executes still kills the worker, as upstream (`cancel_test.go`: a context deadline mid-turn loses the session and the pool recovers).

## montygo-only tests

Tests of behaviour beyond `@pydantic/monty`. Root tests run on every backend through `eachBackend` unless noted.

| File | Tests | Covers |
|---|---|---|
| cancel_test.go | 4 | context cancellation mid-turn, during a host call and a gathered wait, checkout wait |
| lifecycle_test.go | 11 | `Interrupt` during a host call, with a reason, through `AsyncContext`, while Python runs, on a suspended snapshot; `Go`, `CloseNow`, `Done`, `Err`, `Stats` |
| host_test.go | 10 | `Host` validation, `Stubs`, `Restorable`, host names in feeds, `ExternalLookup` override, stubs under type checking, restore of pinned objects, `LoadSession` refusing unpinned objects; `Expose` (no backend) |
| resource_test.go | 10 | `Unlimited`, `MaxRecursionDepth`, `MaxHostObjects`, `MaxPendingFutures`, `ResourceError`; `MaxPendingBytes` throttling; `Pool.Stats`, `Pool.Shutdown`; `Lines` |
| namedtuple_test.go | 3 | `AsNamedTuple`, `NewNamedTuple`, sandbox round trip |
| telemetry_pool_test.go | 2 | `Options.Telemetry` scoping, empty components record nothing |
| serverinfo_test.go | 7 | `FetchServerInfo` against a stub HTTP server: base path, 404, other statuses, malformed body, header errors, scheme, timeout (no backend) |
| version_test.go | 3 + 11 cases | `BindingVersion` resolution table, user agent (no backend) |
| internal/worker/queue_test.go | 5 | frame queue bound, release, close, oversized frame |
| internal/wire/fields_test.go | 2 | codec field table against `monty.proto` |
| internal/wire/codec_fuzz_test.go | 2 fuzz targets | event decoding against `montypb`, request round trip |
| internal/wire/codec_bench_test.go | 4 benchmarks | decode and encode against `montypb` |
| scripts/version_test.sh, scripts/check_pins_test.sh | shell | `scripts/version.sh` states, pin drift detection (`make test-scripts`) |

## `monty-fs` (`crates/monty-fs/tests`)

`internal/mountfs` ports 242 tests: `fs.rs`, `fs_security.rs`, `mount_confinement.rs`, `mount_escape_repro.rs`, `overlay_stale_ref.rs`. Seven skip: two Windows-only tests, two non-UTF-8 filename tests (APFS refuses such names), two race soaks gated by `MONTY_FS_SOAK` as upstream, and `on_no_handler_includes_errno`, which tests the worker's default rather than the mount table.

## Python WebSocket client (`crates/monty-python/tests/test_websocket.py`, `crates/monty-pool/tests/websocket.rs`)

`websocket_test.go` ports `test_websocket.py` against an in-process relay (`websocket_relay_test.go`, the Go analogue of `scripts/websocket_relay.py`). Seven cases pass. Six skip because Go's types cannot express them: a non-callable header callback, unknown limit keys, and the non-mapping, non-string-key, non-string-value and unencodable-value header results. Three montygo subtests have no upstream counterpart: `wss_through_tls_relay`, `health_check_against_relay` and `dial_context_is_used`.

`internal/pool/websocket_test.go` ports `websocket.rs` against a scripted in-process child: 26 pass, 7 skip. Five of the skips test the Rust raw relay path (`turn_raw`), which the Go pool does not have; one tests redacted `Debug` output; one tests dial-time trace headers, which the root pool injects and the root `trace_context_headers_precede_connect_headers` subtest covers.

## Root suite on the `websocket` backend

`make test-docker` runs the root package with `MONTY_TEST_BACKENDS=websocket` against one `monty-server` container, with the test dump key and the per-client quota disabled. Every root test runs, and subtests carry the backend name `websocket`.

- `openPool` maps `Options` onto `WebSocketOptions`. `RequestTimeout` 0 becomes `NoRequestTimeout`.
- `plRequireMemoryError` checks only the `MemoryError` type on websocket. The server's memory ceiling fires before the allocator abort, so the message is the interpreter's own.
- Pool tests that observe worker identity through the worker pid skip through `plNativeOnly` with `websocket backend: <reason>`, as on wasm.
- The crash recovery test forces a timeout with a 500 ms `RequestTimeout` instead of killing the worker pid, as on wasm.
- A server that cannot be reached fails the test instead of skipping it.

## `tests/network`

A separate module of 56 tests for the server image. They have no upstream test counterpart; upstream `docs/server.md` is the specification, and `docs/parity/server.md` lists the deviations.

| Group | File | Tests |
|---|---|---|
| `TestAdmission_` | `server_admission_test.go` | 5 |
| `TestProtocol_` | `server_protocol_test.go` | 8 |
| `TestLimits_` | `server_limits_test.go` | 5 |
| `TestTimeouts_` | `server_timeouts_test.go` | 4 |
| `TestDumps_` | `server_dumps_test.go` | 6 |
| `TestDrain_` | `server_drain_test.go` | 6 |
| `TestTLS_` | `server_tls_test.go` | 3 |
| `TestParallel_` | `server_parallel_test.go` | 2 |
| `TestTelemetry_` | `server_telemetry_test.go` | 2 |
| `TestRepl_` | `repl_session_test.go` | 5 |
| `TestReplCLI_` | `repl_cli_test.go` | 6 |
| `TestPyClient_` | `pyclient_test.go` | 4 |

- `TestTimeouts_SessionTimeout` and `TestTimeouts_KeepaliveDropsFrozenClient` are slow. They skip unless `MONTYGO_SLOW_TESTS_ENABLE` is set.
- `TestProtocol_RemoteDialByContainerIP` runs only on Linux, where container bridge addresses are routable from the host.
- `TestPyClient_*` skip when the Python client image is absent, as in the `docker-arm64` CI job. They run the `pydantic-monty-client` wheel built at the pin, and the PyPI `0.0.23` client for the protocol refusal.
- Counter assertions compare deltas from a baseline, because default units are reused across tests.

## Python OS helpers (`crates/monty-python/tests/test_os_access*.py`)

Package `osaccess` ports all 207 test functions and runs the Monty-driven ones on both backends:

| Python | Go | Tests |
|---|---|---|
| test_os_access.py | `TestOSAccess` | 139 |
| test_os_access_compat.py | `TestOSAccessCompat` | 41, against the OSAccess runner only; the CPython runner has no Go analogue |
| test_os_access_raw.py | `TestOSAccessRaw`, `TestStatHelpers` | 27 |

Adaptations: `PurePosixPath` checks become `montygo.Path` checks, Python reprs become `String()` or field comparisons, and `exception()` round-trips compare the exception type name with `montygo.IsSubclass`.

## Examples (`examples/`)

The `examples` module ports all 12 upstream example programs and the `monty` CLI REPL. Each sandbox script and stub file is byte-identical to upstream and embedded. Every program has an end-to-end test that runs on both backends (`MONTY_EXAMPLES_BACKEND=native|wasm`).

| Upstream | Go | Notes |
|---|---|---|
| `classes/*.py` (9 programs) | `classes/*` | ported |
| `expense_analysis` | `expense_analysis` | ported, type checked |
| `sql_playground` | `sql_playground` | adapted: SQLite (`modernc.org/sqlite`) replaces DuckDB, with `$name` list parameters rewritten for `IN`. The feed skips type checking by default, because upstream imports its stubs as `type_stubs` and the worker names them `repl_type_stubs`; `-type-check` reproduces the upstream failure. |
| `monty` CLI REPL (`crates/monty-runtime/src/run.rs`) | `repl` | adapted: continuation is read from the syntax error of a trial feed instead of an in-process parse. `TestContinuationModeMatchesUpstream` checks every case of upstream `repl_detects_continuation_mode_for_common_cases` plus more, on both backends. Overlay mounts reset after each snippet, and an interrupt replaces the session. |
| `web_scraper` | `web_scraper` | adapted: chromedp and goquery replace Playwright and BeautifulSoup. Agent mode needs `ANTHROPIC_API_KEY`. `-code` runs the embedded `example_code.py` without the model, and browser tests skip without Chrome. |
