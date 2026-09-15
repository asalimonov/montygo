# Testing

- Root tests run every scenario on each backend in `MONTY_TEST_BACKENDS` (default `native,wasm`) via `eachBackend`.
- Each top-level test gets its own pools, like one pool per upstream spec file. Wasm pools recycle workers after every checkout. Tests that inspect worker reuse create their own pools.
- `TestMain` points `MONTY_BIN` at a sibling `../monty/target/debug/monty` when it is unset.
- The upstream TypeScript suite is ported file by file; titles are kept as subtest names. See `docs/parity/tests.md`.
- `example_test.go` holds the README snippets as runnable examples; `public_api_test.go` checks the exported API against `testdata/public_api.golden` (regenerate with `UPDATE_GOLDEN=1`).
- Internal packages have unit tests: codec differential tests, fake-worker pool tests, transport tests and the ported `monty-fs` suite.
- `examples/` is a separate module with a test per example program.
