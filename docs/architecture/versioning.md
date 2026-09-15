# Versioning

montygo has two versions. `MontyVersion` is the upstream Monty release the binding tracks. `BindingVersion()` is this module's own release, derived from git tags at build time. The `monty-server` binary and image carry the binding version.

## Rule

The version of a tree is decided by `scripts/version.sh`, which prints one line:

| State of `HEAD` | Output |
|---|---|
| a `vX.Y.Z[-pre]` tag points at `HEAD` | `X.Y.Z[-pre]` |
| a `v*` tag is reachable from `HEAD` | `<highest reachable tag>-<short hash>` |
| no `v*` tag is reachable | `0.0.0-<short hash>`, with a warning on stderr |
| `git status --porcelain` is not empty | any of the above with `-dirty` appended |
| not a git repository, or no `git` | `0.0.0-unknown`, with a warning on stderr; the build continues |

- Tags are compared as loose semver 2.0.0: `X.Y.Z`, an optional prerelease, an optional build metadata suffix that is ignored when comparing. Tags that do not parse, such as `release-1`, are ignored. The highest tag wins when several apply.
- A development build keeps the last tag as-is. There is no patch bump.
- The short hash is `git rev-parse --short HEAD`.

```
version = base [ "-" hash ] [ "-dirty" ]
base    = major "." minor "." patch [ "-" prerelease ]
hash    = 7*40 hexdigit
```

`scripts/version_test.sh` checks the script against temporary repositories; `make test-scripts` runs it.

## Build time

```
git tag v0.1.0 ── scripts/version.sh ──┬─ Makefile: VERSION, IMAGE_TAG
                                        ├─ go build/test -ldflags "-X github.com/asalimonov/montygo.buildVersion=$(VERSION)"
                                        ├─ docker build --build-arg MONTY_SERVER_VERSION=$(VERSION)
                                        │     └─ server/build.rs → env!("MONTY_SERVER_VERSION") → SERVER_VERSION
                                        └─ image tag monty-server:$(VERSION), label org.opencontainers.image.version
```

- The Makefile evaluates `VERSION` once, from `scripts/version.sh`. `make VERSION=x.y.z` overrides it. `make version` prints it.
- Every Go `test`, `test-native`, `test-wasm`, `examples`, `test-docker` and `test-network` target passes `-ldflags '-X github.com/asalimonov/montygo.buildVersion=$(VERSION)'`.
- `test-network` exports `MONTYGO_BUILD_VERSION=$(VERSION)`. The REPL binary that `tests/network` builds is stamped with it, and `TestAdmission_InfoEndpoint` asserts the server reports it.
- `docker-build` and `docker-push` pass `--build-arg MONTY_SERVER_VERSION=$(VERSION)`. `IMAGE_TAG` defaults to `$(VERSION)`.
- A release build MUST run on a clean checkout of the tag, so that neither `-<hash>` nor `-dirty` appears.

## `BindingVersion()` at run time

`BindingVersion()` in `version.go` resolves in this order:

1. `buildVersion`, the variable stamped by `-ldflags -X`;
2. the module version recorded by `debug.ReadBuildInfo()`: the `Deps` entry for `github.com/asalimonov/montygo` when montygo is imported, the main module when it is built directly; a leading `v` is stripped; a directory `replace` yields `(devel)`, a module `replace` yields the replacement's version;
3. `0.0.0-unknown`.

| Consumer state | `BindingVersion()` |
|---|---|
| `go get github.com/asalimonov/montygo@v0.1.0` | `0.1.0` |
| `go get` of an untagged commit | the Go pseudo-version, for example `0.1.1-0.20260915120000-3f2a9c1abcde` |
| `replace github.com/asalimonov/montygo => ../` without ldflags | `(devel)` |
| this repository's own tests without ldflags | `0.0.0-unknown` |

A Go pseudo-version and the script's `<tag>-<hash>` differ for the same untagged commit. Both name the commit; neither is rewritten to match the other.

`BindingVersion()` is used for the `montygo/<version>` part of the WebSocket `User-Agent`, for the OpenTelemetry instrumentation scope version, and in the REPL banner. `MontyVersion` still goes into `Configure.monty_version`, because the worker compares it with its own build.

## Server

- `server/build.rs` reads `MONTY_SERVER_VERSION` from the environment at compile time and falls back to `CARGO_PKG_VERSION`. It emits `cargo:rustc-env=MONTY_SERVER_VERSION=…` and `rerun-if-env-changed`, so a changed variable rebuilds the crate.
- `server/src/version.rs` defines `SERVER_VERSION = env!("MONTY_SERVER_VERSION")`. It appears in `monty-server --version`, the `GET /` info page, `GET /info` and the `monty_server_build_info` metric.
- The crate version in `server/Cargo.toml` is only the fallback for builds outside the Makefile and the image, such as `cargo test` and `make server-check`.

## Pins

Two files are the sources of the upstream pins:

| Source | Value | Repeated in |
|---|---|---|
| `proto/PROTO_REV` | full 40-character upstream SHA | `montygo.UpstreamRev` (first 8 characters), the three `rev` values in `worker-wasm/Cargo.toml` and in `server/Cargo.toml`, `MONTY_REV` in `server/src/version.rs`, `ARG MONTY_REV` in both Dockerfiles, `MONTY_REV` in `.github/workflows/ci.yml` |
| `MontyVersion` in `montygo.go` | upstream release | `DefaultUserAgent` in `internal/worker/websocket.go`, `version` in `worker-wasm/Cargo.toml` |

`scripts/check-pins.sh` greps every copy and fails with `<file>: expected <value>` for each mismatch. `make check-pins` runs it, and it MUST pass before a release. `scripts/check_pins_test.sh` checks that a drifted copy fails.

## Image

- The image is tagged `$(IMAGE):$(VERSION)` and `$(IMAGE):latest`, and labelled `org.opencontainers.image.version=$(VERSION)`, `org.opencontainers.image.revision=<montygo commit>` and `io.montygo.monty-rev=<MONTY_REV>`.
- `docker-push` adds `--sbom=true --provenance=true`, so the pushed manifest carries an SBOM and a provenance attestation.
- The image bundles the licences of every Rust crate linked into `monty-server` at `/usr/share/licenses/monty-server/THIRD_PARTY_RUST.html`, generated by `cargo-about` from `server/about.toml` and `server/about.hbs`.
