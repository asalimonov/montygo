# montygo

montygo is a Go binding for [Monty](https://github.com/pydantic/monty), a sandboxed Python interpreter. It is a pure-Go protocol parent for Monty workers, with the feature set of the official TypeScript package `@pydantic/monty`. It also ports two Python-binding features: the WebSocket transport and the in-memory OS helpers. The repository also builds `monty-server`, an open Full Monty-compatible WebSocket server, and its Docker image.

- Module: `github.com/asalimonov/montygo`, Go 1.25, no cgo. MIT licence.
- Baseline: Monty `0.0.23` plus upstream `main@f8acf4fa`, wire protocol 3.
- Upstream checkout: the sibling directory `../monty`. Parity is judged against it.
- Versions: `MontyVersion` is the upstream release; `BindingVersion()` is this module's release from git tags via `scripts/version.sh`. See `docs/architecture/versioning.md`.
- Architecture: start with `docs/architecture/overview.md`. Parity status is in `docs/parity/`. The original design record is in `docs/brainstorms/`.

## Layout

| Path | Contents |
|---|---|
| `*.go` (package `montygo`) | the API and its engine: `Runtime`, `Pool`, worker sources, sessions, snapshots, stop policy, the supervisor contract, the upstream pins, `BindingVersion`; the ported upstream suites as `*_test.go` |
| `sandbox/` | Python value model, conversion, print targets, mounts |
| `sandbox/host/` | host registry, host functions, futures, class wrappers, the OS handler |
| `sandbox/osaccess/` | in-memory OS helpers, a port of `pydantic_monty/os_access.py` |
| `monterr/` | every error the library returns: sandbox exceptions, infrastructure failures, sentinels, `OptionError` |
| `supervisor/docker/` | supervisor that runs `monty-server` in a container; imports the root |
| `supervisor/native/` | supervisor that runs `monty-server` as a child process; imports the root |
| `telemetry/` | OpenTelemetry components and the instrumentation that builds them; nothing global |
| `internal/buildinfo` | the binding version for packages that cannot import the root |
| `internal/wire` | framing and the hand-written `monty.v1` protobuf codec |
| `internal/value` | Go model of Python values |
| `internal/pool` | worker pool and per-checkout turn engine |
| `internal/worker` | transports: subprocess, wasm (wazero), websocket |
| `internal/wasmblob` | embedded zstd wasm worker and its checksum |
| `internal/mountfs` | host directory mounts, a port of `monty-fs` |
| `internal/telemetry` | OpenTelemetry protocol mirror, a port of `monty-pool` telemetry |
| `montypb/` | generated protobuf types, used only as a test oracle |
| `proto/` | vendored `monty.proto` and `PROTO_REV`, the full upstream SHA |
| `scripts/` | `version.sh` (version from git tags), `check-pins.sh`, and their tests |
| `worker-wasm/` | Rust crate building the worker for `wasm32-wasip1` |
| `server/` | Rust crate `monty-server`: WebSocket server on `monty-pool` `Checkout::turn_raw` |
| `docker/` | `Dockerfile` for the `monty-server` image, `pyclient.Dockerfile` for the Python client test image |
| `tests/network/` | separate Go module: testcontainers tests of the server image |
| `examples/` | separate Go module with ports of upstream `examples/` and the `monty` CLI REPL |
| `changelogs/` | one release note per version |
| `testdata/public_api.golden` | exported API snapshot |

## Prerequisites

- Go 1.25. Always run Go with `GOTOOLCHAIN=local`.
- Rust stable with the `wasm32-wasip1` target, and `zstd`, to rebuild workers. `server/` needs Rust 1.95 or newer.
- `protoc` with `protoc-gen-go` v1.36.x, only to regenerate `montypb`.
- Docker with buildx, for the image and `tests/network`. Loading a multi-platform build needs Docker's containerd image store; without it, set `PLATFORMS` to one platform.
- On macOS with a beta SDK, cargo linking fails, and the Go tests in `tests/network` link through clang because of testcontainers dependencies. `SDKROOT` MUST point at the 26.5 SDK. The Makefile exports it on Darwin. Export it yourself when running cargo or `go test` in `tests/network` directly.

## Commands

```bash
make build-worker            # native worker: cargo build -p monty-runtime in ../monty
make build-wasm              # embedded worker blob + checksum (uses ../monty via MONTY_SRC when present)
make generate                # regenerate montypb from proto/
make version                 # print the version scripts/version.sh derives from git tags
make check-pins              # verify every copy of the upstream pin and MontyVersion
make test-scripts            # shell tests of scripts/
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -count=1 ./...          # native and wasm backends
make test-native | make test-wasm                 # one backend
GOTOOLCHAIN=local go test -race -count=1 ./...
golangci-lint run ./...
UPDATE_GOLDEN=1 GOTOOLCHAIN=local go test -run TestPublicAPISurface .
make examples                # examples module tests
GOTOOLCHAIN=local go mod tidy -diff               # in the root, examples/ and tests/network/
make server-check            # clippy -D warnings and cargo test in server/ (set MONTY_BIN for session tests)
make docker-build            # monty-server image for PLATFORMS (default linux/amd64,linux/arm64)
make docker-build-pyclient   # Python client test image, host architecture
make test-docker             # root suite on the docker backend against the image
make test-network            # tests/network against the images
make test-network-clean      # remove leaked test containers
```

## Environment variables

| Variable | Effect |
|---|---|
| `MONTY_BIN` | native worker binary; tests default it to `../monty/target/debug/monty`; `server/tests/session.rs` skips without it |
| `MONTY_TEST_BACKENDS` | backends for root tests: `native`, `wasm`, `websocket`, `docker`; default `native,wasm`, plus `websocket` when `MONTY_TEST_WS_URL` is set |
| `MONTYGO_DOCKER_IMAGE` | repository, or pinned reference, of the `monty-server` image `supervisor/docker` runs; default `ghcr.io/asalimonov/monty-server` |
| `MONTYGO_DOCKER_VERSION` | image tag for `supervisor/docker`; unset derives it from `BindingVersion()` |
| `MONTY_TEST_WS_URL` | URL of a running `monty-server`; enables the `websocket` backend for root tests |
| `MONTY_EXAMPLES_BACKEND` | forces `native` or `wasm` in the examples |
| `MONTY_SRC` | upstream checkout used by `make build-worker`, `make build-wasm` and the image builds, default `../monty` |
| `MONTY_DOCKER_SRC` | `auto` (default) stages `MONTY_SRC` into the image build when present; any other value builds from the pinned git fetch |
| `PLATFORMS` | buildx platforms for `make docker-build` and `make docker-push`, default `linux/amd64,linux/arm64` |
| `VERSION` | overrides `scripts/version.sh`; stamped into Go binaries, the server and the image |
| `IMAGE`, `PYCLIENT_IMAGE`, `IMAGE_TAG`, `REGISTRY` | image names, the tag (default `$(VERSION)`) and the push registry |
| `MONTYGO_NETWORK_TESTS` | MUST be `1` for `tests/network` to run |
| `MONTYGO_TEST_PARALLEL` | `tests/network` units and `-parallel` budget, default `4` |
| `MONTYGO_TEST_IMAGE` | server image for `tests/network`, default `monty-server:latest` |
| `MONTYGO_PYCLIENT_IMAGE` | Python client image for `tests/network`, default `monty-pyclient:latest`; absent image skips `TestPyClient_*` |
| `MONTYGO_PYTHON_IMAGE` | image of the OTLP receiver container in `TestTelemetry_*`, default `python:3.13-slim-bookworm` |
| `MONTYGO_DUMP_CONTAINER_LOGS` | `0` disables container log files in `tests/network/output/containers/` |
| `MONTYGO_SLOW_TESTS_ENABLE` | enables the slow session and keepalive timeout tests |
| `MONTY_FS_SOAK` | enables the mount race soak tests |
| `UPDATE_GOLDEN` | rewrites `testdata/public_api.golden` |

## Conventions

- Upstream behaviour is the specification. Error messages, traceback text, telemetry names and limits MUST match upstream at the pinned revision. A deliberate deviation MUST be listed in `docs/parity/`.
- Ported tests MUST keep upstream titles as subtest names and run on every backend through `eachBackend`. A test Go cannot express stays as a subtest that calls `t.Skip` with the reason.
- The worker is untrusted. Code that reads worker output MUST validate it and MUST NOT let a reported limit loosen a configured one.
- Backends MUST only implement `worker.Worker`. Pool, session, mount and telemetry code MUST NOT branch on the transport, except to classify how a worker ended.
- The root package MUST NOT alias another package's types. Value types are named through `sandbox`, host objects through `sandbox/host`, errors through `monterr`. Nothing in the library MAY rely on a process-wide variable; configuration travels through `RuntimeOptions`, `PoolOptions` and contexts.
- The root module MUST build without cgo. New dependencies MUST support Go 1.25.
- Server texts (close reasons, HTTP bodies, the info page) MUST be defined in `server/src/texts.rs` and listed in `docs/architecture/server.md`. The protocol version refusal and `PoolError` texts MUST come from upstream verbatim.
- Deviations of `monty-server` from Full Monty (upstream `docs/server.md`) MUST be listed in `docs/parity/server.md`.
- Server variables use the `MONTY_SERVER_*` prefix. Library and `tests/network` variables use `MONTYGO_*`.
- Comments explain only what the code cannot say. Concepts belong in `docs/architecture/`, not in code comments.
- Docs use short sentences and RFC 2119 keywords for obligations.
- Commit subjects are imperative. A body holds only facts the diff cannot show.
- Every change MUST pass vet, tests on both backends, the race detector and golangci-lint before it is called done.
- Every change to code MUST also pass `make server-check`, `make docker-build`, `make test-docker`, `make test-network`, and `go vet ./...` and `go mod tidy -diff` in `tests/network/`.
- Versions come from git tags through `scripts/version.sh`; nothing else hard-codes the binding version. Upstream pins come from `proto/PROTO_REV` and `MontyVersion`; `make check-pins` MUST pass.
- The server version is stamped at build time through `MONTY_SERVER_VERSION`. The crate version in `server/Cargo.toml` is only the fallback.

## Upstream sources of each port

| montygo | Upstream (`../monty`) |
|---|---|
| `proto/monty/v1/monty.proto` | `crates/monty-proto/proto/monty/v1/monty.proto` |
| `montygo.ProtocolVersion` | `PROTOCOL_VERSION` in `crates/monty-proto/src/lib.rs` |
| `worker-wasm/src/subprocess.rs` | `crates/monty-runtime/src/subprocess.rs`, byte-identical |
| `internal/pool` | `crates/monty-pool/src/{pool,checkout,worker}.rs` |
| `internal/telemetry` | `crates/monty-pool/src/telemetry/` |
| `internal/mountfs` and its tests | `crates/monty-fs/src`, `crates/monty-fs/tests` |
| package `montygo` API | `crates/monty-js/ts/` |
| root `*_test.go` | `crates/monty-js/__test__/*.spec.ts` |
| `workers.go`, `serverinfo.go`, `websocket_test.go`, `internal/pool/websocket_test.go` | `crates/monty-python` WebSocket client and tests, `crates/monty-pool/tests/websocket.rs` |
| `server/` | `docs/server.md` (Full Monty behaviour, flags, defaults), `Checkout::turn_raw` in `crates/monty-pool/src/checkout.rs` |
| `docker/pyclient.Dockerfile`, `tests/network/pyclient/` | `crates/monty-python` (`pydantic-monty-client` wheel, `AsyncMontyWebsocket`) |
| `sandbox/osaccess/` | `crates/monty-python/python/pydantic_monty/os_access.py`, `crates/monty-python/tests/test_os_access*.py` |
| `examples/` | `examples/` |
| `examples/repl` | REPL loop in `crates/monty-runtime/src/run.rs`, continuation in `crates/monty/src/repl.rs` |

## Maintenance

### Routine

- Keep `docs/architecture/` in step with design changes, and `docs/parity/` in step with test and API changes.
- A change to exported API MUST update the golden file, `docs/parity/api.md` and the README. README snippets live in `example_test.go`.
- Keep dependencies current with `go get -u` in the root, `examples/` and `tests/network/`, and `cargo update` in `server/`, then run the full verification.

### Supporting a new Monty release

Let `OLD` be the current pin (`proto/PROTO_REV`) and `NEW` the target tag or commit.

1. **Survey upstream.**
   - Update `../monty` and check out `NEW`.
   - Read `git -C ../monty log --oneline OLD..NEW` and the release notes.
   - Diff every source in the table above, for example `git -C ../monty diff OLD NEW --stat -- crates/monty-proto crates/monty-pool crates/monty-fs crates/monty-js crates/monty-python examples docs/server.md`.
   - List the changes per area before editing. Protocol changes come first, because everything else depends on them.
2. **Move the pins.** `scripts/check-pins.sh` is the authority: it reads `proto/PROTO_REV` and `MontyVersion` and names every file that disagrees. Update `proto/PROTO_REV` (full 40-character SHA) and `montygo.go` (`MontyVersion`, `UpstreamRev`) first, then run `make check-pins` and fix each reported file:
   - `worker-wasm/Cargo.toml`: package version and the three git `rev` values, then `cargo update` in `worker-wasm/`
   - `server/Cargo.toml`: the three git `rev` values, then `cargo update` in `server/`; the package version is the server's own fallback and does not track Monty
   - `server/src/version.rs`: `MONTY_REV`, as a full SHA; `monty_rev_matches_lockfile` checks it against `server/Cargo.lock`
   - `docker/Dockerfile`: `ARG MONTY_REV`; the image version comes from the build, not from a label constant
   - `docker/pyclient.Dockerfile`: `ARG MONTY_REV`
   - `.github/workflows/ci.yml`: `MONTY_REV`, as a full 40-character SHA, because `actions/checkout` looks up a short SHA as a branch or tag. The Makefile reads the image build revision from this line.
   - `internal/worker/websocket.go`: `DefaultUserAgent`
   - `README.md` and `THIRD_PARTY_NOTICES.md`
   - tests that assert the version: `internal/wire/codec_test.go`, `internal/worker/wasm_test.go`, `internal/pool/websocket_test.go`
   - the protocol-rejection client: the PyPI `pydantic-monty-client` version and its `/opt/pypi-<version>` venv in `docker/pyclient.Dockerfile`, `pythonPyPI` in `tests/network/pyclient.go`, and the refusal text in `TestPyClient_PyPIProtocol2IsRejected`. It MUST stay a release whose protocol is older than the pin's.

   Find leftovers with `grep -rn "OLD\|<old version>" --exclude-dir=target --exclude-dir=docs --exclude-dir=changelogs --exclude-dir=output .`.
3. **Update the protocol.**
   - Copy the upstream `monty.proto` into `proto/monty/v1/` and run `make generate`.
   - Port new or changed fields into the hand-written codec in `internal/wire`. `montypb` alone is not used at runtime.
   - Extend the differential tests in `internal/wire/codec_test.go` for each new message, field and value kind.
   - If `PROTOCOL_VERSION` changed, update `montygo.ProtocolVersion`. Workers from older releases will then be rejected, so record that in the changelog.
   - Re-check the frame size limit, the decode budget and the value depth costs against `crates/monty-proto`.
4. **Rebuild the workers.**
   - Copy `crates/monty-runtime/src/subprocess.rs` to `worker-wasm/src/subprocess.rs`. It MUST stay byte-identical.
   - If the upstream subprocess entry gained dependencies, add them to `worker-wasm/Cargo.toml`. The crate MUST NOT depend on `monty-runtime` or `monty-fs`, which do not build for WASI.
   - Run `make build-worker` and `make build-wasm`. Commit the new `monty.wasm.zst` and `blob.sha256` together.
5. **Port behaviour.** Work through the survey list:
   - pool, deadlines, budgets and failure classification from `monty-pool`;
   - telemetry names, attributes and state machines from `monty-pool/src/telemetry`;
   - mounts from `monty-fs`;
   - public API from `monty-js/ts`, keeping Go naming;
   - WebSocket and OS helper changes from `monty-python`;
   - server flags, defaults and behaviour from `docs/server.md`, and `turn_raw` or `CheckoutOptions` changes in `monty-pool`, into `server/`, updating `docs/architecture/server.md` and `docs/parity/server.md`.
6. **Port tests.**
   - Port new and changed spec files in `crates/monty-js/__test__` one file at a time, keeping titles.
   - Port changes in `monty-fs/tests`, `monty-pool/tests/websocket.rs` and the Python tests that montygo mirrors.
   - Update the counts and notes in `docs/parity/tests.md`.
7. **Update the examples.**
   - Diff upstream `examples/`. Copy changed sandbox scripts and stub files unchanged, and check them with `cmp`.
   - Update the Go host code, then `examples/README.md` and the examples section of `docs/parity/tests.md`.
   - Check `examples/repl` against upstream: port new cases of `repl_detects_continuation_mode_for_common_cases` into its expected table, and adjust the syntax error messages its classifier matches if the parser's wording changed.
8. **Update the public API record.**
   - Regenerate the golden file with `UPDATE_GOLDEN=1`, and review every added or removed line.
   - Update `docs/parity/api.md`, the README and `example_test.go`.
9. **Write the release note.** Add `changelogs/vX.Y.Z.md` with the structure of `changelogs/v0.0.23.md`: upstream baseline, what's added, parity table, test parity, known limitations, build notes, full changelog link.
10. **Verify.** All of the following MUST pass:
    - `go vet ./...` and `golangci-lint run ./...`
    - `make check-pins` and `make test-scripts`
    - `go test -count=1 ./...` on both backends, and again with `-race`
    - `make examples` with `MONTY_EXAMPLES_BACKEND=native` and with `MONTY_EXAMPLES_BACKEND=wasm`
    - `go mod tidy -diff` in the root, `examples/` and `tests/network/`, and `go vet ./...` in `tests/network/`
    - `make server-check` with `MONTY_BIN` pointing at the new native worker
    - `make docker-build` and `make docker-build-pyclient`
    - `make test-docker`
    - `make test-network`

### Known upstream quirks

- Prebuilt `0.0.23` worker binaries speak protocol 2 and reject this parent. Build the native worker from the pinned revision.
- `monty-runtime` cannot build for `wasm32-wasip1`, which is why `worker-wasm` exists.
- Upstream `examples/sql_playground/sandbox_code.py` imports stubs as `type_stubs`, while session type checking names them `repl_type_stubs`. The Go port skips that type check by default. Re-test this on each upgrade and drop the workaround once upstream fixes it.
- A worker that ran the type checker keeps its caches after `Reset`. Tests with tight memory limits use their own pools, and wasm test pools recycle workers after every checkout.
- BuildKit honours only a `.dockerignore` inside a named context's directory, and `../monty` has none that excludes `target/`. `make docker-stage-src` therefore stages tracked files into `build/monty-src`.
