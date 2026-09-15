# tests/network

Integration tests of the `monty-server` image. They run server containers through testcontainers-go and talk to them with montygo, raw WebSocket frames, `examples/repl -ws` and the Python client. The module has its own `go.mod` and replaces `github.com/asalimonov/montygo` with `../..`.

## Container pool

```
TestMain ── MONTYGO_NETWORK_TESTS=1 ── GetPool() ── Start(ctx)
                                           │
        ┌──────────────────────────────────┼──────────────────────────────────┐
     Unit 0                             Unit 1               …             Unit N-1
  lifecycle goroutine                lifecycle goroutine
  one monty-server container         one monty-server container
        │                                  │
        └────────────▶  available (chan *Unit)  ◀────────────┘
                              ▲            │
             cmdRelease       │            │ Acquire(ctx, cfg): recreate when cfg differs
   health + idle check,       │            ▼
   or recreate with defaults  │      SetupServer(t, opts...)
                              └────── t.Cleanup: Release(t, unit, dirty)

unit states: cold ─▶ ready ─▶ inUse ─▶ releasing ─▶ ready
                                            └──────▶ dead
```

- `N` is `MONTYGO_TEST_PARALLEL`. Units are exclusive: one test owns a unit at a time, and tests queue on `available`.
- `Start` returns when the first unit is ready. It fails when every unit fails, or at once on an infrastructure error: an unreachable Docker daemon, no space left, a missing image, a denied pull or a name conflict.
- A default container gets `MONTY_SERVER_DUMP_KEY=montygo-network-test-dump-key`, `MONTY_SERVER_MAX_SESSIONS_PER_CLIENT=0`, `--host 0.0.0.0` and the label `montygo.test=network`. Start waits up to 30 s for `/health` and retries 3 times.
- At release, a unit that ran with options, was signalled or recreated is replaced by a fresh default container. Any other unit MUST answer `/health` with 200 and report `monty_server_sessions_active 0` within 5 s, or it is replaced too.
- A unit that cannot be recreated is dead. There are no spare units.

## Running

```bash
make docker-build PLATFORMS=linux/arm64   # the host architecture is enough
make docker-build-pyclient                # optional; TestPyClient_* skip without it
make test-network
```

One group, directly:

```bash
cd tests/network
MONTYGO_NETWORK_TESTS=1 MONTYGO_TEST_IMAGE=monty-server:0.0.23-f8acf4fa \
  GOTOOLCHAIN=local go test -count=1 -parallel 4 -run 'TestDrain_' ./...
```

## Environment variables

| Variable | Default | Effect |
|---|---|---|
| `MONTYGO_NETWORK_TESTS` | unset | MUST be `1`; otherwise `TestMain` prints a skip line and exits 0 |
| `MONTYGO_TEST_PARALLEL` | `4` | number of units; `make test-network` also passes it to `-parallel` |
| `MONTYGO_TEST_IMAGE` | `monty-server:latest` | server image; `make test-network` sets `$(IMAGE):$(IMAGE_TAG)` |
| `MONTYGO_PYCLIENT_IMAGE` | `monty-pyclient:latest` | Python client image; `TestPyClient_*` skip when it is absent |
| `MONTYGO_PYTHON_IMAGE` | `python:3.13-slim-bookworm` | image of the OTLP receiver container in `TestTelemetry_*` |
| `MONTYGO_DUMP_CONTAINER_LOGS` | enabled | `0` disables log files |
| `MONTYGO_SLOW_TESTS_ENABLE` | unset | any value except empty, `0` and `false` enables slow tests |

## Test groups

| Prefix | File | Covers |
|---|---|---|
| `TestAdmission_` | `server_admission_test.go` | 503 at capacity, 429 over quota, `--trust-forwarded-for`, `/`, `/health`, `/metrics`, listener closed during drain |
| `TestProtocol_` | `server_protocol_test.go` | session isolation, protocol version refusal, `Configure` first, lifecycle and text-message 1008 closes, large frames, memory kill, dial by container IP |
| `TestLimits_` | `server_limits_test.go` | memory, duration and recursion ceilings, lower client limits, disabled ceilings |
| `TestTimeouts_` | `server_timeouts_test.go` | idle, turn including a host callback, session (slow), keepalive with a frozen client (slow) |
| `TestDumps_` | `server_dumps_test.go` | signed dump restore after recreate, tampered dump, local wasm dump, previous key, unknown key, dump metrics |
| `TestDrain_` | `server_drain_test.go` | `ShutdownDump` for an idle session, no dump before `Configure`, in-flight turn, grace expiry, second signal, exit code 0 |
| `TestTLS_` | `server_tls_test.go` | `wss://` through a TLS reverse proxy, health check over TLS, `DialContext` |
| `TestParallel_` | `server_parallel_test.go` | 32 sessions on one unit, abandoned turns release their sessions |
| `TestTelemetry_` | `server_telemetry_test.go` | `traceparent` parents the connection span, policy event on the connection span |
| `TestRepl_` | `repl_session_test.go` | REPL sessions: state, errors, dump restore across recreate, drain restore of a suspended feed, type-check stubs |
| `TestReplCLI_` | `repl_cli_test.go` | `examples/repl -ws` over pipes: state and print, continuation, errors, mounts, interrupt, unavailable server |
| `TestPyClient_` | `pyclient_test.go` | pinned Python client: feed, host function and dump restore, drain dump restore; PyPI `0.0.23` client refused |

## Writing a test

- Name it `Test<Group>_<Behaviour>` and put it in the group's file. Call `t.Parallel()` first. Signalling or recreating a container is safe, because units are exclusive.
- `s := SetupServer(t, opts...)` lends a unit until the test ends. Options:
  - `WithArgs(flags...)` appends server flags;
  - `WithEnv(key, value)` sets a container variable.
- Clients:
  - `s.NewPool(montygo.WebSocketOptions{...})` fills `URL` and a 30 s `RequestTimeout`, and closes the pool at test end;
  - `s.Checkout(ctx, pool, opts)` closes the session at test end;
  - `s.WSOptions()` returns options for `montygo.CheckWebSocketHealth`;
  - `s.RawDial(ctx, header)` with `sendRequest`, `readEvent`, `rawConfigured` and `requireClose` from `wire.go` speaks raw protocol frames.
- HTTP: `s.Get(ctx, path)` and `s.Metrics(ctx)`.
- Metrics:
  - Default units are reused, so counters MUST be compared as deltas: `base := s.Baseline()`, then `s.WaitMetricDelta(base, name, labels, delta, timeout)`.
  - `s.WaitMetric(name, labels, want, timeout)` with an absolute value is safe for gauges, and for counters on a unit started with options, which is always fresh.
- Lifecycle:
  - `s.Signal("TERM")` signals the server process and marks the unit dirty;
  - `s.WaitExited(timeout)` returns the container's exit code;
  - `s.Recreate(opts...)` replaces the container, keeping its configuration unless options are given;
  - `s.Logs()` returns the container log so far.
- Helpers: `requireSlowTests(t)` gates a slow test; `pyClientImage(t)` resolves or skips the Python image; `runPyClient` and `startPyClient` run scripts from `pyclient/`; `StartRepl(t, s.URL(), args...)` runs the REPL binary; `startOTLPReceiver(t)` starts an OTLP/HTTP receiver container; pass `receiver.Endpoint()` to `--otlp-endpoint` and read spans with `receiver.WaitSpan`.
- Close every session before the test ends. A unit with open sessions fails the idle check and costs a recreate.

## Time budget

- `make test-network` runs `go test -timeout 25m`.
- `TestMain` waits up to 5 minutes for the first unit. `SetupServer` waits up to 10 minutes for a free unit. Each `testCtx` lasts 5 minutes.
- Every recreate costs a container start. Tests SHOULD use default units unless they need options, SHOULD use 1–2 s flag values and short sleeps, and SHOULD gate anything longer behind `requireSlowTests`.
- A higher `MONTYGO_TEST_PARALLEL` shortens the run on a machine with spare cores.

## Traps

- **Stale `:latest`.** A direct `go test` uses `monty-server:latest`, which can predate the current code. The versioned tag does not change with server code either. Rebuild after every change to `server/` or `docker/`, and set `MONTYGO_TEST_IMAGE` or use `make test-network`.
- **Leaked containers.** A killed run leaves containers labelled `montygo.test`. `make test-network-clean` removes them.
- **macOS SDK.** The testcontainers dependencies link through clang. With a beta macOS SDK, `SDKROOT` MUST point at the 26.5 SDK. The Makefile exports it on Darwin; export it yourself for a direct `go test`.
- **Container addresses.** Bridge addresses are routable from the host only on Linux, so `TestProtocol_RemoteDialByContainerIP` skips elsewhere. Python client containers dial the server's container IP on every OS.
- **Telemetry receiver.** `TestTelemetry_*` export to a receiver container on the default bridge, not to the test process, because host firewalls (for example ufw with `INPUT DROP`) block container-to-host traffic.
- **Logs.** Each unit streams its log to `output/containers/<test>-u<unit>.log`. A failing test prints the path.
