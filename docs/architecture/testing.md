# Testing

## Root suite

- Root tests run every scenario on each backend in `MONTY_TEST_BACKENDS` via `eachBackend`. Backend names are `native`, `wasm`, `websocket` and `docker`. The default is `native,wasm`, plus `websocket` when `MONTY_TEST_WS_URL` is set.
- `websocket` and `docker` are remote backends: `remoteBackend` selects the adaptations they share, such as signed dumps and disconnects instead of crashes.
- Each top-level test gets its own pools, like one pool per upstream spec file. Wasm pools recycle workers after every checkout. Tests that inspect worker reuse create their own pools.
- `TestMain` points `MONTY_BIN` at a sibling `../monty/target/debug/monty` when it is unset. It exits 2 when `websocket` is listed without `MONTY_TEST_WS_URL`.
- The upstream TypeScript suite is ported file by file; titles are kept as subtest names. See `docs/parity/tests.md`.
- `example_test.go` holds the README snippets as runnable examples; `public_api_test.go` checks the exported API against `testdata/public_api.golden` (regenerate with `UPDATE_GOLDEN=1`).
- Internal packages have unit tests: codec differential tests, fake-worker pool tests, transport tests and the ported `monty-fs` suite.
- `examples/` is a separate module with a test per example program.

## `websocket` backend

- `openPool` builds every root test pool. For `websocket` it maps `Options` onto `WebSocketOptions` against `MONTY_TEST_WS_URL`: `MaxProcesses`, `CheckoutTimeout` and `RequestTimeout`, where 0 becomes `NoRequestTimeout`.
- A local backend that cannot start skips its subtests. A configured server that cannot be reached fails them.
- Telemetry child processes receive the backend by name.
- `make test-docker` runs the suite on the `docker` backend with `MONTYGO_DOCKER_IMAGE=$(IMAGE):$(IMAGE_TAG)`. `openPool` calls `NewDocker`, so every test pool starts and stops its own container. Each one gets the same test dump key, so a dump taken from one pool loads into another.

## Supervisors, recovery and rotation

These need no Docker daemon:

- `docker_image_test.go` covers candidate tags, option and variable precedence, pinned references and pseudo-version bases.
- `docker_supervisor_test.go` drives `DockerSupervisor` against a fake `docker` script and an `httptest` server: the start sequence, hardening flags, labels, the dump key passed by name only, image fallback, protocol refusal, restart with a new port, and close.
- `recovery_test.go` covers the attempt loop, non-retryable errors, restart only when enabled, and single-flight restarts.
- `rotation_test.go` drives real sessions over `wsRelay`, which serves `GET /info` with short timeouts and can refuse upgrades: state survives a rotation, an idle session rotates on its timer, a refused reconnect yields `RotationError` whose dump restores, and unusable limits leave rotation off.

Tests live with the code they cover: the image resolver and the fake-CLI supervisor tests in `supervisor/docker`, the recovery loop in `supervisor`, the host internals in `runtime/host`, the driver transitions in `internal/engine`, and the ported upstream suites in the root package against the facade. `supervisor/native` tests spawn a real `monty-server`; they skip when the server binary or the worker is missing.
- Adaptations for this backend are listed in `docs/parity/tests.md`.

## Lifecycle regressions

Lifecycle regressions run through eachBackend, including 1000 immediate
Go/Stop iterations per backend under -race. `lifecycle_state_test.go` owns
driver transitions directly to test before-send, completion/force ordering,
stale timers, stale step cancellation, duplicate call IDs, and ID exhaustion.
`execution_test.go` covers busy rejection, stale handles/snapshots and a callback
that outlives worker termination. `stop_test.go` covers the stop policy: levels,
`Drain`, `KillNow`, catchable delivery, repeated stops, `Close` with a policy,
`Session.State`, `Pool.Run`, `Pool.Shutdown` with `Drain` and `Slot`. `execution_future_test.go` covers shared Futures,
failure cleanup, snapshot context lifetime and manual settlement. The pool lease
tests cover blocked Print, release/force races and delayed observed exits.

Release verification MUST run the race suite against an explicitly configured
WebSocket server as well as native and wasm. A skipped WebSocket backend is not
evidence of a pass. The public API golden requires review; exported names alone
do not validate changed call signatures.

## Server tests

- `make server-check` runs `cargo clippy --all-targets -- -D warnings` and `cargo test` in `server/`.
- Unit tests cover configuration rules, admission, client identity, ceilings, the dump envelope, metrics rendering and texts. `version.rs` checks `MONTY_REV` against `server/Cargo.lock`.
- `server/tests/session.rs` drives real sessions and skips when the worker binary in `MONTY_BIN` is not found.

## `tests/network`

- A separate Go module that tests the image through testcontainers-go. `make test-network` runs it; without `MONTYGO_NETWORK_TESTS=1` it exits at once.
- `ContainerPool` lends `MONTYGO_TEST_PARALLEL` units. A unit is one `monty-server` container; tests queue for a free one.
- A unit that ran with options, was signalled or was recreated is replaced by a fresh default container at release. A unit that is not healthy and idle after a test is replaced too.
- Metric assertions compare deltas from a baseline, because default units are reused.
- REPL tests cover sessions through montygo, and the `examples/repl` binary built once and driven with `-ws` over pipes.
- Python interop tests run the `monty-pyclient` image as one-shot containers that dial the server's container IP. The client built at the pin exercises feeds, host functions, dumps and drain. The PyPI `0.0.23` client asserts the protocol 2 refusal text.
- Container logs stream to `tests/network/output/containers/`. See `tests/network/README.md`.
