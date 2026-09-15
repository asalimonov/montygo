# montygo

Run untrusted Python safely from Go with [Monty](https://github.com/pydantic/monty),
the sandboxed Python interpreter written in Rust.

montygo is a pure-Go binding (no cgo) with the feature set of the official
TypeScript package `@pydantic/monty`. Code runs in crash-isolated Monty workers:

- **native**: `monty subprocess` child processes, spoken to over Monty's
  protobuf wire protocol, exactly like the Python and JavaScript bindings;
- **wasm**: the same worker compiled to WebAssembly and embedded in the Go
  module, run in-process by [wazero](https://wazero.io) — nothing to install;
- **websocket**: a remote worker behind a WebSocket relay, Full Monty, or the
  bundled [`monty-server` image](#dockerized-server).

It tracks Monty `0.0.23` plus upstream `main@f8acf4fa` (wire protocol version 3).

## Installation

```bash
go get github.com/asalimonov/montygo
```

The embedded wasm worker works immediately. For the native backend, install a
protocol-3 `monty` binary (build `cargo build -p monty-runtime` from a Monty
checkout at `f8acf4fa` or newer) and point `MONTY_BIN` at it.

## Basic usage

```go
ctx := context.Background()
pool, err := monty.New(ctx, monty.Options{})
if err != nil {
	return err
}
defer pool.Close(ctx)

session, err := pool.Checkout(ctx, monty.CheckoutOptions{})
if err != nil {
	return err
}
defer session.Close(ctx)

result, err := session.FeedRun(ctx, "1 + 2", nil) // int64(3)
```

A session is a REPL in a dedicated worker, so state persists across feeds:

```go
session.FeedRun(ctx, "x = 21", nil)
session.FeedRun(ctx, "x * 2", nil) // int64(42)
```

`CheckoutOptions.ScriptName` names the script in tracebacks and type-checking diagnostics.

## Backends

`Options.Backend` selects the transport:

| Backend | Workers | Notes |
|---|---|---|
| `BackendAuto` (default) | native when a `monty` binary resolves, else wasm | |
| `BackendNative` | `monty subprocess` children | crash isolation by process; unix only |
| `BackendWasm` | embedded wasip1 worker under wazero | in-process; compiled once per process and cached on disk (`Options.WasmCacheDir`) |
| `NewWebSocket` | a remote worker per checkout | see [WebSocket workers](#websocket-workers) |

The native binary resolves from `Options.BinaryPath`, then `MONTY_BIN`, then
`PATH`, then a cargo `target/` directory in an ancestor or a sibling `monty`
checkout. Pin `BinaryPath` when the environment is not trusted.

## Inputs

```go
session.FeedRun(ctx, "x + y", &monty.FeedOptions{Inputs: map[string]any{"x": 10, "y": 20}}) // 30
```

## External lookup

`ExternalLookup` resolves names a snippet leaves undefined, lazily. A function
entry becomes a host function; any other value is returned when the name is
read; an absent name raises `NameError`.

```go
lookup := map[string]any{
	"add": func(a, b int) int { return a + b },
	"fetch_data": func(url string) *monty.Future {
		return monty.Async(func() (any, error) { return download(url) })
	},
	"greeting": "hello ",
}
session.FeedRun(ctx, "add(2, 3)", &monty.FeedOptions{ExternalLookup: lookup})                   // 5
session.FeedRun(ctx, "await fetch_data('https://example.com')", &monty.FeedOptions{ExternalLookup: lookup})
```

Plain Go functions are adapted by reflection: arguments convert to the parameter
types, an optional leading `context.Context` receives the callback context, a
trailing `monty.Kwargs` parameter receives keyword arguments, and results may be
`(T)`, `(error)` or `(T, error)`. Return a `*monty.Future` (`monty.Async` or
`monty.NewFuture`) to let other sandbox tasks run while the call completes.
`monty.Function` and `monty.FunctionFunc` give full control.

Errors cross into the sandbox as Python exceptions: `monty.Raise("KeyError", "missing")`
raises that type, any other error (or a panic) raises `RuntimeError`.

## Class instances

Wrap a host object in `ClassInstance` to expose it under a policy: attributes
sent eagerly, attributes fetched lazily, and methods the sandbox may call.
Returning the instance from the sandbox gives the host the original object.

```go
type Wallet struct{ Balance int }

func (w *Wallet) Pay(amount int) *Wallet { return &Wallet{Balance: w.Balance - amount} }

func wrapWallet(w *Wallet) *monty.ClassInstance {
	return monty.MustClassInstance(w, monty.ClassInstanceOptions{
		EagerAttrs:     monty.All,
		AllowedMethods: monty.All,
		ConvertValue: func(_ string, v any) (any, error) {
			if next, ok := v.(*Wallet); ok {
				return wrapWallet(next), nil
			}
			return v, nil
		},
	})
}

session.FeedRun(ctx, "w.pay(30).balance", &monty.FeedOptions{Inputs: map[string]any{"w": wrapWallet(&Wallet{100})}}) // 70
```

Exported fields and methods are visible under snake_case names (`Balance` →
`balance`, `GetText` → `get_text`), or under a `monty:"name"` field tag
(`monty:"-"` hides a field). Unexported members are never reachable.
`AttrProvider`, `AttrLister` and `MethodProvider` replace reflection for dynamic
objects. Names outside the policy raise `AttributeError`; sandbox mutations stay
on the sandbox copy. Every wrapper sent into a session is retained until the
session closes.

Instances defined inside the sandbox come back as read-only `*monty.ClassProxy`
values (`Name`, `ID`, `IsDataclass`, `Attributes`); passing a proxy back hands
the sandbox its original object.

### Host classes

`ClassType` exposes a class: `Statics` hold class constants and static methods
(governed by `EagerAttrs`, `LazyAttrs`, `AllowedMethods`), and `Init` lets the
sandbox construct instances, which cross back under the `Instance*` policies.
Without a `Constructor`, construction fills exported fields positionally or by keyword.

```go
walletClass := monty.MustClassType[Wallet](monty.ClassTypeOptions{
	Init:                   true,
	InstanceEagerAttrs:     monty.All,
	InstanceAllowedMethods: monty.All,
})
session.FeedRun(ctx, "w = Wallet(100)\nw.balance", &monty.FeedOptions{Inputs: map[string]any{"Wallet": walletClass}}) // 100
```

Without `Init`, calling the class raises `TypeError: cannot instantiate host class 'Wallet'`.

## Snapshots

`FeedStart` returns a snapshot at each external call, OS call or name lookup
instead of driving the snippet to completion.

```go
snap, _ := session.FeedStart(ctx, `greet(name) + "!"`, &monty.FeedOptions{Inputs: map[string]any{"name": "Ada"}})
if call, ok := snap.(*monty.FunctionSnapshot); ok {
	done, _ := call.Resume(ctx, "hello Ada")
	fmt.Println(done.(*monty.Complete).Output) // hello Ada!
}
```

Snapshots are single-use cursors: `*FunctionSnapshot` (`Resume`, `ResumeError`,
`ResumeNotFound`, `ResumeFuture`, `ResumeNotHandled`), `*NameLookupSnapshot`
(`ResumeUnresolved`, `ResumeFunction`, `ResumeValue`), `*FutureSnapshot`
(`Resume` with `FutureResolution`s) and `*Complete`. Pass `ExternalLookup`/`OS`
to `FeedStart` and call `ResumeAuto` to answer each step the way `FeedRun` would.

`snapshot.Dump` serializes a paused worker and `session.LoadSnapshot` restores it
into a fresh session; `session.Dump` and `session.LoadSession` do the same for an
idle session. Mounts are not stored in dumps: re-supply them to `LoadSnapshot`.

## Print output

Output goes to the host process stdout/stderr unless `FeedOptions.Print` is set.
The worker batches output (`CheckoutOptions.PrintFlushInterval`, default 5 ms;
`0` delivers one callback per line). A print target returning an error fails the feed.

```go
text, _ := monty.NewCollectString(monty.DefaultMaxPrintCollectBytes)
session.FeedRun(ctx, `print("hello")`, &monty.FeedOptions{Print: text})
text.Output() // "hello\n"
```

`CollectStreams` keeps the stream of each chunk. Both collectors cap host memory
at 10 MiB by default; exceeding the cap raises `MemoryError`.

## Filesystem

Mount host directories at virtual POSIX paths. Mount I/O is serviced host-side,
so mounts work with every backend.

```go
mount, _ := monty.NewMountDir(monty.MountDirOptions{HostPath: "/path/on/host", VirtualPath: "/mnt/data", Mode: monty.MountReadOnly})
defer mount.Close()
session.FeedRun(ctx, "open('/mnt/data/file.txt').read()", &monty.FeedOptions{Mount: []*monty.MountDir{mount}})
```

Modes are `read-only`, `read-write` and `overlay` (default: writes stay in memory
and are discarded when the feed ends). Each mount has a 100 MB memory budget
(`MemoryUsageLimit`) and an optional `WriteBytesLimit`. The working directory
defaults to the first mount's virtual path and persists across feeds; `Cwd`
switches it.

OS calls no mount covers reach `FeedOptions.OS`; return `monty.NotHandled` to decline:

```go
handler := func(ctx context.Context, name string, args []any, kwargs monty.Kwargs) (any, error) {
	if name == "os.getenv" && args[0] == "HOME" {
		return "/home/user", nil
	}
	return monty.NotHandled, nil
}
```

Package `osaccess` provides a ready-made in-memory filesystem (`OSAccess`,
`MemoryFile`, `CallbackFile`, `StatResult`) and an `Ops` interface for custom handlers.

## Resource limits

```go
pool.Checkout(ctx, monty.CheckoutOptions{Limits: &monty.ResourceLimits{
	MaxMemory:         100 << 20,
	MaxDuration:       5 * time.Second,
	MaxRecursionDepth: 100,
}})
```

`MaxDuration` counts execution time only (not time suspended on the host) and is
backstopped by killing the worker `Options.DurationLimitGrace` (default 1 s)
after the budget expires. `Options.RequestTimeout` bounds every protocol turn.
`MaxSuspensions` (default 1000) bounds host round trips per feed. `MaxMemory` is
also enforced by the worker's allocator; a worker that breaches it is replaced
and the feed fails with `MemoryError`.

## Type checking

```go
session, _ := pool.Checkout(ctx, monty.CheckoutOptions{TypeCheck: true, TypeCheckStubs: "def fetch(url: str) -> str: ..."})
_, err := session.FeedRun(ctx, "fetch(123)", nil)
var typingErr *monty.TypingError // errors.As(err, &typingErr); typingErr.Diagnostics
```

`TypeCheckFormat` picks ty's rendering (`full`, `concise`, `json`, ...) and
`TypeCheckColor` adds ANSI colour. A snippet that fails type checking does not run.

`AssertMessageAnnotations` controls pytest-style `assert` messages
(`monty.Uint32(0)` restores CPython's bare `AssertionError`).

## Errors

| Type | Meaning |
|---|---|
| `*monty.RuntimeError` | a Python exception (`TypeName`, `Message`, `Frames`, `Display(monty.DisplayTraceback)`) |
| `*monty.SyntaxError` | the snippet did not parse |
| `*monty.TypingError` | type checking rejected the snippet |
| `*monty.CrashedError` | the worker died or timed out; the session is lost, the pool recovers |
| `*monty.DisconnectError`, `*monty.ShutdownError` | WebSocket workers only |
| `*monty.ProtocolError` | a protocol violation; the session is lost |
| `*monty.ConversionError` | a host value cannot cross into the sandbox |

All sandbox errors implement `monty.Error`.

## WebSocket workers

```go
opts := monty.WebSocketOptions{
	URL:       "wss://monty.example.com/",
	TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	ConnectHeaders: func(ctx context.Context) (map[string]string, error) {
		return map[string]string{"authorization": "Bearer ..."}, nil
	},
}
if err := monty.CheckWebSocketHealth(ctx, opts); err != nil {
	fmt.Println("server unavailable:", err)
	return
}
pool, _ := monty.NewWebSocket(ctx, opts)
```

Each checkout dials a single-use worker. `TLSConfig` configures `wss://` dials
and `DialContext` replaces the TCP dialer. `CheckWebSocketHealth` sends
`GET <path>/health` through the same transport with the connect headers. A dropped connection raises
`*monty.DisconnectError`; a draining server raises `*monty.ShutdownError` whose
`Dump` restores the session elsewhere.

## Dockerized server

`monty-server` is an open WebSocket server for Monty workers, compatible with
[Full Monty](https://github.com/pydantic/monty/blob/main/docs/server.md). The
`server/` crate builds it, and `docker/Dockerfile` packages it with the pinned
`monty` worker in a `scratch` image for `linux/amd64` and `linux/arm64`.

```bash
make docker-build                         # both platforms; needs Docker's containerd image store
make docker-build PLATFORMS=linux/arm64   # one platform
docker run --rm \
  -e MONTY_SERVER_DUMP_KEY="$(openssl rand -hex 16)" \
  -p 8000:8000 \
  monty-server:latest
```

The server prints `ws://0.0.0.0:8000/` when it is ready. Connect with
`monty.NewWebSocket(ctx, monty.WebSocketOptions{URL: "ws://127.0.0.1:8000/"})`,
and probe readiness first with `monty.CheckWebSocketHealth`. The REPL example
connects with `go run ./repl -ws ws://127.0.0.1:8000/` from `examples/`.

Each session runs in a fresh worker process. The server clamps client limits to
its ceilings (`MONTY_SERVER_MAX_MEMORY_MIB`, `MONTY_SERVER_MAX_DURATION`,
`MONTY_SERVER_MAX_RECURSION_DEPTH`) and signs every dump with the dump key, so a
dump restores on any server that shares the key. `GET /metrics` serves
Prometheus metrics, and `OTEL_EXPORTER_OTLP_ENDPOINT` exports traces over
OTLP/HTTP.

The listener has no authentication and speaks plain `ws://`. Terminate TLS at an
ingress or reverse proxy and dial it with a `wss://` URL and `TLSConfig`, as in
[WebSocket workers](#websocket-workers). See
[docs/architecture/server.md](docs/architecture/server.md) and
[docs/architecture/docker.md](docs/architecture/docker.md).

## Observability

```go
monty.Instrument(monty.TelemetryComponents{Tracer: tracer, Meter: meter, Logger: logger})
```

Instrumentation is process-wide and must be installed before creating a pool.
Each checkout records a `session {script_name}` span, each run a `run code`
span, each host round trip a child span, printed output as log records, and pool
metrics such as `monty.pool.workers.live` and `monty.run.duration`. Recorded
values include source code, inputs, outputs and printed text.

## Values

| Python | Go |
|---|---|
| `None` | `nil` |
| `bool` | `bool` |
| `int` | `int64` (`*big.Int` beyond int64) |
| `float` | `float64` |
| `str` / `bytes` | `string` / `[]byte` |
| `list` / `tuple` | `[]any` / `monty.Tuple` |
| `dict` | `*monty.Dict` (insertion-ordered) |
| `set` / `frozenset` | `*monty.Set` / `*monty.FrozenSet` |
| `date`, `datetime`, `time`, `timedelta`, `timezone` | `monty.Date`, `monty.DateTime`, `monty.Time`, `monty.TimeDelta`, `monty.TimeZone` |
| `pathlib.Path` | `monty.Path` |
| named tuple | `monty.NamedTuple` |
| file handle | `*monty.FileHandle` |
| class instance | the original host object, or `*monty.ClassProxy` |

Inputs also accept Go integer and float kinds, slices, arrays and maps (keys sorted).

## Development

```bash
make build-worker   # native worker in ../monty (MONTY_SRC)
make build-wasm     # rebuild the embedded wasm worker (Rust + wasm32-wasip1)
make test           # both backends
make server-check   # clippy and tests for server/
make docker-build   # monty-server image
make test-docker    # root suite on the websocket backend against the image
make test-network   # tests/network against the image
```

On macOS with a beta SDK, `make` selects the 26.5 SDK for cargo and for the
`tests/network` build (`SDKROOT`).

## License

MIT. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
