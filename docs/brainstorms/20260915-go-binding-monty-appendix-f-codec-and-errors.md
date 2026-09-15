# Appendix F — Value conversion and error-layer contract (from `monty-js/src/{convert,exceptions,limits,pool}.rs`, `monty-types`, `monty-proto/src/convert`)

Observable behaviour the Go port must reproduce (with Go analogs where JS-specific).

## F.1 Host → MontyObject

Dispatch (JS order; Go analog in brackets):
- `null/undefined` → None [`nil`, typed nil pointer]. `boolean` → Bool. `string` → Str.
- `number`: `Int` iff `fract == 0 && n >= -2^63 && n < 2^63` (half-open; `+2^63` → Float; `-2^63` → Int; NaN/±Inf → Float; `-0.0` → Int 0). No 2^53 boundary on input. [Go: integer kinds → Int/BigInt exactly; `float64` follows the same rule when converting a float to a Python value — i.e. a Go `float64` always maps to Float (Go has real ints); keep the rule only for `json.Number`-like cases if ever needed.]
- `bigint` → Int if fits i64 else BigInt (sign + magnitude). [Go `*big.Int` same.]
- `function` → `Function{name || "<anonymous>"}` (not rejected) — used for name-lookup resolution. [Go: `Function`/func → Function{name}.]
- `Symbol`/`External` → `Cannot convert JS Symbol to Monty value` / `Cannot convert JS External to Monty value`. [Go: unsupported kinds → `Cannot convert Go <type> to Monty value`; struct values → `Cannot convert <Type> instance to a Monty value — wrap it in ClassInstance(...)` (TS `prepare` message).]
- Object order: Buffer/Uint8Array → Bytes; `Map` → Dict (insertion order, any key type, **no hashability check host-side**: the worker rejects with `unhashable dict keys` / `unhashable set element` / `unhashable frozenset element` / `unhashable class instance attr keys` / `unhashable class attr keys`); `Set` → Set (never FrozenSet); Array → Tuple if `__tuple__` truthy else List (holes → None); `__monty_type__` string marker → marker dispatch; plain object → Dict with string keys.
- Markers: `Ellipsis`, `NotImplemented`, `Exception{excType, message}` (`arg = None` iff message == ""; unknown type → `Unknown exception type: <name>`), `Date{year i32, month u8, day u8}`, `DateTime{…, offsetSeconds?, timezoneName?}`, `Time{…, fold? default 0}`, `TimeDelta{days, seconds, microseconds}`, `TimeZone{offsetSeconds, name?}`, `Type` (with `classType` object → host/sandbox type; else `value` builtin name → `unknown type name "<v>"` on failure), `BuiltinFunction{value}` → **`Repr("<built-in function <value>>")`**, `FileHandle{path, mode, position}`, `ClassInstance{type, instanceId, attrs}`; unknown → `Unknown Monty marker type: <t>`.
- `classType` object: `name`, `id` (canonical uuid else `ClassType id must be a canonical uuid string, got "<v>"`), `hostDefined`, `isDataclass`, `attrs` ([name, value] pairs; errors `ClassType attrs entries must be [name, value] pairs`, `ClassType attr name must be a string`, `ClassType attr value missing`; same with `ClassInstance` prefix for instance attrs). `instanceId` bad → `ClassInstance instanceId must be a canonical uuid string, got "<v>"`.
- FileHandle: `MontyFileHandle path must be a string`, `MontyFileHandle mode must be a string`, mode parse errors (`must have exactly one of create/read/write/append mode`, `exclusive creation mode is not supported`, `invalid mode: binary mode specified twice`, `invalid mode: text mode specified twice`, `update modes ('+') are not yet supported`, `invalid mode: 'q'`, `can't have text and binary mode at once`, `Must have exactly one of create/read/write/append mode and at most one plus`), `MontyFileHandle position must be a non-negative safe integer` (finite integral in [0, 2^53−1]).

Builtin type names accepted by `Type{value}`: ellipsis, type, NoneType, bool, int, float, range, slice, datetime.date, datetime.datetime, datetime.timedelta, datetime.timezone, datetime.time, str, bytes, list, collections.deque, list_iterator, callable_iterator, tuple, namedtuple, dict, dict_keys, dict_items, dict_values, set, frozenset, function, builtin_function_or_method, cell, iterator, coroutine, module, _io.TextIOWrapper, _io.BufferedReader, _io.BufferedWriter, _io.BufferedRandom, typing._SpecialForm, PosixPath, property, re.Pattern, re.Match, tuple_iterator, str_ascii_iterator, str_iterator, bytes_iterator, range_iterator, dict_keyiterator, dict_itemiterator, dict_valueiterator, set_iterator, itertools.{count,repeat,pairwise,compress,islice,chain,cycle,takewhile,dropwhile,filterfalse,starmap,accumulate,batched,zip_longest}, Field, NotImplementedType, _DataclassParams, object, functools.partial, types.GenericAlias, typing.Union, plus every exception name.

### Depth (`exceeds_max_value_depth`)
Budget 97 (`PROST_RECURSION_LIMIT 100 − FRAME_WRAPPER_DEPTH 3`); costs list/tuple/set/frozenset/namedtuple 2, dict 3, class instance 4 (+3 for its type branch), bare Type 2, type attrs 2; scalar exceeds iff budget == 0. `MAX_VALUE_DEPTH = 48`. Call sites: feed inputs and `resumeNameLookup(value)` → **reject the call** with `Max input depth exceeded`; resume return / lazy attr / future results → in-sandbox `RuntimeError("Max input depth exceeded")`; a conversion failure there → in-sandbox `TypeError(<reason>)`.

## F.2 MontyObject → Host

None→null[nil]; Ellipsis/NotImplemented → markers [sentinels]; Bool; Int → number if `-2^53 <= i <= 2^53` (inclusive) else BigInt [Go: always `int64`; BigInt arm → `*big.Int`]; Float (NaN/Inf preserved); Str; Bytes → Buffer copy [`[]byte`]; List → Array [`[]any`]; Tuple → Array + non-enumerable writable configurable `__tuple__` [`Tuple`]; **NamedTuple → same as Tuple (type/field names dropped)** [Go: `NamedTuple` struct — an improvement, documented]; Dict → Map in order [`*Dict`]; Set/FrozenSet → Set [`*Set` / `*FrozenSet` — improvement]; Exception → marker (`message: arg ?? ''`); Date/DateTime/Time/TimeDelta/TimeZone → markers with optional fields **omitted** when absent (`fold` always set); Type instance → `{__monty_type__:'Type', classType}`; other Type → `{value: name}`; BuiltinFunction → marker `{value}`; ClassInstance → marker `{type, instanceId, attrs: [[name, value]…]}` (non-string attr keys silently dropped); Path → plain string [`Path`]; FileHandle → frozen object (`path`, `mode` canonical, `position` ≤ 2^53−1 else `MontyFileHandle position exceeds JavaScript's maximum safe integer`; hidden `__monty_type__`, `binary`, `readable`, `writable`); Repr → string; Cycle → placeholder string [`Cycle`]; Function → name string.

## F.3 Turn objects (`NativeTurn`)

`complete{value}`; `functionCall{allowEagerAwait, functionName, args, kwargs: [k,v][], callId, objectId: string|null}`; `osCall{functionName, args, kwargs, callId}`; `nameLookup{name, objectId}`; `resolveFutures{pendingCallIds}`; `error{exception}`; `typingError{diagnostics}`; `crashed{message, timedOut, exitStatus?}`; `protocol{message}`; `loaded`; `ok`; `notMounted`; plus `callbackSpanKey = "<trace_id>:<span_id>"` when a valid span context exists.

OS call names and args (`OsFunctionCall::name()`, paths normalised first):

| name | args | kwargs |
|---|---|---|
| `Path.exists`, `Path.is_file`, `Path.is_dir`, `Path.is_symlink`, `Path.read_text`, `Path.read_bytes`, `Path.stat`, `Path.iterdir`, `Path.resolve`, `Path.absolute`, `Path.unlink`, `Path.rmdir` | `[Path]` | — |
| `Path.write_text`, `Path.append_text` | `[Path, Str data]` | — |
| `Path.write_bytes`, `Path.append_bytes` | `[Path, Bytes data]` | — |
| `open` | `[Path, Str mode]` | — |
| `Path.mkdir` | `[Path]` | `parents: Bool, exist_ok: Bool` (in that order) |
| `Path.rename` | `[Path src, Path dst]` | — |
| `os.getenv` | `[Str key, default]` | — |
| `os.environ`, `date.today` | `[]` | — |
| `datetime.now` | `[TimeZone]` or `[None]` | — |

No-handler defaults (`on_no_handler`): FS ops → `PermissionError: Permission denied: '<path repr>'`; non-FS → `RuntimeError: '<name>' is not supported in this environment`. `Path.stat` result: `NamedTuple("StatResult", st_mode, st_ino, st_dev, st_nlink, st_uid, st_gid, st_size (Int), st_atime, st_mtime, st_ctime (Float))`.

`NativeFutureResult{callId, ok, value?, excType?, message?}`: missing → `missing required field callId|ok|excType|message`; `ok` without value → `Return(None)`; `excType` unknown → `RuntimeError`.

## F.4 Exceptions

`NativeException{excType, message ("" when absent), traceback (rendered), frames[]}`; `NativeFrame{filename, line, column, endLine, endColumn, frameName?, previewLine?, hideCaret, hideFrameName}`.

Rendering (`MontyException` Display = `display('traceback')`):
1. Header `Traceback (most recent call last):\n` only when frames non-empty.
2. Frames with identical `(filename, start.line, frame_name)` runs collapsed: first 3 printed, then `  [Previous line repeated {n-3} more times]\n`.
3. Summary `{exc_type}: {message}` when message is Some (even `""` → `Type: `), else `{exc_type}`; **no trailing newline**.

Frame: `  File "{filename}", line {line}` + (unless `hide_frame_name`) `, in {frame_name or <module>}`; no preview → `\n`; multi-line preview (`start.line != end.line`) → `\n` + each block line as `    {line}\n`, no caret; single line → `\n    {trimmed}\n` then, unless `hide_caret`, caret row: `leading = len(line) - len(trimmed)` (bytes); `caret_start = start.column > leading ? 4 + start.column - leading - 1 : 4`; `caret_len = max(end.column - start.column, 1)`; `" "*caret_start + "~"*caret_len + "\n"`. Only `~`, never `^`. No chained-exception rendering.

`display('type-msg')` → `Type` when message empty else `Type: msg`; `display('msg')` → message; invalid → `Invalid display format: '<f>'. Expected 'traceback', 'type-msg', or 'msg'` (Rust) / TS base: `Invalid display format: '<f>'. Expected 'type-msg' or 'msg'`.

`PoolError` → turn: `Runtime` → error; `Typing` → typingError; `Timeout` → crashed `timedOut: true`; `Crashed{status}` → crashed with `exitStatus = status.to_string()` (e.g. `exit status: 1`, `signal: 9 (SIGKILL)`); `Disconnected`/`Shutdown` → crashed; others → protocol. Display strings as in Appendix D. `{timeout:?}` is Rust `Duration` Debug: `500ms`, `1s`, `1.5s`, `2.5ms`, `750µs`, `100ns`, `60s` — Go must implement this formatter (not `time.Duration.String()`).

TS error classes: `MontyError.message = type + ": " + msg` or type; `MontyCrashedError.typeName = 'RuntimeError'`; `MontyTypingError.typeName = 'TypeError'`, inner message = first diagnostics line; `MontyRuntimeError.display` default `'traceback'`, `MontySyntaxError.display` default `'msg'`; `ProtocolError` is a plain Error.

Exception hierarchy (`is_subclass_of`): BaseException ⊃ all; Exception ⊃ all except BaseException/KeyboardInterrupt/SystemExit; LookupError ⊃ {KeyError, IndexError}; ArithmeticError ⊃ {ZeroDivisionError, OverflowError}; RuntimeError ⊃ {RecursionError, NotImplementedError}; AttributeError ⊃ {FrozenInstanceError}; NameError ⊃ {UnboundLocalError}; ValueError ⊃ {UnicodeDecodeError, UnicodeEncodeError, json.JSONDecodeError, binascii.Error, io.UnsupportedOperation}; ImportError ⊃ {ModuleNotFoundError}; OSError ⊃ {FileNotFoundError, FileExistsError, IsADirectoryError, NotADirectoryError, PermissionError, io.UnsupportedOperation, TimeoutError}.

## F.5 OOM

`poison` → reap with 100 ms grace → exit code 65 → `Runtime(MemoryError("the worker exceeded its memory limit and was terminated"))`, worker gone; the *next* call yields protocol `this checkout has already been finished`. Host print cap: `MemoryError: memory limit exceeded: <used> bytes > <max> bytes`.

## F.6 Limits validation (`limits.rs`)

Order: maxRecursionDepth, maxDurationSecs (`Duration::try_from_secs_f64` errors verbatim), maxMemory, gcInterval, maxSuspensions. Numeric checks in order: `!finite` → `<name> must be a finite number`; `< 0` → `<name> must be non-negative`; `fract != 0` → `<name> must be an integer`; `> 9007199254740991` → `<name> must be a safe integer (<= 9007199254740991)`. [Go: typed `uint64` fields make most checks compile-time; keep `MaxDuration < 0` → error.]

## F.7 Pool/session strings (`pool.rs`)

`maxProcesses must be at least 1`; `minProcesses cannot exceed maxProcesses`; `the pool is not started — create it with Monty.create()`; closed session turn → protocol `the session is closed — check out a new one`; `mount is closed: create a new MountDir`; `invalid mount mode: '<m>'`; `<name> must be a non-negative integer below 2**64` (writeBytesLimit, memoryUsageLimit); `invalid <name>: <err>` (checkoutTimeout, requestTimeout, durationLimitGrace, printFlushInterval); `too many arguments|kwargs`; `traceback too deep`; dump with no worker → `this checkout has already been finished`; `RangeError("unknown typeCheckFormat '<f>', expected one of: full, concise, azure, json, jsonlines, rdjson, pylint, gitlab, github")`; `RangeError('assertMessageAnnotations must be a boolean or an integer between 1 and 2**32 - 1')`.

Print delivery: callback awaited per segment inside the turn (ordering + backpressure); callback errors captured host-side. `workerPid` never blocks (try-lock). Checkout mutex held for a whole turn.

## F.8 Round-trip hazards

NamedTuple→Tuple, Path→string, Repr/Cycle/Function→string, FrozenSet→Set in TS (Go improves the first and last two with dedicated types — document); `BuiltinFunction` marker in → `Repr`; int boundaries asymmetric in TS (Go: exact `int64`/`*big.Int`); kwargs are pair arrays; depth checked after conversion with site-specific failure modes; carets `~` only; empty traceback → no header; `Some("")` message renders `Type: `.
