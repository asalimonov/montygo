# montygo examples

Go ports of the upstream [Monty examples](https://github.com/pydantic/monty/tree/main/examples).
Each directory is a `package main` whose sandbox code is byte-identical to upstream. `repl` has no upstream example; it ports the REPL of the `monty` CLI.

| Go program | Upstream Python | Shows |
| --- | --- | --- |
| `classes/class_instance` | `classes/class_instance.py` | Host object exposed with an explicit policy; the original object comes back |
| `classes/lazy_attrs` | `classes/lazy_attrs.py` | Attributes fetched on demand; names outside the policy raise `AttributeError` |
| `classes/sandbox_copy` | `classes/sandbox_copy.py` | Sandbox mutations stay on the sandbox copy |
| `classes/convert_value` | `classes/convert_value.py` | `ConvertValue` wraps derived host objects as they cross |
| `classes/class_type` | `classes/class_type.py` | Sandbox instantiates a host class only when `Init` is granted |
| `classes/class_type_members` | `classes/class_type_members.py` | Class constants and class-level functions without construction |
| `classes/sandbox_classes` | `classes/sandbox_classes.py` | Sandbox-defined dataclass returned as `*monty.ClassProxy` |
| `classes/sandbox_round_trip` | `classes/sandbox_round_trip.py` | A proxy passed back resolves to the original sandbox object until freed |
| `classes/async_methods` | `classes/async_methods.py` | Host method returning `*monty.Future`, awaited in the sandbox |
| `expense_analysis` | `expense_analysis/main.py`, `data.py` | Type-checked analysis code calling async host tools with keyword arguments |
| `sql_playground` | `sql_playground/main.py`, `external_functions.py` | SQL over CSV, JSON and sentiment tools reading a virtual filesystem (`osaccess`) |
| `repl` | `monty` CLI REPL (`crates/monty-runtime/src/run.rs`, `crates/monty/src/repl.rs`) | Interactive REPL: one persistent session, CPython-style multi-line input, Ctrl-C, mounts and resource limits |
| `web_scraper` | `web_scraper/main.py`, `browser.py`, `external_functions.py`, `sub_agent.py` | LLM agent loop writing Monty code that drives headless Chrome and a BeautifulSoup-like `Tag` |

## Running

The examples are a separate module (Go 1.25) that uses the parent checkout through a `replace` directive.

```bash
cd examples
go run ./classes/class_instance
go test ./...
```

- **Backend.** Pools use the native worker when `MONTY_BIN`, `PATH` or a sibling cargo build (`../monty/target/{debug,release}/monty`) resolves one, else the embedded wasm worker. `MONTY_EXAMPLES_BACKEND=native|wasm` forces a backend.
- **sql_playground** needs a clone of [mafudge/datasets](https://github.com/mafudge/datasets) at `../mafudge_datasets` relative to the repository root, or `-datasets DIR` / `MAFUDGE_DATASETS`. Its end-to-end tests skip when the directory is absent. Upstream `sandbox_code.py` imports its stubs as `type_stubs`, which session type checking does not provide, so the feed runs unchecked unless `-type-check` is passed.
- **web_scraper** agent mode calls the Anthropic Messages API and needs `ANTHROPIC_API_KEY`; `-model` or `WEB_SCRAPER_MODEL` overrides `claude-sonnet-4-5`, `-provider openai|anthropic|groq` or `-url` picks the page. `-code [FILE]` runs code (default: the embedded `example_code.py`) without the LLM, and `-type-check` checks it against the stubs. Tests that drive a real browser need Chrome and skip when it cannot start.
- **repl** reads Python from stdin: `go run ./repl`, or `go run ./repl -c 'x = 1'` / `go run ./repl setup.py` to run code before the first prompt. It takes the `monty` CLI flags `-m host::/virtual[::ro|rw|overlay[::write_limit]]`, `--cwd`, `--max-duration`, `--max-memory`, `--gc-interval`, `--max-recursion-depth` and `--max-suspensions`. `-ws URL` runs the sessions on a remote `monty-server` instead of local workers, for example `docker run --rm -p 127.0.0.1:8000:8000 monty-server:latest` after `make docker-build`, then `go run ./repl -ws ws://127.0.0.1:8000/`. For `wss://`, `-ws-ca FILE` trusts a PEM CA bundle and `-ws-insecure-skip-verify` disables certificate checks; both require `-ws`. Upstream decides whether input is complete by parsing it in-process; the Go REPL feeds the pending text and reads the syntax error instead, so traceback file names (`<python-input-N>`) skip numbers used by incomplete input. Overlay mount writes last one snippet, and Ctrl-C during a run starts a new session.
