# syntax=docker/dockerfile:1.7
ARG RUST_VERSION=1.95
ARG MONTY_REV=f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28

FROM --platform=$BUILDPLATFORM rust:${RUST_VERSION}-bookworm AS toolchain
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN apt-get update \
 && apt-get install -y --no-install-recommends cmake python3-pip git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN pip3 install --break-system-packages "ziglang==0.16.0" "cargo-zigbuild==0.23.4"
RUN rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl

FROM toolchain AS monty-git
ARG MONTY_REV
WORKDIR /src
RUN git init -q && git remote add origin https://github.com/pydantic/monty \
 && git fetch -q --depth 1 origin "${MONTY_REV}" && git checkout -q FETCH_HEAD

# Replaced wholesale by `--build-context monty-src=<dir>`.
FROM scratch AS monty-src
COPY --from=monty-git /src/ /

FROM toolchain AS build
ARG TARGETARCH
COPY --from=monty-src / /monty-src/
COPY server/ /work/server/
RUN mkdir -p /out
RUN --mount=type=cache,id=cargo-registry-${TARGETARCH},target=/usr/local/cargo/registry \
    --mount=type=cache,id=monty-target-${TARGETARCH},target=/monty-src/target \
    --mount=type=cache,id=server-target-${TARGETARCH},target=/work/server/target \
    case "${TARGETARCH}" in \
      amd64) T=x86_64-unknown-linux-musl ;; \
      arm64) T=aarch64-unknown-linux-musl ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && cd /monty-src \
 && cargo zigbuild --locked --release --target "$T" -p monty-runtime --no-default-features \
 && cp "target/$T/release/monty" /out/monty \
 && cd /work/server \
 && P='patch."https://github.com/pydantic/monty"' \
 && cargo zigbuild --release --target "$T" \
      --config "$P.monty-pool.path=\"/monty-src/crates/monty-pool\"" \
      --config "$P.monty-proto.path=\"/monty-src/crates/monty-proto\"" \
      --config "$P.monty-types.path=\"/monty-src/crates/monty-types\"" \
      --config "$P.monty-fs.path=\"/monty-src/crates/monty-fs\"" \
      --config "$P.monty-macros.path=\"/monty-src/crates/monty-macros\"" \
 && cp "target/$T/release/monty-server" /out/monty-server \
 && cp /monty-src/LICENSE /out/LICENSE.monty

FROM scratch
ARG MONTY_REV
ARG MONTYGO_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/asalimonov/montygo" \
      org.opencontainers.image.version="0.0.23" \
      org.opencontainers.image.revision="${MONTYGO_REVISION}" \
      io.montygo.monty-rev="${MONTY_REV}"
COPY --from=toolchain /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/monty /usr/local/bin/monty
COPY --from=build /out/monty-server /usr/local/bin/monty-server
COPY --from=build /out/LICENSE.monty /usr/share/licenses/monty/LICENSE
COPY LICENSE /usr/share/licenses/montygo/LICENSE
COPY THIRD_PARTY_NOTICES.md /usr/share/licenses/montygo/THIRD_PARTY_NOTICES.md
ENV MONTY_BIN=/usr/local/bin/monty \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s CMD ["/usr/local/bin/monty-server", "probe"]
ENTRYPOINT ["/usr/local/bin/monty-server"]
CMD ["--host", "0.0.0.0"]
