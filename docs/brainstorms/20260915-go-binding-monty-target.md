# Target architecture: `montygo` — Go binding for Monty with `@pydantic/monty` parity

Date: 2026-09-15. Companion log: `20260915-go-binding-monty.md` (decisions A1–A18) and appendices A–G (TS API, Python/protocol, TS tests, pool spec, mount spec, codec/errors, Python extras).

## 1. Initial request (verbatim)

> you need to design and implement golang binding to monthy with 100% functional parity of official binding to TypeScript
> You need to design a golang packge which implements binding to pydantic monthy (checkouted there ../monthy).
> It should containt semantically the same tests as for typescript. You can use any 3rd party libraries for better and simpler port, you can use WASM technologies for some cases.
> You can also find python bindings in ../monthy
> After successfull porting you need to write ./changelogs/v0.0.23.md (as the latest release of monthy) where describe the current state and what was added.
> monthy has MIT license and gomonthy is empty dir (project).

Baseline (A1): upstream `../monty` at `main` commit `f8acf4fa` (25 commits after tag `v0.0.23`), **wire protocol version 3**. Package version string `0.0.23`; changelog `changelogs/v0.0.23.md` MUST state "upstream 0.0.23 + main@f8acf4fa (protocol 3)".

## 2. High-level architecture

```
                    github.com/asalimonov/montygo  (package monty)
 ┌──────────────────────────────────────────────────────────────────────────┐
 │ pool.go  session.go  snapshot.go  function.go  classinstance.go          │
 │ errors.go  print.go  mount.go  options.go  telemetry.go  binary.go       │
 │ websocket.go  values.go (aliases)                                        │
 │   drive loop (TurnAnswerer analog) · instance store · prepare/restore    │
 └───────┬──────────────────┬───────────────────┬──────────────┬────────────┘
         │                  │                   │              │
 ┌───────▼───────┐  ┌───────▼────────┐  ┌───────▼───────┐  ┌───▼──────────┐
 │ internal/pool │  │ internal/value │  │ internal/     │  │ osaccess/    │
 │ Pool,Checkout │  │ Dict,Set,…     │  │ telemetry     │  │ (public)     │
 │ turns,budgets │  │ markers,depth  │  │ recorder      │  └──────────────┘
 └───────┬───────┘  └───────▲────────┘  └───────────────┘
         │                  │
 ┌───────▼───────┐  ┌───────┴────────┐   ┌────────────────┐  ┌────────────────┐
 │ internal/     │  │ internal/wire  │   │ internal/      │  │ montypb        │
 │ worker        │  │ frames, hybrid │   │ mountfs        │  │ (generated pb) │
 │ Subprocess    │  │ protowire codec│   │ direct+overlay │  └────────────────┘
 │ Wasm (wazero) │  │ decode budget  │   └────────────────┘
 │ WebSocket     │  └────────────────┘
 └───────┬───────┘
   ┌─────┴─────────────────────────────────────────────┐
   │ monty subprocess (native binary)                  │ ← MONTY_BIN / PATH / cargo target
   │ monty-wasi-worker.wasm (embedded, wazero)         │ ← internal/wasmblob (go:embed .zst)
   │ remote child over WebSocket (relay / monty-server)│
   └───────────────────────────────────────────────────┘
```

Both official bindings are "protocol parents" driving `monty subprocess` children over 4-byte-LE-framed protobuf (Appendix D). Go re-implements **both** layers that TS gets from Rust (`monty-pool`) and TypeScript (`session.ts`): the pool/turn engine and the host drive loop. No cgo.

## 3. Decision table

| # | Topic | Decision | Alternatives rejected |
|---|---|---|---|
| A1 | Baseline | local `main` f8acf4fa, protocol 3 | tag v0.0.23 (protocol 2, prebuilt binaries) |
| A2 | Transport | native subprocess (primary) + wazero-hosted wasip1 worker (secondary), same pool/drive loop | cgo/purego FFI (ewhauser/gomonty style) |
| A4 | Worker resolution | `BinaryPath` → `MONTY_BIN` → `PATH` → cargo `target/{debug,release}` walk → embedded wasm; `Backend: Auto|Native|Wasm` | downloader; embedded native binaries |
| A5 | Dict repr | `*monty.Dict` always (ordered, any hashable key) | `map[string]any` when string-keyed; pair slice |
| A6 | Host functions | `Function` interface + reflect adapter `Func`; `*Future` for async; `*RaisedError` picks the exception type | interface-only; reflect-only |
| A7 | Host objects | reflect over exported fields/methods + `AttrProvider`/`MethodProvider`; `NewClassType[T]` with constructor + statics | providers only; reflect only |
| A8 | Telemetry | full OTel-go parity (spans, metrics, logs), synchronous recording | spans+metrics; hooks; none |
| A9 | Tests | 1:1 port of the TS suite, matrix over both backends; JS-only tests mapped/excluded with reasons | native only; Python suite |
| A10 | Naming | module `github.com/asalimonov/montygo`, package `monty` (assumed) | package `montygo` |
| A11 | Wasm blob | committed `internal/wasmblob/monty.wasm.zst` (~4.1 MB), `go:embed`, zstd decode, on-disk wazero compilation cache | separate module; go generate; download |
| A12 | Upstream pin | `worker-wasm/Cargo.toml` git rev `f8acf4fa` + `MONTY_SRC` path override; vendored proto with rev | path only; submodule |
| A13 | Windows | unsupported in v0.0.23 (native backend `//go:build unix`) | best effort; full CI |
| A15 | Decode budget | hybrid hand-written `protowire` codec for value-bearing messages with 1 GiB resident budget | frame cap only |
| A16 | Meta tests | `Example…` functions for README snippets; export-list test | drop |
| A17 | Python extras | WebSocket transport + in-memory OS helpers in scope; `ClassTypeProxy` postponed | none |
| A18 | Extra tests | Python `test_websocket.py`, `test_os_access*.py` ported 1:1 | subsets |

## 4. Repository layout (all files CREATED)

```
montygo/
  go.mod                         module github.com/asalimonov/montygo ; go 1.25
  LICENSE                        MIT (montygo) + THIRD_PARTY_NOTICES.md (monty MIT, vendored proto, subprocess.rs port)
  README.md                      user docs mirroring crates/monty-js/README.md (Go snippets = Example tests)
  Makefile                       targets below
  monty.go pool.go session.go snapshot.go function.go future.go values.go dict.go set.go
  classinstance.go errors.go traceback.go print.go mount.go options.go telemetry.go binary.go websocket.go
  *_test.go                      ported suite (§9) + example_test.go + public_api_test.go
  osaccess/                      public helpers (AbstractOS/OSAccess/MemoryFile/CallbackFile/StatResult analogs)
  montypb/monty.pb.go            generated (protoc-gen-go); go:generate directive in montypb/generate.go
  proto/monty/v1/monty.proto     vendored copy + PROTO_REV file ("f8acf4fa")
  internal/value/                value model, hashing, depth accounting, markers
  internal/wire/                 frames, hybrid codec, request/event structs, decode budget
  internal/worker/               Worker interface; subprocess_unix.go; wasm.go; websocket.go
  internal/wasmblob/             monty.wasm.zst (embedded), blob.go (decode, sha256, cache dir)
  internal/pool/                 pool.go checkout.go budget.go errors.go mounts.go
  internal/mountfs/              table.go path.go direct.go overlay.go state.go budget.go errors.go repr.go
  internal/telemetry/            recorder.go spans.go metrics.go logs.go encode.go
  internal/testutil/             fixtures shared by tests (pool per package, backend matrix, ws relay)
  worker-wasm/                   Rust crate (Cargo.toml, src/main.rs, src/subprocess.rs) building the wasip1 worker
  docs/architecture/*.md         architecture docs written at implementation (§11)
  docs/parity/api.md tests.md    parity checklists (TS API ↔ Go; TS test ↔ Go test / exclusion reason)
  changelogs/v0.0.23.md          release notes (§12)
  .github/workflows/ci.yml       build worker from the git pin, run both backends on linux + macos
```

Storage schema: **no database, no persistent store**. The only on-disk state is the wazero compilation cache (`<UserCacheDir>/montygo/wazero/<sha256(blob)>/`, managed by wazero, no TTL, safe to delete) and the decompressed blob is kept in memory only. Existing tables: none.

## 5. Public API — package `monty` (per file)

### 5.1 `monty.go` (CREATED)
```go
const Version = "0.0.23"          // package version reported in Configure.monty_version
const UpstreamRev = "f8acf4fa"    // upstream commit the protocol/tests were taken from
const ProtocolVersion uint32 = 3
const MaxValueDepth = 48
```

### 5.2 `pool.go` (CREATED)
```go
type Backend int
const ( BackendAuto Backend = iota; BackendNative; BackendWasm )
func (b Backend) String() string   // "auto" | "native" | "wasm"

const NoDurationLimitGrace time.Duration = -1

type Options struct {
    Backend               Backend
    BinaryPath            string
    MinProcesses          int            // default 1
    MaxProcesses          int            // default runtime.NumCPU()
    CheckoutTimeout       time.Duration  // 0 = wait forever
    RequestTimeout        time.Duration  // 0 = off
    DurationLimitGrace    time.Duration  // 0 = 1s; NoDurationLimitGrace disables
    MaxCheckoutsPerWorker int            // 0 = unlimited
    WasmCacheDir          string         // "" = default; ignored when DisableWasmCache
    DisableWasmCache      bool
}

type Pool struct { /* unexported */ }
func New(ctx context.Context, opts Options) (*Pool, error)
func (p *Pool) Checkout(ctx context.Context, opts CheckoutOptions) (*Session, error)
func (p *Pool) Close(ctx context.Context) error
func (p *Pool) Backend() Backend
func (p *Pool) BinaryPath() string     // "" for wasm / websocket

type CheckoutOptions struct {
    ScriptName               string          // "main.py"
    Limits                   *ResourceLimits
    TypeCheck                bool
    TypeCheckStubs           string
    TypeCheckFormat          TypeCheckFormat // "" → FormatFull
    TypeCheckColor           bool
    AssertMessageAnnotations *uint32         // nil default(120), 0 off
    PrintFlushInterval       *time.Duration  // nil 5ms, 0 line-buffered, <0 error
}
type ResourceLimits struct {
    MaxDuration       time.Duration   // 0 unlimited; <0 → error "invalid maxDurationSecs: …"
    MaxMemory         uint64
    GCInterval        uint64
    MaxRecursionDepth uint64          // 0 → 1000
    MaxSuspensions    uint64          // 0 → 1000
}
```
Validation errors (returned from `New`/`Checkout`, type `*OptionError{Message}`): `maxProcesses must be at least 1`, `minProcesses cannot exceed maxProcesses`, `invalid printFlushInterval: expected a non-negative duration, got <d>`, `invalid mount mode: '<m>'`, `unknown typeCheckFormat '<f>', expected one of: full, concise, azure, json, jsonlines, rdjson, pylint, gitlab, github`. `Checkout` after `Close` → `ErrPoolClosed` (`the pool is closed — create a new Monty pool`).

### 5.3 `session.go` (CREATED)
```go
type Session struct { /* unexported */ }
func (s *Session) FeedRun(ctx context.Context, code string, opts *FeedOptions) (any, error)
func (s *Session) FeedStart(ctx context.Context, code string, opts *FeedOptions) (Snapshot, error)
func (s *Session) LoadSession(ctx context.Context, state []byte) error
func (s *Session) LoadSnapshot(ctx context.Context, state []byte, opts *LoadSnapshotOptions) (Snapshot, error)
func (s *Session) Dump(ctx context.Context) ([]byte, error)
func (s *Session) InstallDependencies(ctx context.Context, requirements []string) error
func (s *Session) WorkerPID() (int, bool)
func (s *Session) Close(ctx context.Context) error   // idempotent

type FeedOptions struct {
    Inputs         map[string]any
    ExternalLookup map[string]any
    Print          PrintTarget
    Mount          []*MountDir
    Cwd            string
    OS             OSHandler
    SkipTypeCheck  bool
}
type LoadSnapshotOptions struct { Print PrintTarget; Mount []*MountDir; ExternalLookup map[string]any; OS OSHandler }
```
Sentinel errors (`errors.go`): `ErrSessionClosed` ("the session is closed — check out a new one"), `ErrNotFresh` ("loadSession / loadSnapshot is only valid on a fresh session, before any feedRun / feedStart / loadSession / loadSnapshot"), `ErrDumpIsSuspended` ("this dump is a suspended snapshot — use loadSnapshot() to resume it"), `ErrDumpIsIdle` ("this dump is an idle session — use loadSession() to restore it"), `ErrTurnCancelled` ("a previous protocol turn was cancelled mid-flight; the worker was discarded").

### 5.4 `snapshot.go` (CREATED)
```go
type Snapshot interface{ isSnapshot() }
type Complete struct{ Output any }
type FunctionSnapshot struct {
    FunctionName string; Args []any; Kwargs Kwargs; CallID uint32
    IsOSFunction bool; AllowEagerAwait bool; ObjectID string
    // unexported: driver *snapshotDriver, used atomic.Bool
}
func (f *FunctionSnapshot) Resume(ctx context.Context, value any) (Snapshot, error)
func (f *FunctionSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error)
func (f *FunctionSnapshot) ResumeError(ctx context.Context, err error) (Snapshot, error)
func (f *FunctionSnapshot) ResumeNotFound(ctx context.Context) (Snapshot, error)
func (f *FunctionSnapshot) ResumeFuture(ctx context.Context) (Snapshot, error)
func (f *FunctionSnapshot) ResumeNotHandled(ctx context.Context) (Snapshot, error)
func (f *FunctionSnapshot) Dump(ctx context.Context) ([]byte, error)
type NameLookupSnapshot struct{ VariableName string; ObjectID string }
func (n *NameLookupSnapshot) ResumeUnresolved(ctx context.Context) (Snapshot, error)
func (n *NameLookupSnapshot) ResumeFunction(ctx context.Context, functionName string) (Snapshot, error)
func (n *NameLookupSnapshot) ResumeValue(ctx context.Context, value any) (Snapshot, error)
func (n *NameLookupSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error)
func (n *NameLookupSnapshot) Dump(ctx context.Context) ([]byte, error)
type FutureSnapshot struct{ PendingCallIDs []uint32 }
type FutureResolution struct{ CallID uint32; Value any; Err error }
func (f *FutureSnapshot) Resume(ctx context.Context, results []FutureResolution) (Snapshot, error)
func (f *FutureSnapshot) ResumeAuto(ctx context.Context) (Snapshot, error)
func (f *FutureSnapshot) Dump(ctx context.Context) ([]byte, error)
```
`ErrSnapshotResumed` ("snapshot has already been resumed"), `ErrNotOSCall` ("resumeNotHandled is only valid for OS-call snapshots").

### 5.5 `function.go`, `future.go` (CREATED)
```go
type Kwargs map[string]any
type Function interface{ Call(ctx context.Context, args []any, kwargs Kwargs) (any, error) }
type FunctionFunc func(ctx context.Context, args []any, kwargs Kwargs) (any, error)
func (f FunctionFunc) Call(ctx context.Context, args []any, kwargs Kwargs) (any, error)
func Func(fn any) (Function, error)          // reflect adapter; error if fn is not a func
func MustFunc(fn any) Function
type RaisedError struct{ ExcType string; Message string }
func (e *RaisedError) Error() string          // "ExcType: Message" or "ExcType"
var NotHandled = &notHandledSentinel{}
type OSHandler func(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error)

type Future struct{ /* done chan struct{}; value any; err error; once sync.Once */ }
func Async(fn func(ctx context.Context) (any, error)) *Future   // goroutine; ctx = context.Background() derived; cancelled on session close
func NewFuture() (f *Future, settle func(value any, err error))
func (f *Future) Done() <-chan struct{}
func (f *Future) Result() (any, error)        // blocks
```
Reflect adapter rules (`Func`): parameters converted from sandbox values with `value.Assign(dst reflect.Value, src any) error`; optional first `context.Context`; optional last `Kwargs`; variadic tail allowed; returns `()`, `(T)`, `(error)`, `(T, error)`; argument-count/type mismatch → in-sandbox `TypeError` with message `<fn name>() takes N positional arguments but M were given` / `cannot convert <pytype> to <GoType> for parameter <i>`. A returned `*Future` (any position) is the async signal.

### 5.6 `values.go`, `dict.go`, `set.go` (CREATED) — aliases of `internal/value`
```go
type Dict = value.Dict; type Pair = value.Pair; type Tuple = value.Tuple
type Set = value.Set; type FrozenSet = value.FrozenSet
type Date = value.Date; type DateTime = value.DateTime; type Time = value.Time
type TimeDelta = value.TimeDelta; type TimeZone = value.TimeZone
type Exception = value.Exception; type Type = value.Type; type TypeOrigin = value.TypeOrigin
type BuiltinFunction = value.BuiltinFunction; type Path = value.Path
type NamedTuple = value.NamedTuple; type FileHandle = value.FileHandle; type Cycle = value.Cycle
var Ellipsis = value.Ellipsis; var NotImplemented = value.NotImplemented
func NewDict(pairs ...Pair) *Dict; func NewSet(items ...any) *Set; func NewFrozenSet(items ...any) *FrozenSet
func NewFileHandle(path, mode string, position uint64) (*FileHandle, error)
func DateTimeFromTime(t time.Time) DateTime; func TimeDeltaFromDuration(d time.Duration) TimeDelta
```

### 5.7 `classinstance.go` (CREATED)
```go
type AttrPolicy struct{ all bool; names []string }
var All = AttrPolicy{all: true}
func Names(names ...string) AttrPolicy
func (p AttrPolicy) Allows(name string) bool   // none→false; all→!strings.HasPrefix(name,"_") && exported; names→contains

type ClassInstanceOptions struct {
    EagerAttrs, LazyAttrs, AllowedMethods AttrPolicy
    Name string; ConvertValue func(name string, v any) (any, error); ID string; ClassType *ClassType
}
type ClassInstance struct{ /* instance any; opts; id string; classType *ClassType; rt reflect.Value */ }
func NewClassInstance(instance any, opts ClassInstanceOptions) (*ClassInstance, error)
func (c *ClassInstance) ID() string; func (c *ClassInstance) Instance() any
func (c *ClassInstance) ClassType() *ClassType; func (c *ClassInstance) Name() string

type ClassTypeOptions struct {
    EagerAttrs, LazyAttrs, AllowedMethods AttrPolicy; Statics map[string]any
    Name string; ConvertValue func(name string, v any) (any, error); ID string
    Init bool; Constructor any
    InstanceEagerAttrs, InstanceLazyAttrs, InstanceAllowedMethods AttrPolicy
    InstanceWrapper func(instance any) (*ClassInstance, error)
}
type ClassType struct{ /* goType reflect.Type; opts; id string */ }
func NewClassType[T any](opts ClassTypeOptions) (*ClassType, error)
func (c *ClassType) ID() string; func (c *ClassType) Name() string; func (c *ClassType) GoType() reflect.Type
func (c *ClassType) Construct(ctx context.Context, args []any, kwargs Kwargs) (*ClassInstance, error)

type AttrProvider interface{ GetAttr(name string) (any, error) }
type MethodProvider interface{ CallMethod(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error) }
var ErrAttrNotExposed = errors.New("attribute not exposed")

type ClassProxy struct{ Name string; ID string; IsDataclass bool; Attributes *Dict }
```
Constructor errors (`*ValueError{Message}` in Go, TS `TypeError`): `ClassInstance expects an object instance` (nil / non-struct-non-pointer), `ClassInstance id must be a canonical uuid string, got "<id>"`, `classType does not match the instance's class`, `pass name on the ClassType wrapper, not alongside classType`, `<field> must be All, zero or a list of names` (unreachable by construction; kept for docs), `ClassType expects a struct or pointer-to-struct type parameter`, `wrapper id '<id>' already identifies a different object in this session`.

### 5.8 `errors.go`, `traceback.go` (CREATED)
```go
type DisplayFormat string
const ( DisplayTraceback DisplayFormat = "traceback"; DisplayTypeMsg = "type-msg"; DisplayMsg = "msg" )
type ExceptionInfo struct{ TypeName, Message string }
type Frame struct{ Filename string; Line, Column, EndLine, EndColumn uint32; FunctionName *string; SourceLine *string }
type Error interface{ error; Exception() ExceptionInfo; Display(format DisplayFormat) string }

type RuntimeError struct{ TypeName, Message string; Frames []Frame; Traceback string }
func (e *RuntimeError) Error() string          // "<TypeName>: <Message>" or "<TypeName>"
func (e *RuntimeError) Exception() ExceptionInfo
func (e *RuntimeError) Display(format DisplayFormat) string   // "" → traceback
func (e *RuntimeError) TracebackFrames() []Frame
type SyntaxError struct{ Message, Traceback string }          // Display "" → msg
type TypingError struct{ Diagnostics string }                 // Exception{"TypeError", firstLine}; Display ignores format
type CrashedError struct{ Message string; TimedOut bool; ExitStatus string }   // Exception{"RuntimeError", Message}
type ProtocolError struct{ Message string }                   // not a monty.Error
type ConversionError struct{ Message string }                 // host-supplied inputs/externalLookup unconvertible (TS TypeError)
type OptionError struct{ Message string }                     // invalid options (TS RangeError/TypeError)
type ValueError struct{ Message string }                      // wrapper/handle construction errors (TS TypeError)
func IsSubclass(excType, base string) bool                    // Python hierarchy (Appendix F.4)
func formatDuration(d time.Duration) string                   // Rust Duration Debug: 500ms, 1s, 1.5s, 2.5ms, 750µs, 100ns, 60s
func renderTraceback(frames []Frame, summary string) string   // TS fallback renderer (used when Traceback == "")
```
`PythonExcNames` (39 names, Appendix A.5) exported as `var PythonExceptionNames = map[string]struct{}`.

### 5.9 `print.go` (CREATED)
```go
type Stream string
const ( Stdout Stream = "stdout"; Stderr Stream = "stderr" )
type PrintTarget interface{ Print(stream Stream, text string) error }
type PrintFunc func(stream Stream, text string) error
const DefaultMaxPrintCollectBytes int64 = 10 << 20
const UnlimitedPrintCollect int64 = -1
type CollectString struct{ /* mu, buf strings.Builder, max int64, used int64 */ }
func NewCollectString(maxBytes int64) (*CollectString, error)   // < -1 → OptionError "maxBytes must be a finite non-negative number or null"
func (c *CollectString) Output() string
func (c *CollectString) Print(stream Stream, text string) error  // over cap → &RuntimeError{TypeName:"MemoryError", Message:"memory limit exceeded: <used> bytes > <max> bytes"}
type CollectedStreamEntry struct{ Stream Stream; Text string }
type CollectStreams struct{ … }
func NewCollectStreams(maxBytes int64) (*CollectStreams, error)
func (c *CollectStreams) Output() []CollectedStreamEntry        // copy
func (c *CollectStreams) Print(stream Stream, text string) error // +64 per entry, no merging
```

### 5.10 `mount.go` (CREATED)
```go
type MountMode string
const ( MountReadOnly MountMode = "read-only"; MountReadWrite MountMode = "read-write"; MountOverlay MountMode = "overlay" )
type MountDirOptions struct{ HostPath, VirtualPath string; Mode MountMode; WriteBytesLimit *uint64; MemoryUsageLimit *uint64 }
type MountDir struct{ HostPath, VirtualPath string; Mode MountMode; WriteBytesLimit *uint64; MemoryUsageLimit uint64; /* root *mountfs.Root; closed atomic.Bool */ }
func NewMountDir(opts MountDirOptions) (*MountDir, error)
func (m *MountDir) Close() error      // idempotent
func (m *MountDir) String() string    // "MountDir(host_path='<h>', virtual_path='<v>', mode='<m>')"
```
Errors: `invalid mount mode: '<m>'. Expected 'read-only', 'read-write' or 'overlay'`; `virtual path must be absolute, got: '<v>'`; `cannot open host path '<p>': <err>`; feeding a closed mount → `mount is closed: create a new MountDir`.

### 5.11 `options.go` (CREATED)
```go
type TypeCheckFormat string
const ( FormatFull TypeCheckFormat = "full"; FormatConcise = "concise"; FormatAzure = "azure"; FormatJSON = "json"; FormatJSONLines = "jsonlines"; FormatRDJSON = "rdjson"; FormatPylint = "pylint"; FormatGitLab = "gitlab"; FormatGitHub = "github" )
func encodeTypeCheckFormat(f TypeCheckFormat) (montypb.TypeCheckFormat, error)
func encodeAssertMessageAnnotations(v *uint32) *uint32
func encodePrintFlushInterval(d *time.Duration) (*uint32, error)   // nil→nil; 0→0; else clamp(ms,1,MaxUint32); <0 error
```

### 5.12 `telemetry.go` (CREATED)
```go
type TelemetryComponents struct{ Tracer trace.Tracer; Meter metric.Meter; Logger log.Logger }
func Instrument(c TelemetryComponents) error   // "Monty telemetry is already configured" / "at least one OpenTelemetry component is required"
func Flush(ctx context.Context) error
type InstrumentationConfig struct{ Enabled, Traces, Metrics, Logs *bool }   // nil = true
type Instrumentation struct{ … }
func NewInstrumentation(cfg InstrumentationConfig) (*Instrumentation, error)
func (i *Instrumentation) Name() string               // "github.com/asalimonov/montygo"
func (i *Instrumentation) Version() string
func (i *Instrumentation) Enable() error; Disable(); SetTracerProvider(trace.TracerProvider); SetMeterProvider(metric.MeterProvider); SetLoggerProvider(log.LoggerProvider)
func (i *Instrumentation) Config() InstrumentationConfig; SetConfig(InstrumentationConfig)
func (i *Instrumentation) ForceFlush(ctx context.Context) error
```

### 5.13 `binary.go` (CREATED)
```go
func FindMontyBinary(explicit string) (string, error)
// order: explicit (must exist: "monty binary not found at binaryPath: <p>") → $MONTY_BIN → exec.LookPath("monty")
//        → walk up ≤ 6 dirs from cwd and from the module dir for target/debug/monty then target/release/monty (newest mtime)
//        → error "could not locate the monty binary (tried: …). Install pydantic-monty-runtime, set MONTY_BIN, or pass BinaryPath."
```

### 5.14 `websocket.go` (CREATED) — Python-only feature (A17)
```go
type WebSocketOptions struct {
    URL             string
    MaxProcesses    int                 // default runtime.NumCPU()
    CheckoutTimeout time.Duration
    RequestTimeout  time.Duration       // default 10s (Python default)
    ConnectHeaders  func() (map[string]string, error)   // called once per checkout before capacity wait
}
func NewWebSocket(ctx context.Context, opts WebSocketOptions) (*Pool, error)   // Pool.Backend() == BackendWebSocket
const BackendWebSocket Backend = 3
type DisconnectError struct{ Context string }                   // "monty worker connection closed while <ctx>"
type ShutdownError struct{ Dump []byte }                        // "monty server is shutting down; the request did not run (session dump attached)" / "… did not run"
```
Details in §8.9 (after Appendix G).

### 5.15 `osaccess/` (CREATED) — Python-only feature (A17); signatures finalised in §8.10 from Appendix G.

## 6. Internal packages — signatures per file

### 6.1 `internal/value` (CREATED)
```go
// value.go
type Dict struct{ keys []any; vals []any; index map[hashKey]int }
func NewDict(pairs ...Pair) *Dict; (d *Dict) Get(k any) (any, bool); Set(k, v any) error /* unhashable key */; Delete(k any) bool; Len() int
func (d *Dict) Keys() []any; Values() []any; Pairs() []Pair; Range(func(k, v any) bool); ToStringMap() (map[string]any, error); Clone() *Dict; Equal(o *Dict) bool; String() string
type Pair struct{ Key, Value any }
type Tuple []any
type Set struct{ items []any; index map[hashKey]int }; type FrozenSet struct{ Set }
func NewSet(items ...any) *Set; (s *Set) Add(v any) error; Has(v any) bool; Items() []any; Len() int
type hashKey struct{ kind uint8; s string; i int64; f float64; b bool; ptr uintptr; tuple string }   // Python hash equivalence: True==1==1.0, "a", bytes, tuples of hashables, frozensets, None
func HashKey(v any) (hashKey, error)   // error "unhashable type: 'list'" (Python wording)
type Date struct{ Year int32; Month, Day uint8 }
type DateTime struct{ Year int32; Month, Day, Hour, Minute, Second uint8; Microsecond uint32; OffsetSeconds *int32; TimezoneName *string }
type Time struct{ Hour, Minute, Second uint8; Microsecond uint32; OffsetSeconds *int32; TimezoneName *string; Fold uint8 }
type TimeDelta struct{ Days, Seconds, Microseconds int32 }
type TimeZone struct{ OffsetSeconds int32; Name *string }
type Exception struct{ ExcType string; Message string }     // Message=="" ⇔ arg absent
type TypeOrigin uint8; const ( OriginBuiltin TypeOrigin = 1; OriginSandbox = 2; OriginHost = 3 )
type Type struct{ Name string; ID string; Origin TypeOrigin; IsDataclass bool; Attrs *Dict }
type ClassInstanceMarker struct{ Type Type; InstanceID string; Attrs *Dict }   // wire-level marker; root maps ↔ wrappers/proxies
type BuiltinFunction string; type Path string
type NamedTuple struct{ TypeName string; FieldNames []string; Values []any }
type FileHandle struct{ Path, Mode string; Position uint64 }; func NewFileHandle(path, mode string, pos uint64) (*FileHandle, error); (h *FileHandle) Binary()/Readable()/Writable() bool
type Cycle struct{ Identity uint64; Placeholder string }
type Function struct{ Name string; Docstring *string }
type ellipsisT struct{}; var Ellipsis ellipsisT; var NotImplemented notImplementedT
// filemode.go
func CanonicalFileMode(mode string) (string, error)   // Appendix F/E error strings
// depth.go
const ProstRecursionLimit = 100; FrameWrapperDepth = 3; MaxProtoValueDepth = 97; MaxValueDepth = 48
func ExceedsMaxDepth(v any) bool                       // costs: list-like 2, dict 3, class instance 4(+3 type), Type 2, type attrs 2
// pytype.go
func PyTypeName(v any) string                          // for "'<t>' object is not callable"
// repr.go
func StringRepr(s string) string                       // CPython repr of str (quotes/escapes)
```

### 6.2 `internal/wire` (CREATED)
```go
// frame.go
const MaxFrameLen = 256 << 20; const DefaultMaxDecodeBytes = 4 * MaxFrameLen; const ReadChunk = 8 << 10; const RetainBufMax = 64 << 10
var ErrFrameTooLarge, ErrTruncated error
type FrameReader struct{ r io.Reader; buf []byte; /* partial state */ }
func NewFrameReader(r io.Reader) *FrameReader
func (f *FrameReader) Next(ctx context.Context) ([]byte, error)   // nil,io.EOF at a clean boundary; ErrTruncated mid-frame; cancel-safe (state kept)
func WriteFrame(w io.Writer, payload []byte) error                // len > MaxFrameLen → ErrFrameTooLarge before writing
// request.go — parent → child (Go structs; Encode uses protowire, delegating value-free arms to montypb)
type Request struct{ Kind RequestKind; Configure *montypb.Configure; Install *montypb.InstallDependencies; Feed *Feed; ResumeCall *ResumeCall; ResumeNameLookup *ResumeNameLookup; ResumeFutures *ResumeFutures; AbortFeed *montypb.AbortFeed; TraceParent *string }
type Feed struct{ Code string; Inputs []NamedValue; SkipTypeCheck bool; Cwd string }
type NamedValue struct{ Name string; Value any }
type ExtResult struct{ Kind ExtResultKind; Value any; Error *montypb.RaisedException; FutureCallID uint32; NotFoundName string }
type ResumeCall struct{ CallID uint32; Result ExtResult }
type ResumeNameLookup struct{ Kind NameLookupResultKind; Value any; Error *montypb.RaisedException }
type ResumeFutures struct{ Results []FutureResult }; type FutureResult struct{ CallID uint32; Result ExtResult }
func (r *Request) Encode(buf []byte) ([]byte, error)             // errors: ErrDepth ("Max input depth exceeded"), ErrUnconvertible{msg}
// event.go — child → parent
type Event struct{ Kind EventKind; Print []PrintSegment; FunctionCall *FunctionCall; OsCall *OsCall; NameLookup *NameLookup; ResolveFutures []uint32; Complete any; Error *montypb.RaisedException; TypingError string; DumpState []byte; Fatal string; ShutdownDump []byte; HasShutdownDump bool
                    TotalExecutionMicros uint64; MaxDurationMicros *uint64; MaxSuspensions *uint64; RestoredScriptName *string }
type PrintSegment struct{ Stream uint8; Text string }
type FunctionCall struct{ FunctionName string; Args []any; Kwargs []value.Pair; CallID uint32; ObjectID string; AllowEagerAwait bool }
type NameLookup struct{ Name string; ObjectID string }
type OsCall struct{ CallID uint32; Op OsOp; Path, Path2 string; Text string; Bytes []byte; Mode string; Parents, ExistOK bool; Key string; Default any; TZ *value.TimeZone; HasTZ bool }
type OsOp uint8   // one per proto arm (2..24); (o OsOp) Name() string → "Path.exists" … "datetime.now"
func DecodeEvent(payload []byte, budget *Budget) (*Event, error)  // ErrProtocol{msg}: "invalid payload from worker: …", "OsCall event with no call", "invalid OS call payload: …", "NameLookup.object_id is not a 16-byte uuid", "unexpected event"
// codec.go — MontyObject ⇄ Go values (hand-written protowire)
type Budget struct{ remaining int64 }; func NewBudget(n int64) *Budget; (b *Budget) Charge(n int64) error /* ErrBudget */
func EncodeValue(buf []byte, v any) ([]byte, error)             // accepts value.* types, Go scalars/slices/maps; rejects Repr/Cycle/OriginSandbox Type inputs
func DecodeValue(b []byte, budget *Budget) (any, error)          // validates dates/timedelta/uuids/exc names while decoding; charges host size (88 B per scalar node, + payload)
func HostSize(v any) int64
// oscall.go
func (o *OsCall) Args() ([]any, value.Pairs)                     // Appendix F.3 shapes: paths as value.Path, mkdir kwargs parents/exist_ok
func (o *OsCall) IsFS() bool; IsWrite() bool; IsExistenceCheck() bool; PrimaryPath() string; NullMessage(dst bool) string; NoHandlerException() *montypb.RaisedException
```

### 6.3 `montypb` (CREATED, generated)
`protoc -I proto --go_out=montypb --go_opt=module=github.com/asalimonov/montygo/montypb --go_opt=Mmonty/v1/monty.proto=github.com/asalimonov/montygo/montypb proto/monty/v1/monty.proto`, run by `make generate`, writes `montypb/monty.pb.go`. Used for value-free messages and as the differential oracle in `internal/wire` tests (`codec_differential_test.go`: random value trees encoded by both codecs must be byte-identical; decoded by both must be equal).

### 6.4 `internal/worker` (CREATED)
```go
type Status struct{ Code int; Signal string; Known bool }   // String(): "exit status: 65" | "signal: 9 (SIGKILL)" | "" ; (native format mirrors Rust ExitStatus Display)
type Worker interface {
    Send(ctx context.Context, frame []byte) error
    Recv(ctx context.Context) ([]byte, error)          // FrameReader.Next; io.EOF → ErrTruncated inside a checkout
    Kill()                                             // idempotent, immediate
    Wait(ctx context.Context) Status                   // reap; ctx bounds the wait (grace)
    PID() (int, bool)
    Kind() Kind                                        // KindSubprocess | KindWasm | KindWebSocket
    Alive() bool                                       // non-blocking liveness (try-wait)
}
type Spawner interface{ Spawn(ctx context.Context) (Worker, error) }
// subprocess_unix.go  (//go:build unix)
type SubprocessSpawner struct{ BinaryPath string }
func (s *SubprocessSpawner) Spawn(ctx context.Context) (Worker, error)   // exec.Command(bin, "subprocess"), Env: []string{} (Windows would add SystemRoot), stdin/stdout pipes, stderr inherited, Setpgid false
// wasm.go
type WasmSpawner struct{ rt wazero.Runtime; compiled wazero.CompiledModule; once sync.Once; cacheDir string }
func NewWasmSpawner(ctx context.Context, blob []byte, cacheDir string) (*WasmSpawner, error)   // compile once per process
func (s *WasmSpawner) Spawn(ctx context.Context) (Worker, error)   // instantiate with io.Pipe stdin/stdout, stderr → io.Discard (or WORKER_STDERR), args ["monty","subprocess"], empty env; runs InstantiateModule in a goroutine; Kill = cancel instantiate ctx + module.Close; Wait maps sys.ExitError code (65 → Status{65}); ExitCodeContextCanceled → killed
func (s *WasmSpawner) Close(ctx context.Context) error
// websocket.go
type WebSocketDialer struct{ URL string; Headers func() (map[string]string, error); DialTimeout time.Duration }
func (d *WebSocketDialer) Spawn(ctx context.Context) (Worker, error)   // coder/websocket, one binary message per frame, User-Agent "monty-pool/0.0.23" first, traceparent/tracestate, reader goroutine, ping/pong consumed; Kill = close frame ≤1s then Close; Wait → Status{Known:false}
```

### 6.5 `internal/wasmblob` (CREATED)
```go
//go:embed monty.wasm.zst
var compressed []byte
func Bytes() ([]byte, error)          // zstd decode once (sync.Once), sha256 verified against embedded blob.sha256
func SHA256() string
func DefaultCacheDir() (string, error) // os.UserCacheDir()/montygo/wazero/<sha256>
```

### 6.6 `internal/pool` (CREATED)
```go
// errors.go
type ErrorKind uint8
const ( KindCrashed ErrorKind = iota; KindTimeout; KindProtocol; KindRuntime; KindTyping; KindExhausted; KindSpawn; KindFinished; KindDisconnected; KindShutdown )
type Error struct{ Kind ErrorKind; Message string; Status worker.Status; Announced bool; Timeout time.Duration; Exception *montypb.RaisedException; Diagnostics string; Dump []byte; HasDump bool; WorkerLost bool }
func (e *Error) Error() string        // Display strings from Appendix D.5
// pool.go
type Config struct{ Spawner worker.Spawner; MinProcesses, MaxProcesses int; CheckoutTimeout, RequestTimeout, DurationLimitGrace time.Duration; GraceDisabled bool; MaxCheckoutsPerWorker int; SingleUse bool; Telemetry *telemetry.Recorder }
type Pool struct{ mu sync.Mutex; idle []*slot; total int; notify chan struct{}; closed bool; cfg Config }
func New(ctx context.Context, cfg Config) (*Pool, error)           // prewarm MinProcesses (not for SingleUse)
func (p *Pool) Checkout(ctx context.Context, repl ReplConfig, opts CheckoutOptions) (*Checkout, error)
func (p *Pool) Close(ctx context.Context) error                     // Shutdown to idle workers, reap with 500ms grace concurrently, kill
func (p *Pool) acquire(ctx) (*slot, error); release(s *slot); discard(s *slot, reason string)
// checkout.go
type ReplConfig struct{ ScriptName string; Limits *montypb.ResourceLimits; TypeCheck bool; TypeCheckStubs *string; TypeCheckFormat montypb.TypeCheckFormat; TypeCheckColor bool; AssertMessageAnnotations *uint32; PrintFlushIntervalMs *uint32 }
type CheckoutOptions struct{ TraceParent *string; TraceState *string; ConnectHeaders map[string]string }
type OnPrint func(ctx context.Context, stream uint8, text string) error
type TurnEvent struct{ Kind TurnKind; Value any; Call *wire.FunctionCall; Os *wire.OsCall; Lookup *wire.NameLookup; PendingCallIDs []uint32 }
type Checkout struct{ /* worker, pending, feedMounts *mountfs.Table, budget state (§8.3), turnInFlight, cwdSet, restoredScriptName */ }
func (c *Checkout) Feed(ctx, code string, inputs []wire.NamedValue, mounts []*mountfs.Spec, cwd *string, skipTypeCheck bool, onPrint OnPrint) (*TurnEvent, error)
func (c *Checkout) Resume(ctx, r wire.ExtResult, onPrint OnPrint) (*TurnEvent, error)
func (c *Checkout) ResumeNameLookup(ctx, r wire.ResumeNameLookup, onPrint OnPrint) (*TurnEvent, error)
func (c *Checkout) ResumeFutures(ctx, results []wire.FutureResult, onPrint OnPrint) (*TurnEvent, error)
func (c *Checkout) ResumeFromMounts(ctx, onPrint OnPrint) (*TurnEvent, bool, error)   // (event, handled, err)
func (c *Checkout) AbortFeed(ctx, exc *montypb.RaisedException, onPrint OnPrint) (*TurnEvent, error)
func (c *Checkout) Dump(ctx) ([]byte, error)
func (c *Checkout) Restore(ctx, state []byte, mounts []*mountfs.Spec, onPrint OnPrint) (*TurnEvent, *string, error)   // nil event = idle dump
func (c *Checkout) InstallDependencies(ctx, reqs []string) error
func (c *Checkout) Finish(ctx) error        // Reset → Ok → release; else discard
func (c *Checkout) Abandon()                // kill + release capacity
func (c *Checkout) PID() (int, bool)
func (c *Checkout) CallbackContext() context.Context   // span-carrying ctx for host callbacks (telemetry)
// budget.go
type sessionBudget struct{ durationBudget *time.Duration; reportedExec uint64; suspensionLimit uint64; suspensionsSeen uint64; pendingLoad *sessionBudget }
func (b *sessionBudget) deadline(requestTimeout, grace time.Duration, graceDisabled bool, control bool) (time.Duration, bool)
func (b *sessionBudget) observe(ev *wire.Event)    // ratchet exec, adopt max_duration, tighten limit, count suspensions
func (b *sessionBudget) overLimit(ev *wire.Event) bool
```

### 6.7 `internal/mountfs` (CREATED) — full contract in Appendix E
```go
type Mode uint8; const ( ReadOnly Mode = iota; ReadWrite; Overlay ); func ParseMode(s string) (Mode, error)
type Root struct{ virtualPath, hostPath string; root *os.Root }
func OpenRoot(virtualPath, hostPath string) (*Root, error)          // InvalidMount errors (TypeError)
func (r *Root) Close() error
type Spec struct{ Root *Root; Mode Mode; WriteBytesLimit *uint64; MemoryUsageLimit uint64 }
type Table struct{ mounts []*mount /* sorted by len(vpath) desc */ }
func BuildTable(specs []*Spec) *Table                                // fresh overlay state per table; no I/O
func (t *Table) HandleOsCall(ctx context.Context, call *wire.OsCall) Outcome   // Outcome{Handled bool; Value any; Err *MountError}
func (t *Table) FirstVirtualPath() (string, bool)
type MountError struct{ Kind ErrKind; Path, Path2 string; Msg string; IoKind IoKind; Limit uint64; Utf8 *Utf8Error }
func (e *MountError) Exception() *montypb.RaisedException          // Appendix E.3.2 table incl. UnicodeErrorData
func NormalizeVirtualPath(p string) string; func ValidateCwd(p string) (string, error)
func rejectOverlongPath(p string) error; rejectNullBytes(p, msg string) error; rejectDriveOrUNC(rel, normalized string) error
func FormatBytesPretty(n uint64) string
// direct.go: exists/isFile/isDir/isSymlink/readText/readBytes/stat/iterdir/unlink/rmdir/write/append/mkdir/rename/open on *os.Root
// overlay.go + state.go: OverlayState{entries *btree(ordered map), usage int64}; entry kinds File/RealFileRef/Directory/Deleted; ENTRY_MEMORY_USAGE=256, ENTRY_SIZE=48, LISTING_ENTRY=128, REAL_DESCENDANT=512
// budget.go: MemoryBudget{available, limit}; Check/Shrink/Halved; write limit check/commit
```
`os.Root` escape → Go returns `*os.PathError` wrapping `os.ErrPermission`? (Go 1.24+: `openat` escapes yield `errors.Is(err, os.ErrPermission)` isn't guaranteed; it returns an error containing "path escapes from parent"). Rule: `errors.Is(err, os.ErrInvalid) || strings.Contains(err.Error(), "escapes from parent")` → `PathEscape`; a real `syscall.EACCES` → `Io(PermissionDenied)`. Both render `[Errno 13] Permission denied`.

### 6.8 `internal/telemetry` (CREATED)
```go
type Recorder struct{ tracer trace.Tracer; meter metric.Meter; logger log.Logger; instruments struct{…}; enabled atomic.Bool }
func Configure(c Components) (*Recorder, error); func Current() *Recorder /* may be nil */
func (r *Recorder) SessionStart(ctx, scriptName string) (context.Context, func(outcome string))     // span "session {script_name}"
func (r *Recorder) RunStart(ctx, code string, inputs []wire.NamedValue) (context.Context, func(result any, err error, suspensions int, execMicros uint64))   // span "run code"
func (r *Recorder) SuspensionStart(ctx, ev *wire.Event) (context.Context, func(result wire.ExtResult))  // "call {function_name}" | "os call {function}" | "name lookup {name}" | "resolve futures"
func (r *Recorder) Print(ctx, stream string, text string)     // log record body "print stdout"/"print stderr", attr text
func (r *Recorder) Snapshot(ctx, bytes int, kind string)       // monty.snapshot.bytes
func (r *Recorder) Pool… (workers live/idle/suspended up-down counters, checkout wait histogram, worker terminated counter{reason}, session duration histogram{outcome})
func (r *Recorder) Turn(duration time.Duration, kind string); Frame(bytes int, direction string)
func EncodeAttr(v any) (string, bool)                           // logfire-style JSON, 64 KB cap → attr `length_limit_exceeded=true`
func (r *Recorder) Flush(ctx) error
```
Span/metric names and attributes are fixed to the upstream set (Appendix A.11); values recorded only when instrumented; sandbox-supplied dimensions never become metric attributes.

## 7. Key data structures (root package, unexported)

```go
// session.go
type Session struct {
    mu        sync.Mutex          // one protocol turn at a time; callers queue
    co        *pool.Checkout
    pool      *Pool
    closed    atomic.Bool
    driven    bool                // any FeedRun/FeedStart/LoadSession/LoadSnapshot/InstallDependencies
    broken    error               // poison; every later call returns it
    instances *instanceStore
    tele      context.Context     // session span ctx (Background when not instrumented)
}

// classinstance.go
type wrapper interface {               // ClassInstance and ClassType
    id() string; name() string
    eagerAttrs() ([]value.Pair, error)                 // policy + ConvertValue applied
    lazyAttr(name string) (any, error)                  // ErrAttrNotExposed → AttributeError
    callMethod(ctx, name string, args []any, kwargs Kwargs) (any, error)
    convertValue(name string, v any) (any, error)
}
type instanceStore struct {
    mu   sync.Mutex
    byID map[string]wrapper        // uuid → wrapper; one namespace for instances and class types
}
func (s *instanceStore) register(w *ClassInstance) error   // same id, different object → ValueError "wrapper id '<id>' already identifies a different object in this session"
func (s *instanceStore) registerClass(c *ClassType) error; registerClassIfAbsent(c *ClassType)
func (s *instanceStore) get(id string) (wrapper, bool)

// drive loop
type turnAnswerer struct {           // one per FeedRun / per snapshot chain
    sess    *Session
    lookup  map[string]any
    os      OSHandler
    pending map[uint32]*pendingFuture   // callId → future
    onPrint pool.OnPrint
    print   *printTarget
}
type pendingFuture struct{ f *Future; convert func(any) (any, error) }
type printTarget struct{ target PrintTarget; failure error }   // captures first Print error; later writes dropped
type snapshotDriver struct{ answerer *turnAnswerer; print *printTarget; sess *Session }   // shared by a snapshot chain
```

Value flow at the boundary (`prepare`/`restore`, root `convert.go`):
```
host value ──prepare(store, depth≤48)──▶ value.* tree (markers for wrappers/proxies) ──wire.EncodeValue──▶ frame
frame ──wire.DecodeValue(budget)──▶ value.* tree ──restore(store)──▶ host value (original object | *ClassProxy | Type)
```
`prepare` rules (Appendix A.8 / F.1): scalars pass; `*ClassInstance` → `ClassInstanceMarker{Type: classType marker (attrs from ClassType eager policy), InstanceID, Attrs: eagerAttrs}` and registers instance + class (if absent); `*ClassType` → `Type{Origin: OriginHost, Name, ID, Attrs}` and registers; `*ClassProxy` → `ClassInstanceMarker{Type: proxy.type with empty attrs, InstanceID: proxy.ID, Attrs: nil}`; slices/arrays → `[]any`; `Tuple` kept; maps → `*Dict` with sorted keys (`map[string]any` keys sorted; other comparable key types via `fmt`-free ordering: ints numeric, strings lexical); `*Dict`, `*Set`, `*FrozenSet` recursed; `value.Type{Origin: OriginSandbox}` input → ConversionError `raw Type markers are not accepted — pass the class through ClassType(...)`; `value.ClassInstanceMarker` input → ConversionError `raw ClassInstance markers are not accepted — wrap it in ClassInstance(...)`; struct / pointer-to-struct not wrapped → ConversionError `Cannot convert <TypeName> instance to a Monty value — wrap it in ClassInstance(...)`; func values → `value.Function{Name}`; other → ConversionError `Cannot convert Go <type> to Monty value`; depth > 48 → `Max input depth exceeded`.
`restore`: `ClassInstanceMarker` → `store.get(id)` is a `*ClassInstance` → its `Instance()`; else `*ClassProxy{Name, ID, IsDataclass, Attributes}`; `Type{Origin: OriginHost}` → registered `*ClassType` if known, else the `Type` value unchanged; containers recursed; everything else as-is.

## 8. Data flows

### 8.1 Pool creation (`monty.New`)
1. Validate options (§5.2). Resolve backend: `BackendNative` → `FindMontyBinary(BinaryPath)` (error propagates); `BackendWasm` → `wasmblob.Bytes()` + `worker.NewWasmSpawner` (compile once, cache dir); `BackendAuto` → try native resolution, on failure fall back to wasm (the resolution error is kept in `Pool.autoFallbackReason` for diagnostics).
2. `pool.New(cfg)` validates sizes (`invalid pool size: min_processes=… max_processes=…` → OptionError), prewarms `MinProcesses` workers (spawn errors fail `New`), registers pool metrics.
3. Returns `*Pool`. Failure modes: binary missing (native) → error text from §5.13; wasm blob corrupt → `wasm worker blob checksum mismatch`; compile failure → `failed to compile the embedded monty worker: <err>`.

### 8.2 Checkout
1. `ErrPoolClosed` if closed. Encode `ReplConfig` (formats, annotations, flush interval, limits: `MaxRecursionDepth`/`MaxSuspensions` default 1000 always sent; duration → micros).
2. Telemetry: `Recorder.SessionStart(ctx, scriptName)` → session ctx; `traceparent` derived for `Configure.trace_parent`.
3. `pool.Checkout`: `acquire` (idle LIFO pop skipping dead workers → spawn if `total < max` → wait with `CheckoutTimeout` → `Exhausted` "no monty worker became available within the checkout timeout"); send `Configure` (deadline `RequestTimeout`); reply must be `Ok` else `Protocol("unexpected reply to Configure: <kind>")` and discard.
4. Wrap in `*Session` with a fresh `instanceStore`.

### 8.3 `FeedRun` happy path
```
FeedRun(ctx, code, opts)
  lock session; ensureUsable (closed → ErrSessionClosed; broken → broken)
  driven = true; printTarget := newPrintTarget(opts.Print); answerer := newTurnAnswerer(...)
  inputs := prepareInputs(opts.Inputs)            // ConversionError rejects the call, feed never starts
  mounts := specsFrom(opts.Mount)                 // closed mount → OptionError
  ev, err := co.Feed(ctx, code, inputs, mounts, cwd(opts), skip, answerer.onPrint)
  loop:
    switch:
      pool.Error(Runtime)  → printTarget.throwIfFailed; SyntaxError if exc_type=="SyntaxError" else RuntimeError; return
      pool.Error(Typing)   → TypingError
      pool.Error(Crashed/Timeout/Disconnected/Shutdown) → poison(CrashedError{TimedOut, ExitStatus}); return
      pool.Error(Protocol/Finished) → poison(ProtocolError); return
      Complete             → throwIfFailed; return restore(value)
      suspension           → throwIfFailed (a print failure here POISONS: worker awaits a resume that never comes)
                             ev, err = answerer.answer(ctx, ev); on answer error: broken ??= err; return
```
`cwd(opts)`: `Cwd != ""` → validated absolute path (else `ValueError` from `ValidateCwd`, returned as `*RuntimeError{TypeName:"ValueError"}` to mirror upstream); `""` → nil (pool picks first mount / "/" on first feed, keeps afterwards).

### 8.4 Suspension answering (`turnAnswerer.answer`)
| Event | Steps |
|---|---|
| FunctionCall, ObjectID=="" | own-key lookup in `lookup`; absent → `Resume(NotFound)`; value not callable → `Resume(Error TypeError "'<pytype>' object is not callable")`; callable → `fn.Call(cbCtx, restore(args), kwargsRecord)`; panic → recovered as RuntimeError; `error` → `Resume(Error(errParts))`; `*Future` → `AllowEagerAwait ? awaitThen ResumeFutures([{callId, result}]) : register + Resume(Future)`; value → `prepare` then `Resume(Return)` (prepare error → `Resume(Error TypeError <msg>)`) |
| FunctionCall, ObjectID set | wrapper := store.get; name starts with `_` && name != `__call__` → AttributeError `'<Name>' object has no attribute '<n>'`; missing wrapper → RuntimeError `no host object registered for method call '<name>' (id <id>) — the instance store is empty after loading a dump into a fresh session`; `wrapper.callMethod` (ClassType `__call__` → Construct → new ClassInstance registered → returned as marker); `ErrAttrNotExposed` → AttributeError; futures as above |
| NameLookup, ObjectID set | wrapper missing or `_`-prefixed → `ResumeNameLookup(Undefined)` (AttributeError); `lazyAttr` → `ErrAttrNotExposed` → Undefined; other error → `ResumeNameLookup(Error errParts)`; ok → `prepare` → `ResumeNameLookup(Value)` (prepare error → Error TypeError, raised in sandbox) |
| NameLookup plain | absent → Undefined (NameError); callable → `ResumeNameLookup(Value: value.Function{Name})`; value → `prepare` → Value (ConversionError → in TS rejects the turn host-side; Go: return ConversionError and poison? **No**: mirror TS — the turn is rejected with `ConversionError` and the session stays usable? In TS a rejected `resumeNameLookup` leaves the worker suspended; the next feed fails with "feed called while a suspension is awaiting an answer". Go: on ConversionError answer the lookup with `Error(TypeError <msg>)` instead so the session stays consistent, and ALSO return the ConversionError to the caller. Documented divergence `porting:improvement-over-upstream`.) |
| OsCall | `co.ResumeFromMounts` → handled → continue with its event; `os == nil` → `Resume(NotHandled)`; `os(cbCtx, name, args, kwargs)`; `NotHandled` sentinel → NotHandled; error → Error; value → prepare → Return |
| ResolveFutures | unknown id → ProtocolError `worker reported unknown pending call id <id>`; empty → `worker reported ResolveFutures with no pending call ids`; wait on first settled (`reflect.Select` over `Done()` channels + ctx); collect all settled; `ResumeFutures(results)` |

Error → exception parts (`errParts`): `*RaisedError` → `{ExcType, Message}` (ExcType validated against Python names else RuntimeError); `*RuntimeError`/`*SyntaxError`/`*TypingError` → their `Exception()`; other → `{"RuntimeError", err.Error()}`.

### 8.5 `FeedStart` / snapshots
`FeedStart` = same preamble; the driver wraps each event: Complete → `*Complete{restore(v)}`; Runtime/Typing → error (after `throwIfFailed`); Crashed/Protocol → poison; OsCall → `*FunctionSnapshot{IsOSFunction: true, FunctionName: op.Name(), Args, Kwargs, CallID}`; FunctionCall → `*FunctionSnapshot`; NameLookup → `*NameLookupSnapshot`; ResolveFutures → `*FutureSnapshot`. Every snapshot is single-use (`used.CompareAndSwap`); `ResumeAuto` delegates to the shared `turnAnswerer` (identical resolution to `FeedRun`, one step). `Resume(value)` → prepare → `co.Resume(Return)`; prepare failure degrades to `Resume(Error)`. `ResumeNotHandled` checks `IsOSFunction` before claiming. `Dump` → `co.Dump` (session stays suspended).

### 8.6 Dump / load
`Session.Dump` → `co.Dump` (control turn, deadline `RequestTimeout`). `LoadSession(state)`: `claimFresh` (driven → ErrNotFresh) → `co.Restore(state, nil, print)`; reply suspension → `failedLoad(ErrDumpIsSuspended)`; ok → return nil. `LoadSnapshot(state, opts)`: `claimFresh` → `co.Restore(state, mounts, print)`; nil event → `failedLoad(ErrDumpIsIdle)`; else driver.advance(event). `failedLoad` = poison + `co.Finish` (errors swallowed) → not retryable. `InstallDependencies` sets `driven`; empty list → no frame; sandbox worker → RuntimeError `dependency installation is only supported by the CPython worker` (from the worker), session usable.

### 8.7 Turn engine (`internal/pool.Checkout.turn`)
```
turn(ctx, req, control bool, onPrint):
  ensureReady()            // Finished → KindFinished; turnInFlight → discard + KindProtocol(cancelled)
  deadline := budget.deadline(cfg.RequestTimeout, cfg.DurationLimitGrace, control)
  turnInFlight = true; defer turnInFlight = false (only on normal completion)
  tctx := ctx (+deadline)
  payload := req.Encode()  // ErrDepth → Runtime(RuntimeError "Max input depth exceeded"); too large → Runtime(RuntimeError "request frame of N bytes exceeds the maximum of M bytes"); nothing sent
  w.Send(tctx, payload)   // io error → poison("sending a request")
  for:
    raw, err := w.Recv(tctx)
      ctx deadline exceeded → poisonTimeout()   // Kill immediately, Wait(100ms), KindTimeout{Timeout: deadline}
      ctx cancelled by caller → discard worker, KindProtocol("a previous protocol turn was cancelled mid-flight; the worker was discarded")  (session lost)
      EOF/io error → poison("waiting for a reply")
    ev := wire.DecodeEvent(raw, budget)   // ErrProtocol/ErrBudget → discard, KindProtocol("invalid payload from worker: …")
    if ev.Kind == Print: for seg: onPrint(seg) (errors captured by root printTarget; pool ignores); continue
    budget.observe(ev)
    if budget.overLimit(ev): sendAbortFeed(); abortInFlight = true; continue   // next non-Print must be Error/Fatal/ShutdownDump else Protocol("worker answered AbortFeed with something other than an Error")
    if ev.RestoredScriptName != nil: remember
    switch ev.Kind: (Appendix D.3 table)  → set pending / clear feedMounts / build TurnEvent or Error
```
`poison(context)`: take worker, clear pending & feedMounts; websocket → KindDisconnected; else `Kill? no: Wait(100ms grace) then Kill`; status.Code == 65 → `KindRuntime{Exception: MemoryError "the worker exceeded its memory limit and was terminated", WorkerLost: true}`; else `KindCrashed{Status, Message: "monty worker crashed while <context>"}`. FatalError event → Wait(100ms) → `KindCrashed{Announced: true, Message: "monty worker crashed: <reason>"}`. Wasm: `sys.ExitError` code 65 → same; `ExitCodeContextCanceled` after our own kill → Timeout/cancel; module trap (`err != nil`, non-exit) → crash with `Status{Known:false}`.

Deadline formula: `backstop = (durationBudget != nil && !graceDisabled) ? saturating(durationBudget − reportedExec) + grace : none`; execution turns `min(requestTimeout, backstop)`, control turns `requestTimeout` only; no deadline when both absent.

### 8.8 Mount servicing (`ResumeFromMounts`)
Requires `pending = call with os`; `feedMounts == nil` → not handled. `table.HandleOsCall(ctx, call)`: not handled → keep pending, return `(nil, false)`; handled → `Resume(Return(value))` or `Resume(Error(exception))`; if the Return is rejected pre-send (depth/size) while pending is set → re-answer with `Error(that exception)`. Host I/O runs on the calling goroutine (no deadline armed, inside `turnInFlight`).

### 8.9 WebSocket transport — see §8.9 (added after Appendix G; based on Appendix D.7): single-use workers, `MinProcesses` forced 0, one binary message per frame, no `Reset` on finish (close frame), `ShutdownDump` → `ShutdownError{Dump}`, disconnect → `DisconnectError`, exit codes unknown, `PID` none.

### 8.10 OS helpers (`osaccess`) — see §8.10 (after Appendix G).

### 8.11 Telemetry flow
`Instrument` sets the process-wide recorder (must precede `New`). Checkout → session span; each `FeedRun`/`FeedStart` chain → `run code` span (code, inputs attrs, result/exception on end, `monty.run.duration`, `monty.run.execution_time` from `TotalExecutionMicros`, `monty.run.suspensions`); each suspension → child span (`call {name}` / `os call {name}` / `name lookup {name}` / `resolve futures`) whose ctx is handed to host callbacks (`Function.Call(ctx …)`, `OSHandler`, providers) so user spans nest; print segments → log records parented to the run span; dump/load → `monty.snapshot.bytes`; frames → `monty.wire.frame.bytes`; pool → live/idle/suspended up-down counters, `monty.pool.checkout.wait`, `monty.pool.worker.terminated{reason}`, `monty.pool.session.duration{outcome: ok|error|abandoned}`. Attribute values encoded logfire-style JSON capped at 64 KB (`length_limit_exceeded`). Recording is synchronous; a panicking/erroring SDK component disables that signal only (`spansDisabled/logsDisabled/metricsDisabled`), never the others.

### 8.12 Pool close / session close / abandonment
`Session.Close`: lock; if broken or worker lost → `co.Abandon()` (kill, capacity released) else `co.Finish()` (Reset→Ok→idle, recycle when `checkoutsServed >= MaxCheckoutsPerWorker` or single-use). Second `Close` → nil. A `*Session` garbage-collected without `Close` leaks a worker until `Pool.Close` (documented; a `runtime.AddCleanup` kills the worker as a safety net). `Pool.Close`: drain idle, send `Shutdown`, concurrently `Wait(500ms)` then `Kill`; checked-out sessions keep their workers; wasm runtime closed after the last worker.

### 8.13 Failure-mode table
| Situation | Where detected | Error to caller | Worker | Session |
|---|---|---|---|---|
| Sandbox exception | `Error` event | `*RuntimeError` / `*SyntaxError` | kept | usable |
| Type check failure | `TypingError` event | `*TypingError` | kept | usable |
| Print callback returns error | printTarget | that error (first) at turn boundary | kept (on Complete/Error) / **lost** (on suspension) | usable / poisoned |
| Host function panics | answerer | recovered → in-sandbox `RuntimeError: <panic value>` | kept | usable |
| Unconvertible input / externalLookup value | prepare | `*ConversionError` before send | kept | usable (feed never started) |
| Unconvertible host return | prepare in answerer | in-sandbox `TypeError: Cannot convert …` | kept | usable |
| Too deep input | encode | `*RuntimeError{RuntimeError, "Max input depth exceeded"}` | kept | usable |
| Frame > 256 MiB | encode | `*RuntimeError{RuntimeError, "request frame of N bytes exceeds the maximum of M bytes"}` | kept | usable (suspension still answerable) |
| Worker EOF without FatalError | Recv | `*CrashedError{Message: "monty worker crashed while <ctx>" (+ " (<status>)")}` | lost | poisoned |
| FatalError event (e.g. version skew) | turn | `*CrashedError{Message: "monty worker crashed: <reason>"}` | lost | poisoned |
| Exit code 65 | poison | `*RuntimeError{MemoryError, "the worker exceeded its memory limit and was terminated"}`; next call → `*ProtocolError{"this checkout has already been finished"}` | lost | finished |
| Request timeout / duration backstop | deadline | `*CrashedError{TimedOut: true, "monty worker killed after exceeding request timeout of <dur>"}` | killed | poisoned |
| Caller ctx cancelled mid-turn | Recv | `ErrTurnCancelled` (session) | killed | poisoned |
| Caller ctx cancelled while queued on the session mutex | lock | `ctx.Err()` | kept | usable |
| Suspension budget exceeded | budget | in-sandbox uncatchable `RuntimeError: suspension limit N exceeded` → `*RuntimeError` | kept | usable |
| Malformed frame from worker | decode | `*ProtocolError{"monty worker protocol error: invalid payload from worker: …"}` | discarded | poisoned |
| Decode budget exceeded | decode | `*ProtocolError` | discarded | poisoned |
| Pool exhausted | acquire | `*ProtocolError`? **No** → `ErrCheckoutTimeout` ("no monty worker became available within the checkout timeout") | — | — |
| Spawn failure | acquire | `*SpawnError{"failed to spawn monty worker: <msg>"}` | — | — |
| WebSocket dropped | Recv | `*DisconnectError` | lost | poisoned |
| Server shutdown | `ShutdownDump` | `*ShutdownError{Dump}` | lost | poisoned (dump restorable elsewhere) |
| Wasm blob checksum mismatch | New | error | — | — |
| Load of wrong dump kind | Load* | `ErrDumpIsSuspended` / `ErrDumpIsIdle` | released | poisoned |

## 9. Test plan

### 9.1 Harness (`internal/testutil` + `*_test.go` in root)
- `TestMain` creates one pool per backend listed in `MONTY_TEST_BACKENDS` (default `native,wasm`; `native` skipped with a message when no binary resolves and `MONTY_TEST_REQUIRE_NATIVE` unset). Each ported spec file becomes `Test<Spec>` with sub-tests `t.Run(backend, …)` → `t.Run("<verbatim TS title>", …)`.
- `run(t, backend, code, RunOptions)` splits checkout-level vs feed-level options exactly like `helpers.ts`.
- `assertMemoryError(t, err, expectedUsed, max)` with ±1024 B tolerance; per-backend expected figures in a table (native = node figures; wasm re-derived on first run and pinned).
- Fixtures: temp dirs for mounts; `mkfifo` guarded by `runtime.GOOS`; `/proc/<pid>/environ` check on linux only; process kill via `syscall.Kill(pid, SIGKILL)` on native only.
- `docs/parity/tests.md` lists every TS title → Go test name or exclusion tag.

### 9.2 Spec → Go file mapping
| TS spec | Go file | Notes |
|---|---|---|
| basic, public_api, repl, inputs | `basic_test.go`, `public_api_test.go`, `repl_test.go`, `inputs_test.go` | `await using` → `defer Close`; `Buffer.isBuffer` → `[]byte` non-empty |
| types, value_codec | `types_test.go`, `wire/codec_test.go` | BigInt → `*big.Int`; Map/Set → `*Dict`/`*Set`; `__tuple__` → `Tuple`; codec unit tests re-target `wire.EncodeValue` |
| external, async | `external_test.go`, `async_test.go` | JS `Error.name` cases → `*RaisedError{ExcType}`; inherited-property tests → "unexported/absent key" tests; Symbol → `chan int` (unconvertible) |
| exceptions | `exceptions_test.go` | class unit tests → constructing Go error values and `errors.As` |
| type_check | `type_check_test.go` | inherited-names test → invalid format string test |
| limits | `limits_test.go` | transport unit test → `pool` package unit test with a fake `Worker` |
| print | `print_test.go` | identical host error object → `errors.Is(err, sentinel)` |
| feed_start | `feed_start_test.go` | mounts branch: native+wasm both support mounts in Go (improvement) |
| pool | `pool_test.go` | wasm transport unit → `worker/wasm_test.go`; kill via `syscall.Kill` |
| install_dependencies, mount | `install_dependencies_test.go`, `mount_test.go` | mount tests run on both backends |
| class_instance | `class_instance_test.go` | JS machinery hardening (8) → excluded, replaced by `unexported members are unreachable` + `Names cannot name unexported members` |
| node_telemetry, node_telemetry_components, node_callback_context | `telemetry_test.go`, `callback_context_test.go` | OTel-go `tracetest.InMemoryExporter`, `metricdata` reader, `logtest` recorder; AsyncLocalStorage → `context.WithValue` propagated through callback ctx |
| node_docs, node_entrypoint_exports | `example_test.go`, `public_api_test.go` | A16 |
| wasm_* | `wasm_*_test.go` (backend wasm only) | memory-limit #3 expectation changes to MemoryError classification (documented) |
| Python test_websocket | `websocket_test.go` | Go relay fixture (§8.9) |
| Python test_os_access* | `osaccess/*_test.go` | A18 |

### 9.3 Unit tests (internal)
`wire`: frame reader boundary cases (partial header, coalesced frames, EOF mid-frame, oversize), codec differential vs `montypb` (fuzz + golden), budget trip, depth costs; `pool`: fake worker driving every event kind, budget/deadline formulas, AbortFeed handshake, restore budget swap; `mountfs`: port of `monty-fs/tests` cases (path policy strings, overlay semantics, budgets); `value`: hash equivalence, repr; `telemetry`: encode cap.

## 10. Build & tooling

`Makefile` targets: `generate` (protoc → montypb), `build-worker` (`cargo build -p monty-runtime` in `$MONTY_SRC` (default `../monty`), macOS: exports `SDKROOT=$(xcrun --sdk macosx26.5 --show-sdk-path)` fallback note), `build-wasm` (`cd worker-wasm && cargo build --release --target wasm32-wasip1`, then `zstd -19` → `internal/wasmblob/monty.wasm.zst` + `blob.sha256`), `test` (`go test ./... -race`), `test-native`, `test-wasm`, `lint` (`golangci-lint`), `bench`.
`worker-wasm/Cargo.toml`: `monty-proto = { git = "https://github.com/pydantic/monty", rev = "f8acf4fa", features = ["worker"] }`, `monty-alloc = { …, features = ["exit-code"] }`, `monty-types`; `[patch."https://github.com/pydantic/monty"]` entries switched on by `MONTY_SRC` through `.cargo/config.toml` generated by `make build-wasm MONTY_SRC=../monty`. `src/subprocess.rs` is a verbatim copy of upstream (MIT notice kept); `src/main.rs` installs `monty_alloc::LimitedAllocator` and calls `subprocess::run()`.
CI (`.github/workflows/ci.yml`): matrix `ubuntu-latest`, `macos-latest`; steps: rustup stable + wasip1 target, `cargo build -p monty-runtime` from the git rev (cache), `make build-wasm` verify checksum equals committed blob, `go vet`, `go test -race` with `MONTY_TEST_BACKENDS=native,wasm`.

## 11. Documentation deliverables
- `README.md`: install (`go get`), quick start, inputs, external lookup (sync/async/futures), class instances/types, snapshots, print, mounts, resource limits, assert annotations, type checking, errors, pool options, backends (native/wasm/websocket), OS helpers, telemetry, value conversion table (Go column), binary resolution. Every snippet is an `Example` test.
- `docs/architecture/overview.md` (layering, transports), `protocol.md` (framing, codec, budget), `pool.md` (lifecycle, deadlines, classification), `values.md` (mapping, wrappers), `mounts.md`, `telemetry.md`, `testing.md` (backend matrix, parity lists).
- `docs/parity/api.md` (TS export → Go identifier), `docs/parity/tests.md`.

## 12. `changelogs/v0.0.23.md` specification
Format follows upstream releases: title `# montygo v0.0.23 — 2026-MM-DD`, then sections: **Upstream baseline** (Monty 0.0.23 + main@f8acf4fa, protocol 3, dump version), **What's Added** (bullet per feature area, mirroring the README headings: pool/sessions, external lookup & futures, class instances/types, snapshots & dumps, print collectors, mounts, limits, type checking, errors, backends: native subprocess and embedded wasm worker via wazero, WebSocket transport, OS helpers, OpenTelemetry), **Parity status** (table: TS feature → status: parity / improvement / gap; explicit gaps: Windows, ClassTypeProxy, Node telemetry queueing), **Test parity** (counts: ported, adapted, excluded with reasons; both backends), **Known limitations** (wasm ms clock, compile cost, memory figures), **Build notes** (Rust toolchain ≥ 1.95, SDKROOT on macOS 27 beta), **Full Changelog** link to upstream compare `v0.0.22...v0.0.23` and montygo initial commit.

## 13. NOT CONSIDERED & TODO (final, tagged)
| Item | Tag(s) |
|---|---|
| Windows support (native backend, mount locking, `SystemRoot`) | porting:postponned-to-the-next-task |
| `ClassTypeProxy` for unregistered host classes | postponed |
| Node telemetry bounded-queue delivery semantics | porting:simplification |
| `installDependencies` on a CPython worker | porting:deffered |
| Memory-figure expectations per backend | postponed (derive at implementation) |
| JS prototype hardening tests | porting:deffered (N/A) |
| Decode-budget parity proof (fuzz against Rust decoder) | potential-improvement |
| Cwd/`__file__` edge cases beyond the TS tests | postponed |
| Non-UTF-8 host filenames in mounts | too-complex (follow Appendix E rules; tested only on ext4 in CI) |
| Wazero interpreter fallback for platforms without the compiler (e.g. `GOARCH=386`) | potential-improvement |
| `Instrumentation` as a true OTel-go "instrumentation library" (none exists in otel-go) | porting:simplification |
| Prebuilt native binaries shipped with the module | porting:too-high-risk (size, signing) |
| WebSocket relay server implementation (only the client + a test relay) | porting:postponned-to-the-next-task |
| PEP 723 / CPython worker semantics | porting:deffered |
| Go `time.Time` implicit conversion | postponed (explicit helpers only) |

## 8.9 (final) WebSocket transport (`websocket.go`, `internal/worker/websocket.go`) — Appendix G.6–G.8

Public API (§5.14) adjustments after Appendix G:
```go
type WebSocketOptions struct {
    URL             string
    MaxProcesses    int                                   // default runtime.NumCPU()
    CheckoutTimeout time.Duration                         // 0 = wait forever
    RequestTimeout  time.Duration                         // 0 = Python default 10 s; NoRequestTimeout (-1) disables
    ConnectHeaders  func(ctx context.Context) (map[string]string, error)   // called once per Checkout, before capacity wait and dial
}
const NoRequestTimeout time.Duration = -1
func NewWebSocket(ctx context.Context, opts WebSocketOptions) (*Pool, error)
type DisconnectError struct{ Context string }   // Error(): "monty worker connection closed while <Context>"; Exception(): {"RuntimeError", msg}
type ShutdownError struct{ Dump []byte; HasDump bool }   // Error(): "monty server is shutting down; the request did not run (session dump attached)" / "… did not run"
```
Go has no contextvars; the `ctx` passed to `Checkout` is what `ConnectHeaders` receives (test "sent per checkout" uses `context.WithValue`). Header validation (before dial): name must satisfy `httpguts.ValidHeaderFieldName` → `failed to spawn monty worker: <url>: connect header "<name>": invalid HTTP header name`; value `httpguts.ValidHeaderFieldValue` → `… connect header "<name>" value: failed to parse header value` (`*SpawnError`). Header set: `User-Agent: monty-pool/0.0.23` first, then `traceparent`/`tracestate` when tracing, then caller headers (last wins, case-insensitive).
Flow (`internal/worker.WebSocketDialer.Spawn`): `websocket.Dial(ctx, url, &DialOptions{HTTPHeader, CompressionMode: Disabled})` with dial deadline `RequestTimeout` (else 30 s); `conn.SetReadLimit(MaxFrameLen)`; a reader goroutine (`conn.Read` loop → channel depth 1; non-binary/close/error → terminal `ErrTruncated`); pings answered by coder/websocket automatically while `Read` is being called (the reader goroutine guarantees it). `Send` = `conn.Write(ctx, MessageBinary, frame)` (no length prefix). `Kill` = `conn.Close(StatusNormalClosure, "")` with 1 s write budget then drop (a detached goroutine when called from a non-blocking path). `Wait` → `Status{Known: false}`. Pool config: `SingleUse: true`, `MinProcesses: 0`, no `Reset` on finish (Kill = close frame), `ShutdownDump` event → `KindShutdown{Dump}` (subprocess/wasm: protocol violation), any transport error → `KindDisconnected{Context}`. Session mapping: `KindDisconnected` → `*DisconnectError` (poison), `KindShutdown` → `*ShutdownError` (poison; dump restorable on a fresh checkout via `LoadSession`/`LoadSnapshot`).
Test fixture (`internal/testutil/relay.go`): Go port of `scripts/websocket_relay.py` using `net/http` + `coder/websocket`: per connection spawn `monty subprocess`, pump WS binary → 4-byte-LE-prefixed stdin, stdout frames → binary messages, close stdin on WS end, kill child on teardown; returns `ws://127.0.0.1:<port>`; a header-capturing variant records upgrade headers. A second fixture, `mockChild`, replays the Rust scripted server (`Feed` → `Complete(42)`, else `Ok`; `ShutdownDump` scripts; close-frame assertions) for `internal/pool` websocket tests.

## 8.10 (final) OS helpers — package `osaccess` — Appendix G.1–G.5

```go
package osaccess

// Ops is the Go analog of AbstractOS's overridable methods. Embed Base to get the defaults.
type Ops interface {
    PathExists(path monty.Path) (bool, error); PathIsFile(monty.Path) (bool, error); PathIsDir(monty.Path) (bool, error); PathIsSymlink(monty.Path) (bool, error)
    PathOpen(path monty.Path, mode string) (*monty.FileHandle, error)
    PathReadText(monty.Path) (string, error); PathReadBytes(monty.Path) ([]byte, error)
    PathWriteText(path monty.Path, data string) (int, error); PathWriteBytes(path monty.Path, data []byte) (int, error)
    PathAppendText(path monty.Path, data string) (int, error); PathAppendBytes(path monty.Path, data []byte) (int, error)
    PathMkdir(path monty.Path, parents, existOK bool) error; PathUnlink(monty.Path) error; PathRmdir(monty.Path) error
    PathIterdir(monty.Path) ([]monty.Path, error); PathStat(monty.Path) (StatResult, error)
    PathRename(path, target monty.Path) error; PathResolve(monty.Path) (string, error); PathAbsolute(monty.Path) (string, error)
    Getenv(key string, def any) (any, error); GetEnviron() (map[string]string, error)
    DateToday() (monty.Date, error); DatetimeNow(tz *monty.TimeZone) (monty.DateTime, error)
}
var ErrNotImplemented = errors.New("not implemented")   // returned by a method ⇒ NOT_HANDLED (Python NotImplementedError)
type Base struct{}   // PathOpen/PathAppend* return ErrNotImplemented; DateToday/DatetimeNow use the host clock; all abstract ops return ErrNotImplemented too (Go cannot force implementation)
func Handler(ops Ops) monty.OSHandler            // dispatch: name → method; unknown name → NotHandled; ErrNotImplemented → NotHandled; *monty.RaisedError / OSError-typed errors cross as their Python exception
func Dispatch(ctx context.Context, ops Ops, name string, args []any, kwargs monty.Kwargs) (any, error)

type StatResult struct{ StMode, StIno, StDev, StNlink, StUID, StGID, StSize int64; StAtime, StMtime, StCtime float64 }
func FileStat(size int64, mode int64 /*0 → 0o644*/, mtime *float64) StatResult; func DirStat(mode int64 /*0 → 0o755*/, mtime *float64) StatResult
func (s StatResult) NamedTuple() monty.NamedTuple   // "StatResult" with the 10 fields

type File interface{ Path() monty.Path; SetPath(monty.Path); Name() string; Permissions() int64; Deleted() bool; ReadContent() (any /* string | []byte */, error); WriteContent(any) error; Delete() }
type MemoryFile struct{ … }; func NewMemoryFile(path string, content any, permissions ...int64) *MemoryFile   // String(): "MemoryFile(path=<p>, content='...', permissions=<dec>)" ("b'...'" for []byte)
type CallbackFile struct{ … }; func NewCallbackFile(path string, read func(monty.Path) (any, error), write func(monty.Path, any) error, permissions ...int64) *CallbackFile
type OSAccess struct{ Files []File; Environ map[string]string; tree map[string]any /* File | map */ }
func New(files []File, environ map[string]string, opts ...Option) (*OSAccess, error)   // Option: WithRootDir(string) (must be absolute); nested-under-file → error "Cannot put file <repr> within sub-directory of file <repr>"
func (o *OSAccess) Handler() monty.OSHandler; (o *OSAccess) String() string   // "OSAccess(files=[…], environ={…})"
// plus every Ops method with the semantics of Appendix G.4; errors are *monty.RaisedError{ExcType: "FileNotFoundError", Message: "[Errno 2] No such file or directory: '<p>'"} etc.
```
Error typing: helper errors are `*monty.RaisedError` with the Python exception name (`FileNotFoundError`, `IsADirectoryError`, `NotADirectoryError`, `FileExistsError`, `OSError`, `ValueError`) so they cross the boundary with the right type and message. `monty.Path` values arrive for paths (the drive loop converts `value.Path` → `monty.Path`); handles are never passed to read/write handlers (parity note).

## 14. Pseudo-code for key methods (Go-flavoured)

### 14.1 `wire.FrameReader.Next`
```go
func (f *FrameReader) Next(ctx context.Context) ([]byte, error) {
    for {
        if len(f.buf) >= 4 {
            n := binary.LittleEndian.Uint32(f.buf[:4])
            if n > MaxFrameLen { return nil, ErrFrameTooLarge }
            if len(f.buf) >= 4+int(n) {
                frame := f.buf[4 : 4+n]                      // caller must not retain beyond next call
                f.buf = f.buf[4+n:]                          // retain coalesced bytes
                if cap(f.buf) > RetainBufMax && len(f.buf) == 0 { f.buf = nil }
                return frame, nil
            }
            f.grow(4 + int(n))
        }
        n, err := f.readWithCtx(ctx, f.chunk())             // read into spare capacity, ≥ ReadChunk
        f.buf = f.buf[:len(f.buf)+n]
        if err == io.EOF { if len(f.buf) == 0 { return nil, io.EOF }; return nil, ErrTruncated }
        if err != nil { return nil, err }                    // ctx errors propagate; partial state stays in f.buf (cancel-safe)
    }
}
```

### 14.2 `wire.DecodeValue` (streaming, budget-charged)
```go
func decodeValue(b []byte, depth int, budget *Budget) (any, error) {
    if depth > ProstRecursionLimit { return nil, errDepth }
    if err := budget.Charge(88); err != nil { return nil, err }        // one MontyObject node
    var kind any; seen := false
    for len(b) > 0 {
        num, typ, n := protowire.ConsumeTag(b); b = b[n:]
        switch num {
        case 5:  v, n := protowire.ConsumeVarint(b); kind = int64(protowire.DecodeZigZag(v))   // sint64
        case 6:  sub := consumeBytes(); kind = decodeBigInt(sub, budget)                         // charge len(magnitude)
        case 7:  kind = math.Float64frombits(consumeFixed64())
        case 8:  s := consumeBytes(); budget.Charge(len(s)); kind = string(s)
        case 9:  s := consumeBytes(); budget.Charge(len(s)); kind = bytes.Clone(s)
        case 11, 12, 15, 16: items := decodeList(sub, depth+1, budget); kind = wrap(num, items)  // list/tuple/set/frozenset; sets validate hashability → protocol error "unhashable …"
        case 13: kind = decodeNamedTuple(sub, depth+1, budget)
        case 14: kind = decodeDict(sub, depth+1, budget)                                          // Pair: key(1) value(2); duplicate keys → last wins; unhashable → error
        case 17..21: kind = decodeTemporal(num, sub)                                              // validate ranges: year 1..9999, month 1..12, day valid, hour<24, minute/second<60, micro<1e6, timedelta seconds 0..86399, micros 0..999999, tz name only with offset
        case 22: kind = decodeException(sub)                                                     // exc_type must be a known Python exception name
        case 23: kind = decodeType(sub)                                                          // origin UNSPECIFIED → error; BUILTIN must have no id and a known builtin name; SANDBOX/HOST need a 16-byte uuid
        case 24: kind = decodeClassInstance(sub, depth+1, budget)                                // type origin never BUILTIN
        case 25: kind = value.Function{…}; case 26: kind = value.BuiltinFunction(s); case 27: kind = value.Path(s)
        case 28: kind = decodeFileHandle(sub)                                                    // mode canonical set
        case 29: kind = string(s) /* repr → str */; case 30: kind = value.Cycle{…}
        case 1: Ellipsis; case 2: nil; case 3: NotImplemented; case 4: bool
        default: skipField(typ)
        }
        seen = true
    }
    if !seen { return nil, errProtocol("MontyObject with no kind") }
    return kind, nil
}
```
Encoding mirrors this with `protowire.Append*`; `EncodeValue` rejects `value.Cycle`, repr-only inputs (there is no Go `Repr` type — plain strings encode as `str`, matching TS), `Type{Origin: OriginSandbox}`, `ClassInstanceMarker` with sandbox-origin type (allowed only when produced by `restore`-then-`prepare` of a proxy: origin sandbox with id — the worker accepts it because it resolves by identity).

### 14.3 `pool.Checkout.turn` — see §8.7; deadline computation:
```go
func (b *sessionBudget) deadline(reqTO, grace time.Duration, graceOff, control bool) (time.Duration, bool) {
    var d time.Duration; has := false
    if reqTO > 0 { d, has = reqTO, true }
    if !control && b.durationBudget != nil && !graceOff {
        rem := *b.durationBudget - time.Duration(b.reportedExec)*time.Microsecond
        if rem < 0 { rem = 0 }
        back := rem + grace
        if !has || back < d { d, has = back, true }
    }
    return d, has
}
func (b *sessionBudget) observe(ev *wire.Event) {
    if ev.TotalExecutionMicros > b.reportedExec { b.reportedExec = ev.TotalExecutionMicros }
    if b.durationBudget == nil && ev.MaxDurationMicros != nil { d := time.Duration(*ev.MaxDurationMicros) * time.Microsecond; b.durationBudget = &d }
    if ev.MaxSuspensions != nil && *ev.MaxSuspensions < b.suspensionLimit { b.suspensionLimit = *ev.MaxSuspensions }
    if ev.IsSuspension() { b.suspensionsSeen++ }
}
```

### 14.4 `pool.Checkout.poison` / crash classification
```go
func (c *Checkout) poison(ctx context.Context, context string) *Error {
    w := c.takeWorker(); c.pending = nil; c.feedMounts = nil
    if w == nil { return &Error{Kind: KindFinished} }
    if w.Kind() == worker.KindWebSocket { w.Kill(); return &Error{Kind: KindDisconnected, Message: "monty worker connection closed while " + context} }
    wctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
    st := w.Wait(wctx); cancel()
    if !st.Known { w.Kill(); st = w.Wait(context.Background()) }
    if st.Code == 65 { c.metrics.terminated("oom"); return &Error{Kind: KindRuntime, WorkerLost: true, Exception: raised("MemoryError", "the worker exceeded its memory limit and was terminated")} }
    c.metrics.terminated("crash")
    return &Error{Kind: KindCrashed, Status: st, Message: "monty worker crashed while " + context + statusSuffix(st)}
}
```
Wasm `Wait`: `sys.ExitError` → `Status{Code, Known: true}`; `ExitCodeContextCanceled` → `Status{Known: true, Code: -1, Signal: "killed"}`; trap error → `Status{Known: false}`; nil → `Status{Code: 0}`.

### 14.5 `turnAnswerer.answer` — host function call arm
```go
case wire.EventFunctionCall:
    call := ev.Call
    if call.ObjectID != "" { return a.answerMethodCall(ctx, call) }
    entry, ok := a.lookup[call.FunctionName]
    if !ok { return a.co.Resume(ctx, wire.ExtResult{Kind: wire.ResultNotFound, NotFoundName: call.FunctionName}, a.onPrint) }
    fn, callable := asFunction(entry)                      // Function | reflect.Func → Func(entry)
    if !callable { return a.resumeError(ctx, "TypeError", fmt.Sprintf("'%s' object is not callable", value.PyTypeName(entry))) }
    args := a.restoreValues(call.Args); kwargs := a.kwargs(call.Kwargs)
    cbCtx := a.sess.callbackContext(ctx, ev)               // telemetry child span + caller values
    out, err := safeCall(func() (any, error) { return fn.Call(cbCtx, args, kwargs) })   // recover panics → RuntimeError
    if err != nil { t, m := errParts(err); return a.resumeError(ctx, t, m) }
    if fut, ok := out.(*Future); ok {
        if call.AllowEagerAwait {
            v, ferr := fut.wait(ctx)                          // ctx cancel → session lost (turn abandoned)
            return a.co.ResumeFutures(ctx, []wire.FutureResult{a.settled(call.CallID, v, ferr)}, a.onPrint)
        }
        a.pending[call.CallID] = &pendingFuture{f: fut}
        return a.co.Resume(ctx, wire.ExtResult{Kind: wire.ResultFuture, FutureCallID: call.CallID}, a.onPrint)
    }
    wv, perr := a.prepare(out)
    if perr != nil { return a.resumeError(ctx, "TypeError", perr.Error()) }
    return a.co.Resume(ctx, wire.ExtResult{Kind: wire.ResultReturn, Value: wv}, a.onPrint)
```

### 14.6 `turnAnswerer.answer` — resolve futures arm
```go
case wire.EventResolveFutures:
    if len(ev.PendingCallIDs) == 0 { return protocolErr("worker reported ResolveFutures with no pending call ids") }
    cases := make([]reflect.SelectCase, 0, len(ids)+1)
    for _, id := range ids { p, ok := a.pending[id]; if !ok { return protocolErr(fmt.Sprintf("worker reported unknown pending call id %d", id)) }; cases = append(cases, selectRecv(p.f.Done())) }
    cases = append(cases, selectRecv(ctx.Done()))
    if chosen, _, _ := reflect.Select(cases); chosen == len(ids) { return ctx.Err() }
    var results []wire.FutureResult
    for _, id := range ids { p := a.pending[id]; if p.f.settled() { v, err := p.f.Result(); results = append(results, a.settled(id, v, err)); delete(a.pending, id) } }
    return a.co.ResumeFutures(ctx, results, a.onPrint)
```
`a.settled(id, v, err)`: err → `{CallID, Result: Error(errParts)}`; else prepare(v) → Return, prepare failure → Error(TypeError).

### 14.7 `Session.FeedRun` (see §8.3) and `printTarget`
```go
func (p *printTarget) write(ctx context.Context, stream uint8, text string) error {
    if p.failure != nil { return nil }                       // drop after first failure
    s := Stdout; if stream == 2 { s = Stderr }
    if p.target == nil { fmt.Fprint(hostStream(s), text); return nil }
    if err := safe(func() error { return p.target.Print(s, text) }); err != nil { p.failure = err }
    return nil                                               // pool never sees callback errors
}
func (p *printTarget) throwIfFailed() error { return p.failure }
```

### 14.8 `mountfs.Table.HandleOsCall` (Appendix E.1.4/E.2)
```go
func (t *Table) HandleOsCall(ctx context.Context, call *wire.OsCall) Outcome {
    if !call.IsFS() { return notHandled(call) }
    p := call.PrimaryPath()
    if err := rejectOverlongPath(p); err != nil { return t.policyError(call, err) }
    if err := rejectNullBytes(p, call.NullMessage(false)); err != nil { return t.policyError(call, err) }
    if call.Op == wire.OpRename {
        if err := rejectOverlongPath(call.Path2); err != nil { return handledErr(err) }
        if err := rejectNullBytes(call.Path2, call.NullMessage(true)); err != nil { return handledErr(err) }
        si, sok := t.match(p); di, dok := t.match(call.Path2)
        switch { case !sok && !dok: return notHandled(call); case sok && dok && si == di: return t.mounts[si].execute(ctx, call)
                 default: return handledErr(&MountError{Kind: CrossMountRename, Path: p, Path2: call.Path2}) }
    }
    i, ok := t.match(p); if !ok { return notHandled(call) }
    m := t.mounts[i]
    if m.mode == ReadOnly && call.IsWrite() { return handledErr(&MountError{Kind: ReadOnlyErr, Path: p}) }
    return m.execute(ctx, call)          // direct.go or overlay.go
}
func (t *Table) policyError(call *wire.OsCall, err *MountError) Outcome { if call.IsExistenceCheck() { return handledValue(false) }; return handledErr(err) }
```

### 14.9 `ClassInstance` reflection defaults
```go
func (c *ClassInstance) eagerAttrs() ([]value.Pair, error) {
    names := c.opts.EagerAttrs.resolve(func() []string { return exportedFieldNames(c.rt.Type()) })
    out := make([]value.Pair, 0, len(names))
    for _, n := range names {
        v, err := c.readField(n)                          // AttrProvider.GetAttr when implemented; else reflect FieldByName on the deref'd struct; missing → ValueError "'<Name>' object has no attribute '<n>'"
        if err != nil { return nil, err }
        cv, err := c.convertValue(n, v); if err != nil { return nil, err }
        out = append(out, value.Pair{Key: n, Value: cv})
    }
    return out, nil
}
func (c *ClassInstance) callMethod(ctx context.Context, name string, args []any, kwargs Kwargs) (any, error) {
    if name == "__call__" || !c.opts.AllowedMethods.Allows(name) { return nil, ErrAttrNotExposed }
    if mp, ok := c.instance.(MethodProvider); ok { return c.afterCall(name, mp.CallMethod(ctx, name, args, kwargs)) }
    m := c.rt.MethodByName(name)                          // method set of the value (pointer receiver ok when instance is a pointer)
    if !m.IsValid() { return nil, ErrAttrNotExposed }
    fn, _ := Func(m.Interface())                          // reflect adapter, ctx/Kwargs conventions
    return c.afterCall(name, fn.Call(ctx, args, kwargs))  // *Future results are converted after settling (convertValue applied in a.settled)
}
```
`ClassType.callMethod("__call__")` → `Construct`: `!opts.Init` → `TypeError("cannot instantiate host class '<name>'")`; call `Constructor` via `Func`; result → `opts.InstanceWrapper` or default `NewClassInstance(inst, {instance policies, ConvertValue, ClassType: c when reflect.TypeOf(inst) matches})`; register in the store; return the wrapper (prepare → marker with `Type.ID = c.ID()` so `type(x) is Wallet`).

### 14.10 `Pool.acquire`
```go
func (p *Pool) acquire(ctx context.Context) (*slot, error) {
    var timer <-chan time.Time; if p.cfg.CheckoutTimeout > 0 { t := time.NewTimer(p.cfg.CheckoutTimeout); defer t.Stop(); timer = t.C }
    for {
        p.mu.Lock()
        if p.closed { p.mu.Unlock(); return nil, ErrPoolClosed }
        if !p.cfg.SingleUse {
            for len(p.idle) > 0 {
                s := p.idle[len(p.idle)-1]; p.idle = p.idle[:len(p.idle)-1]
                if s.w.Alive() { p.mu.Unlock(); return s, nil }
                p.total--; p.metrics.terminated("died_idle"); s.w.Kill()
            }
        }
        if p.total < p.cfg.MaxProcesses {
            p.total++; p.mu.Unlock()
            w, err := p.cfg.Spawner.Spawn(ctx)
            if err != nil { p.mu.Lock(); p.total--; p.mu.Unlock(); p.wake(); return nil, &Error{Kind: KindSpawn, Message: "failed to spawn monty worker: " + err.Error()} }
            return &slot{w: w}, nil
        }
        wait := p.waiter(); p.mu.Unlock()                  // registered before unlocking: no lost wakeups
        select { case <-wait: case <-ctx.Done(): return nil, ctx.Err(); case <-timer: return nil, &Error{Kind: KindExhausted} }
    }
}
```

### 14.11 `wasmblob` + `WasmSpawner`
```go
func NewWasmSpawner(ctx context.Context, cacheDir string) (*WasmSpawner, error) {
    blob, err := wasmblob.Bytes(); if err != nil { return nil, err }      // zstd decode once; sha256 check → "wasm worker blob checksum mismatch"
    cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
    if cacheDir != "" { if c, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil { cfg = cfg.WithCompilationCache(c) } }   // cache failure → no cache (log once)
    rt := wazero.NewRuntimeWithConfig(ctx, cfg); wasi_snapshot_preview1.MustInstantiate(ctx, rt)
    compiled, err := rt.CompileModule(ctx, blob); if err != nil { rt.Close(ctx); return nil, fmt.Errorf("failed to compile the embedded monty worker: %w", err) }
    return &WasmSpawner{rt: rt, compiled: compiled}, nil
}
func (s *WasmSpawner) Spawn(ctx context.Context) (Worker, error) {
    inR, inW := io.Pipe(); outR, outW := io.Pipe()
    runCtx, cancel := context.WithCancel(context.Background())
    w := &wasmWorker{stdin: inW, reader: wire.NewFrameReader(outR), cancel: cancel, done: make(chan struct{})}
    cfg := wazero.NewModuleConfig().WithStdin(inR).WithStdout(outW).WithStderr(stderrSink()).WithArgs("monty", "subprocess").WithName("")   // empty env; unique anonymous instance name
    go func() { _, err := s.rt.InstantiateModule(runCtx, s.compiled, cfg); w.exit = err; outW.Close(); inR.Close(); close(w.done) }()
    return w, nil
}
func (w *wasmWorker) Kill() { w.cancel(); w.stdin.Close() }
func (w *wasmWorker) Wait(ctx context.Context) Status { select { case <-w.done: return classify(w.exit); case <-ctx.Done(): return Status{} } }
```
`Alive()` = `done` not closed. Memory: `WithMemoryLimitPages` left at wazero default (4 GiB) — the worker's own allocator enforces `max_memory`.

### 14.12 `osaccess.Dispatch`
```go
func Dispatch(ctx context.Context, ops Ops, name string, args []any, kwargs monty.Kwargs) (any, error) {
    var out any; var err error
    switch name {
    case "Path.exists": out, err = ops.PathExists(path(args, 0))
    … (one arm per name; Path.mkdir reads kwargs["parents"], kwargs["exist_ok"] defaulting false; os.environ/date.today take no args; datetime.now takes an optional *TimeZone)
    default: return monty.NotHandled, nil
    }
    if errors.Is(err, ErrNotImplemented) { return monty.NotHandled, nil }
    if err != nil { return nil, err }                 // *monty.RaisedError crosses typed; other errors → RuntimeError
    return convertResult(out), nil                    // StatResult → NamedTuple; []monty.Path → list of Path; string → str
}
```

## 15. Summary of what the implementer builds, in order (suggested milestones for `/go-plan`)
1. Repo scaffold, vendored proto, `montypb` generation, `internal/value`, `internal/wire` (frames + hybrid codec + differential tests).
2. `internal/worker` subprocess + `internal/pool` (turn engine, budgets, classification) + fake-worker unit tests; native round-trip test (`basic_test.go`).
3. Root API: pool/session/drive loop/values/errors/print; port basic, inputs, types, external, async, exceptions, repl, limits, print, type_check, install_dependencies, feed_start, pool tests (native).
4. Class wrappers + instance store; port class_instance tests.
5. `internal/mountfs` (direct + overlay + path policy) + `mount.go`; port mount tests and `monty-fs` unit cases.
6. `worker-wasm` crate, `wasmblob`, `WasmSpawner`; run the whole suite on the wasm backend; port `wasm_*` tests; fix per-backend figures.
7. Telemetry recorder + facade; port the 17 telemetry/callback-context tests.
8. WebSocket transport + relay/mock fixtures; port `test_websocket.py`.
9. `osaccess` package; port `test_os_access*.py`.
10. README + Example tests, `public_api_test.go`, `docs/architecture`, `docs/parity`, CI, `changelogs/v0.0.23.md`.

BRAINSTORM DONE
