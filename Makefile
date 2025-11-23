SHELL := /usr/bin/env bash

# Local directories
LOCALBIN ?= $(shell pwd)/bin
LOCALLIB ?= $(shell pwd)/lib

# Build tools and dependencies are defined in Makefile.tools.mk.
include Makefile.tools.mk
# Cluster (Kubernetes/OpenShift) specific targets are defined in Makefile.cluster.mk.
include Makefile.cluster.mk
# Kind specific targets are defined in Makefile.kind.mk.
include Makefile.kind.mk

# Defaults
TARGETOS ?= $(shell command -v go >/dev/null 2>&1 && go env GOOS || uname -s | tr '[:upper:]' '[:lower:]')
TARGETARCH ?= $(shell command -v go >/dev/null 2>&1 && go env GOARCH || uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/; s/armv7l/arm/')
PROJECT_NAME ?= llm-d-inference-scheduler
SIDECAR_IMAGE_NAME ?= llm-d-routing-sidecar
VLLM_SIMULATOR_IMAGE_NAME ?= llm-d-inference-sim
SIDECAR_NAME ?= pd-sidecar
IMAGE_REGISTRY ?= ghcr.io/llm-d
IMAGE_TAG_BASE ?= $(IMAGE_REGISTRY)/$(PROJECT_NAME)
EPP_TAG ?= dev
export EPP_IMAGE ?= $(IMAGE_TAG_BASE):$(EPP_TAG)
SIDECAR_TAG ?= dev
SIDECAR_IMAGE_TAG_BASE ?= $(IMAGE_REGISTRY)/$(SIDECAR_IMAGE_NAME)
export SIDECAR_IMAGE ?= $(SIDECAR_IMAGE_TAG_BASE):$(SIDECAR_TAG)
VLLM_SIMULATOR_TAG ?= v0.6.1
VLLM_SIMULATOR_TAG_BASE ?= $(IMAGE_REGISTRY)/$(VLLM_SIMULATOR_IMAGE_NAME)
export VLLM_SIMULATOR_IMAGE ?= $(VLLM_SIMULATOR_TAG_BASE):$(VLLM_SIMULATOR_TAG)
NAMESPACE ?= hc4ai-operator

# Map go arch to platform-specific arch
ifeq ($(TARGETOS),darwin)
	ifeq ($(TARGETARCH),amd64)
		TOKENIZER_ARCH = x86_64
		TYPOS_TARGET_ARCH = x86_64
	else ifeq ($(TARGETARCH),arm64)
		TOKENIZER_ARCH = arm64
		TYPOS_TARGET_ARCH = aarch64
	else
		TOKENIZER_ARCH = $(TARGETARCH)
		TYPOS_TARGET_ARCH = $(TARGETARCH)
	endif
	TAR_OPTS = --strip-components 1
	TYPOS_ARCH = $(TYPOS_TARGET_ARCH)-apple-darwin
else
	ifeq ($(TARGETARCH),amd64)
		TOKENIZER_ARCH = amd64
		TYPOS_TARGET_ARCH = x86_64
	else ifeq ($(TARGETARCH),arm64)
		TOKENIZER_ARCH = arm64
		TYPOS_TARGET_ARCH = aarch64
	else
		TOKENIZER_ARCH = $(TARGETARCH)
		TYPOS_TARGET_ARCH = $(TARGETARCH)
	endif
	TAR_OPTS = --wildcards '*/typos'
	TYPOS_ARCH = $(TYPOS_TARGET_ARCH)-unknown-linux-musl
endif

CONTAINER_RUNTIME := $(shell { command -v docker >/dev/null 2>&1 && echo docker; } || { command -v podman >/dev/null 2>&1 && echo podman; } || echo "")
export CONTAINER_RUNTIME
BUILDER := $(shell command -v buildah >/dev/null 2>&1 && echo buildah || echo $(CONTAINER_RUNTIME))
PLATFORMS ?= linux/amd64 # linux/arm64 # linux/s390x,linux/ppc64le

GIT_COMMIT_SHA ?= "$(shell git rev-parse HEAD 2>/dev/null)"
BUILD_REF ?= $(shell git describe --abbrev=0 2>/dev/null)

# go source files
SRC = $(shell find . -type f -name '*.go')

# Tokenizer & Linking
LDFLAGS ?= -extldflags '-L$(LOCALLIB)'
CGO_ENABLED=1 # TODO: do we need this? it is set in both Dockerfiles

# Unified Python configuration detection. This block runs once.
PYTHON_CONFIG ?= $(shell command -v python$(PYTHON_VERSION)-config || command -v python3-config)
ifeq ($(PYTHON_CONFIG),)
	ifeq ($(TARGETOS),darwin)
        # macOS: Find Homebrew's python-config script for the most reliable flags.
        BREW_PREFIX := $(shell command -v brew >/dev/null 2>&1 && brew --prefix python@$(PYTHON_VERSION) 2>/dev/null)
        PYTHON_CONFIG ?= $(BREW_PREFIX)/bin/python$(PYTHON_VERSION)-config
        PYTHON_ERROR := "Could not execute 'python$(PYTHON_VERSION)-config'. Please ensure Python is installed correctly with: 'brew install python@$(PYTHON_VERSION)' or install python3.12 manually."
	else ifeq ($(TARGETOS),linux)
        # Linux: Use standard system tools to find flags.
        PYTHON_ERROR := "Python $(PYTHON_VERSION) development headers not found. Please install with: 'sudo apt install python$(PYTHON_VERSION)-dev' or 'sudo dnf install python$(PYTHON_VERSION)-devel'"
	else
        PYTHON_ERROR := "you should set up PYTHON_CONFIG variable manually"
	endif
endif

ifeq ($(PYTHON_CONFIG),)
    $(error ${PYTHON_ERROR})
endif

ifeq ($(shell $(PYTHON_CONFIG) --cflags 2>/dev/null),)
    $(error Python configuration tool cannot provide cflags)
endif

PYTHON_CFLAGS := $(shell $(PYTHON_CONFIG) --cflags)
PYTHON_LDFLAGS := $(shell $(PYTHON_CONFIG) --ldflags --embed)

# CGO flags with all dependencies
CGO_CFLAGS := $(PYTHON_CFLAGS) '-I$(shell pwd)/lib'
CGO_LDFLAGS := $(PYTHON_LDFLAGS) $(PYTHON_LIBS) '-L$(shell pwd)/lib' -ltokenizers -ldl -lm


# Internal variables for generic targets
epp_IMAGE = $(EPP_IMAGE)
sidecar_IMAGE = $(SIDECAR_IMAGE)
epp_NAME = epp
sidecar_NAME = $(SIDECAR_NAME)
epp_LDFLAGS = -ldflags="$(LDFLAGS)"
sidecar_LDFLAGS =
epp_CGO_CFLAGS = "${CGO_CFLAGS}"
sidecar_CGO_CFLAGS =
epp_CGO_LDFLAGS = "${CGO_LDFLAGS}"
sidecar_CGO_LDFLAGS =
epp_TEST_FILES = go list ./... | grep -v /test/ | grep -v ./pkg/sidecar/
sidecar_TEST_FILES = go list ./pkg/sidecar/...

.PHONY: help
help: ## Print help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)


##@ Development

.PHONY: clean
clean: ## Clean build artifacts, tools and caches
	go clean -testcache -cache
	rm -rf $(LOCALLIB) $(LOCALBIN) build

.PHONY: format
format: check-golangci-lint ## Format Go source files
	@printf "\033[33;1m==== Running go fmt ====\033[0m\n"
	@gofmt -l -w $(SRC)
	$(GOLANGCI_LINT) fmt

.PHONY: lint
lint: check-golangci-lint check-typos ## Run lint
	@printf "\033[33;1m==== Running linting ====\033[0m\n"
	CGO_CFLAGS="${CGO_CFLAGS}" $(GOLANGCI_LINT) run
	$(TYPOS)

.PHONY: install-hooks
install-hooks: ## Install git hooks
	git config core.hooksPath hooks

.PHONY: test
test: test-unit test-e2e ## Run all tests (unit and e2e)

.PHONY: test-unit
test-unit: test-unit-epp test-unit-sidecar ## Run unit tests

.PHONY: test-unit-%
test-unit-%: download-tokenizer install-python-deps check-dependencies ## Run unit tests
	@printf "\033[33;1m==== Running Unit Tests ====\033[0m\n"
	@KV_CACHE_PKG=$$(go list -m -f '{{.Dir}}/pkg/preprocessing/chat_completions' github.com/llm-d/llm-d-kv-cache-manager 2>/dev/null || echo ""); \
	PYTHONPATH="$$KV_CACHE_PKG:$(VENV_DIR)/lib/python$(PYTHON_VERSION)/site-packages" \
	CGO_CFLAGS=${$*_CGO_CFLAGS} CGO_LDFLAGS=${$*_CGO_LDFLAGS} go test $($*_LDFLAGS) -v $$($($*_TEST_FILES) | tr '\n' ' ')

.PHONY: test-integration
test-integration: download-tokenizer check-dependencies ## Run integration tests
	@printf "\033[33;1m==== Running Integration Tests ====\033[0m\n"
	go test -ldflags="$(LDFLAGS)" -v -tags=integration_tests ./test/integration/

.PHONY: test-e2e
test-e2e: image-build image-pull ## Run end-to-end tests against a new kind cluster
	@printf "\033[33;1m==== Running End to End Tests ====\033[0m\n"
	PATH=$(LOCALBIN):$$PATH ./test/scripts/run_e2e.sh

.PHONY: post-deploy-test
post-deploy-test: ## Run post deployment tests
	echo Success!
	@echo "Post-deployment tests passed."


##@ Build

.PHONY: build
build: build-epp build-sidecar ## Build the project for both epp and sidecar

.PHONY: build-%
build-%: check-go download-tokenizer ## Build the project
	@printf "\033[33;1m==== Building ====\033[0m\n"
	CGO_CFLAGS=${$*_CGO_CFLAGS} CGO_LDFLAGS=${$*_CGO_LDFLAGS} go build $($*_LDFLAGS) -o bin/$($*_NAME) cmd/$($*_NAME)/main.go

##@ Container image Build/Push/Pull

.PHONY:	image-build
image-build: image-build-epp image-build-sidecar ## Build Container image using $(CONTAINER_RUNTIME)

.PHONY: image-build-%
image-build-%: check-container-tool ## Build Container image using $(CONTAINER_RUNTIME)
	@printf "\033[33;1m==== Building Docker image $($*_IMAGE) ====\033[0m\n"
	$(CONTAINER_RUNTIME) build \
		--platform linux/$(TARGETARCH) \
 		--build-arg TARGETOS=linux \
		--build-arg TARGETARCH=$(TARGETARCH) \
		--build-arg PYTHON_VERSION=$(PYTHON_VERSION) \
		--build-arg COMMIT_SHA=${GIT_COMMIT_SHA} \
		--build-arg BUILD_REF=${BUILD_REF} \
 		-t $($*_IMAGE) -f Dockerfile.$* .

.PHONY: image-push
image-push: image-push-epp image-push-sidecar ## Push container images to registry using $(CONTAINER_RUNTIME)

.PHONY: image-push-%
image-push-%: check-container-tool ## Push container image to registry using $(CONTAINER_RUNTIME)
	@printf "\033[33;1m==== Pushing Container image $($*_IMAGE) ====\033[0m\n"
	$(CONTAINER_RUNTIME) push $($*_IMAGE)

.PHONY: image-pull
image-pull: check-container-tool ## Pull all related images using $(CONTAINER_RUNTIME)
	@printf "\033[33;1m==== Pulling Container images ====\033[0m\n"
	./scripts/pull_images.sh

##@ Container Run

.PHONY: run-container
run-container: check-container-tool ## Run app in container using $(CONTAINER_RUNTIME)
	@echo "Starting container with $(CONTAINER_RUNTIME)..."
	$(CONTAINER_RUNTIME) run -d --name $(PROJECT_NAME)-container $(EPP_IMAGE)
	@echo "$(CONTAINER_RUNTIME) started successfully."
	@echo "To use $(PROJECT_NAME), run:"
	@echo "alias $(PROJECT_NAME)='$(CONTAINER_RUNTIME) exec -it $(PROJECT_NAME)-container /app/$(PROJECT_NAME)'"

.PHONY: stop-container
stop-container: check-container-tool ## Stop and remove container
	@echo "Stopping and removing container..."
	$(CONTAINER_RUNTIME) stop $(PROJECT_NAME)-container && $(CONTAINER_RUNTIME) rm $(PROJECT_NAME)-container
	@echo "$(CONTAINER_RUNTIME) stopped and removed. Remove alias if set: unalias $(PROJECT_NAME)"

##@ Environment
.PHONY: env
env: ## Print environment variables
	@echo "TARGETOS=$(TARGETOS)"
	@echo "TARGETARCH=$(TARGETARCH)"
	@echo "PYTHON_VERSION=$(PYTHON_VERSION)"
	@echo "CONTAINER_RUNTIME=$(CONTAINER_RUNTIME)"
	@echo "IMAGE_TAG_BASE=$(IMAGE_TAG_BASE)"
	@echo "EPP_TAG=$(EPP_TAG)"
	@echo "EPP_IMAGE=$(EPP_IMAGE)"
	@echo "SIDECAR_TAG=$(SIDECAR_TAG)"
	@echo "SIDECAR_IMAGE=$(SIDECAR_IMAGE)"
	@echo "VLLM_SIMULATOR_TAG=$(VLLM_SIMULATOR_TAG)"
	@echo "VLLM_SIMULATOR_IMAGE=$(VLLM_SIMULATOR_IMAGE)"

.PHONY: print-namespace
print-namespace: ## Print the current namespace
	@echo "$(NAMESPACE)"

.PHONY: print-project-name
print-project-name: ## Print the current project name
	@echo "$(PROJECT_NAME)"

##@ Deprecated aliases for backwards compatibility
.PHONY: install-docker
install-docker: ## DEPRECATED: Use 'make run-container' instead
	@echo "WARNING: 'make install-docker' is deprecated. Use 'make run-container' instead."
	@$(MAKE) run-container

.PHONY: uninstall-docker
uninstall-docker: ## DEPRECATED: Use 'make stop-container' instead
	@echo "WARNING: 'make uninstall-docker' is deprecated. Use 'make stop-container' instead."
	@$(MAKE) stop-container

.PHONY: install
install: ## DEPRECATED: Use 'make run-container' instead
	@echo "WARNING: 'make install' is deprecated. Use 'make run-container' instead."
	@$(MAKE) run-container

.PHONY: uninstall
uninstall: ## DEPRECATED: Use 'make stop-container' instead
	@echo "WARNING: 'make uninstall' is deprecated. Use 'make stop-container' instead."
	@$(MAKE) stop-container
