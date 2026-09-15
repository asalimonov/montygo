# Docker image

`docker/Dockerfile` builds the `monty-server` image. `docker/pyclient.Dockerfile` builds a Python client image that only `tests/network` uses.

## Image layout

The final stage is `scratch`: no shell, no package manager, no libc.

| Path | Contents |
|---|---|
| `/usr/local/bin/monty-server` | the server, a static musl binary |
| `/usr/local/bin/monty` | the upstream worker built at `MONTY_REV`, a static musl binary |
| `/etc/ssl/certs/ca-certificates.crt` | CA bundle for OTLP export over HTTPS |
| `/usr/share/licenses/monty/LICENSE` | upstream Monty licence |
| `/usr/share/licenses/montygo/LICENSE`, `THIRD_PARTY_NOTICES.md` | montygo licence and notices |

| Setting | Value |
|---|---|
| `USER` | `65532:65532` |
| `ENV` | `MONTY_BIN=/usr/local/bin/monty`, `SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt` |
| `EXPOSE` | `8000` |
| `ENTRYPOINT` | `/usr/local/bin/monty-server` |
| `CMD` | `--host 0.0.0.0` |
| `HEALTHCHECK` | `monty-server probe`, every 10 s, timeout 3 s, start period 5 s |
| labels | `org.opencontainers.image.source`, `.version`, `.revision` (montygo commit), `io.montygo.monty-rev` |

- `scratch` has no shell, so the healthcheck runs the server binary. `probe` sends `GET http://127.0.0.1:${MONTY_SERVER_PORT:-8000}/health` and exits 0 on 200.
- Arguments after the image name replace `CMD`. A caller that passes flags MUST include `--host 0.0.0.0`, or the server binds the container's loopback.
- The server refuses to start without `MONTY_SERVER_DUMP_KEY`.

## Build stages

| Stage | Base | Work |
|---|---|---|
| `toolchain` | `rust:${RUST_VERSION}-bookworm` on `$BUILDPLATFORM` | cmake, git, CA certificates, `ziglang==0.16.0`, `cargo-zigbuild==0.23.4`, both musl targets |
| `monty-git` | `toolchain` | shallow fetch of `MONTY_REV` from `pydantic/monty` |
| `monty-src` | `scratch` | the `monty-git` tree; a named build context replaces it |
| `build` | `toolchain` | builds `monty` and `monty-server` for `$TARGETARCH` |
| final | `scratch` | assembles the image |

The `build` stage:

1. maps `TARGETARCH` to `x86_64-unknown-linux-musl` or `aarch64-unknown-linux-musl`; any other architecture fails;
2. builds the worker in `/monty-src` with `cargo zigbuild --locked --release -p monty-runtime --no-default-features`;
3. builds the server in `/work/server` with `cargo zigbuild --release`, patching `monty-pool`, `monty-proto`, `monty-types`, `monty-fs` and `monty-macros` to paths inside `/monty-src` through `--config patch."https://github.com/pydantic/monty".<crate>.path`.

- The patches make the worker and the server share one source tree, whichever context supplied it.
- The server build omits `--locked`, because the path patches change the sources recorded in `server/Cargo.lock`.

## Cross-compilation

- Both architectures compile on the build platform. No stage runs under QEMU.
- cargo-zigbuild uses zig as the C compiler and linker for the musl targets, including the C code of `aws-lc-sys`, which also needs cmake.
- The binaries are static, so the final stage needs no libc.
- Cache mounts are per architecture: `cargo-registry-${TARGETARCH}`, `monty-target-${TARGETARCH}` and `server-target-${TARGETARCH}`. Buildx runs the platform builds in parallel, and separate ids keep them off each other's cargo locks and target directories.

## Upstream sources

- `MONTY_REV` is a full 40-character SHA. The Makefile reads it from the `MONTY_REV:` line of `.github/workflows/ci.yml` and passes it as a build argument. Both Dockerfiles default to the same SHA.
- `MONTY_DOCKER_SRC=auto`, the default, uses the local checkout when `$(MONTY_SRC)/crates/monty-proto` exists. Any other value, or a missing checkout, builds from the `monty-git` fetch.
- `make docker-stage-src` copies the files that `git ls-files --cached --others --exclude-standard` lists in `MONTY_SRC` into `build/monty-src`. The build then passes `--build-context monty-src=build/monty-src`.
- The checkout is staged instead of passed directly. BuildKit honours only a `.dockerignore` inside a named context's own directory, and `../monty/target` holds gigabytes of build output.
- The root `.dockerignore` admits only `server/` without `server/target/`, `docker/`, `tests/network/pyclient/`, `LICENSE` and `THIRD_PARTY_NOTICES.md`.
- An image built from a staged checkout still carries the pinned `MONTY_REV` in its label and in the dump MAC. Such images are for development; their dumps might not load into images built at the pin.

## Tags

| Image | Tags |
|---|---|
| server | `$(IMAGE):$(IMAGE_TAG)` and `$(IMAGE):latest`; `IMAGE` defaults to `monty-server` |
| Python client | `$(PYCLIENT_IMAGE):$(IMAGE_TAG)` and `$(PYCLIENT_IMAGE):latest`; `PYCLIENT_IMAGE` defaults to `monty-pyclient` |
| pushed | `$(REGISTRY)/$(IMAGE):$(IMAGE_TAG)` only |

`IMAGE_TAG` is `<Version>-<UpstreamRev>` from `monty.go`, for example `monty-server:0.0.23-f8acf4fa`. The tag does not change with server code, so an image MUST be rebuilt after a change to `server/` or `docker/`.

## Makefile targets

| Target | Effect |
|---|---|
| `docker-stage-src` | stages `MONTY_SRC` into `build/monty-src` when the override applies |
| `docker-build` | `docker buildx build --platform $(PLATFORMS) --load` of the server image; passes `BUILDX_CACHE_FROM` and `BUILDX_CACHE_TO` as `--cache-from` and `--cache-to` when set |
| `docker-build-pyclient` | builds and loads the Python client image for the host architecture |
| `docker-push` | pushes the multi-architecture server manifest to `REGISTRY`, which is required |
| `server-check` | `cargo clippy --locked --all-targets -- -D warnings` and `cargo test --locked` in `server/` |
| `test-docker` | root suite on the `websocket` backend against one container |
| `test-network` | `tests/network` against `$(IMAGE):$(IMAGE_TAG)` and `$(PYCLIENT_IMAGE):$(IMAGE_TAG)` |
| `test-network-clean` | removes containers labelled `montygo.test` |

- `PLATFORMS` defaults to `linux/amd64,linux/arm64`. Loading a multi-platform build with `--load` needs Docker's containerd image store. Without it, set `PLATFORMS` to one platform.
- `docker-push` does not pass `MONTYGO_REVISION`, so pushed images carry the revision label `unknown`.

## Running

```bash
docker run --rm \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --pids-limit 512 --memory 6g \
  -e MONTY_SERVER_DUMP_KEY="$(openssl rand -hex 16)" \
  -p 127.0.0.1:8000:8000 \
  monty-server:latest
```

- Configuration SHOULD come from environment variables. `CMD` then stays intact, and the dump key stays out of `ps` and shell history.
- The container SHOULD run with `--read-only`, `--cap-drop ALL` and `--security-opt no-new-privileges`. The server binds an unprivileged port as uid 65532.
- `--pids-limit` SHOULD leave room above `--max-sessions`: every session runs one worker process, and the limit also counts threads.
- `--memory` SHOULD bound the container. `--max-memory-mib` limits allocator bytes per session, not RSS. Size it from `--max-sessions × (--max-memory-mib + worker baseline + headroom)` plus server overhead, as upstream `docs/server.md` describes.
- The listener speaks plain `ws://` without authentication. It SHOULD be reachable only from a private network or through a TLS ingress. See `server.md`.
- Replicas MUST share `MONTY_SERVER_DUMP_KEY`, so a dump restores on any of them.
- The orchestrator's stop grace period SHOULD exceed `--drain-grace`.

## Python client image

`docker/pyclient.Dockerfile` is a test fixture for `tests/network`. It is built for the host architecture only.

| Stage | Work |
|---|---|
| `toolchain` | `python:3.13-bookworm` with a minimal stable Rust toolchain and `maturin==1.15.0` |
| `monty-git`, `monty-src` | same source selection as the server image |
| `wheel` | `maturin build --release --locked -m crates/monty-python/Cargo.toml -i python3.13 --compatibility linux` |
| final | `python:3.13-slim-bookworm` |

- `/opt/pin` holds a venv with the `pydantic_monty_client` wheel built at the pin.
- `/opt/pypi-0.0.23` holds a venv with `pydantic-monty-client==0.0.23` from PyPI. That release speaks protocol 2, and the tests assert the server's refusal.
- `tests/network/pyclient/*.py` are copied to `/scripts`. The image runs as uid 65532 with `ENTRYPOINT /opt/pin/bin/python`.
- Cache mounts use the ids `cargo-registry` and `pyclient-target`.

## CI

| Job | Runner | Steps |
|---|---|---|
| `docker` | `ubuntu-latest` | native worker, `make server-check`, amd64 image with the GitHub Actions cache, Python client image, `make test-docker`, vet and tidy of `tests/network`, `make test-network` |
| `docker-arm64` | `ubuntu-24.04-arm` | arm64 image, `make test-network` |

- Both jobs check out `pydantic/monty` at `MONTY_REV` into `monty-src` and build with `MONTY_SRC=monty-src`.
- `docker-arm64` builds no Python client image, so `TestPyClient_*` skip there.
