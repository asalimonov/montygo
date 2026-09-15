# Appendix G — Python-only features in scope (A17): in-memory OS helpers and WebSocket client

Sources: `crates/monty-python/python/pydantic_monty/os_access.py`, `_monty.pyi`, `docs/filesystem.md`, `docs/server.md`, `crates/monty-python/src/{pool,exceptions,build}.rs`, `tests/test_os_access*.py`, `tests/test_websocket.py`, `scripts/websocket_relay.py`, `crates/monty-pool/tests/websocket.rs`.

## G.1 `AbstractOS` dispatch contract

23 function names: `Path.exists, Path.is_file, Path.is_dir, Path.is_symlink, open, Path.read_text, Path.read_bytes, Path.write_text, Path.write_bytes, Path.append_text, Path.append_bytes, Path.mkdir, Path.unlink, Path.rmdir, Path.iterdir, Path.stat, Path.rename, Path.resolve, Path.absolute, os.getenv, os.environ, date.today, datetime.now`.

`__call__(name, args, kwargs)` → `dispatch(...)`; a `NotImplementedError` from dispatch becomes `NOT_HANDLED`. `dispatch` routes: `Path.mkdir` → `path_mkdir(path, parents=kwargs.get('parents', False), exist_ok=kwargs.get('exist_ok', False))` (assert ≤ 2 kwargs); `os.environ` → `get_environ()` (no args); `date.today` → `date_today()`; `datetime.now` → `datetime_now(tz)`; unknown name → `NotImplementedError('Unknown OS function: <name>')` → NOT_HANDLED.

Abstract (must implement): `path_exists, path_is_file, path_is_dir, path_is_symlink, path_read_text, path_read_bytes, path_write_text, path_write_bytes, path_mkdir, path_unlink, path_rmdir, path_iterdir, path_stat, path_rename, path_resolve, path_absolute, getenv, get_environ`. Optional, default NOT_HANDLED: `path_open, path_append_text, path_append_bytes`. Optional with host-clock defaults: `date_today() → date.today()`, `datetime_now(tz) → datetime.now(tz)`.

Handler shapes: `path_exists/is_file/is_dir/is_symlink(path) → bool`; `path_open(path, mode) → FileHandle`; `path_read_text(path|handle) → str`; `path_read_bytes → bytes`; `path_write_text(path, data) → int chars`; `path_write_bytes → int bytes`; `path_append_text → int chars`; `path_append_bytes → int bytes`; `path_mkdir(path, parents, exist_ok) → None`; `path_unlink/rmdir(path) → None`; `path_iterdir(path) → list[Path]` (full paths); `path_stat(path) → StatResult`; `path_rename(path, target) → None`; `path_resolve/absolute(path) → str`; `getenv(key, default=None) → str|None`; `get_environ() → dict[str,str]`.

`NOT_HANDLED`: unique sentinel; returning it declines the call; sandbox raises the no-handler default (`PermissionError: Permission denied: '/tmp'`). Resolution order in feed_run: mounts → os handler → default. Monty passes normalized absolute paths; handles are NOT passed to read/write handlers (typed payload carries the path only).

## G.2 `StatResult`

NamedTuple fields in order: `st_mode, st_ino, st_dev, st_nlink, st_uid, st_gid, st_size, st_atime, st_mtime, st_ctime`. `file_stat(size, mode=0o644, mtime=None)`: `mode < 0o1000 → mode |= 0o100000`; mtime default now; `(mode, 0, 0, 1, 0, 0, size, mtime, mtime, mtime)`. `dir_stat(mode=0o755, mtime=None)`: `mode |= 0o040000`; `(mode, 0, 0, 2, 0, 0, 4096, mtime, mtime, mtime)`.

## G.3 Files

`AbstractFile` protocol: `path`, `name`, `permissions`, `deleted`, `read_content() → str|bytes`, `write_content(c)`, `delete()`. Duck-typing: file ⇔ has `path`; directory ⇔ dict.
`MemoryFile(path, content, *, permissions=0o644)`: repr `MemoryFile(path=/test/file.txt, content='...', permissions=420)` (`b'...'` for bytes; permissions decimal). `CallbackFile(path, read, write, *, permissions=0o644)`: `read(path)`, `write(path, content)`; repr `CallbackFile(path=…, read=…, write=…, permissions=…)`.

## G.4 `OSAccess(files=None, environ=None, *, root_dir='/')`

Tree `{'/': {}}` always; relative file paths rebased onto `root_dir` (mutating `file.path`); directories inferred from path components; a file under a file → `ValueError('Cannot put file <repr> within sub-directory of file <repr>')`. Repr `OSAccess(files=[…], environ={…})`.

Helpers: `_get_entry` (None if missing / intermediate is a file); `_get_entry_exists` → `FileNotFoundError('[Errno 2] No such file or directory: <repr path>')`; `_get_file` → `IsADirectoryError('[Errno 21] Is a directory: <repr>')`; `_get_dir` → `NotADirectoryError('[Errno 20] Not a directory: <repr>')`.

| Op | Semantics |
|---|---|
| exists / is_file / is_dir | tree lookup; never raise; `is_symlink` always False |
| open(path, mode) | construct handle first (bad mode → ValueError; `+` → `update modes ('+') are not yet supported`); `r`: must exist and not be dir; `w`: `_write_file(path, empty)`; `a`: create empty if missing, dir → IsADirectoryError |
| read_text / read_bytes | `_get_file`; decode/encode UTF-8 as needed |
| write_text / write_bytes | `_write_file`; returns len (chars / bytes); missing parent → FileNotFoundError; dir → IsADirectoryError |
| append_text / append_bytes | concatenate preserving incoming type; missing → create; dir → IsADirectoryError; returns len(data) |
| mkdir | file at path → FileExistsError; dir → exist_ok ? ok : `FileExistsError('[Errno 17] File exists: <repr>')`; parent dir → create; parent file → NotADirectoryError; parents=True → setdefault chain (file component → NotADirectoryError); else FileNotFoundError |
| unlink | `_get_file` → `file.delete()`; `del parent[file.name]` |
| rmdir | `_get_dir`; non-empty → `OSError('[Errno 39] Directory not empty: <repr>')`; `del parent[name]` |
| iterdir | full paths, insertion order |
| stat | file → `file_stat(size=len(bytes or utf8), mode=permissions)`; dir → `dir_stat()` |
| rename | src missing → `FileNotFoundError('[Errno 2] No such file or directory: <src repr> -> <dst repr>')`; target parent not dir → same; file→dir → `IsADirectoryError('[Errno 21] Is a directory: <src> -> <dst>')`; file over file → target.delete() then replace (moved file `.path` NOT updated); dir→file → `NotADirectoryError('[Errno 20] Not a directory: <src> -> <dst>')`; dir → non-empty dir → `OSError('[Errno 66] Directory not empty: <src> -> <dst>')`; dir move rewrites contained file paths |
| resolve / absolute | absolute → str; relative → `'/' + p` (cwd hard-coded `/`) |
| getenv / get_environ | `environ.get(k, default)`; returns the dict by reference |
| date_today / datetime_now | host clock |

`_write_file`: existing file → `write_content`; dir → IsADirectoryError; parent dir → new `MemoryFile(path, data)` appended to `self.files`; else FileNotFoundError. Permissions are metadata only (no PermissionError from OSAccess). Sandbox wrapper enforces handle modes: writing an `r` handle → `OSError('not writable')`, reading a `w` handle → `OSError('not readable')`.

## G.5 Python OS-helper tests (207 functions; compat ×2 runners = 248 instances)

`test_os_access.py` (139): init & validation (5: non_absolute_path, file_nested_within_file_rejected, empty_initialization, environ_parameter, time_methods_direct_api); existence (11); reading (8: incl. `'/missing.txt'`, `'/missing.bin'`, `'/test/subdir'` messages); writing via Monty (5) and direct (6); appending (2: non-ASCII char count 3 vs byte count 6); open via Monty (14: read text/bytes, missing `'/data/missing.txt'`, directory `'/data/inner'`, w truncates/creates, write returns 11, a preserves/creates, char count, wb byte count, `not writable`, `not readable`, keyword args); open direct (6: handle props `(False, True, False)`, `rt`→`r`, `+` modes rejected, w truncates, r missing, invalid mode `('wxyz','axyz','w!','a?')` no side effect); mkdir via Monty (5) and direct (7 incl. `'/test/file.txt/subdir'` NotADirectory); rmdir via Monty (4) and direct (4); iterdir (4); unlink via Monty (3) and direct (3); stat (6: `(11, 0o644)`, `0o755`, dir `0o040755`, missing, bytes size 5, `'☃'` size 3); rename via Monty (3) and direct (6 incl. Errno 66 and path rewrite); path resolution (4); environment (15: getenv/environ dict/KeyError `KEY`/get/len/contains/keys/values/items/empty); MemoryFile (7); CallbackFile (4); custom AbstractFile (2); direct API (1); edge cases (4: root dir, empty file, 10-level nesting, special chars).
`test_os_access_compat.py` (41 × monty/cpython runners): existence (5), reading (3), tree (3), stat (2), iterdir (1), FileNotFoundError (4), IsADirectoryError (2), NotADirectoryError (1), FileExistsError (3), mkdir parent (2), unlink (2, `unlink_is_directory` accepts IsADirectoryError|PermissionError), rmdir (3), rename (1), writes (5), environ (4, KeyError str is the repr-quoted key).
`test_os_access_raw.py` (27): custom `TestOS(AbstractOS)` with flat dict + frozen clock (`date(2024,1,15)`, `datetime(2024,1,15,10,30,5,123456)`): basic (13: exists, missing, date_today, datetime_now tz, dispatch, dispatch not handled → NOT_HANDLED, NOT_HANDLED falls back to `Permission denied: '/tmp'`, is_file, is_dir, read_text, read_text missing `'FileNotFoundError: No such file: /missing.txt'` with `exception()` type round-trip, read_bytes, open passes path (not handle) to read handler); stat (3: `(11, 0o100644)`, `0o040755`, missing with display traceback `File "<python-input-0>", line 2, in <module>`); iterdir (2); resolve/absolute/getenv (5); helpers (2); path marshalling (2: sandbox Path → host `PurePosixPath`; host `PurePosixPath` → sandbox `PosixPath('/foo/bar/thing.txt')`).

## G.6 WebSocket client (`AsyncMontyWebsocket`)

`AsyncMontyWebsocket(url, *, max_processes=None, checkout_timeout=None, request_timeout=10.0, connect_headers=None)`. URL dialed verbatim; `min_processes` 0; connections single-use; `checkout()` takes the standard kwargs; the connection is opened when the session is entered. No sync counterpart.
`connect_headers`: zero-arg callable → mapping str→str; called once per session on the checking-out task **before** capacity wait and dial; non-callable at construction → `TypeError("'dict' object is not callable")`; non-mapping → `TypeError("connect_headers must return a mapping of str to str, got 'list'")`; bad key/value type → `… got 'int' header name` / `… got 'int' header value`; malformed header → `RuntimeError('failed to spawn monty worker: <url>: connect header "bad header": invalid HTTP header name')` / `… connect header "x-token" value: failed to parse header value`; inactive pool → `RuntimeError('the pool is not active — enter the Monty / AsyncMonty context manager first')` (callback not called); callback exception propagates, pool stays usable; duplicates last-wins incl. over `User-Agent` (`monty-pool/<version>`) and telemetry `traceparent`/`tracestate`; debug repr redacts values.
Errors: `MontyDisconnectError` (RuntimeError-typed message, no attrs; any drop other than shutdown; retry on a fresh session); `MontyShutdown{dump: bytes|None}` (request did not run; restore via load_session/load_snapshot; a suspension-answering request re-runs the callback).
`checkout(limits={'max_memroy': …})` → `ValueError("unknown limits key 'max_memroy'; accepted keys are 'max_duration_secs', 'max_memory', 'gc_interval', 'max_recursion_depth', 'max_suspensions'")`.

Server (`docs/server.md`, Full Monty): one worker per connection; `GET /` info, `GET /health`; flags `--host --port --monty-bin --max-sessions 64 --max-sessions-per-client 10 --idle-timeout 60 --keepalive 5 --session-timeout 3600 --turn-timeout 300 --drain-grace 30 --max-memory-mib 64 --max-duration 60 --max-recursion-depth 1000 --trust-forwarded-for --dump-key --logfire-token`; capacity rejections at upgrade (503 global, 429 per client); limits are ceilings; drain → `MontyShutdown` on next request.

## G.7 Test relay (`scripts/websocket_relay.py`) — protocol to reimplement in Go

Accept a WebSocket; spawn `monty subprocess` per connection (stdin/stdout pipes); pump WS→child by prepending the 4-byte LE length; child→WS by reading the 4-byte length then the body and sending one **binary** message; `str` messages accepted and encoded; races both pumps, treats `IncompleteReadError`/`ConnectionError`/`ConnectionClosed` as clean; closes child stdin when WS ends; kills child on teardown; `max_size=None`. Prints one line `ws://<host>:<port>` (wildcard bind mapped to `127.0.0.1`/`[::1]`), no path. CLI `--host 127.0.0.1 --port 8799 (0 = ephemeral) --monty-bin` (explicit → `$MONTY_BIN` → PATH → `monty`).

Rust mock (`monty-pool/tests/websocket.rs`): tungstenite server on `127.0.0.1:0`; one protobuf message per binary frame (no length prefix); `Feed` → `Complete(Int 42)`, everything else → `Ok`; header-capturing variant via `accept_hdr`; assertions: `pid() == None`; close frame sent on finish / drop (detached task) / timeout; server close while idle → next turn `Disconnected` (never `Crashed`); pings answered while idle (background reader); `ShutdownDump` on Feed → `Shutdown{dump}` and feed did not run; shutdown during suspension re-announces the call after restore; shutdown before a session → `checkout()` fails with `Shutdown{None}`; oversize raw load keeps budget; suspension limit enforced; cancelled finish does not leak capacity.

## G.8 `test_websocket.py` (10 functions, 16 collected)
`test_feed_run_over_websocket` (1+1, state persists; request_timeout 30); `test_inputs_and_async_external_function_over_websocket` (`await double(n) + 1 == 41`); `test_separate_checkouts_are_isolated` (`name 'leaked' is not defined`); `test_connect_headers_sent_per_checkout` (2 concurrent checkouts, traceparent values `['00-aaa-111-01','00-bbb-222-01']`); `test_connect_headers_accepts_any_mapping` (`x-token: t`); `test_connect_headers_not_callable`; `test_connect_headers_failure_leaves_the_pool_usable`; `test_connect_headers_not_called_on_an_inactive_pool`; `test_connect_headers_errors_raise_on_entry` (7 cases above + surrogate `UnicodeEncodeError`); `test_checkout_rejects_unknown_limits`.
