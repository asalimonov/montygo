# Third-party notices

montygo vendors or embeds the following MIT-licensed material from [pydantic/monty](https://github.com/pydantic/monty) at commit f8acf4fa:

- `proto/monty/v1/monty.proto` (the wire protocol schema) and the generated `montypb` package.
- `worker-wasm/src/subprocess.rs` (copied from `crates/monty-runtime/src/subprocess.rs`).
- `internal/wasmblob/monty.wasm.zst` (the Monty worker compiled to wasm32-wasip1).
- Ported behaviour and tests from `crates/monty-js`, `crates/monty-pool`, `crates/monty-fs` and `crates/monty-python`.

```text
The MIT License (MIT)

Copyright (c) Pydantic Services Inc. 2026 to present

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## `monty-server` image

The image built from `docker/Dockerfile` redistributes:

- the upstream `monty` worker binary, built from pydantic/monty at the same commit (MIT, licence above), together with the upstream crates it links. The image also ships the upstream licence at `/usr/share/licenses/monty/LICENSE`;
- the upstream crates `monty-pool`, `monty-proto`, `monty-types`, `monty-fs` and `monty-macros` from the same commit (MIT, licence above), linked into `monty-server`;
- the Rust dependency tree recorded in `server/Cargo.lock`, statically linked into `monty-server`;
- the Debian `ca-certificates` bundle (the Mozilla CA certificate list, MPL-2.0) at `/etc/ssl/certs/ca-certificates.crt`.

The main crates of the `monty-server` dependency tree:

| Crate | Licence |
|---|---|
| `tokio`, `tokio-util` | MIT |
| `axum` | MIT |
| `hyper` | MIT |
| `tokio-tungstenite` | MIT |
| `tungstenite` | MIT OR Apache-2.0 |
| `bytes` | MIT |
| `futures-util` | MIT OR Apache-2.0 |
| `prost` | Apache-2.0 |
| `clap` | MIT OR Apache-2.0 |
| `hmac`, `sha2` | MIT OR Apache-2.0 |
| `prometheus-client` | Apache-2.0 OR MIT |
| `opentelemetry`, `opentelemetry_sdk`, `opentelemetry-otlp` | Apache-2.0 |
| `logfire` | MIT |
| `reqwest` | MIT OR Apache-2.0 |
| `rustls` | Apache-2.0 OR ISC OR MIT |
| `aws-lc-rs` | ISC AND (Apache-2.0 OR ISC) |
| `aws-lc-sys` | ISC AND (Apache-2.0 OR ISC) AND Apache-2.0 AND MIT AND BSD-3-Clause AND (Apache-2.0 OR ISC OR MIT) AND (Apache-2.0 OR ISC OR MIT-0) |

Each crate's licence text is included in its source package on crates.io. `cargo tree --manifest-path server/Cargo.toml` lists the full tree.

The `monty-pyclient` image built from `docker/pyclient.Dockerfile` is a test fixture for `tests/network` and is not distributed.
