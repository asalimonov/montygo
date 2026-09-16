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

## Purpose of the project

1. Extend harness of agents on Golang by providing ability to construct task-specific flow at runtime for compact and safe runtime
2. Implement dynamic runtime where interpreators are forbidden by security reasons
3. Experiments with ultra-cheap and limited  by resources runtimes for workers w/o QEMU/KVM and firecracker

Python executes in a crash-isolated Monty worker: a child process, an
in-process WebAssembly instance, or a remote `monty-server`. The host process
exposes only what it names in a `Runtime`: functions, objects, an OS handler,
mounts. A worker that crashes, breaches a limit or violates the protocol is
discarded; the pool recovers.

### What is in repository

`montygo` library, allows to extend and use Monty Pyhon runtime in Golang application.

`monty-server`, an open Full Monty-compatible WebSocket server on Rust which can manage
its local Monty workers.

`montygo-server` Docker image with `monty-server` which spawns workers inside its container. So the same Go program with
`montygo` can run local workers and manage pools of workers on its local host and remote servers.

## Installation

```bash
go get github.com/asalimonov/montygo
```

```go
import "github.com/asalimonov/montygo"
```

The embedded wasm worker works immediately. For native workers, install a
protocol-3 `monty` binary (build `cargo build -p monty-runtime` from a Monty
checkout at `f8acf4fa` or newer) and point `MONTY_BIN` at it. Remote workers
need a `monty-server`; see [Workers](#workers).

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
`ScriptName` (the name in tracebacks and diagnostics), `Limits` (replacing the
runtime's) and `Stop`.

Host extensions are registered once on a `host.Host` and validated when the
runtime is built:

```go
h := host.NewHost()
h.Func("sleep", func(ctx context.Context, ms int) error {
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	case <-ctx.Done(): // ends on Stop, Close or a cancelled feed
		return ctx.Err()
	}
})
h.Object("wallet", &Wallet{Balance: 100}, host.ClassInstanceOptions{
	AllowedMethods: host.Expose[Payer](), // exactly the methods of interface Payer
})
rt, _ := montygo.NewRuntime(montygo.RuntimeOptions{
	Host:   h,
	Limits: &montygo.ResourceLimits{MaxMemory: 100 << 20, MaxDuration: 5 * time.Second},
})
session, _ := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
session.FeedRun(ctx, "sleep(10)\nwallet.pay(30).balance", nil) // int64(70)
```

Value types are named through `sandbox`, host objects through `sandbox/host`
and errors through `monterr`; the root package aliases nothing. Sessions,
snapshots, the stop policy, slots, print, host objects and value conversion are
described in [docs/architecture/session.md](docs/architecture/session.md),
mounts in [mounts.md](docs/architecture/mounts.md), limits and pools in
[pool.md](docs/architecture/pool.md), telemetry in
[telemetry.md](docs/architecture/telemetry.md), and runnable snippets live in
[`example_test.go`](example_test.go).

## Workers

`PoolOptions.Workers` says where a pool's workers come from; `montygo.Auto()`,
the default, picks native when a `monty` binary resolves and wasm otherwise,
and never dials. `Pool.Workers` reports the kind in use (`native`, `wasm` or
`websocket`).

### Native

```go
pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{Workers: montygo.Native(montygo.NativeOptions{})})
```

Native workers are `monty subprocess` child processes, spoken to over Monty's
protobuf wire protocol exactly like the Python and JavaScript bindings: crash
isolation by process, unix only. The binary resolves from
`NativeOptions.BinaryPath`, then `MONTY_BIN`, then `PATH`, then a cargo
`target/` directory in an ancestor or a sibling `monty` checkout. Pin
`BinaryPath` when the environment is not trusted. `MinWorkers` (default 1)
workers are prewarmed; `MaxWorkers` (default `runtime.NumCPU()`) caps live
workers.

### WASM

```go
pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{Workers: montygo.Wasm(montygo.WasmOptions{})})
```

The same worker compiled to WebAssembly is embedded in the Go module and run
in-process by [wazero](https://wazero.io), so nothing has to be installed and
it works where a native worker cannot, such as Windows. The module is verified
against a pinned digest, compiled once per process and cached on disk
(`WasmOptions.CacheDir`, `DisableCache`). Mounts, host objects and telemetry
work as on native. See [docs/architecture/wasm.md](docs/architecture/wasm.md).

### Dockerized server and Docker-managed server

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
the session and turn timeouts stay at their defaults, and `RotateSessions`
moves each session to a fresh connection before the session timeout ends it.
`MaxSessions` sizes the server (default `2 × runtime.NumCPU()`); give it twice
the `MaxWorkers` of the pools that dial it, because a rotation briefly holds
two connections. `docker.Options.Env` sets any `MONTY_SERVER_*` variable, and
`RunArgs` adds `docker run` flags such as `--memory 2g`. `supervisor/native`
runs the `monty-server` binary as a child process with the same options, less
the container ones.

`docker.New` needs a local Docker daemon. A container left by a process that
died carries the label `io.montygo.supervisor`; `docker.Options.Reaper` is the
hook for removing them, and `docker rm -f $(docker ps -aq --filter label=io.montygo.supervisor)`
does it by hand. See [docs/architecture/supervisor.md](docs/architecture/supervisor.md).

### Remote workers

A remote pool dials one WebSocket connection per session to a `monty-server`
that a `ServerSupervisor` names. The server need not be started by the library:
any Full Monty-compatible server, behind any ingress, works. For a fixed URL,
`montygo.StaticServer` is the supervisor:

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

For servers the application starts itself, on other hosts or through an
orchestrator, implement the two-method contract:

```go
type ServerSupervisor interface {
	Endpoint(ctx context.Context) (montygo.ServerEndpoint, error) // where to dial now: URL, headers, TLS
	Restart(ctx context.Context, failed montygo.ServerEndpoint) error
}
```

`Endpoint` runs before every dial, so a server may move; `Restart` is asked
once per incident when `RemoteOptions.Recovery.RestartServer` is set.
`RemoteOptions.Recovery` retries a failed dial (3 attempts of 5 s by default),
`RotateSessions` keeps a session alive across the server's session timeout, and
`montygo.FetchServerInfo` reads the server's version and limits before dialing.
The pool never owns a supervisor: close it after the pool. Details are in
[docs/architecture/supervisor.md](docs/architecture/supervisor.md) and
[websocket.md](docs/architecture/websocket.md).

## License

MIT. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
