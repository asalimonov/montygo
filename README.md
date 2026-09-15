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

```go
import "github.com/asalimonov/montygo"
```

The embedded wasm worker works immediately. For the native backend, install a
protocol-3 `monty` binary (build `cargo build -p monty-runtime` from a Monty
checkout at `f8acf4fa` or newer) and point `MONTY_BIN` at it.

## Versions

`montygo.MontyVersion` is the upstream Monty release the binding tracks;
`montygo.BindingVersion()` is this module's own release. `BindingVersion()`
returns the value stamped with `-ldflags "-X github.com/asalimonov/montygo.buildVersion=<version>"`,
else the module version from the binary's build info (`0.1.0` after
`go get github.com/asalimonov/montygo@v0.1.0`, `(devel)` under a directory
`replace`), else `0.0.0-unknown`. `make version` prints the version
`scripts/version.sh` derives from git tags, and every `make` target stamps it.
`Version` is a deprecated alias of `MontyVersion`. See
[docs/architecture/versioning.md](docs/architecture/versioning.md).

For a local directory replacement, stamp the binding's tree, not the consumer's:

```sh
montygo_version=$(cd ../montygo && ./scripts/version.sh)
GOTOOLCHAIN=local go build -ldflags "-X github.com/asalimonov/montygo.buildVersion=${montygo_version}" ./...
```

The script preserves `-dirty`. Runtime build metadata cannot identify a replaced
dependency's commit from the consumer's `vcs.revision`.

## Basic usage

```go
ctx := context.Background()
pool, err := montygo.New(ctx, montygo.Options{})
if err != nil {
	return err
}
defer pool.Close(ctx)

session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
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

`Session.Close(ctx)` waits within the caller's budget and returns the worker to
the pool. A timeout while waiting leaves the session open. `CloseNow` kills and
retires the worker without joining Go callbacks. Later results report
`ErrSessionClosed`, which does **not** match `ErrSessionLost`. `Session.Done`
and `Session.Err` report closure or loss, including worker death while idle.

One execution owns a session, including while a snapshot is paused. Overlapping
feeds and incompatible control calls return `ErrSessionBusy`; they never queue.

## Interrupting a feed

`Session.Go` registers a feed before returning its `*Run`, then drives it on a
goroutine. An immediate interrupt cannot miss it. `Run.Interrupt` always targets
that run, even after the session starts another execution.
`Session.Interrupt` (or `Run.Interrupt`) stops it from any goroutine:

```go
lookup := map[string]any{"wait": func(ctx context.Context) error {
	<-ctx.Done() // the host call's context ends on Interrupt
	return ctx.Err()
}}
run := session.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: lookup})
result, stopErr := run.Interrupt(ctx, montygo.InterruptOptions{
	Grace: montygo.DurationPtr(250 * time.Millisecond),
})
if stopErr != nil {
	return stopErr // InterruptPending means the watchdog still owns the request
}
if !result.RunDone {
	_, _ = run.WaitContext(ctx) // use an application shutdown budget here
}
_, err := run.Wait()               // *RuntimeError, TypeName KeyboardInterrupt
session.FeedRun(ctx, "1 + 1", nil) // the session is still usable after BeforeStart/Aborted
```

`InterruptOptions.Reason` defaults to `KeyboardInterrupt`; `Raise` selects another
exception. `Grace` is a `*time.Duration`: nil inherits the checkout default
(100 ms), zero forces immediately, and negative values are invalid. The first
accepted reason wins; subsequent requests can shorten, never extend, the deadline.

Before the first send, interruption reports `InterruptBeforeStart`. At a
suspension, `AbortFeed` reports `InterruptAborted` and preserves the session; Python
cannot catch it. At grace expiry, the worker is killed and retired, including
when a Go callback ignores cancellation. The result is `InterruptKilled` and
`SessionErr` is a `*SessionKilledError` wrapping the reason and matching
`ErrSessionLost`. `RunDone` stays false until remaining synchronous Go work returns.

The Interrupt context limits waiting, not the accepted request. Timeout reports
`InterruptPending`; the watchdog continues. `Run.WaitContext(ctx)` only waits and
never requests cancellation. Other outcomes are `InterruptNotRunning`,
`InterruptAlreadyFinished`, and `InterruptFinished` for a completion race;
`InterruptUnknown` is the zero value. Check `SessionErr` to decide whether to reuse
the session. Cancelling the feed context during Python execution still kills the
worker immediately; at a suspension it requests the cooperative abort path.

`montygo.AsyncContext(ctx, fn)` starts asynchronous host work under the
callback context, so it observes the same cancellation; `montygo.Async` cannot
be cancelled.

### Lifecycle choice

| Need | Use | Completion guarantee |
|---|---|---|
| Execute and wait | `FeedRun` | returns after the driver finishes |
| Execute in background | `Go` | execution is registered before return; inspect `Wait` for admission errors |
| Stop one execution | `Run.Interrupt` | outcome identifies abort, kill, or independent completion |
| Stop the current execution or snapshot | `Session.Interrupt` | captures one owner; idle returns `InterruptNotRunning` |
| Wait within a budget | `Run.WaitContext` | waiting only; no cancellation |
| Gracefully return a session | `Session.Close` | context bounds waiting; paused handles are invalidated |
| End a session immediately | `Session.CloseNow` | worker retirement starts; callbacks can remain active |
| End pool admission | `Pool.Close` | checked-out sessions retain their lifecycle |
| Drain and force at deadline | `Pool.Shutdown` | retains the documented five-second force grace |
| Stable host API | `Host` | validate registrations and stubs before checkout |
| Per-feed override | `ExternalLookup` | an explicitly present entry overrides `Host` |

Successful host-side writes are not rolled back when `AbortFeed` ends Python.
Applications remain responsible for transaction and idempotency policy. To
reuse a session, check `Session.Err()==nil` and handle a concurrent
`ErrSessionBusy` admission result.

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

### Pool lifecycle

`MinProcesses` (default 1) workers are prewarmed and `MaxProcesses` caps live
workers. `Pool.Stats` counts them by state, and `Pool.Shutdown` waits for open
sessions and retiring workers, where `Pool.Close` only retires idle workers:

```go
pool, _ := montygo.New(ctx, montygo.Options{MaxProcesses: 2})
session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
fmt.Printf("%+v\n", pool.Stats()) // {Starting:0 Active:1 Idle:0 Retiring:0}

go func() {
	session.FeedRun(ctx, "1 + 1", nil)
	session.Close(ctx)
}()
pool.Shutdown(ctx) // waits for the session and every worker
```

A worker leaving a session is retired asynchronously and counts toward
`MaxProcesses` until it has exited. When `Shutdown`'s context ends first, open
sessions are closed with `CloseNow`, the wait continues for 5 s, and the
context error is returned when workers remain. `Checkout` after `Shutdown`
returns `ErrPoolClosed`.

## Inputs

```go
session.FeedRun(ctx, "x + y", &montygo.FeedOptions{Inputs: map[string]any{"x": 10, "y": 20}}) // 30
```

## External lookup

`ExternalLookup` resolves names a snippet leaves undefined, lazily. A function
entry becomes a host function; any other value is returned when the name is
read; an absent name raises `NameError`.

```go
lookup := map[string]any{
	"add": func(a, b int) int { return a + b },
	"fetch_data": func(url string) *montygo.Future {
		return montygo.Async(func() (any, error) { return download(url) })
	},
	"greeting": "hello ",
}
session.FeedRun(ctx, "add(2, 3)", &montygo.FeedOptions{ExternalLookup: lookup})                   // 5
session.FeedRun(ctx, "await fetch_data('https://example.com')", &montygo.FeedOptions{ExternalLookup: lookup})
```

Plain Go functions are adapted by reflection: arguments convert to the parameter
types, an optional leading `context.Context` receives the callback context, a
trailing `montygo.Kwargs` parameter receives keyword arguments, and results may be
`(T)`, `(error)` or `(T, error)`. Return a `*montygo.Future` (`montygo.Async` or
`montygo.NewFuture`) to let other sandbox tasks run while the call completes.
`montygo.Function` and `montygo.FunctionFunc` give full control.

Errors cross into the sandbox as Python exceptions: `montygo.Raise("KeyError", "missing")`
raises that type (`montygo.KnownExceptionNames()` lists them), any other error
(or a panic) raises `RuntimeError`.

### Host registry

`montygo.Host` validates functions and objects when they are registered
instead of when the sandbox calls them, and `CheckoutOptions.Host` exposes
them to every feed of the session. Names must be Python identifiers and unique;
`FeedOptions.ExternalLookup` entries override them. `Host.Stubs` renders Python
stubs for `TypeCheckStubs`.

```go
host := montygo.NewHost()
host.Func("add", func(a, b int) int { return a + b })
host.Object("wallet", &Wallet{Balance: 100}, montygo.ClassInstanceOptions{
	AllowedMethods: montygo.Expose[Payer](), // exactly the methods of interface Payer
})
session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{Host: host, TypeCheck: true, TypeCheckStubs: host.Stubs()})
session.FeedRun(ctx, "add(1, wallet.pay(30).balance)", nil) // 71
```

Stubs declare functions and allowed methods; attributes are not declared, so a
type-checked feed reads them through a method or `Any`. Set
`ClassInstanceOptions.ID` on every object that a dump must find again after
`LoadSession` on another checkout with the same `Host`; `Host.Restorable`
reports the first object without one as `ErrHostObjectNotRestorable`, and
`LoadSession` returns that error before loading.

Configure the host before checkout. Fixed Go parameters are positional-only in
stubs (`def add(arg0: int, arg1: int, /) -> int`), matching runtime binding.
Optional labels improve readability without enabling keyword arguments:

```go
host.Func("add", func(a, b int) int { return a + b },
	montygo.HostFuncOptions{ParameterNames: []string{"left", "right"}})
```

Labels exclude `context.Context` and trailing `Kwargs`, but include a variadic
parameter. Supplied labels must match the signature, be unique, and not be
Python keywords. Direct `Function` implementations retain generic stubs.

## Class instances

Wrap a host object in `ClassInstance` to expose it under a policy: attributes
sent eagerly, attributes fetched lazily, and methods the sandbox may call.
Returning the instance from the sandbox gives the host the original object.

```go
type Wallet struct{ Balance int }

func (w *Wallet) Pay(amount int) *Wallet { return &Wallet{Balance: w.Balance - amount} }

func wrapWallet(w *Wallet) *montygo.ClassInstance {
	return montygo.MustClassInstance(w, montygo.ClassInstanceOptions{
		EagerAttrs:     montygo.All(),
		AllowedMethods: montygo.All(),
		ConvertValue: func(_ string, v any) (any, error) {
			if next, ok := v.(*Wallet); ok {
				return wrapWallet(next), nil
			}
			return v, nil
		},
	})
}

session.FeedRun(ctx, "w.pay(30).balance", &montygo.FeedOptions{Inputs: map[string]any{"w": wrapWallet(&Wallet{100})}}) // 70
```

Exported fields and methods are visible under snake_case names (`Balance` →
`balance`, `GetText` → `get_text`), or under a `monty:"name"` field tag
(`monty:"-"` hides a field). Unexported members are never reachable. A policy
is `montygo.All()` (every public name), `montygo.Names("pay", "balance")`, or
`montygo.Expose[Payer]()`, which exposes exactly the methods of an interface,
so a method added to the Go type later is not callable until the interface
names it. `AttrProvider`, `AttrLister` and `MethodProvider` replace reflection
for dynamic objects. Names outside the policy raise `AttributeError`; sandbox
mutations stay on the sandbox copy. Every wrapper sent into a session is
retained until the session closes, bounded by `CheckoutOptions.MaxHostObjects`
(default 10 000; instances and their class types both count). Past the bound
the sandbox raises `RuntimeError: host object limit N exceeded`, and the host
sees a `*montygo.ResourceError`. `Session.Stats` reports the counts.

Plain structs cross as named tuples: `montygo.AsNamedTuple(value)` converts a
struct's exported fields in declaration order, and
`montygo.NewNamedTuple("Point", montygo.Pair{Key: "x", Value: 1})` builds one
by hand.

Instances defined inside the sandbox come back as read-only `*montygo.ClassProxy`
values (`Name`, `ID`, `IsDataclass`, `Attributes`); passing a proxy back hands
the sandbox its original object.

### Record-shaped method results

`monty` tags do not automatically turn arbitrary structs into sandbox records.
Use a per-object converter for your explicit transport type:

```go
func recordResult(_ string, v any) (any, error) {
	switch r := v.(type) {
	case Record:
		return montygo.AsNamedTuple(r)
	case *Record:
		if r == nil {
			return nil, nil
		}
		return montygo.AsNamedTuple(*r)
	default:
		return v, nil
	}
}
// Set this before checkout:
host.Object("records", table, montygo.ClassInstanceOptions{
	Name: "Records", AllowedMethods: montygo.Expose[RecordsAPI](),
	ConvertValue: recordResult,
})
```

Methods can return `(Record, error)` or nullable `(*Record, error)`; Future-resolved
method results use the same converter. Lists/maps containing records require
their own explicit conversion. Storage rows and datetime conversion remain
application responsibilities. The executable example is in
[`record_conversion_test.go`](record_conversion_test.go).

### Host classes

`ClassType` exposes a class: `Statics` hold class constants and static methods
(governed by `EagerAttrs`, `LazyAttrs`, `AllowedMethods`), and `Init` lets the
sandbox construct instances, which cross back under the `Instance*` policies.
Without a `Constructor`, construction fills exported fields positionally or by keyword.

```go
walletClass := montygo.MustClassType[Wallet](montygo.ClassTypeOptions{
	Init:                   true,
	InstanceEagerAttrs:     montygo.All(),
	InstanceAllowedMethods: montygo.All(),
})
session.FeedRun(ctx, "w = Wallet(100)\nw.balance", &montygo.FeedOptions{Inputs: map[string]any{"Wallet": walletClass}}) // 100
```

Without `Init`, calling the class raises `TypeError: cannot instantiate host class 'Wallet'`.

## Snapshots

`FeedStart` returns a snapshot at each external call, OS call or name lookup
instead of driving the snippet to completion.

```go
snap, _ := session.FeedStart(ctx, `greet(name) + "!"`, &montygo.FeedOptions{Inputs: map[string]any{"name": "Ada"}})
if call, ok := snap.(*montygo.FunctionSnapshot); ok {
	done, _ := call.Resume(ctx, "hello Ada")
	fmt.Println(done.(*montygo.Complete).Output) // hello Ada!
}
```

Snapshots are single-use cursors: `*FunctionSnapshot` (`Resume`, `ResumeError`,
`ResumeNotFound`, `ResumeFuture`, `ResumeNotHandled`), `*NameLookupSnapshot`
(`ResumeUnresolved`, `ResumeFunction`, `ResumeValue`), `*FutureSnapshot`
(`Resume` with `FutureResolution`s) and `*Complete`. Pass `ExternalLookup`/`OS`
to `FeedStart` and call `ResumeAuto` to answer each step the way `FeedRun` would.

Each cursor belongs to one execution and suspension. Replaying a used cursor
returns `ErrSnapshotResumed`; an invalid generation returns `ErrSnapshotStale`.
After an abort, an unused old cursor returns that execution's error without
touching a later feed. Cancelled or busy claims do not consume the cursor.
Pending `AsyncContext` work survives successful steps and is cancelled when the
execution ends. Pending subscriptions belong to call IDs, not Future pointers;
ending one execution never settles a caller-owned Future shared elsewhere.

`snapshot.Dump` serializes a paused worker and `session.LoadSnapshot` restores it
into a fresh session; `session.Dump` and `session.LoadSession` do the same for an
idle session. Mounts are not stored in dumps: re-supply them to `LoadSnapshot`.

## Print output

Output goes to the host process stdout/stderr unless `FeedOptions.Print` is set.
The worker batches output (`CheckoutOptions.PrintFlushInterval`, default 5 ms;
`0` delivers one callback per line). A print target returning an error fails the feed.

```go
text, _ := montygo.NewCollectString(montygo.DefaultMaxPrintCollectBytes)
session.FeedRun(ctx, `print("hello")`, &montygo.FeedOptions{Print: text})
text.Output() // "hello\n"
```

`CollectStreams` keeps the stream of each chunk. Both collectors cap host memory
at 10 MiB by default; exceeding the cap raises `MemoryError`.

`montygo.Lines` delivers complete lines per stream without their newline. A
trailing partial line is delivered when the feed ends, because a
`FlushingPrintTarget` has its `Flush` called at every turn end:

```go
lines := montygo.Lines(func(stream montygo.Stream, line string) error {
	fmt.Printf("%s: %q\n", stream, line)
	return nil
})
session.FeedRun(ctx, "print('a\\nb')\nprint('c', end='')", &montygo.FeedOptions{Print: lines})
// stdout: "a"
// stdout: "b"
// stdout: "c"
```

## Filesystem

Mount host directories at virtual POSIX paths. Mount I/O is serviced host-side,
so mounts work with every backend.

```go
mount, _ := montygo.NewMountDir(montygo.MountDirOptions{HostPath: "/path/on/host", VirtualPath: "/mnt/data", Mode: montygo.MountReadOnly})
defer mount.Close()
session.FeedRun(ctx, "open('/mnt/data/file.txt').read()", &montygo.FeedOptions{Mount: []*montygo.MountDir{mount}})
```

Modes are `read-only`, `read-write` and `overlay` (default: writes stay in memory
and are discarded when the feed ends). Each mount has a 100 MB memory budget
(`MemoryUsageLimit`) and an optional `WriteBytesLimit`. The working directory
defaults to the first mount's virtual path and persists across feeds; `Cwd`
switches it.

OS calls no mount covers reach `FeedOptions.OS`; return `montygo.NotHandled` to decline:

```go
handler := func(ctx context.Context, name string, args []any, kwargs montygo.Kwargs) (any, error) {
	if name == "os.getenv" && args[0] == "HOME" {
		return "/home/user", nil
	}
	return montygo.NotHandled, nil
}
```

Package `osaccess` provides a ready-made in-memory filesystem (`OSAccess`,
`MemoryFile`, `CallbackFile`, `StatResult`) and an `Ops` interface for custom handlers.

## Resource limits

```go
pool.Checkout(ctx, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{
	MaxMemory:         100 << 20,
	MaxDuration:       5 * time.Second,
	MaxRecursionDepth: 100,
}})
```

`MaxDuration` counts execution time only (not time suspended on the host) and is
backstopped by killing the worker `Options.DurationLimitGrace` (default 1 s)
after the budget expires. `Options.RequestTimeout` bounds every protocol turn.
`MaxSuspensions` (default 1000) bounds host round trips per session; the count
is reset by `LoadSession` and `LoadSnapshot`. `MaxMemory` is also enforced by
the worker's allocator; a worker that breaches it is replaced and the feed
fails with `MemoryError`.

A zero limit keeps the default. `montygo.Unlimited` disables `MaxMemory`,
`MaxSuspensions`, `MaxHostObjects` and `MaxPendingFutures`;
`montygo.UnlimitedDuration` disables `MaxDuration`. `MaxRecursionDepth` cannot
be unlimited.

Host-side bounds keep a long session in check: `CheckoutOptions.MaxHostObjects`
(default 10 000) bounds retained host objects and `MaxPendingFutures` (default
1000) bounds unresolved futures per feed. Exceeding either raises
`RuntimeError: <resource> limit N exceeded` in the sandbox; the host sees a
`*montygo.ResourceError` and the session stays usable. `Options.MaxPendingBytes`
(default 64 MiB, `montygo.UnlimitedPendingBytes` disables) bounds the worker
output buffered by the parent for native and wasm workers; past it the reader
blocks and the worker stalls on its write instead of growing host memory.

## Type checking

```go
session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{TypeCheck: true, TypeCheckStubs: "def fetch(url: str) -> str: ..."})
_, err := session.FeedRun(ctx, "fetch(123)", nil)
var typingErr *montygo.TypingError // errors.As(err, &typingErr); typingErr.Diagnostics
```

`TypeCheckFormat` picks ty's rendering (`full`, `concise`, `json`, ...) and
`TypeCheckColor` adds ANSI colour. A snippet that fails type checking does not run.

`AssertMessageAnnotations` controls pytest-style `assert` messages
(`montygo.Uint32(0)` restores CPython's bare `AssertionError`).

## Errors

| Type | Meaning |
|---|---|
| `*montygo.RuntimeError` | a Python exception (`TypeName`, `Message`, `Frames`, `Display(montygo.DisplayTraceback)`) |
| `*montygo.SyntaxError` | the snippet did not parse |
| `*montygo.TypingError` | type checking rejected the snippet |
| `*montygo.CrashedError` | the worker died or timed out; the session is lost, the pool recovers |
| `*montygo.DisconnectError`, `*montygo.ShutdownError` | WebSocket workers only; `DisconnectError` carries the close frame's `Code` and `Reason` |
| `*montygo.ProtocolError` | a protocol violation; the session is lost |
| `*montygo.ResourceError` | a host-side bound (`MaxHostObjects`, `MaxPendingFutures`) was reached; the session stays usable |
| `*montygo.ConversionError` | a host value cannot cross into the sandbox |
| `*montygo.SessionKilledError` | interruption forced worker termination; wraps the first reason |
| `montygo.ErrSessionBusy` | another execution or incompatible control operation owns the session |
| `montygo.ErrSessionClosed` | deliberate closure; not session loss |

All sandbox errors implement `montygo.Error`. Unexpected session loss matches
`montygo.ErrSessionLost`; deliberate `ErrSessionClosed` does not. A fatal worker
memory error preserves its `*RuntimeError` details and also matches session loss:

```go
if _, err := session.FeedRun(ctx, code, nil); errors.Is(err, montygo.ErrSessionLost) {
	session, err = pool.Checkout(ctx, montygo.CheckoutOptions{}) // check out a new one
}
```

## WebSocket workers

```go
opts := montygo.WebSocketOptions{
	URL:       "wss://monty.example.com/",
	TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	ConnectHeaders: func(ctx context.Context) (map[string]string, error) {
		return map[string]string{"authorization": "Bearer ..."}, nil
	},
}
if err := montygo.CheckWebSocketHealth(ctx, opts); err != nil {
	fmt.Println("server unavailable:", err)
	return
}
pool, _ := montygo.NewWebSocket(ctx, opts)
```

Each checkout dials a single-use worker. `TLSConfig` configures `wss://` dials
and `DialContext` replaces the TCP dialer. `CheckWebSocketHealth` sends
`GET <path>/health` through the same transport with the connect headers. A dropped connection raises
`*montygo.DisconnectError`; a draining server raises `*montygo.ShutdownError` whose
`Dump` restores the session elsewhere.

`montygo.FetchServerInfo` reads `GET <path>/info` from a `monty-server` and
returns its version, upstream revision, protocol version and effective limits,
so a client can size its pool and detect drift before dialing:

```go
info, err := montygo.FetchServerInfo(ctx, opts)
if errors.Is(err, montygo.ErrNoServerInfo) {
	// a server built before /info existed
}
fmt.Println(info.Version, info.MontyRev, info.ProtocolVersion, info.Limits.MaxSessions, info.Limits.SessionTimeout)
```

A zero duration or count in `ServerLimits` means the server has that limit
disabled.

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
`montygo.NewWebSocket(ctx, montygo.WebSocketOptions{URL: "ws://127.0.0.1:8000/"})`,
and probe readiness first with `montygo.CheckWebSocketHealth`. The REPL example
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
montygo.Instrument(montygo.TelemetryComponents{Tracer: tracer, Meter: meter, Logger: logger})
```

Instrumentation is process-wide and must be installed before creating a pool.
`Options.Telemetry` and `WebSocketOptions.Telemetry` give one pool its own
components instead: nil uses the process-wide installation, and a value with
no components records nothing for that pool.

```go
pool, _ := montygo.New(ctx, montygo.Options{Telemetry: &montygo.TelemetryComponents{Tracer: tracer}})
```

Each checkout records a `session {script_name}` span, each run a `run code`
span, each host round trip a child span, printed output as log records, and pool
metrics such as `monty.pool.workers.live`, `monty.pool.workers.retiring`,
`monty.pool.pending_frame_bytes` and `monty.run.duration`. Recorded values
include source code, inputs, outputs and printed text.

## Values

| Python | Go |
|---|---|
| `None` | `nil` |
| `bool` | `bool` |
| `int` | `int64` (`*big.Int` beyond int64) |
| `float` | `float64` |
| `str` / `bytes` | `string` / `[]byte` |
| `list` / `tuple` | `[]any` / `montygo.Tuple` |
| `dict` | `*montygo.Dict` (insertion-ordered) |
| `set` / `frozenset` | `*montygo.Set` / `*montygo.FrozenSet` |
| `date`, `datetime`, `time`, `timedelta`, `timezone` | `montygo.Date`, `montygo.DateTime`, `montygo.Time`, `montygo.TimeDelta`, `montygo.TimeZone` |
| `pathlib.Path` | `montygo.Path` |
| named tuple | `montygo.NamedTuple` |
| file handle | `*montygo.FileHandle` |
| class instance | the original host object, or `*montygo.ClassProxy` |

Inputs also accept Go integer and float kinds, slices, arrays and maps (keys sorted).

## Development

```bash
make build-worker   # native worker in ../monty (MONTY_SRC)
make build-wasm     # rebuild the embedded wasm worker (Rust + wasm32-wasip1)
make version        # version derived from git tags (scripts/version.sh)
make check-pins     # every copy of the upstream pin agrees
make test           # both backends
make bench          # wire codec benchmarks against the generated protobuf code
make fuzz           # wire codec fuzz targets
make server-check   # clippy and tests for server/
make docker-build   # monty-server image
make test-docker    # root suite on the websocket backend against the image
make test-network   # tests/network against the image
```

On macOS with a beta SDK, `make` selects the 26.5 SDK for cargo and for the
`tests/network` build (`SDKROOT`).

## License

MIT. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
