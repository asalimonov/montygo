MONTY_SRC ?= ../monty
GO ?= go
export GOTOOLCHAIN ?= local

ifeq ($(shell uname -s),Darwin)
SDKROOT ?= $(shell xcrun --sdk macosx26.5 --show-sdk-path 2>/dev/null || xcrun --show-sdk-path)
export SDKROOT
endif

ifeq ($(origin VERSION), undefined)
VERSION := $(shell scripts/version.sh)
endif
GO_LDFLAGS := -X github.com/asalimonov/montygo.buildVersion=$(VERSION)

WASM_TARGET := worker-wasm/target/wasm32-wasip1/release/monty-wasi-worker.wasm
PATCH := patch."https://github.com/pydantic/monty"

.PHONY: help
help: ## Show targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "%-16s %s\n", $$1, $$2}'

.PHONY: version
version: ## Print the version scripts/version.sh derives from git
	@echo $(VERSION)

.PHONY: check-pins
check-pins: ## Check every copy of the upstream revision and Monty version against proto/PROTO_REV and montygo.go
	scripts/check-pins.sh

.PHONY: test-scripts
test-scripts: ## Run the shell script tests in scripts/
	scripts/version_test.sh
	scripts/check_pins_test.sh

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
	$(GO) test -count=1 -ldflags '$(GO_LDFLAGS)' ./...

.PHONY: test-native
test-native: ## Run tests on the native backend only
	MONTY_TEST_BACKENDS=native $(GO) test -count=1 -ldflags '$(GO_LDFLAGS)' ./...

.PHONY: test-wasm
test-wasm: ## Run tests on the wasm backend only
	MONTY_TEST_BACKENDS=wasm $(GO) test -count=1 -ldflags '$(GO_LDFLAGS)' ./...

.PHONY: examples
examples: ## Run every example program's tests
	cd examples && $(GO) test -count=1 -ldflags '$(GO_LDFLAGS)' ./...

MONTY_REV_FULL := $(shell sed -n 's/^[[:space:]]*MONTY_REV: //p' .github/workflows/ci.yml)
IMAGE ?= monty-server
GHCR_IMAGE ?= ghcr.io/asalimonov/monty-server
PYCLIENT_IMAGE ?= monty-pyclient
IMAGE_TAG ?= $(VERSION)
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
DOCKER_BUILD_ARGS := --build-arg MONTY_REV=$(MONTY_REV_FULL) \
	--build-arg MONTY_SERVER_VERSION=$(VERSION) \
	--build-arg MONTYGO_REVISION=$(shell git rev-parse HEAD 2>/dev/null || echo unknown)

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
		$(DOCKER_BUILD_ARGS) $(DOCKER_SRC_CONTEXT) $(BUILDX_CACHE) \
		-t $(IMAGE):$(IMAGE_TAG) -t $(IMAGE):latest -t $(GHCR_IMAGE):$(IMAGE_TAG) .

.PHONY: docker-build-pyclient
docker-build-pyclient: docker-stage-src ## Build the Python client test image (host arch)
	docker buildx build --load -f docker/pyclient.Dockerfile \
		--build-arg MONTY_REV=$(MONTY_REV_FULL) $(DOCKER_SRC_CONTEXT) \
		-t $(PYCLIENT_IMAGE):$(IMAGE_TAG) -t $(PYCLIENT_IMAGE):latest .

.PHONY: docker-push
docker-push: docker-stage-src ## Push the multi-arch monty-server manifest with SBOM and provenance to REGISTRY
	@test -n "$(REGISTRY)" || { echo "REGISTRY is required" >&2; exit 2; }
	docker buildx build --platform $(PLATFORMS) --push --sbom=true --provenance=true -f docker/Dockerfile \
		$(DOCKER_BUILD_ARGS) $(DOCKER_SRC_CONTEXT) \
		-t $(REGISTRY)/$(IMAGE):$(IMAGE_TAG) .

.PHONY: server-check
server-check: ## Clippy and tests for the Rust server
	cd server && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked

.PHONY: test-docker
test-docker: ## Run the root suite on the docker backend against the image
	MONTY_TEST_BACKENDS=docker MONTYGO_DOCKER_IMAGE=$(IMAGE):$(IMAGE_TAG) \
		$(GO) test -count=1 -timeout 30m -ldflags '$(GO_LDFLAGS)' .

.PHONY: test-network
test-network: ## Run tests/network against the images
	cd tests/network && MONTYGO_NETWORK_TESTS=1 MONTYGO_BUILD_VERSION=$(VERSION) \
		MONTYGO_TEST_IMAGE=$(IMAGE):$(IMAGE_TAG) MONTYGO_PYCLIENT_IMAGE=$(PYCLIENT_IMAGE):$(IMAGE_TAG) \
		$(GO) test -count=1 -timeout 25m -parallel $${MONTYGO_TEST_PARALLEL:-4} -ldflags '$(GO_LDFLAGS)' ./...

.PHONY: test-network-clean
test-network-clean: ## Remove leaked test containers
	docker ps -aq --filter label=montygo.test | xargs -r docker rm -f

FUZZTIME ?= 30s
FUZZ_TARGETS := $(shell grep -ho '^func Fuzz[A-Za-z0-9_]*' internal/wire/*_test.go | cut -c6-)

.PHONY: fuzz
fuzz: ## Run each wire codec fuzz target for FUZZTIME
	@for f in $(FUZZ_TARGETS); do \
		echo "fuzz $$f"; \
		$(GO) test ./internal/wire -run='^$$' -fuzz="^$$f$$" -fuzztime=$(FUZZTIME) || exit $$?; \
	done

.PHONY: bench
bench: ## Run the wire codec benchmarks against the generated protobuf code
	$(GO) test ./internal/wire -run='^$$' -bench=. -benchmem
