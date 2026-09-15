# syntax=docker/dockerfile:1.7
ARG MONTY_REV=f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28

FROM python:3.13-bookworm AS toolchain
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo PATH=/usr/local/cargo/bin:$PATH
RUN curl -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain stable \
 && pip install --no-cache-dir "maturin==1.15.0"

FROM toolchain AS monty-git
ARG MONTY_REV
WORKDIR /src
RUN git init -q && git remote add origin https://github.com/pydantic/monty \
 && git fetch -q --depth 1 origin "${MONTY_REV}" && git checkout -q FETCH_HEAD

# Replaced wholesale by `--build-context monty-src=<dir>`.
FROM scratch AS monty-src
COPY --from=monty-git /src/ /

FROM toolchain AS wheel
COPY --from=monty-src / /monty-src/
WORKDIR /monty-src
RUN --mount=type=cache,id=cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=pyclient-target,target=/monty-src/target \
    maturin build --release --locked -m crates/monty-python/Cargo.toml \
      -i python3.13 --compatibility linux -o /wheels

FROM python:3.13-slim-bookworm
COPY --from=wheel /wheels/ /wheels/
RUN python -m venv /opt/pin \
 && /opt/pin/bin/pip install --no-cache-dir /wheels/pydantic_monty_client-*.whl \
 && python -m venv /opt/pypi-0.0.23 \
 && /opt/pypi-0.0.23/bin/pip install --no-cache-dir "pydantic-monty-client==0.0.23"
COPY tests/network/pyclient/ /scripts/
USER 65532:65532
ENTRYPOINT ["/opt/pin/bin/python"]
