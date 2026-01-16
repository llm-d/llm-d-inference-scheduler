SHELL := /usr/bin/env bash

# Local directories
LOCALBIN ?= $(shell pwd)/bin

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
BUILDER_IMAGE_NAME ?= llm-d-builder
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
BUILDER_TAG ?= dev
BUILDER_TAG_BASE ?= $(IMAGE_REGISTRY)/$(BUILDER_IMAGE_NAME)
export BUILDER_IMAGE ?= $(BUILDER_TAG_BASE):$(BUILDER_TAG)
NAMESPACE ?= hc4ai-operator

CONTAINER_RUNTIME := $(shell { command -v docker >/dev/null 2>&1 && echo docker; } || { command -v podman >/dev/null 2>&1 && echo podman; } || echo "")
export CONTAINER_RUNTIME

GIT_COMMIT_SHA ?= "$(shell git rev-parse HEAD 2>/dev/null)"
BUILD_REF ?= $(shell git describe --abbrev=0 2>/dev/null)

# go source files
SRC = $(shell find . -type f -name '*.go')

# test packages
epp_TEST_PACKAGES = $$(go list ./... | grep -v /test/ | grep -v ./pkg/sidecar/)
sidecar_TEST_PACKAGES = ./pkg/sidecar/...

# Internal variables for generic targets
epp_IMAGE = $(EPP_IMAGE)
sidecar_IMAGE = $(SIDECAR_IMAGE)
epp_NAME = epp
sidecar_NAME = $(SIDECAR_NAME)

.PHONY: help
help: ## Print help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: clean
clean: ## Clean build artifacts, tools and caches
	go clean -testcache -cache
	rm -rf $(LOCALBIN) build

.PHONY: format
format: image-build-builder ## Format Go source files
	@printf "\033[33;1m==== Running go fmt ====\033[0m\n"
	@gofmt -l -w $(SRC)
	$(CONTAINER_RUNTIME) run --rm -u $$(id -u):$$(id -g) -v $$(pwd):/app:Z -w /app $(BUILDER_IMAGE) \
		golangci-lint fmt

.PHONY: lint
lint: image-build-builder ## Run lint
	@printf "\033[33;1m==== Running linting ====\033[0m\n"
	$(CONTAINER_RUNTIME) run --rm -u $$(id -u):$$(id -g) -v $$(pwd):/app:Z -w /app $(BUILDER_IMAGE) \
		sh -c 'golangci-lint run --config=./.golangci.yml && typos'

.PHONY: install-hooks
install-hooks: ## Install git hooks
	git config core.hooksPath hooks

.PHONY: test
test: test-unit test-e2e ## Run all tests (unit and e2e)

.PHONY: test-unit
test-unit: test-unit-epp test-unit-sidecar ## Run unit tests

.PHONY: test-unit-%
test-unit-%: image-build-builder
	@printf "\033[33;1m==== Running $* Unit Tests ====\033[0m\n"
	$(CONTAINER_RUNTIME) run --rm -u $$(id -u):$$(id -g) -v $$(pwd):/app:Z -w /app $(BUILDER_IMAGE) \
		go test -v $($*_TEST_PACKAGES)

.PHONY: test-integration
test-integration: image-build-builder ## Run integration tests (requires KUBECONFIG and running cluster)
	@printf "\033[33;1m==== Running Integration Tests ====\033[0m\n"
	$(CONTAINER_RUNTIME) run --rm -u $$(id -u):$$(id -g) -v $$(pwd):/app:Z -w /app \
		--network=host \
		-v $${HOME}/.kube:/.kube:ro \
		-e KUBECONFIG=/.kube/config $(BUILDER_IMAGE) \
		go test -v -tags=integration_tests ./test/integration/

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
build-%: image-build-builder ## Build the project
	@printf "\033[33;1m==== Building $* ====\033[0m\n"
	$(CONTAINER_RUNTIME) run --rm -u $$(id -u):$$(id -g) -v $$(pwd):/app:Z -w /app $(BUILDER_IMAGE) \
		go build -o bin/$($*_NAME) cmd/$($*_NAME)/main.go

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
		--build-arg COMMIT_SHA=${GIT_COMMIT_SHA} \
		--build-arg BUILD_REF=${BUILD_REF} \
 		-t $($*_IMAGE) -f Dockerfile.$* .

.PHONY: image-build-builder
image-build-builder:
	@printf "\033[33;1m==== Building image $(BUILDER_IMAGE) ====\033[0m\n"
	$(CONTAINER_RUNTIME) build --quiet \
		-f Dockerfile.builder \
		-t $(BUILDER_IMAGE) .

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
	@echo "CONTAINER_RUNTIME=$(CONTAINER_RUNTIME)"
	@echo "IMAGE_TAG_BASE=$(IMAGE_TAG_BASE)"
	@echo "EPP_TAG=$(EPP_TAG)"
	@echo "EPP_IMAGE=$(EPP_IMAGE)"
	@echo "SIDECAR_TAG=$(SIDECAR_TAG)"
	@echo "SIDECAR_IMAGE=$(SIDECAR_IMAGE)"
	@echo "VLLM_SIMULATOR_TAG=$(VLLM_SIMULATOR_TAG)"
	@echo "VLLM_SIMULATOR_IMAGE=$(VLLM_SIMULATOR_IMAGE)"
	@echo "BUILDER_IMAGE=$(BUILDER_IMAGE)"

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
