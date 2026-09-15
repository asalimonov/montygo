# Testing

## Root suite

- Root tests run every scenario on each backend in `MONTY_TEST_BACKENDS` via `eachBackend`. Backend names are `native`, `wasm` and `websocket`. The default is `native,wasm`, plus `websocket` when `MONTY_TEST_WS_URL` is set.
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
- `make test-docker` runs the suite against the image. It starts one container on a random loopback port with the test dump key, the per-client quota disabled and the label `montygo.test=docker`. It waits for `/health`, runs `go test -count=1 -timeout 30m .` with `MONTY_TEST_BACKENDS=websocket`, and stops the container.
- Adaptations for this backend are listed in `docs/parity/tests.md`.

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
