# montygo

montygo is a Go binding for [Monty](https://github.com/pydantic/monty), a sandboxed Python interpreter. It is a pure-Go protocol parent for Monty workers, with the feature set of the official TypeScript package `@pydantic/monty`. It also ports two Python-binding features: the WebSocket transport and the in-memory OS helpers.

- Module: `github.com/asalimonov/montygo`, Go 1.25, no cgo. MIT licence.
- Baseline: Monty `0.0.23` plus upstream `main@f8acf4fa`, wire protocol 3.
- Upstream checkout: the sibling directory `../monty`. Parity is judged against it.
- Architecture: start with `docs/architecture/overview.md`. Parity status is in `docs/parity/`. The original design record is in `docs/brainstorms/`.

## Layout

| Path | Contents |
|---|---|
| `*.go` (package `monty`) | public API: pools, sessions, snapshots, host objects, value conversion, print, mounts, errors, telemetry |
| `osaccess/` | in-memory OS helpers, a port of `pydantic_monty/os_access.py` |
| `internal/wire` | framing and the hand-written `monty.v1` protobuf codec |
| `internal/value` | Go model of Python values |
| `internal/pool` | worker pool and per-checkout turn engine |
| `internal/worker` | transports: subprocess, wasm (wazero), websocket |
| `internal/wasmblob` | embedded zstd wasm worker and its checksum |
| `internal/mountfs` | host directory mounts, a port of `monty-fs` |
| `internal/telemetry` | OpenTelemetry protocol mirror, a port of `monty-pool` telemetry |
| `montypb/` | generated protobuf types, used only as a test oracle |
| `proto/` | vendored `monty.proto` and `PROTO_REV` |
| `worker-wasm/` | Rust crate building the worker for `wasm32-wasip1` |
| `examples/` | separate Go module with ports of upstream `examples/` and the `monty` CLI REPL |
| `changelogs/` | one release note per version |
| `testdata/public_api.golden` | exported API snapshot |

## Prerequisites

- Go 1.25. Always run Go with `GOTOOLCHAIN=local`.
- Rust stable with the `wasm32-wasip1` target, and `zstd`, to rebuild workers.
- `protoc` with `protoc-gen-go` v1.36.x, only to regenerate `montypb`.
- On macOS with a beta SDK, cargo linking fails. The Makefile sets `SDKROOT` to the 26.5 SDK. Export it yourself when running cargo directly.

## Commands

```bash
make build-worker            # native worker: cargo build -p monty-runtime in ../monty
make build-wasm              # embedded worker blob + checksum (uses ../monty via MONTY_SRC when present)
make generate                # regenerate montypb from proto/
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -count=1 ./...          # native and wasm backends
make test-native | make test-wasm                 # one backend
GOTOOLCHAIN=local go test -race -count=1 ./...
golangci-lint run ./...
UPDATE_GOLDEN=1 GOTOOLCHAIN=local go test -run TestPublicAPISurface .
make examples                # examples module tests
GOTOOLCHAIN=local go mod tidy -diff               # in the root and in examples/
```

## Environment variables

| Variable | Effect |
|---|---|
| `MONTY_BIN` | native worker binary; tests default it to `../monty/target/debug/monty` |
| `MONTY_TEST_BACKENDS` | backends for root tests, default `native,wasm` |
| `MONTY_EXAMPLES_BACKEND` | forces `native` or `wasm` in the examples |
| `MONTY_SRC` | upstream checkout used by `make build-worker` and `make build-wasm`, default `../monty` |
| `MONTY_FS_SOAK` | enables the mount race soak tests |
| `UPDATE_GOLDEN` | rewrites `testdata/public_api.golden` |

## Conventions

- Upstream behaviour is the specification. Error messages, traceback text, telemetry names and limits MUST match upstream at the pinned revision. A deliberate deviation MUST be listed in `docs/parity/`.
- Ported tests MUST keep upstream titles as subtest names and run on every backend through `eachBackend`. A test Go cannot express stays as a subtest that calls `t.Skip` with the reason.
- The worker is untrusted. Code that reads worker output MUST validate it and MUST NOT let a reported limit loosen a configured one.
- Backends MUST only implement `worker.Worker`. Pool, session, mount and telemetry code MUST NOT branch on the transport, except to classify how a worker ended.
- The root module MUST build without cgo. New dependencies MUST support Go 1.25.
- Comments explain only what the code cannot say. Concepts belong in `docs/architecture/`, not in code comments.
- Docs use short sentences and RFC 2119 keywords for obligations.
- Commit subjects are imperative. A body holds only facts the diff cannot show.
- Every change MUST pass vet, tests on both backends, the race detector and golangci-lint before it is called done.

## Upstream sources of each port

| montygo | Upstream (`../monty`) |
|---|---|
| `proto/monty/v1/monty.proto` | `crates/monty-proto/proto/monty/v1/monty.proto` |
| `monty.ProtocolVersion` | `PROTOCOL_VERSION` in `crates/monty-proto/src/lib.rs` |
| `worker-wasm/src/subprocess.rs` | `crates/monty-runtime/src/subprocess.rs`, byte-identical |
| `internal/pool` | `crates/monty-pool/src/{pool,checkout,worker}.rs` |
| `internal/telemetry` | `crates/monty-pool/src/telemetry/` |
| `internal/mountfs` and its tests | `crates/monty-fs/src`, `crates/monty-fs/tests` |
| package `monty` API | `crates/monty-js/ts/` |
| root `*_test.go` | `crates/monty-js/__test__/*.spec.ts` |
| `websocket.go`, `internal/pool/websocket_test.go` | `crates/monty-python` WebSocket client and tests, `crates/monty-pool/tests/websocket.rs` |
| `osaccess/` | `crates/monty-python/python/pydantic_monty/os_access.py`, `crates/monty-python/tests/test_os_access*.py` |
| `examples/` | `examples/` |
| `examples/repl` | REPL loop in `crates/monty-runtime/src/run.rs`, continuation in `crates/monty/src/repl.rs` |

## Maintenance

### Routine

- Keep `docs/architecture/` in step with design changes, and `docs/parity/` in step with test and API changes.
- A change to exported API MUST update the golden file, `docs/parity/api.md` and the README. README snippets live in `example_test.go`.
- Keep dependencies current with `go get -u` in both modules, then run the full verification.

### Supporting a new Monty release

Let `OLD` be the current pin (`proto/PROTO_REV`) and `NEW` the target tag or commit.

1. **Survey upstream.**
   - Update `../monty` and check out `NEW`.
   - Read `git -C ../monty log --oneline OLD..NEW` and the release notes.
   - Diff every source in the table above, for example `git -C ../monty diff OLD NEW --stat -- crates/monty-proto crates/monty-pool crates/monty-fs crates/monty-js crates/monty-python examples`.
   - List the changes per area before editing. Protocol changes come first, because everything else depends on them.
2. **Move the pins.** Update every place that names the old revision or version:
   - `proto/PROTO_REV`
   - `monty.go`: `Version` and `UpstreamRev`
   - `worker-wasm/Cargo.toml`: package version and the three git `rev` values, then `cargo update` in `worker-wasm/`
   - `.github/workflows/ci.yml`: `MONTY_REV`, as a full 40-character SHA, because `actions/checkout` looks up a short SHA as a branch or tag
   - `internal/worker/websocket.go`: `DefaultUserAgent`
   - `README.md` and `THIRD_PARTY_NOTICES.md`
   - tests that assert the version: `internal/wire/codec_test.go`, `internal/worker/wasm_test.go`, `internal/pool/websocket_test.go`

   Find leftovers with `grep -rn "OLD\|<old version>" --exclude-dir=target --exclude-dir=docs --exclude-dir=changelogs .`.
3. **Update the protocol.**
   - Copy the upstream `monty.proto` into `proto/monty/v1/` and run `make generate`.
   - Port new or changed fields into the hand-written codec in `internal/wire`. `montypb` alone is not used at runtime.
   - Extend the differential tests in `internal/wire/codec_test.go` for each new message, field and value kind.
   - If `PROTOCOL_VERSION` changed, update `monty.ProtocolVersion`. Workers from older releases will then be rejected, so record that in the changelog.
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
   - WebSocket and OS helper changes from `monty-python`.
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
    - `go test -count=1 ./...` on both backends, and again with `-race`
    - `make examples` with `MONTY_EXAMPLES_BACKEND=native` and with `MONTY_EXAMPLES_BACKEND=wasm`
    - `go mod tidy -diff` in the root and in `examples/`

### Known upstream quirks

- Prebuilt `0.0.23` worker binaries speak protocol 2 and reject this parent. Build the native worker from the pinned revision.
- `monty-runtime` cannot build for `wasm32-wasip1`, which is why `worker-wasm` exists.
- Upstream `examples/sql_playground/sandbox_code.py` imports stubs as `type_stubs`, while session type checking names them `repl_type_stubs`. The Go port skips that type check by default. Re-test this on each upgrade and drop the workaround once upstream fixes it.
- A worker that ran the type checker keeps its caches after `Reset`. Tests with tight memory limits use their own pools, and wasm test pools recycle workers after every checkout.
