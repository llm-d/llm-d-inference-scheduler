# The Go and Python based tools are defined in Makefile.tools.mk.
include Makefile.tools.mk

SHELL := /usr/bin/env bash

# Defaults
TARGETOS ?= $(shell go env GOOS)
TARGETARCH ?= $(shell go env GOARCH)
PROJECT_NAME ?= llm-d-inference-scheduler
IMAGE_REGISTRY ?= ghcr.io/llm-d
IMAGE_TAG_BASE ?= $(IMAGE_REGISTRY)/$(PROJECT_NAME)
EPP_TAG ?= dev
IMG = $(IMAGE_TAG_BASE):$(EPP_TAG)
NAMESPACE ?= hc4ai-operator

# Map go arch to typos arch
ifeq ($(TARGETARCH),amd64)
TYPOS_TARGET_ARCH = x86_64
else ifeq ($(TARGETARCH),arm64)
TYPOS_TARGET_ARCH = aarch64
else
TYPOS_TARGET_ARCH = $(TARGETARCH)
endif

ifeq ($(TARGETOS),darwin)
ifeq ($(TARGETARCH),amd64)
TOKENIZER_ARCH = x86_64
else
TOKENIZER_ARCH = $(TARGETARCH)
endif
TAR_OPTS = --strip-components 1
TYPOS_ARCH = $(TYPOS_TARGET_ARCH)-apple-darwin
else
TOKENIZER_ARCH = $(TARGETARCH)
TAR_OPTS = --wildcards '*/typos'
TYPOS_ARCH = $(TYPOS_TARGET_ARCH)-unknown-linux-musl
endif

CONTAINER_TOOL := $(shell { command -v docker >/dev/null 2>&1 && echo docker; } || { command -v podman >/dev/null 2>&1 && echo podman; } || echo "")
BUILDER := $(shell command -v buildah >/dev/null 2>&1 && echo buildah || echo $(CONTAINER_TOOL))
PLATFORMS ?= linux/amd64 # linux/arm64 # linux/s390x,linux/ppc64le

GIT_COMMIT_SHA ?= "$(shell git rev-parse HEAD 2>/dev/null)"
BUILD_REF ?= $(shell git describe --abbrev=0 2>/dev/null)

# go source files
SRC = $(shell find . -type f -name '*.go')

.PHONY: help
help: ## Print help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Tokenizer & Linking

LDFLAGS ?= -extldflags '-L$(shell pwd)/lib'
CGO_ENABLED=1
TOKENIZER_LIB = lib/libtokenizers.a
# Extract RELEASE_VERSION from Dockerfile
TOKENIZER_VERSION := $(shell grep '^ARG RELEASE_VERSION=' Dockerfile | cut -d'=' -f2)

.PHONY: download-tokenizer
download-tokenizer: $(TOKENIZER_LIB)
$(TOKENIZER_LIB):
	## Download the HuggingFace tokenizer bindings.
	@echo "Downloading HuggingFace tokenizer bindings for version $(TOKENIZER_VERSION)..."
	mkdir -p lib
	curl -L https://github.com/daulet/tokenizers/releases/download/$(TOKENIZER_VERSION)/libtokenizers.$(TARGETOS)-$(TOKENIZER_ARCH).tar.gz | tar -xz -C lib
	ranlib lib/*.a

##@ Development

.PHONY: clean
clean:
	go clean -testcache -cache
	rm -f $(TOKENIZER_LIB)
	rmdir lib

.PHONY: format
format: ## Format Go source files
	@printf "\033[33;1m==== Running gofmt ====\033[0m\n"
	@gofmt -l -w $(SRC)

.PHONY: test
test: test-unit test-e2e ## Run unit tests and e2e tests

.PHONY: test-unit
test-unit: download-tokenizer install-dependencies ## Run unit tests
	@printf "\033[33;1m==== Running Unit Tests ====\033[0m\n"
	go test -ldflags="$(LDFLAGS)" -v $$(echo $$(go list ./... | grep -v /test/))

.PHONY: test-integration
test-integration: download-tokenizer install-dependencies ## Run integration tests
	@printf "\033[33;1m==== Running Integration Tests ====\033[0m\n"
	go test -ldflags="$(LDFLAGS)" -v -tags=integration_tests ./test/integration/

.PHONY: test-e2e
test-e2e: image-build ## Run end-to-end tests against a new kind cluster
	@printf "\033[33;1m==== Running End to End Tests ====\033[0m\n"
	./test/scripts/run_e2e.sh

.PHONY: post-deploy-test
post-deploy-test: ## Run post deployment tests
	echo Success!
	@echo "Post-deployment tests passed."

.PHONY: lint
lint: check-golangci-lint check-typos ## Run lint
	@printf "\033[33;1m==== Running linting ====\033[0m\n"
	golangci-lint run
	$(TYPOS)

##@ Build

.PHONY: build
build: check-go install-dependencies download-tokenizer ## Build the project
	@printf "\033[33;1m==== Building ====\033[0m\n"
	go build -ldflags="$(LDFLAGS)" -o bin/epp cmd/epp/main.go

##@ Container Build/Push

.PHONY:	image-build
image-build: check-container-tool ## Build Docker image ## Build Docker image using $(CONTAINER_TOOL)
	@printf "\033[33;1m==== Building Docker image $(IMG) ====\033[0m\n"
	$(CONTAINER_TOOL) build \
		--platform linux/$(TARGETARCH) \
 		--build-arg TARGETOS=linux \
		--build-arg TARGETARCH=$(TARGETARCH) \
		--build-arg COMMIT_SHA=${GIT_COMMIT_SHA} \
		--build-arg BUILD_REF=${BUILD_REF} \
 		-t $(IMG) .

.PHONY: image-push
image-push: check-container-tool ## Push Docker image $(IMG) to registry
	@printf "\033[33;1m==== Pushing Docker image $(IMG) ====\033[0m\n"
	$(CONTAINER_TOOL) push $(IMG)

##@ Install/Uninstall Targets

# Default install/uninstall (Docker)
install: install-docker ## Default install using Docker
	@echo "Default Docker install complete."

uninstall: uninstall-docker ## Default uninstall using Docker
	@echo "Default Docker uninstall complete."

### Docker Targets

.PHONY: install-docker
install-docker: check-container-tool ## Install app using $(CONTAINER_TOOL)
	@echo "Starting container with $(CONTAINER_TOOL)..."
	$(CONTAINER_TOOL) run -d --name $(PROJECT_NAME)-container $(IMG)
	@echo "$(CONTAINER_TOOL) installation complete."
	@echo "To use $(PROJECT_NAME), run:"
	@echo "alias $(PROJECT_NAME)='$(CONTAINER_TOOL) exec -it $(PROJECT_NAME)-container /app/$(PROJECT_NAME)'"

.PHONY: uninstall-docker
uninstall-docker: check-container-tool ## Uninstall app from $(CONTAINER_TOOL)
	@echo "Stopping and removing container in $(CONTAINER_TOOL)..."
	$(CONTAINER_TOOL) stop $(PROJECT_NAME)-container && $(CONTAINER_TOOL) rm $(PROJECT_NAME)-container
	@echo "$(CONTAINER_TOOL) uninstallation complete. Remove alias if set: unalias $(PROJECT_NAME)"

### Kubernetes Targets (kubectl)

.PHONY: install-k8s
install-k8s: check-kubectl check-kustomize check-envsubst ## Install on Kubernetes
	export PROJECT_NAME=${PROJECT_NAME}
	export NAMESPACE=${NAMESPACE}
	@echo "Creating namespace (if needed) and setting context to $(NAMESPACE)..."
	kubectl create namespace $(NAMESPACE) 2>/dev/null || true
	kubectl config set-context --current --namespace=$(NAMESPACE)
	@echo "Deploying resources from deploy/ ..."
	# Build the kustomization from deploy, substitute variables, and apply the YAML
	kustomize build deploy/environments/openshift-base | envsubst | kubectl apply -f -
	@echo "Waiting for pod to become ready..."
	sleep 5
	@POD=$$(kubectl get pod -l app=$(PROJECT_NAME)-statefulset -o jsonpath='{.items[0].metadata.name}'); \
	echo "Kubernetes installation complete."; \
	echo "To use the app, run:"; \
	echo "alias $(PROJECT_NAME)='kubectl exec -n $(NAMESPACE) -it $$POD -- /app/$(PROJECT_NAME)'"

.PHONY: uninstall-k8s
uninstall-k8s: check-kubectl check-kustomize check-envsubst ## Uninstall from Kubernetes
	export PROJECT_NAME=${PROJECT_NAME}
	export NAMESPACE=${NAMESPACE}
	@echo "Removing resources from Kubernetes..."
	kustomize build deploy/environments/openshift-base | envsubst | kubectl delete --force -f - || true
	POD=$$(kubectl get pod -l app=$(PROJECT_NAME)-statefulset -o jsonpath='{.items[0].metadata.name}'); \
	echo "Deleting pod: $$POD"; \
	kubectl delete pod "$$POD" --force --grace-period=0 || true; \
	echo "Kubernetes uninstallation complete. Remove alias if set: unalias $(PROJECT_NAME)"

### OpenShift Targets (oc)

.PHONY: install-openshift
install-openshift: check-kubectl check-kustomize check-envsubst ## Install on OpenShift
	@echo $$PROJECT_NAME $$NAMESPACE $$IMAGE_TAG_BASE $$VERSION
	@echo "Creating namespace $(NAMESPACE)..."
	kubectl create namespace $(NAMESPACE) 2>/dev/null || true
	@echo "Deploying common resources from deploy/ ..."
	# Build and substitute the base manifests from deploy, then apply them
	kustomize build deploy/environments/openshift-base | envsubst '$$PROJECT_NAME $$NAMESPACE $$IMAGE_TAG_BASE $$VERSION' | kubectl apply -n $(NAMESPACE) -f -
	@echo "Waiting for pod to become ready..."
	sleep 5
	@POD=$$(kubectl get pod -l app=$(PROJECT_NAME)-statefulset -n $(NAMESPACE) -o jsonpath='{.items[0].metadata.name}'); \
	echo "OpenShift installation complete."; \
	echo "To use the app, run:"; \
	echo "alias $(PROJECT_NAME)='kubectl exec -n $(NAMESPACE) -it $$POD -- /app/$(PROJECT_NAME)'"

.PHONY: uninstall-openshift
uninstall-openshift: check-kubectl check-kustomize check-envsubst ## Uninstall from OpenShift
	@echo "Removing resources from OpenShift..."
	kustomize build deploy/environments/openshift-base | envsubst '$$PROJECT_NAME $$NAMESPACE $$IMAGE_TAG_BASE $$VERSION' | kubectl delete --force -f - || true
	# @if kubectl api-resources --api-group=route.openshift.io | grep -q Route; then \
	#   envsubst '$$PROJECT_NAME $$NAMESPACE $$IMAGE_TAG_BASE $$VERSION' < deploy/openshift/route.yaml | kubectl delete --force -f - || true; \
	# fi
	@POD=$$(kubectl get pod -l app=$(PROJECT_NAME)-statefulset -n $(NAMESPACE) -o jsonpath='{.items[0].metadata.name}'); \
	echo "Deleting pod: $$POD"; \
	kubectl delete pod "$$POD" --force --grace-period=0 || true; \
	echo "OpenShift uninstallation complete. Remove alias if set: unalias $(PROJECT_NAME)"

### RBAC Targets (using kustomize and envsubst)

.PHONY: install-rbac
install-rbac: check-kubectl check-kustomize check-envsubst ## Install RBAC
	@echo "Applying RBAC configuration from deploy/rbac..."
	kustomize build deploy/environments/openshift-base/rbac | envsubst '$$PROJECT_NAME $$NAMESPACE $$IMAGE_TAG_BASE $$VERSION' | kubectl apply -f -

.PHONY: uninstall-rbac
uninstall-rbac: check-kubectl check-kustomize check-envsubst ## Uninstall RBAC
	@echo "Removing RBAC configuration from deploy/rbac..."
	kustomize build deploy/environments/openshift-base/rbac | envsubst '$$PROJECT_NAME $$NAMESPACE $$IMAGE_TAG_BASE $$VERSION' | kubectl delete -f - || true

##@ Environment
.PHONY: env
env: ## Print environment variables
	@echo "IMAGE_TAG_BASE=$(IMAGE_TAG_BASE)"
	@echo "IMG=$(IMG)"
	@echo "CONTAINER_TOOL=$(CONTAINER_TOOL)"

.PHONY: check-typos
check-typos: $(TYPOS) ## Check for spelling errors using typos (exits with error if found)
	@echo "🔍 Checking for spelling errors with typos..."
	@TYPOS_OUTPUT=$$($(TYPOS) --format brief 2>&1); \
	if [ $$? -eq 0 ]; then \
		echo "✅ No spelling errors found!"; \
		echo "🎉 Spelling check completed successfully!"; \
	else \
		echo "❌ Spelling errors found!"; \
		echo "🔧 You can try 'make fix-typos' to automatically fix the spelling errors and run 'make check-typos' again"; \
		echo "$$TYPOS_OUTPUT"; \
		exit 1; \
	fi
	
##@ Tools

.PHONY: check-tools
check-tools: \
  check-go \
  check-ginkgo \
  check-golangci-lint \
  check-kustomize \
  check-envsubst \
  check-container-tool \
  check-kubectl \
  check-buildah
	@echo "✅ All required tools are installed."

.PHONY: check-go
check-go:
	@command -v go >/dev/null 2>&1 || { \
	  echo "❌ Go is not installed. Install it from https://golang.org/dl/"; exit 1; }

.PHONY: check-ginkgo
check-ginkgo:
	@command -v ginkgo >/dev/null 2>&1 || { \
	  echo "❌ ginkgo is not installed. Install with: go install github.com/onsi/ginkgo/v2/ginkgo@latest"; exit 1; }

.PHONY: check-golangci-lint
check-golangci-lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "❌ golangci-lint is not installed. Install from https://golangci-lint.run/usage/install/"; exit 1; }

.PHONY: check-kustomize
check-kustomize:
	@command -v kustomize >/dev/null 2>&1 || { \
	  echo "❌ kustomize is not installed. Install it from https://kubectl.docs.kubernetes.io/installation/kustomize/"; exit 1; }

.PHONY: check-envsubst
check-envsubst:
	@command -v envsubst >/dev/null 2>&1 || { \
	  echo "❌ envsubst is not installed. It is part of gettext."; \
	  echo "🔧 Try: sudo apt install gettext OR brew install gettext"; exit 1; }

.PHONY: check-container-tool
check-container-tool:
	@command -v $(CONTAINER_TOOL) >/dev/null 2>&1 || { \
	  echo "❌ $(CONTAINER_TOOL) is not installed."; \
	  echo "🔧 Try: sudo apt install $(CONTAINER_TOOL) OR brew install $(CONTAINER_TOOL)"; exit 1; }

.PHONY: check-kubectl
check-kubectl:
	@command -v kubectl >/dev/null 2>&1 || { \
	  echo "❌ kubectl is not installed. Install it from https://kubernetes.io/docs/tasks/tools/"; exit 1; }

.PHONY: check-builder
check-builder:
	@if [ -z "$(BUILDER)" ]; then \
		echo "❌ No container builder tool (buildah, docker, or podman) found."; \
		exit 1; \
	else \
		echo "✅ Using builder: $(BUILDER)"; \
	fi

##@ Alias checking
.PHONY: check-alias
check-alias: check-container-tool
	@echo "🔍 Checking alias functionality for container '$(PROJECT_NAME)-container'..."
	@if ! $(CONTAINER_TOOL) exec $(PROJECT_NAME)-container /app/$(PROJECT_NAME) --help >/dev/null 2>&1; then \
	  echo "⚠️  The container '$(PROJECT_NAME)-container' is running, but the alias might not work."; \
	  echo "🔧 Try: $(CONTAINER_TOOL) exec -it $(PROJECT_NAME)-container /app/$(PROJECT_NAME)"; \
	else \
	  echo "✅ Alias is likely to work: alias $(PROJECT_NAME)='$(CONTAINER_TOOL) exec -it $(PROJECT_NAME)-container /app/$(PROJECT_NAME)'"; \
	fi

.PHONY: print-namespace
print-namespace: ## Print the current namespace
	@echo "$(NAMESPACE)"

.PHONY: print-project-name
print-project-name: ## Print the current project name
	@echo "$(PROJECT_NAME)"

.PHONY: install-hooks
install-hooks: ## Install git hooks
	git config core.hooksPath hooks

##@ Dev Environments

KIND_CLUSTER_NAME ?= llm-d-inference-scheduler-dev
KIND_GATEWAY_HOST_PORT ?= 30080

.PHONY: env-dev-kind
env-dev-kind: ## Run under kind ($(KIND_CLUSTER_NAME))
	@if [ "$$PD_ENABLED" = "true" ] && [ "$$KV_CACHE_ENABLED" = "true" ]; then \
		echo "Error: Both PD_ENABLED and KV_CACHE_ENABLED are true. Skipping env-dev-kind."; \
		exit 1; \
	else \
		$(MAKE) image-build && \
		CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		GATEWAY_HOST_PORT=$(KIND_GATEWAY_HOST_PORT) \
		IMAGE_REGISTRY=$(IMAGE_REGISTRY) \
		EPP_TAG=$(EPP_TAG) \
		./scripts/kind-dev-env.sh; \
	fi

.PHONY: clean-env-dev-kind
clean-env-dev-kind:      ## Cleanup kind setup (delete cluster $(KIND_CLUSTER_NAME))
	@echo "INFO: cleaning up kind cluster $(KIND_CLUSTER_NAME)"
	kind delete cluster --name $(KIND_CLUSTER_NAME)


# Kubernetes Development Environment - Deploy
# This target deploys the inference scheduler stack in a specific namespace for development and testing.
.PHONY: env-dev-kubernetes
env-dev-kubernetes: check-kubectl check-kustomize check-envsubst
	IMAGE_REGISTRY=$(IMAGE_REGISTRY) ./scripts/kubernetes-dev-env.sh 2>&1

# Kubernetes Development Environment - Teardown
.PHONY: clean-env-dev-kubernetes
clean-env-dev-kubernetes: check-kubectl check-kustomize check-envsubst
	@CLEAN=true ./scripts/kubernetes-dev-env.sh 2>&1
	@echo "INFO: Finished cleanup of development environment for namespace $(NAMESPACE)"

##@ Dependencies

.PHONY: install-dependencies
install-dependencies: ## Install development dependencies based on OS/ARCH
	@echo "Checking and installing development dependencies..."
	@if [ "$(TARGETOS)" = "linux" ]; then \
	  if [ -x "$$(command -v apt)" ]; then \
	    if ! dpkg -s libzmq3-dev >/dev/null 2>&1 || ! dpkg -s g++ >/dev/null 2>&1; then \
	      echo "Installing dependencies with apt..."; \
	      apt-get update && apt-get install -y libzmq3-dev g++; \
	    else \
	      echo "✅ ZMQ and g++ are already installed."; \
	    fi; \
	  elif [ -x "$$(command -v dnf)" ]; then \
	    if ! dnf -q list installed zeromq-devel >/dev/null 2>&1 || ! dnf -q list installed gcc-c++ >/dev/null 2>&1; then \
	      echo "Installing dependencies with dnf..."; \
	      dnf install -y zeromq-devel gcc-c++; \
	    else \
	      echo "✅ ZMQ and gcc-c++ are already installed."; \
	    fi; \
	  else \
	    echo "Unsupported Linux package manager. Install libzmq and g++/gcc-c++ manually."; \
	    exit 1; \
	  fi; \
	elif [ "$(TARGETOS)" = "darwin" ]; then \
	  if [ -x "$$(command -v brew)" ]; then \
	    if ! brew list zeromq pkg-config >/dev/null 2>&1; then \
	      echo "Installing dependencies with brew..."; \
	      brew install zeromq pkg-config; \
	    else \
	      echo "✅ ZeroMQ and pkgconf are already installed."; \
	    fi; \
	  else \
	    echo "Homebrew is not installed and is required to install zeromq. Install it from https://brew.sh/"; \
	    exit 1; \
	  fi; \
	else \
	  echo "Unsupported OS: $(TARGETOS). Install development dependencies manually."; \
	  exit 1; \
	fi
