# Appendix E — Host-side mount table specification (port of `monty-fs`)

Sources: `crates/monty-fs/{README.md,src/*.rs,tests/*.rs}`, `crates/monty-types/src/{os,virtual_path,file_mode,format,exceptions,object}.rs`, `crates/monty/src/types/file.rs`, `crates/monty-pool/src/checkout.rs`. Go package: `internal/mountfs`.

## E.0 Architecture

The sandbox performs no I/O. It suspends with an `OsCall`; the host's `MountTable.HandleOsCall(call)` returns `Handled(value | error)` or `NotHandled(call)`. Errors convert to sandbox exceptions. Confinement is structural: every mount holds a directory **descriptor** opened once at mount time; all operations are relative to it (Go: `os.OpenRoot`, Go ≥ 1.24). Path arithmetic alone is not the boundary.

## E.1 Virtual path normalisation and path-security policy

### E.1.1 Lexical normalisation
```
normalize(path):
  if is_normalized(path): return path
  out = ""
  for seg in split(path, "/"):
    if seg == "" or seg == ".": skip
    elif seg == "..": out = out[:lastIndexOf(out, "/")]   // 0 if none
    else: out += "/" + seg
  return out == "" ? "/" : out
is_normalized(p) := p == "/" || (p starts with "/" && !p ends with "/" && no segment after the first is ""|"."|"..")
```
`/mnt/subdir/../hello.txt`→`/mnt/hello.txt`; `/mnt/./subdir`→`/mnt/subdir`; `/mnt/`→`/mnt`; `""`→`/`; `..`,`/..`→`/`; `data`→`/data`; `/mnt\hello.txt` unchanged. `validate_cwd`: NUL → `cwd must not contain NUL bytes: {cwd:?}`; not absolute → `cwd must be an absolute POSIX path: {cwd:?}`; trim trailing `/` (root stays). Raised as `ValueError`.

### E.1.2 Length / depth (`reject_overlong_path`) — measured on the path **as sent**
`PATH_MAX = 4096` bytes, `NAME_MAX = 255` bytes, `DEPTH_MAX = 64` components, `ERROR_PATH_EDGE = 20` chars. `too_long = len(path) > 4096 || any(len(c) > 255 for first 64 components) || 65th component exists`. Error: `OSError: [Errno 36] File name too long: {repr(elided)}` where `elide` keeps 20 chars at each end joined by `…` when the path has ≥ 41 chars.

### E.1.3 NUL bytes → `ValueError(msg)`
| OsCall | message |
|---|---|
| `Path.mkdir` | `mkdir: embedded null character in path` |
| `Path.unlink` | `unlink: embedded null character in path` |
| `Path.rmdir` | `rmdir: embedded null character in path` |
| `Path.stat` | `stat: embedded null character in path` |
| `Path.iterdir` | `scandir: embedded null character in path` |
| `Path.resolve` | `lstat: embedded null character in path` |
| `Path.rename` src / dst | `rename: embedded null character in src` / `…in dst` |
| others (`read_*`, `write_*`, `append_*`, `open`, `absolute`) | `embedded null byte` |
| `exists`, `is_file`, `is_dir`, `is_symlink` | never raise → `False` |

### E.1.4 Order of checks in `HandleOsCall`
1. No FS primary path (`os.getenv`, `os.environ`, `date.today`, `datetime.now`) → `NotHandled`.
2. Length check on the primary path first (O(1)); length error wins over NUL.
3. NUL check.
4. On error: existence predicate → `Handled(Ok(False))`; else `Handled(Err)` — **even when no mount covers the path**.
5. Routing (E.2). `rename` repeats 2+3 on `dst` with the `dst` wording.

### E.1.5 Drive / UNC / backslash segments (after prefix stripping, all modes)
Relative remainder containing `\`, or any segment matching `^[A-Za-z]:` → `PathEscape{normalized}` → `PermissionError: [Errno 13] Permission denied: {repr}`. Applies to predicates too (they raise here). `::double.txt`, `note:2026.txt` are fine. A backslash before the mount boundary is a routing miss → `NotHandled`. On a read-only mount the read-only gate runs first.

### E.1.6 Mount-relative resolution
```
resolve_virtual_path(vpath, mount_vpath):
  reject_null_bytes(vpath)                       // "embedded null byte"
  n = normalize(vpath)
  rel = strip_mount_prefix(n, mount_vpath) else NoMountPoint(vpath)
  reject_drive_or_unc_segments(rel, n)
  return rel                                     // "" = mount root ("." for dir ops)
strip_mount_prefix(n, m): m == "/" ? n[1:] : n == m ? "" : n.trimPrefix(m).trimPrefix("/") (only if hasPrefix)
```

### E.1.7 Symlink policy
- Direct modes: in-mount relative targets followed; **absolute targets refused even when inside**; climbing relative targets refused; dangling outside refused for reads and writes. Refusal = `PathEscape` (descriptor's synthetic PermissionDenied with no errno) → `PermissionError: [Errno 13] Permission denied: {repr(vpath)}` — identical text to a real EACCES. Messages never contain host paths.
- Overlay: refuses symlinks outright. `resolve_real = resolve_virtual_path + reject_symlink_chain` (walk every component with `lstat`: Symlink → PathEscape; Absent/File → stop OK; Dir → descend). Not atomic (TOCTOU bounded by the descriptor). Component walk descends a directory handle per level.
- `iterdir` filter (all modes): for symlink entries do a path-based `stat` through the descriptor of the **raw** name; on error skip. Outbound/broken links invisible; inbound listed.
- `is_symlink`: direct → `lstat(rel).is_symlink()` (errors → false; an outbound link answers True while `exists`/`is_file` answer False). Overlay → any overlay entry → false; else resolve + parent chain link-free + `lstat`; never raises.
- `resolve`/`absolute` never touch the FS: both return `Path(normalize(input))`; differ only in NUL message.
- `unlink`/`rename` act on the link entry.

## E.2 Routing

Mount: `{virtual_path (normalized), host_path (canonical, diagnostics), dir (descriptor), mode, write_bytes_used, write_bytes_limit?, memory_usage_limit (100_000_000)}`.
Creation: non-absolute virtual path → `InvalidMount("virtual path must be absolute, got: '{vp}'")`; open host dir **first** (failure → `InvalidMount("cannot open host path '{display}': {e}")`); canonicalise after (`InvalidMount("cannot resolve host path '{display}': {e}")`). `InvalidMount` → `TypeError`. macOS: search-only dirs fail to open.
Ordering: sorted by `len(virtual_path)` descending, insertion at first position where the existing entry is not strictly longer; first match wins (longest prefix).
Match: `mount == "/" || n == mount || (hasPrefix(n, mount) && n[len(mount)] == '/')`.
Rename: `src_index`; length + NUL on dst (`Handled(Err)`); `(None, None)` → `NotHandled`; same mount → execute; else `CrossMountRename{src,dst}` → `OSError: [Errno 18] Invalid cross-device link: {repr(src)} -> {repr(dst)}` (original spellings).
Uncovered FS path → `NotHandled`; host default (`on_no_handler`): FS → `PermissionError: Permission denied: {repr(normalized)}` (**no `[Errno 13]`**, empty path stays empty); non-FS → `RuntimeError: '{name}' is not supported in this environment`.
Read-only gate (before backend): writes = `WriteText|WriteBytes|AppendText|AppendBytes|Mkdir|Unlink|Rmdir|Rename|Open(mode.create())` (`w`,`w+`,`a`,`a+`); on ReadOnly → `PermissionError: [Errno 30] Read-only file system: {repr(primary path, original spelling)}` (rename → src).

## E.3 Per-operation contract

Values: `stat` → `NamedTuple("StatResult", st_mode, st_ino, st_dev, st_nlink, st_uid, st_gid, st_size, st_atime, st_mtime, st_ctime)`: file `0o100644`, dir `0o040755`; ino/dev/uid/gid 0; nlink 1 (file) / 2 (dir); size real (saturating i64) / **4096** for dirs; atime=mtime=ctime = mtime seconds float (0.0 if unavailable). Real permission bits never reported. `iterdir` → `List[Path]` of `format_child_path(request_path_as_supplied, name)` (`parent` ends with `/` ? `parent+child` : `parent+"/"+child`); order: overlay keys lexicographic then host order. `open` → `FileHandle{normalize(path), mode, 0}`.

### E.3.1 Direct backend
| OsCall | Result | Failures |
|---|---|---|
| exists / is_file / is_dir | Bool via stat (follows links) | PathEscape on drive/UNC; else never raises |
| is_symlink | Bool via lstat | same |
| read_text | Str (UTF-8) | read pipeline; invalid UTF-8 → UnicodeDecodeError |
| read_bytes | Bytes | read pipeline |
| stat | StatResult | missing → `FileNotFoundError: [Errno 2] No such file or directory: {repr}` |
| iterdir | List[Path] | see budgets |
| resolve / absolute | Path(normalize) | policy only |
| unlink | None | unlinkat errors mapped; no root guard |
| rmdir | None | root → PathEscape; `DirectoryNotEmpty` → `OSError: [Errno 39] Directory not empty: {repr}` |
| write_text / write_bytes | Int(chars) / Int(bytes) | write pipeline |
| append_text / append_bytes | Int(chars) / Int(bytes) | append pipeline |
| mkdir | None | table below |
| rename | None | root on either side → PathEscape; renameat errors with **src** path |
| open | FileHandle | table below |

Read pipeline: `reject_non_regular(rel)` (dir → `IsADirectoryError: [Errno 21] Is a directory: {repr}`; FIFO/socket/device → `PermissionError: [Errno 13] Permission denied: {repr}`; missing passes) → open `O_RDONLY|O_NONBLOCK` → handle metadata re-check (authoritative) → `budget.check(meta.len)` → read at most `available+1` → `budget.check(bytes_read)`.
Write pipeline: `check_write_limit(len)` (before path resolution) → resolve → reject_non_regular → open write|create|truncate (+O_NONBLOCK) → re-check → write_all → `commit_write_bytes(len)`. Append: same with create|append.
mkdir: `parents=true`: existing dir → `exist_ok ? None : FileExistsError: [Errno 17] File exists: {repr}`; existing file → FileExistsError always; absent → mkdir -p. `parents=false`: mkdirat; AlreadyExists → `exist_ok && is_dir ? None : FileExistsError`; missing parent → `FileNotFoundError: [Errno 2] …`; parent is file → `NotADirectoryError: [Errno 20] Not a directory: {repr}`.
open: `r/rb` → stat (missing → FileNotFoundError) then reject_non_regular; `w/wb` → `check_write_limit(0)`, truncate/create, `commit(0)`; `a/ab` → append zero bytes (create if missing), no write-limit check.

### E.3.2 Error → exception (complete)
| MountError | ExcType | Message |
|---|---|---|
| NoMountPoint(path) | PermissionError | `[Errno 13] Permission denied: {repr}` |
| PathEscape{vpath} | PermissionError | `[Errno 13] Permission denied: {repr}` |
| EmbeddedNullByte(msg) | ValueError | `{msg}` |
| ReadOnly(path) | PermissionError | `[Errno 30] Read-only file system: {repr}` |
| CrossMountRename{src,dst} | OSError | `[Errno 18] Invalid cross-device link: {repr(src)} -> {repr(dst)}` |
| Io NotFound | FileNotFoundError | `[Errno 2] No such file or directory: {repr}` |
| Io AlreadyExists | FileExistsError | `[Errno 17] File exists: {repr}` |
| Io PermissionDenied | PermissionError | `[Errno 13] Permission denied: {repr}` |
| Io IsADirectory | IsADirectoryError | `[Errno 21] Is a directory: {repr}` |
| Io NotADirectory | NotADirectoryError | `[Errno 20] Not a directory: {repr}` |
| Io DirectoryNotEmpty | OSError | `[Errno 39] Directory not empty: {repr}` |
| Io InvalidFilename | OSError | `[Errno 36] File name too long: {repr}` |
| Io other | OSError | `{err}: {repr}` |
| InvalidUtf8 | UnicodeDecodeError | below |
| InvalidMount(msg) | TypeError | `{msg}` |
| WriteLimitExceeded(limit) | OSError | `disk write limit of {pretty(limit)} exceeded` |
| MemoryUsageLimitExceeded(limit) | MemoryError | `mount memory usage limit of {pretty(limit)} exceeded` |

Errnos are hardcoded POSIX values. `repr(s)` = CPython string repr (`'` unless the string has `'` and no `"`; escapes `\\`, `\n`, `\t`, `\r`, quote; non-printable → `\xNN`/`\uNNNN`/`\UNNNNNNNN`).
UTF-8 failure: `start = valid_up_to; end = start + error_len (or len)`; reason `unexpected end of data` (no error_len) / `invalid continuation byte` (0xC2 ≤ b ≤ 0xF4) / `invalid start byte`; message `'utf-8' codec can't decode byte 0x{b:02x} in position {start}: {reason}` when `end-start == 1`, else `'utf-8' codec can't decode bytes in position {start}-{end-1}: {reason}`; structured data `{encoding:"utf-8", object: bytes, start, end, reason}` omitted when the file > 64 KiB.

### E.3.3 Overlay backend
`relative_path` = reject NUL → normalize → strip prefix → reject drive/UNC (without symlink walk; `resolve_real` adds it).

| OsCall | overlay hit | fall-through |
|---|---|---|
| exists | File/RealFileRef/Directory → true; Deleted → false | policy errors raise; symlink refusal / lookup failure → false; else stat |
| is_file / is_dir | by entry kind | as above then stat |
| is_symlink | false | E.1.7 |
| read_text/read_bytes | File → budget check then content; RealFileRef → re-validate chain then host read; Directory → IsADirectoryError; Deleted → FileNotFoundError | resolve_real → host read with residual budget |
| stat | File → file_stat(len, mtime); RealFileRef → re-validate, file_stat(size, mtime); Directory → dir_stat; Deleted → FileNotFoundError | resolve_real → host stat |
| iterdir | Directory → overlay merge; File/RealFileRef → NotADirectoryError; Deleted → FileNotFoundError | resolve_real; real dir → merge; missing → FileNotFoundError; non-dir → NotADirectoryError |
| unlink | File/RealFileRef → insert Deleted; Directory → IsADirectoryError; Deleted → FileNotFoundError | ensure_parent_exists → resolve_real → classify: File → Deleted; Dir → IsADirectoryError; Symlink → PathEscape; Absent → FileNotFoundError |
| rmdir | root → PathEscape; Directory → non-Deleted descendants → `[Errno 39]`; else Deleted; File/RealFileRef → NotADirectoryError; Deleted → FileNotFoundError | ensure_parent_exists → resolve_real; not dir → NotADirectoryError/FileNotFoundError; real children (not tombstoned) and overlay children must be empty; insert Deleted |

Write (`write_text`/`write_bytes`): `check_write_limit(bytes)` → relative_path → `ensure_parent_exists` → `reject_directory_target` → `check_file_replacement(rel, len, limit)` → insert `File{content, mtime: now}` → commit → Int(chars|bytes).
`ensure_parent_exists`: for each parent prefix: overlay Directory OK; other overlay entry → FileNotFoundError; none: real Dir OK, Symlink → PathEscape, File/Absent → FileNotFoundError.
`reject_directory_target`: overlay Directory → IsADirectoryError; other overlay entry OK; none → resolve_real then classify: Dir → IsADirectoryError, Symlink → PathEscape, File/Absent OK. Writing over any symlink → PermissionError.
Append: relative_path → ensure_parent_exists → reject_directory_target; `existing_len` (File len; Deleted 0; RealFileRef re-validated size; Directory → IsADirectoryError; none → stat size or 0); `charged = (limit set && target not overlay File) ? existing_len + len : len`; `check_write_limit(charged)`; fast path extend in place if File (check `usage + len <= limit`); slow path: `check_file_replacement(final_len)`, `budget.check(final_len)`, load with `budget.shrink(len)`, concat, insert; commit(charged); Int(bytes|chars).
mkdir: Directory → `exist_ok ? None : FileExistsError`; File/RealFileRef → FileExistsError; Deleted → create; none → resolve_real + classify (Dir+exist_ok → None; Dir/File → FileExistsError; Symlink → PathEscape; Absent → proceed). `parents=true` → walk every component (Directory continue; File/RealFileRef → `NotADirectoryError {repr(prefix vpath)}`; Deleted → insert Directory; none: real Dir continue, Symlink → PathEscape{prefix}, File → NotADirectoryError{prefix}, Absent → insert Directory); `parents=false` → ensure_parent_exists. Insert `Directory{mtime}`.
rename (preflight everything before mutating): relative both; root either side → PathEscape; ensure_parent_exists(dst); no overlay entry at dst → resolve_real(dst); ensure_parent_exists(src); src Deleted → FileNotFoundError; `src_is_dir` (overlay kind or resolve_real + host_is_dir); type mismatch: dst dir & src not → `IsADirectoryError {repr(dst)}`; dst not dir & src dir → `NotADirectoryError {repr(dst)}` (dst Deleted counts as absent); src dir → dst non-empty dir → `[Errno 39] {repr(dst)}`; dst under src → `Io(InvalidInput "Invalid argument")` with src; src not in overlay → resolve_real + classify (File → RealFileRef; Dir → Directory{mtime}; Symlink → PathEscape; Absent → FileNotFoundError); directory move plan: overlay descendants remapped; real descendants DFS (non-UTF-8 name → `OSError` `directory contains an entry with a non-UTF-8 name`; symlink anywhere → PathEscape; files → RealFileRef; dirs → Directory); memory preflight `check_replacements`; commit: Deleted at old keys, moved entries at new keys.
open: `r/rb` → File/RealFileRef OK; Directory → IsADirectoryError; Deleted → FileNotFoundError; none → real dir → IsADirectoryError, present OK, missing FileNotFoundError (permission errors propagate). `w/wb` → `write_text(path, "")`. `a/ab` → `ensure_append_target_exists` (no content pulled): File/RealFileRef no-op; Directory → IsADirectoryError; Deleted → ensure_parent + insert empty File; none → resolve_real + classify (Dir → IsADirectoryError; Symlink → PathEscape; File → ensure_parent; Absent → ensure_parent + insert empty File).

## E.4 Modes and overlay storage
Mode strings `read-only|read-write|overlay`; invalid → `Invalid mode '{other}', expected 'read-only', 'read-write', or 'overlay'`. Read-write writes are untrusted (never execute). Overlay: ordered map keyed by mount-relative path (root = `""`); entries `File{content, mtime}`, `RealFileRef{relative, mtime, size}` (lazy, re-validated on every dereference), `Directory{mtime}`, `Deleted` (whiteout; charged to memory → `unlink` can raise MemoryError). Prefix scans over `[prefix, prefix[:-1]+"0")`. Entry wins absolutely over the real file. Real directory never modified. Fresh overlay per feed (pool `build_mount_table`); descriptor shared across rebuilds.
iterdir merge: overlay direct children first (lexicographic; charged `2*len(rest)+128` then `len(child)+128`), tombstones suppress real names; then real names not in `seen` (listing phase on a halved budget).

## E.5 Limits
Memory: `available = limit − state.memory_usage` (overlay) or `limit` (direct); `check(n)`: `n > available` → `MemoryUsageLimitExceeded(limit)`; `shrink(n)`; `halved()`. Retained usage: `ENTRY_MEMORY_USAGE 256 + len(key) + 48` + variable (`File` len(content); `RealFileRef` len(relative); others 0). `insert` projects `usage − old + new > limit`; `remove` subtracts; `append_file` charges `len(data)`; `check_file_replacement(key, len)`; `check_replacements(pairs)` last-write-wins memo. Transient: `LISTING_ENTRY_MEMORY_USAGE 128`, `REAL_DESCENDANT_MEMORY_USAGE 512`; reads charge stat size then bytes read.
`pretty(bytes)`: `< 1000` → `{n} bytes`; KB/MB/GB/TB decimal; `tenths = round(v*10) % 10`; `tenths == 0 ? "{v:.0} {unit}" : "{v:.1} {unit}"` (`1 KB`, `1.5 KB`, `1.5 MB`).
Write limit (optional, cumulative, monotonic, only tracked when set): `check(n)`: `used + n > limit` → `WriteLimitExceeded`; charged: write_text/append_text bytes of data; write_bytes/append_bytes len; direct `open(w)` 0; direct `open(a)` nothing; overlay `open(w)` 0; overlay append to a non-overlay-File target `existing_len + len`; mkdir/unlink/rmdir/rename nothing. Message `disk write limit of {pretty(limit)} exceeded` → OSError (no errno).

## E.6 File handles after `open`
Host keeps no OS handle. `FileHandle{path (virtual, normalized), mode (canonical), position 0}`. Interpreter re-emits path calls: first read/readline/readlines/seek → `Path.read_text` / `Path.read_bytes` (whole file, sliced in-sandbox; later reads none); first `write` in `w`/`wb` → `Path.write_text`/`Path.write_bytes`; writes in `a`/`ab` or any write after the first successful one → `Path.append_*`; close/tell/seek-after-load → none. `first_write_done` flips only after host confirmation. Position is interpreter-side (chars in text mode, bytes in binary). Text I/O is whole-file UTF-8, no error handlers, no newline translation. Closed file → `ValueError("I/O operation on closed file.")`. `repr(f)` = `<{TextIOWrapper|BufferedReader|BufferedWriter|BufferedRandom} name={repr(path)} mode={repr(mode)}>`.
FileMode: chars `r w a b t`; canonical `r rb r+ rb+ w wb w+ wb+ a ab a+ ab+`; `rt`→`r`, `br`→`rb`; errors as in Appendix F; predicates `create() = w|w+|a|a+`, `truncate() = w|w+`, `is_append() = a|a+`, `readable() = r|r+|w+|a+`, `writable() = w|w+|a|a+|r+`, `is_binary()`.

## E.7 Windows notes (out of scope for v0.0.23, kept for later)
Backslash/drive segments rejected on every host; errnos hardcoded POSIX; `reject_non_regular` exists for cross-platform `IsADirectoryError`; `O_NONBLOCK` Unix-only; mounted dir locked on Windows (`ERROR_SHARING_VIOLATION`); symlinks need Developer Mode; non-UTF-8 names listed lossily but matched raw.

## E.8 Go checklist
1. `OsCall` tagged union with `FSPrimaryPath()`, `RenameDestination()`, `IsWrite()`, `IsExistenceCheck()`, `Name()`, `EmbeddedNullMessage(forDst)`.
2. `os.OpenRoot` per mount; never concatenate host paths and check afterwards.
3. Map `os.Root` escape errors (`errors.Is(err, os.ErrNotExist)`? no: Go's root escape returns `*PathError` with `syscall.EPERM`-like "path escapes from parent") to `PathEscape`; real `EACCES` → `Io`. Both render identically.
4. Exact `repr()`.
5. Budgets in exact order; boundary tests (5-byte file passes 5-byte limit, 6 fails).
6. `sizeof(OverlayEntry) = 48` constant.
7. `NotHandled` for uncovered paths (host default has no `[Errno 13]`).
