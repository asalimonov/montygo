# montygo

Run untrusted Python safely from Go with [Monty](https://github.com/pydantic/monty),
the sandboxed Python interpreter written in Rust.

montygo is a pure-Go binding (no cgo) with the feature set of the official
TypeScript package `@pydantic/monty`. Code runs in crash-isolated Monty workers:

- **native**: `monty subprocess` child processes, spoken to over Monty's
  protobuf wire protocol, exactly like the Python and JavaScript bindings;
- **wasm**: the same worker compiled to WebAssembly and embedded in the Go
  module, run in-process by [wazero](https://wazero.io) — nothing to install;
- **remote**: a worker behind a WebSocket server, Full Monty or the bundled
  [`monty-server` image](#dockerized-server), reached through a supervisor.

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

Two values shape every session: a `Runtime` is the sandbox configuration and
its host extensions, a `Pool` manages the workers. `Pool.Checkout` joins them.

```go
ctx := context.Background()
pool, err := montygo.NewPool(ctx, montygo.PoolOptions{})
if err != nil {
	return err
}
defer pool.Close(ctx)

rt, err := montygo.NewRuntime(montygo.RuntimeOptions{})
if err != nil {
	return err
}
session, err := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
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

A runtime is immutable and safe to share: one runtime serves any number of
sessions of any number of pools. `RuntimeOptions` hold the host registry, the
OS handler, mounts, the default print target, resource limits, type checking
and the host-side bounds. `CheckoutOptions` hold what differs per session:
`ScriptName` (the name in tracebacks and type-checking diagnostics), `Limits`
(replacing the runtime's) and `Stop`.

`Session.Close(ctx)` stops a running feed with the session's [stop policy](#stopping-a-run),
invalidates a paused snapshot and returns the worker to the pool, so
`defer session.Close(ctx)` is always safe. The context bounds only the caller's
wait: when it ends first, `Close` returns `ctx.Err()` and the close continues in
the background. `Close(ctx, montygo.KillNow)` kills and retires the worker
without joining Go callbacks. Later results report `ErrSessionClosed`, which
does **not** match `ErrSessionLost`. `Session.Done` and `Session.Err` report
closure or loss, including worker death while idle, and `Session.State` reports
`SessionIdle`, `SessionRunning`, `SessionPaused` or `SessionClosed`.

One execution owns a session, including while a snapshot is paused. Overlapping
feeds and incompatible control calls return `ErrSessionBusy`; they never queue.

## One-shot runs

`Pool.Run` checks out a session, feeds once and closes it:

```go
result, err := pool.Run(ctx, rt, "sum(range(n))", &montygo.RunOptions{
	FeedOptions: montygo.FeedOptions{Inputs: map[string]any{"n": 10}},
}) // int64(45)
```

`RunOptions` embeds `CheckoutOptions` and `FeedOptions`; a nil pointer uses the
defaults. The close after the feed is bounded by the session's stop policy
(`Timeout + Join`), never by a cancelled caller context.

## Stopping a run

`Session.Go` registers a feed before returning its `*Run`, then drives it on a
goroutine, so an immediate stop cannot miss it. `Run.Stop` ends that run, and
only that run, from any goroutine. The library owns the whole procedure:

```go
lookup := map[string]any{"wait": func(ctx context.Context) error {
	<-ctx.Done() // the host call's context ends on Stop
	return ctx.Err()
}}
run := session.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: lookup})
stopped, err := run.Stop(ctx) // KeyboardInterrupt now, kill after 3 s, join the callback
// err == nil, stopped.How == montygo.StopAborted, stopped.SessionKept() == true
// stopped.Err is a *monterr.RuntimeError with TypeName KeyboardInterrupt
session.FeedRun(ctx, "1 + 1", nil) // the session is still usable
```

A stop follows one timeline, described by a `StopPolicy`:

| Time | Phase | Effect |
|---|---|---|
| t0 | `Stop` called | with `Drain > 0`, the run may still end on its own |
| t0 + `Drain` | request | `Reason` (default `KeyboardInterrupt`) is delivered at the next host call, await or suspension; the callback context is cancelled. By default the delivery is an uncatchable `AbortFeed`; with `Catchable` it is an ordinary exception |
| + `Timeout` | kill | the run has not ended: the worker is killed and the session is lost (`*monterr.SessionKilledError`) |
| + `Join` | join | a Go callback that still runs after the kill makes `Stop` return `monterr.ErrCallbackDetached` |

Python that never yields (`while True: pass`) is killed at `Timeout`. Python
that yields, through a host call, an `await` or a paused snapshot, is aborted
and the session is kept.

`Stop` returns a `Stopped{How, Err, SessionErr}`. `How` is a `StopKind` whose
`String()` is one of `pending`, `not running`, `aborted`, `killed` or
`finished`; `Err` is what `Run.Wait` returns; `SessionErr` is the session's
terminal error, and `SessionKept()` reports whether the session is still usable.
A run that ended on its own reports `StopFinished` with its own result, also
when the worker died or the server closed the connection (`Err` is then the
loss error and `SessionErr` is set, so check `SessionKept`, not the kind). A
caught catchable stop that then ends normally is `StopFinished`; an uncaught
one is `StopAborted`. `Session.Stop` targets whatever is running or paused and
reports `StopNotRunning` on an idle session.

The context bounds only the caller's wait. When it ends first, `Stop` returns
`Stopped{How: StopPending}` and `ctx.Err()`, and the stop continues to its
deadlines; `Run.Wait` or `Run.WaitContext` observe the end. A second `Stop` on
the same run joins the first and can only shorten its deadlines; the first
`Reason` and `Catchable` win.

### Policy levels

```go
pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{Stop: montygo.StopPolicy{Timeout: time.Second}})      // per pool
session, _ := pool.Checkout(ctx, rt, montygo.CheckoutOptions{Stop: montygo.StopPolicy{Catchable: true}}) // per session
stopped, _ := run.Stop(ctx, montygo.StopPolicy{Reason: monterr.Raise("TimeoutError", "budget spent")})    // per call
```

Zero fields inherit from the level above, so a call only names what it changes;
the last level is the built-in policy of `Timeout` 3 s and `Join` 3 s. There is
no process-wide setting. `Drain` and `Join` MUST be non-negative
(`*monterr.OptionError` otherwise), and at most one policy may be passed per
call. `montygo.KillNow` (`Timeout: -1`) kills at once, without a request phase.
Because `false` is the zero value, `Catchable` cannot be switched off per call
once a checkout set it; use `KillNow` or a short `Timeout` instead. The same
policy ends a run whose feed context is cancelled (`Go`, `FeedRun` and
`FeedStart` steps): `FeedRun` returns `KeyboardInterrupt` with the session kept
when Python yields, or a `*monterr.SessionKilledError` after `Timeout`.

### Catchable stops

With `Catchable`, the reason is raised inside the sandbox at the next host call
or await, so `except KeyboardInterrupt` can run cleanup that makes further host
calls. Pending futures registered before the request are cancelled with the old
callback context; host calls made after the delivery get a live context. The
run is still killed at `Timeout` if it does not end.

```go
script := `try:
    wait()
except KeyboardInterrupt:
    result = cleanup()
result`
run := session.Go(ctx, script, &montygo.FeedOptions{ExternalLookup: lookup})
stopped, err := run.Stop(ctx, montygo.StopPolicy{Catchable: true})
result, _ := run.Wait() // "cleaned up"; stopped.How == montygo.StopFinished
```

A catchable stop of a paused `FeedStart` snapshot falls back to the uncatchable
`AbortFeed`, because the application owns the snapshot chain.

`host.AsyncContext(ctx, fn)` starts asynchronous host work under the callback
context, so it observes the same cancellation; `host.Async` cannot be
cancelled.

## Slots

`Pool.Slot` returns a holder that owns at most one session of a runtime, checked
out with fixed options. It checks out lazily, and again after the session is
lost, so a long-lived service always has a usable session. Sandbox state is not
restored after a loss; re-feed the setup.

```go
slot := pool.Slot(rt, montygo.CheckoutOptions{})
defer slot.Close(ctx)

slot.State()                        // idle: no session yet
slot.FeedRun(ctx, "x = 21", nil)    // checks out
slot.FeedRun(ctx, "x * 2", nil)     // 42, same session
run, err := slot.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: lookup})
stopped, _ := run.Stop(ctx, montygo.KillNow) // killed; the session is lost
slot.FeedRun(ctx, "x", nil)         // a fresh session: NameError
```

`Slot.Go`, `FeedRun` and `FeedStart` return `ErrSessionBusy` while an execution
is running or paused, and `ErrSessionClosed` after `Slot.Close`. `Slot.Session`
is the current session or nil, `Slot.State` its state (`SessionIdle` without
one, `SessionClosed` after `Close`), and `Slot.Stop` stops whatever it runs.

### Lifecycle choice

| Need | Use | Completion guarantee |
|---|---|---|
| Run one snippet | `Pool.Run` | checkout, feed and close in one call |
| Execute and wait | `FeedRun` | returns after the run ends; a cancelled context ends it through the stop policy |
| Execute in background | `Go` | execution is registered before return; inspect `Wait` for admission errors |
| Stop one run | `Run.Stop(ctx[, policy])` | returns when the run has ended: aborted, killed or finished on its own |
| Stop the current run or snapshot | `Session.Stop(ctx[, policy])` | idle returns `StopNotRunning` |
| Wait within a budget | `Run.WaitContext` | waiting only; no cancellation |
| Return a session | `Session.Close(ctx[, policy])` | stops a running feed first; ctx bounds the wait, the close continues |
| End a session at once | `Session.Close(ctx, montygo.KillNow)` | worker retirement starts; callbacks can remain active |
| Inspect a session | `Session.State`, `Session.Err`, `Session.Done` | coarse state; terminal cause; closure |
| Keep one usable session | `Pool.Slot` | re-checks out after a loss; one execution at a time |
| End pool admission | `Pool.Close` | checked-out sessions retain their lifecycle |
| Stop everything | `Pool.Shutdown(ctx[, policy])` | every open session is closed with the policy; waits for every worker within ctx |
| Stable host API | `RuntimeOptions.Host` | registrations and stubs are validated before any checkout |
| Per-feed override | `ExternalLookup` | an explicitly present entry overrides the runtime's host |

Successful host-side writes are not rolled back when a stop ends Python.
Applications remain responsible for transaction and idempotency policy. To
reuse a session, check `Stopped.SessionKept()` or `Session.Err() == nil` and
handle a concurrent `ErrSessionBusy` admission result.

## Packages

| Import | Holds |
|---|---|
| `github.com/asalimonov/montygo` | runtimes, pools, worker sources, sessions, snapshots, stop policies, the supervisor contract |
| `montygo/sandbox` | the Python value model, print targets, mounts |
| `montygo/sandbox/host` | the host registry, host functions, futures, class wrappers, the OS handler |
| `montygo/sandbox/osaccess` | in-memory OS helpers |
| `montygo/monterr` | every error the library returns |
| `montygo/supervisor/docker` | a supervisor that runs `monty-server` in a container |
| `montygo/supervisor/native` | a supervisor that runs `monty-server` as a child process |
| `montygo/telemetry` | the OpenTelemetry components a pool records into |

The root package has no aliases: a program names value types through `sandbox`,
host objects through `host` and errors through `monterr`.

## Workers

`PoolOptions.Workers` says where a pool's workers come from:

| Source | Workers | Notes |
|---|---|---|
| `montygo.Auto()` (default) | native when a `monty` binary resolves, else wasm | never dials |
| `montygo.Native(NativeOptions{BinaryPath})` | `monty subprocess` children | crash isolation by process; unix only |
| `montygo.Wasm(WasmOptions{CacheDir, DisableCache})` | embedded wasip1 worker under wazero | in-process; compiled once per process and cached on disk |
| `montygo.Remote(supervisor, RemoteOptions{...})` | one WebSocket connection per session to a supervised `monty-server` | see [Remote workers](#remote-workers) |

`Pool.Workers` reports the kind in use: `WorkerNative`, `WorkerWasm` or
`WorkerRemote` (`String()` is `native`, `wasm` or `websocket`).

The native binary resolves from `NativeOptions.BinaryPath`, then `MONTY_BIN`,
then `PATH`, then a cargo `target/` directory in an ancestor or a sibling
`monty` checkout. Pin `BinaryPath` when the environment is not trusted.

### Pool lifecycle

`MinWorkers` (default 1) workers are prewarmed and `MaxWorkers` (default
`runtime.NumCPU()`) caps live workers. `Pool.Stats` counts them by state.
`Pool.Shutdown` ends admission, closes every open session with the stop policy
concurrently and waits for every worker to exit, where `Pool.Close` only retires
idle workers:

```go
pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{MaxWorkers: 2})
session, _ := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
fmt.Printf("%+v\n", pool.Stats()) // {Starting:0 Active:1 Idle:0 Retiring:0}

run := session.Go(ctx, "sum(range(10))", nil)
pool.Shutdown(ctx, montygo.StopPolicy{Drain: 5 * time.Second}) // lets the run end, closes the session, waits for the workers
run.Wait()                                                        // 45; session.State() == montygo.SessionClosed
```

Without `Drain`, running feeds are stopped at once: `KeyboardInterrupt` where
Python yields, a kill at `Timeout` where it does not. There is no separate
force grace. A worker leaving a session is retired asynchronously and counts
toward `MaxWorkers` until it has exited. The context bounds only the caller's
wait: `Shutdown` returns `ctx.Err()` when workers remain, and the stops continue.
`Checkout` after `Shutdown` returns `ErrPoolClosed`.

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
	"fetch_data": func(url string) *host.Future {
		return host.Async(func() (any, error) { return download(url) })
	},
	"greeting": "hello ",
}
session.FeedRun(ctx, "add(2, 3)", &montygo.FeedOptions{ExternalLookup: lookup})                   // 5
session.FeedRun(ctx, "await fetch_data('https://example.com')", &montygo.FeedOptions{ExternalLookup: lookup})
```

Plain Go functions are adapted by reflection: arguments convert to the parameter
types, an optional leading `context.Context` receives the callback context, a
trailing `host.Kwargs` parameter receives keyword arguments, and results may be
`(T)`, `(error)` or `(T, error)`. Return a `*host.Future` (`host.Async` or
`host.NewFuture`) to let other sandbox tasks run while the call completes.
`host.Function` and `host.FunctionFunc` give full control.

Errors cross into the sandbox as Python exceptions: `monterr.Raise("KeyError", "missing")`
raises that type (`monterr.KnownExceptionNames()` lists them), any other error
(or a panic) raises `RuntimeError`.

### Host registry

`host.Host` validates functions and objects when they are registered instead
of when the sandbox calls them, and `RuntimeOptions.Host` exposes them to every
session of the runtime. Names must be Python identifiers and unique;
`FeedOptions.ExternalLookup` entries override them. `Host.Stubs` renders Python
stubs for `TypeCheckStubs`.

```go
h := host.NewHost()
h.Func("add", func(a, b int) int { return a + b })
h.Object("wallet", &Wallet{Balance: 100}, host.ClassInstanceOptions{
	AllowedMethods: host.Expose[Payer](), // exactly the methods of interface Payer
})
rt, _ := montygo.NewRuntime(montygo.RuntimeOptions{Host: h, TypeCheck: true, TypeCheckStubs: h.Stubs()})
session, _ := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
session.FeedRun(ctx, "add(1, wallet.pay(30).balance)", nil) // 71
```

Stubs declare functions and allowed methods; attributes are not declared, so a
type-checked feed reads them through a method or `Any`. Set
`ClassInstanceOptions.ID` on every object that a dump must find again after
`LoadSession` on another checkout of the same runtime; `Host.Restorable`
reports the first object without one as `monterr.ErrHostObjectNotRestorable`,
and `LoadSession` returns that error before loading.

Configure the host before building the runtime. Fixed Go parameters are
positional-only in stubs (`def add(arg0: int, arg1: int, /) -> int`), matching
runtime binding. Optional labels improve readability without enabling keyword
arguments:

```go
h.Func("add", func(a, b int) int { return a + b },
	host.HostFuncOptions{ParameterNames: []string{"left", "right"}})
```

Labels exclude `context.Context` and trailing `Kwargs`, but include a variadic
parameter. Supplied labels must match the signature, be unique, and not be
Python keywords. Direct `Function` implementations retain generic stubs. The
`/` marker follows a fixed parameter only, so a parameterless method renders as
`def reset(self) -> None: ...`.

Methods are labelled by sandbox name through `ClassInstanceOptions.ParameterNames`
and `ClassTypeOptions.ParameterNames`, which covers statics and instance methods
of a host class. Every key must name an exposed method, every list must match
its signature, and instance names override class names:

```go
h.Object("wallet", &Wallet{Balance: 100}, host.ClassInstanceOptions{
	AllowedMethods: host.Expose[Payer](),
	ParameterNames: map[string][]string{"pay": {"amount"}}, // def pay(self, amount: int, /) -> Any: ...
})
```

## Class instances

Wrap a host object in `host.ClassInstance` to expose it under a policy:
attributes sent eagerly, attributes fetched lazily, and methods the sandbox may
call. Returning the instance from the sandbox gives the host the original object.

```go
type Wallet struct{ Balance int }

func (w *Wallet) Pay(amount int) *Wallet { return &Wallet{Balance: w.Balance - amount} }

func wrapWallet(w *Wallet) *host.ClassInstance {
	return host.MustClassInstance(w, host.ClassInstanceOptions{
		EagerAttrs:     host.All(),
		AllowedMethods: host.All(),
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
is `host.All()` (every public name), `host.Names("pay", "balance")`, or
`host.Expose[Payer]()`, which exposes exactly the methods of an interface,
so a method added to the Go type later is not callable until the interface
names it. `AttrProvider`, `AttrLister` and `MethodProvider` replace reflection
for dynamic objects. Names outside the policy raise `AttributeError`; sandbox
mutations stay on the sandbox copy. Every wrapper sent into a session is
retained until the session closes, bounded by `RuntimeOptions.MaxHostObjects`
(default 10 000; instances and their class types both count). Past the bound
the sandbox raises `RuntimeError: host object limit N exceeded`, and the host
sees a `*monterr.ResourceError`. `Session.Stats` reports the counts.

Plain structs cross as named tuples: `host.AsNamedTuple(value)` converts a
struct's exported fields in declaration order, and
`host.NewNamedTuple("Point", sandbox.Pair{Key: "x", Value: 1})` builds one by
hand.

Instances defined inside the sandbox come back as read-only `*host.ClassProxy`
values (`Name`, `ID`, `IsDataclass`, `Attributes`); passing a proxy back hands
the sandbox its original object.

### Record-shaped method results

`monty` tags do not automatically turn arbitrary structs into sandbox records.
Use a per-object converter for your explicit transport type:

```go
func recordResult(_ string, v any) (any, error) {
	switch r := v.(type) {
	case Record:
		return host.AsNamedTuple(r)
	case *Record:
		if r == nil {
			return nil, nil
		}
		return host.AsNamedTuple(*r)
	default:
		return v, nil
	}
}
// Register this before building the runtime:
h.Object("records", table, host.ClassInstanceOptions{
	Name: "Records", AllowedMethods: host.Expose[RecordsAPI](),
	ConvertValue: recordResult,
})
```

Methods can return `(Record, error)` or nullable `(*Record, error)`; Future-resolved
method results use the same converter. Lists/maps containing records require
their own explicit conversion. Storage rows and datetime conversion remain
application responsibilities. The executable example is in
[`record_conversion_test.go`](record_conversion_test.go).

### Host classes

`host.ClassType` exposes a class: `Statics` hold class constants and static
methods (governed by `EagerAttrs`, `LazyAttrs`, `AllowedMethods`), and `Init`
lets the sandbox construct instances, which cross back under the `Instance*`
policies. Without a `Constructor`, construction fills exported fields
positionally or by keyword.

```go
walletClass := host.MustClassType[Wallet](host.ClassTypeOptions{
	Init:                   true,
	InstanceEagerAttrs:     host.All(),
	InstanceAllowedMethods: host.All(),
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
(`Resume` with `FutureResolution`s) and `*Complete`. Pass `ExternalLookup` to
`FeedStart`, give the runtime an `OS` handler, and call `ResumeAuto` to answer
each step the way `FeedRun` would.

Each cursor belongs to one execution and suspension. Replaying a used cursor
returns `ErrSnapshotResumed`; an invalid generation returns `ErrSnapshotStale`.
After an abort, an unused old cursor returns that execution's error without
touching a later feed. Cancelled or busy claims do not consume the cursor.
Pending `AsyncContext` work survives successful steps and is cancelled when the
execution ends. Pending subscriptions belong to call IDs, not Future pointers;
ending one execution never settles a caller-owned Future shared elsewhere.

`snapshot.Dump` serializes a paused worker and `session.LoadSnapshot` restores it
into a fresh session; `session.Dump` and `session.LoadSession` do the same for an
idle session. Mounts are not stored in dumps: the runtime's mounts apply again,
and feed mounts are re-supplied to `LoadSnapshot`.

## Print output

Output goes to `FeedOptions.Print`, else `RuntimeOptions.Print`, else the host
process stdout/stderr. The worker batches output (`RuntimeOptions.PrintFlushInterval`,
default 5 ms; `0` delivers one callback per line). A print target returning an
error fails the feed.

```go
text, _ := sandbox.NewCollectString(sandbox.DefaultMaxPrintCollectBytes)
session.FeedRun(ctx, `print("hello")`, &montygo.FeedOptions{Print: text})
text.Output() // "hello\n"
```

`sandbox.CollectStreams` keeps the stream of each chunk. Both collectors cap host
memory at 10 MiB by default; exceeding the cap raises `MemoryError`.

`sandbox.Lines` delivers complete lines per stream without their newline. A
trailing partial line is delivered when the feed ends, because a
`FlushingPrintTarget` has its `Flush` called at every turn end:

```go
lines := sandbox.Lines(func(stream sandbox.Stream, line string) error {
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
so mounts work with every worker kind. `RuntimeOptions.Mounts` are visible to
every feed; `FeedOptions.Mount` adds mounts for one feed.

```go
mount, _ := sandbox.NewMountDir(sandbox.MountDirOptions{HostPath: "/path/on/host", VirtualPath: "/mnt/data", Mode: sandbox.MountReadOnly})
defer mount.Close()
session.FeedRun(ctx, "open('/mnt/data/file.txt').read()", &montygo.FeedOptions{Mount: []*sandbox.MountDir{mount}})
```

Modes are `read-only`, `read-write` and `overlay` (default: writes stay in memory
and are discarded when the feed ends). Each mount has a 100 MB memory budget
(`MemoryUsageLimit`) and an optional `WriteBytesLimit`. The working directory
defaults to the first mount's virtual path and persists across feeds; `Cwd`
switches it.

OS calls no mount covers reach `RuntimeOptions.OS`; return `host.NotHandled` to decline:

```go
handler := func(ctx context.Context, name string, args []any, kwargs host.Kwargs) (any, error) {
	if name == "os.getenv" && args[0] == "HOME" {
		return "/home/user", nil
	}
	return host.NotHandled, nil
}
rt, _ := montygo.NewRuntime(montygo.RuntimeOptions{OS: handler})
```

Package `sandbox/osaccess` provides a ready-made in-memory filesystem (`OSAccess`,
`MemoryFile`, `CallbackFile`, `StatResult`) and an `Ops` interface for custom
handlers; `osaccess.New(files, environ).Handler()` is an OS handler.

## Resource limits

```go
rt, _ := montygo.NewRuntime(montygo.RuntimeOptions{Limits: &montygo.ResourceLimits{
	MaxMemory:         100 << 20,
	MaxDuration:       5 * time.Second,
	MaxRecursionDepth: 100,
}})
session, _ := pool.Checkout(ctx, rt, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxDuration: time.Second}}) // replaces the runtime's limits for this session
```

`MaxDuration` counts execution time only (not time suspended on the host) and is
backstopped by killing the worker `PoolOptions.DurationLimitGrace` (default 1 s)
after the budget expires. `PoolOptions.RequestTimeout` bounds every protocol
turn (default none for local workers, 10 s for remote ones; `NoRequestTimeout`
disables it). `MaxSuspensions` (default 1000) bounds host round trips per
session; the count is reset by `LoadSession` and `LoadSnapshot`. `MaxMemory` is
also enforced by the worker's allocator; a worker that breaches it is replaced
and the feed fails with `MemoryError`.

A zero limit keeps the default. `montygo.Unlimited` disables `MaxMemory`,
`MaxSuspensions`, `MaxHostObjects` and `MaxPendingFutures`;
`montygo.UnlimitedDuration` disables `MaxDuration`. `MaxRecursionDepth` cannot
be unlimited.

Host-side bounds keep a long session in check: `RuntimeOptions.MaxHostObjects`
(default 10 000) bounds retained host objects and `MaxPendingFutures` (default
1000) bounds unresolved futures per feed. Exceeding either raises
`RuntimeError: <resource> limit N exceeded` in the sandbox; the host sees a
`*monterr.ResourceError` and the session stays usable. `PoolOptions.MaxPendingBytes`
(default 64 MiB, `montygo.UnlimitedPendingBytes` disables) bounds the worker
output buffered by the parent for native and wasm workers; past it the reader
blocks and the worker stalls on its write instead of growing host memory.

## Type checking

```go
rt, _ := montygo.NewRuntime(montygo.RuntimeOptions{TypeCheck: true, TypeCheckStubs: "def fetch(url: str) -> str: ..."})
session, _ := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
_, err := session.FeedRun(ctx, "fetch(123)", nil)
var typingErr *monterr.TypingError // errors.As(err, &typingErr); typingErr.Diagnostics
```

`TypeCheckFormat` picks ty's rendering (`full`, `concise`, `json`, ...) and
`TypeCheckColor` adds ANSI colour. A snippet that fails type checking does not
run. `NewRuntime` rejects an unknown format with a `*monterr.OptionError`.

`AssertMessageAnnotations` controls pytest-style `assert` messages
(`montygo.Uint32(0)` restores CPython's bare `AssertionError`).

## Errors

Every error the library returns is declared in `monterr`.

| Type | Meaning |
|---|---|
| `*monterr.RuntimeError` | a Python exception (`TypeName`, `Message`, `Frames`, `Display(monterr.DisplayTraceback)`) |
| `*monterr.SyntaxError` | the snippet did not parse |
| `*monterr.TypingError` | type checking rejected the snippet |
| `*monterr.CrashedError` | the worker died or timed out; the session is lost, the pool recovers |
| `*monterr.DisconnectError`, `*monterr.ShutdownError` | remote workers only; `DisconnectError` carries the close frame's `Code` and `Reason` |
| `*monterr.RotationError` | a session could not move to a fresh connection; `Dump` restores it elsewhere |
| `*monterr.SpawnError` | a worker could not be started or a server could not be dialed |
| `*monterr.ProtocolError` | a protocol violation; the session is lost |
| `*monterr.ResourceError` | a host-side bound (`MaxHostObjects`, `MaxPendingFutures`) was reached; the session stays usable |
| `*monterr.ConversionError` | a host value cannot cross into the sandbox |
| `*monterr.OptionError` | an option was rejected by `NewRuntime`, `NewPool`, `Checkout` or a supervisor |
| `*monterr.SessionKilledError` | a stop killed the worker at `Timeout`; wraps the stop reason |
| `monterr.ErrCallbackDetached` | `Stop` returned after a kill, but a Go callback still runs past `Join` |
| `monterr.ErrSessionBusy` | another execution or incompatible control operation owns the session |
| `monterr.ErrSessionClosed` | deliberate closure; not session loss |

All sandbox errors implement `monterr.Error`. Unexpected session loss matches
`monterr.ErrSessionLost`; deliberate `ErrSessionClosed` does not. A fatal worker
memory error preserves its `*RuntimeError` details and also matches session loss:

```go
if _, err := session.FeedRun(ctx, code, nil); errors.Is(err, monterr.ErrSessionLost) {
	session, err = pool.Checkout(ctx, rt, montygo.CheckoutOptions{}) // check out a new one
}
```

## Remote workers

A remote pool dials one WebSocket connection per session to a `monty-server`
that a `ServerSupervisor` names. `montygo.StaticServer` is the supervisor of a
fixed URL:

```go
server := montygo.StaticServer(
	"wss://monty.example.com/",
	&tls.Config{MinVersion: tls.VersionTLS12},
	func(ctx context.Context) (map[string]string, error) {
		return map[string]string{"authorization": "Bearer ..."}, nil
	},
)
remote := montygo.RemoteOptions{DialTimeout: 5 * time.Second}
if err := montygo.CheckServerHealth(ctx, server, remote); err != nil {
	fmt.Println("server unavailable:", err)
	return
}
pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{Workers: montygo.Remote(server, remote)})
```

The headers callback runs once per checkout, before the dial, and its error
fails that checkout unchanged. `RemoteOptions.TLSConfig` configures `wss://`
dials (an endpoint's own `TLSConfig` overrides it), `DialContext` replaces the
TCP dialer, and `DialTimeout` bounds each attempt. `CheckServerHealth` sends
`GET <path>/health` through the same transport with the same headers. A dropped
connection raises `*monterr.DisconnectError`; a draining server raises
`*monterr.ShutdownError` whose `Dump` restores the session elsewhere.

A supervisor whose server can move, such as `supervisor/docker` and
`supervisor/native`, resolves the endpoint before every dial attempt.
`RemoteOptions.Recovery` retries a failed dial (3 attempts of 5 s by default)
and, with `RestartServer`, asks the supervisor to restart the server once.
`RotateSessions` moves a session to a fresh connection before the server's
session timeout ends it, keeping the same `*Session`; a rotation that cannot
complete returns `*monterr.RotationError`. Implement `ServerSupervisor` for
servers on other hosts. The pool never owns a supervisor: close it after the
pool. See [docs/architecture/supervisor.md](docs/architecture/supervisor.md).

`montygo.FetchServerInfo` reads `GET <path>/info` from a `monty-server` and
returns its version, upstream revision, protocol version and effective limits,
so a client can size its pool and detect drift before dialing:

```go
info, err := montygo.FetchServerInfo(ctx, server, remote)
if errors.Is(err, monterr.ErrNoServerInfo) {
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
`montygo.Remote(montygo.StaticServer("ws://127.0.0.1:8000/", nil, nil), montygo.RemoteOptions{})`,
and probe readiness first with `montygo.CheckServerHealth`. The REPL example
connects with `go run ./repl -ws ws://127.0.0.1:8000/` from `examples/`.

Each session runs in a fresh worker process. The server clamps client limits to
its ceilings (`MONTY_SERVER_MAX_MEMORY_MIB`, `MONTY_SERVER_MAX_DURATION`,
`MONTY_SERVER_MAX_RECURSION_DEPTH`) and signs every dump with the dump key, so a
dump restores on any server that shares the key. `GET /metrics` serves
Prometheus metrics, and `OTEL_EXPORTER_OTLP_ENDPOINT` exports traces over
OTLP/HTTP.

The listener has no authentication and speaks plain `ws://`. Terminate TLS at an
ingress or reverse proxy and dial it with a `wss://` URL and `TLSConfig`, as in
[Remote workers](#remote-workers). See
[docs/architecture/server.md](docs/architecture/server.md) and
[docs/architecture/docker.md](docs/architecture/docker.md).

### Docker-managed server

`supervisor/docker` starts that image for you through the local `docker` CLI
and serves its endpoint to a pool:

```go
sup, err := docker.New(ctx, docker.Options{MaxSessions: 16})
if err != nil {
	return err
}
defer sup.Close(ctx) // stops and removes the container, after the pool
pool, err := montygo.NewPool(ctx, montygo.PoolOptions{
	Workers:    montygo.Remote(sup, montygo.RemoteOptions{RotateSessions: true}),
	MaxWorkers: 8,
})
if err != nil {
	return err
}
defer pool.Shutdown(ctx)
```

The image tag comes from this package's own version, so a program built against
`montygo v0.3.0` runs `ghcr.io/asalimonov/monty-server:0.3.0`:

| `BindingVersion()` | Images tried, in order |
|---|---|
| `0.3.0` | `…/monty-server:0.3.0` |
| `0.3.0-3f2a9c1`, `0.3.0-3f2a9c1-dirty` | that exact tag, then `…/monty-server:0.3.0` |
| a Go pseudo-version | the release it follows |
| `(devel)`, `0.0.0-unknown` | none; set an override or stamp `-ldflags "-X github.com/asalimonov/montygo.buildVersion=…"` |

Each candidate is used from the local daemon when present, else pulled.
`docker.Options.Image` and `docker.Options.Version`, or `MONTYGO_DOCKER_IMAGE`
and `MONTYGO_DOCKER_VERSION`, override the repository and the tag; an option
wins over a variable, and an explicit version or a pinned `repo:tag` reference
is used verbatim.

The container runs read-only with no capabilities on an ephemeral loopback port,
with a random dump key. montygo disables the server's idle timeout and its memory
and duration ceilings, so the runtime's limits govern as on the local workers;
the session and turn timeouts stay at their defaults. `MaxSessions` sizes the
server (default `2 × runtime.NumCPU()`); give it twice the `MaxWorkers` of the
pools that dial it, because a rotation briefly holds two connections.
`docker.Options.Env` sets any `MONTY_SERVER_*` variable, and `RunArgs` adds
`docker run` flags such as `--memory 2g`. `supervisor/native` runs the
`monty-server` binary as a child process with the same options, less the
container ones.

`docker.New` needs a local Docker daemon. A container left by a process that
died carries the label `io.montygo.supervisor`; `docker.Options.Reaper` is the
hook for removing them, and `docker rm -f $(docker ps -aq --filter label=io.montygo.supervisor)`
does it by hand. See [docs/architecture/supervisor.md](docs/architecture/supervisor.md).

## Observability

Telemetry is a pool parameter. `telemetry.Components` name the OpenTelemetry
tracer, meter and logger a pool records into; nil records nothing, and there is
no process-wide installation:

```go
pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{Telemetry: &telemetry.Components{Tracer: tracer, Meter: meter, Logger: logger}})
```

`telemetry.NewInstrumentation` builds components from providers, the global
OpenTelemetry providers unless replaced, under an `InstrumentationConfig` that
switches each signal:

```go
inst, _ := telemetry.NewInstrumentation(telemetry.InstrumentationConfig{Logs: telemetry.Bool(false)})
inst.SetTracerProvider(tracerProvider)
pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{Telemetry: inst.Components()})
```

Each checkout records a `session {script_name}` span, each run a `run code`
span, each host round trip a child span, printed output as log records, and pool
metrics such as `monty.pool.workers.live`, `monty.pool.workers.retiring`,
`monty.pool.pending_frame_bytes` and `monty.run.duration`. Recorded values
include source code, inputs, outputs and printed text. Flushing belongs to the
providers the application owns.

## Values

| Python | Go |
|---|---|
| `None` | `nil` |
| `bool` | `bool` |
| `int` | `int64` (`*big.Int` beyond int64) |
| `float` | `float64` |
| `str` / `bytes` | `string` / `[]byte` |
| `list` / `tuple` | `[]any` / `sandbox.Tuple` |
| `dict` | `*sandbox.Dict` (insertion-ordered) |
| `set` / `frozenset` | `*sandbox.Set` / `*sandbox.FrozenSet` |
| `date`, `datetime`, `time`, `timedelta`, `timezone` | `sandbox.Date`, `sandbox.DateTime`, `sandbox.Time`, `sandbox.TimeDelta`, `sandbox.TimeZone` |
| `pathlib.Path` | `sandbox.Path` |
| named tuple | `sandbox.NamedTuple` |
| file handle | `*sandbox.FileHandle` |
| class instance | the original host object, or `*host.ClassProxy` |

Inputs also accept Go integer and float kinds, slices, arrays and maps (keys sorted).

## Migration from v0.2.0

v0.3.0 replaces the interrupt mechanism with the stop policy and moves the
sandbox configuration into a `Runtime`. Removed names and their replacements:

| v0.2.0 | v0.3.0 |
|---|---|
| `montygo.New(ctx, Options{Backend: BackendWasm, MaxProcesses: n})` | `montygo.NewPool(ctx, PoolOptions{Workers: montygo.Wasm(WasmOptions{}), MaxWorkers: n})`; `Native`, `Auto` and `Remote` are the other sources |
| `Options.BinaryPath`, `WasmCacheDir`, `DisableWasmCache` | `NativeOptions.BinaryPath`, `WasmOptions.CacheDir`, `WasmOptions.DisableCache` |
| `Pool.Backend()` | `Pool.Workers()` (`WorkerKind`) |
| `pool.Checkout(ctx, CheckoutOptions{Host, OS, TypeCheck, ...})` | `pool.Checkout(ctx, rt, CheckoutOptions{ScriptName, Limits, Stop})` with `rt` from `NewRuntime(RuntimeOptions{Host, OS, Mounts, Print, Limits, TypeCheck, ..., MaxHostObjects, MaxPendingFutures})` |
| `FeedOptions.OS`, `LoadSnapshotOptions.OS` | `RuntimeOptions.OS` |
| `pool.Run(ctx, code, opts)`, `pool.Slot(opts)` | `pool.Run(ctx, rt, code, opts)`, `pool.Slot(rt, opts)` |
| `NewWebSocket(ctx, WebSocketOptions{URL, ConnectHeaders, TLSConfig, ...})` | `NewPool(ctx, PoolOptions{Workers: Remote(StaticServer(url, tls, headers), RemoteOptions{...})})` |
| `WebSocketOptions.Supervisor` | `Remote(supervisor, RemoteOptions{Recovery, RotateSessions, RotationMargin})` |
| `CheckWebSocketHealth(ctx, opts)`, `FetchServerInfo(ctx, opts)` | `CheckServerHealth(ctx, sup, RemoteOptions)`, `FetchServerInfo(ctx, sup, RemoteOptions)` |
| `NewDocker`, `NewDockerSupervisor`, `DockerOptions` | `docker.New(ctx, docker.Options{...})` in `supervisor/docker`, dialed through `Remote`; the application closes the supervisor |
| `Instrument`, `Flush`, `TelemetryComponents` | `PoolOptions.Telemetry *telemetry.Components`; providers flush themselves |
| `DefaultStopPolicy` | the built-in policy (`Timeout` 3 s, `Join` 3 s), overridden by `PoolOptions.Stop` |
| root value, host and error names (`montygo.Dict`, `montygo.NewHost`, `montygo.RuntimeError`, ...) | `sandbox.Dict`, `host.NewHost`, `monterr.RuntimeError`, ... |
| `Run.Interrupt(ctx, InterruptOptions{...})` | `Run.Stop(ctx[, StopPolicy{...}])` |
| `Session.Interrupt(ctx, InterruptOptions{...})` | `Session.Stop(ctx[, StopPolicy{...}])` |
| `InterruptOptions.Reason` / `Grace` | `StopPolicy.Reason` / `Timeout` |
| `InterruptResult{Outcome, RunDone, SessionErr}` | `Stopped{How, Err, SessionErr}`; `Stop` returns after the run ended |
| `InterruptOutcome` and `InterruptAborted`, `InterruptKilled`, `InterruptFinished`, `InterruptNotRunning`, `InterruptPending` | `StopKind` and `StopAborted`, `StopKilled`, `StopFinished`, `StopNotRunning`, `StopPending` |
| `InterruptBeforeStart`, `InterruptAlreadyFinished`, `InterruptUnknown` | `StopAborted`, `StopFinished`; no zero-value outcome |
| `CheckoutOptions.InterruptGrace` | `CheckoutOptions.Stop.Timeout`; also `PoolOptions.Stop` |
| `Session.CloseNow()` | `Session.Close(ctx, montygo.KillNow)` |
| `Session.Close(ctx)` waiting for a running feed and leaving the session open on timeout | `Session.Close(ctx[, policy])` stops the feed first; on timeout it returns `ctx.Err()` and the close continues |
| `Pool.Shutdown(ctx)` with a 5 s force grace | `Pool.Shutdown(ctx[, policy])` stops sessions with the policy; ctx bounds the wait only |
| a cancelled feed context killing the worker while Python runs | the session's stop policy: `KeyboardInterrupt`, then a kill at `Timeout` |

`DurationPtr` stays for `PrintFlushInterval`.

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
make test-docker    # root suite on the docker backend against the image
make test-network   # tests/network against the image
```

On macOS with a beta SDK, `make` selects the 26.5 SDK for cargo and for the
`tests/network` build (`SDKROOT`).

## License

MIT. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
