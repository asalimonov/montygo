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

VERSION := $(shell sed -n 's/^[[:space:]]*Version = "\(.*\)"/\1/p' monty.go)
UPSTREAM_REV := $(shell sed -n 's/^[[:space:]]*UpstreamRev = "\(.*\)"/\1/p' monty.go)
MONTY_REV_FULL := $(shell sed -n 's/^[[:space:]]*MONTY_REV: //p' .github/workflows/ci.yml)
IMAGE ?= monty-server
PYCLIENT_IMAGE ?= monty-pyclient
IMAGE_TAG ?= $(VERSION)-$(UPSTREAM_REV)
PLATFORMS ?= linux/amd64,linux/arm64
MONTY_DOCKER_SRC ?= auto
DOCKER_SRC_STAGE := build/monty-src
TEST_DUMP_KEY := montygo-network-test-dump-key
BUILDX_CACHE_FROM ?=
BUILDX_CACHE_TO ?=

ifeq ($(MONTY_DOCKER_SRC),auto)
DOCKER_SRC_CONTEXT := $(if $(wildcard $(MONTY_SRC)/crates/monty-proto),--build-context monty-src=$(DOCKER_SRC_STAGE),)
else
DOCKER_SRC_CONTEXT :=
endif
BUILDX_CACHE := $(if $(BUILDX_CACHE_FROM),--cache-from $(BUILDX_CACHE_FROM),) $(if $(BUILDX_CACHE_TO),--cache-to $(BUILDX_CACHE_TO),)

.PHONY: docker-stage-src
docker-stage-src: ## Stage tracked MONTY_SRC files (no target/) as the override build context
	@if [ -n "$(DOCKER_SRC_CONTEXT)" ]; then \
		rm -rf $(DOCKER_SRC_STAGE) && mkdir -p $(DOCKER_SRC_STAGE) && \
		(cd $(MONTY_SRC) && git ls-files -z --cached --others --exclude-standard | \
			tar --null --no-recursion -T - -cf - 2>/dev/null) | tar -xf - -C $(DOCKER_SRC_STAGE); \
	fi

.PHONY: docker-build
docker-build: docker-stage-src ## Build monty-server images for PLATFORMS and load them
	docker buildx build --platform $(PLATFORMS) --load -f docker/Dockerfile \
		--build-arg MONTY_REV=$(MONTY_REV_FULL) \
		--build-arg MONTYGO_REVISION=$(shell git rev-parse HEAD 2>/dev/null || echo unknown) \
		$(DOCKER_SRC_CONTEXT) $(BUILDX_CACHE) \
		-t $(IMAGE):$(IMAGE_TAG) -t $(IMAGE):latest .

.PHONY: docker-build-pyclient
docker-build-pyclient: docker-stage-src ## Build the Python client test image (host arch)
	docker buildx build --load -f docker/pyclient.Dockerfile \
		--build-arg MONTY_REV=$(MONTY_REV_FULL) $(DOCKER_SRC_CONTEXT) \
		-t $(PYCLIENT_IMAGE):$(IMAGE_TAG) -t $(PYCLIENT_IMAGE):latest .

.PHONY: docker-push
docker-push: docker-stage-src ## Push the multi-arch monty-server manifest to REGISTRY
	@test -n "$(REGISTRY)" || { echo "REGISTRY is required" >&2; exit 2; }
	docker buildx build --platform $(PLATFORMS) --push -f docker/Dockerfile \
		--build-arg MONTY_REV=$(MONTY_REV_FULL) \
		--build-arg MONTYGO_REVISION=$(shell git rev-parse HEAD 2>/dev/null || echo unknown) \
		$(DOCKER_SRC_CONTEXT) \
		-t $(REGISTRY)/$(IMAGE):$(IMAGE_TAG) .

.PHONY: server-check
server-check: ## Clippy and tests for the Rust server
	cd server && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked

.PHONY: test-docker
test-docker: ## Run the root suite on the websocket backend against the image
	@cid=$$(docker run -d --rm -p 127.0.0.1::8000 \
		-e MONTY_SERVER_DUMP_KEY=$(TEST_DUMP_KEY) -e MONTY_SERVER_MAX_SESSIONS_PER_CLIENT=0 \
		--label montygo.test=docker $(IMAGE):$(IMAGE_TAG)) && \
	trap 'docker stop $$cid >/dev/null' EXIT && \
	port=$$(docker port $$cid 8000/tcp | head -1 | sed 's/.*://') && \
	for i in $$(seq 1 100); do curl -sf http://127.0.0.1:$$port/health >/dev/null && break; sleep 0.1; done && \
	MONTY_TEST_WS_URL=ws://127.0.0.1:$$port/ MONTY_TEST_BACKENDS=websocket $(GO) test -count=1 -timeout 30m .

.PHONY: test-network
test-network: ## Run tests/network against the images
	cd tests/network && MONTYGO_NETWORK_TESTS=1 \
		MONTYGO_TEST_IMAGE=$(IMAGE):$(IMAGE_TAG) MONTYGO_PYCLIENT_IMAGE=$(PYCLIENT_IMAGE):$(IMAGE_TAG) \
		$(GO) test -count=1 -timeout 25m -parallel $${MONTYGO_TEST_PARALLEL:-4} ./...

.PHONY: test-network-clean
test-network-clean: ## Remove leaked test containers
	docker ps -aq --filter label=montygo.test | xargs -r docker rm -f
