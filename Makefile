MONTY_SRC ?= ../monty
GO ?= go
export GOTOOLCHAIN ?= local

ifeq ($(shell uname -s),Darwin)
SDKROOT ?= $(shell xcrun --sdk macosx26.5 --show-sdk-path 2>/dev/null || xcrun --show-sdk-path)
export SDKROOT
endif

WASM_TARGET := worker-wasm/target/wasm32-wasip1/release/monty-wasi-worker.wasm
PATCH := patch."https://github.com/pydantic/monty"

.PHONY: help
help: ## Show targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "%-16s %s\n", $$1, $$2}'

.PHONY: generate
generate: ## Regenerate montypb from the vendored proto
	cd montypb && $(GO) generate ./...

.PHONY: build-worker
build-worker: ## Build the native monty worker in MONTY_SRC (debug)
	cd $(MONTY_SRC) && cargo build -p monty-runtime

.PHONY: build-wasm
build-wasm: ## Build the embedded wasip1 worker (uses MONTY_SRC when present, else the git rev)
	cd worker-wasm && if [ -d "$(abspath $(MONTY_SRC))/crates/monty-proto" ]; then \
		mkdir -p target && cp Cargo.lock target/Cargo.lock.committed; \
		cargo build --release --target wasm32-wasip1 \
			--config '$(PATCH).monty-proto.path="$(abspath $(MONTY_SRC))/crates/monty-proto"' \
			--config '$(PATCH).monty-alloc.path="$(abspath $(MONTY_SRC))/crates/monty-alloc"' \
			--config '$(PATCH).monty-types.path="$(abspath $(MONTY_SRC))/crates/monty-types"'; \
		status=$$?; mv target/Cargo.lock.committed Cargo.lock; exit $$status; \
	else cargo build --locked --release --target wasm32-wasip1; fi
	shasum -a 256 $(WASM_TARGET) | cut -d' ' -f1 > internal/wasmblob/blob.sha256
	zstd -19 -q -f $(WASM_TARGET) -o internal/wasmblob/monty.wasm.zst

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: test
test: ## Run all tests on the native and wasm backends
	$(GO) test -count=1 ./...

.PHONY: test-native
test-native: ## Run tests on the native backend only
	MONTY_TEST_BACKENDS=native $(GO) test -count=1 ./...

.PHONY: test-wasm
test-wasm: ## Run tests on the wasm backend only
	MONTY_TEST_BACKENDS=wasm $(GO) test -count=1 ./...

.PHONY: examples
examples: ## Run every example program's tests
	cd examples && $(GO) test -count=1 ./...
