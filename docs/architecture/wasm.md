# Embedded wasm worker

- `worker-wasm` builds `monty subprocess` for `wasm32-wasip1`. It depends on `monty-proto` (worker), `monty-alloc` and `monty-types` only, because `monty-runtime` pulls `monty-fs`, which does not build for wasi.
- The release module is zstd-compressed into `internal/wasmblob/monty.wasm.zst`; `blob.sha256` pins the uncompressed digest, verified at load.
- wazero compiles the module once per process and caches compiled code under the user cache directory, keyed by the digest.
- Each worker is a module instance with piped stdin and stdout, an empty environment, the host's monotonic and wall clocks, and a crypto random source. Without real clocks the worker's print batching and execution clock would drift.
- Killing a worker cancels the instance context and closes its pipes. A module exit code 65 is classified as a memory-limit breach, like the native worker.
- `make build-wasm` rebuilds the blob; with `MONTY_SRC` it patches the git dependencies to a local checkout.
