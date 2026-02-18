## Local directories are defined in main Makefile
$(LOCALBIN):
	[ -d $@ ] || mkdir -p $@

$(LOCALLIB):
	[ -d $@ ] || mkdir -p $@

## Tool binary names.
GINKGO = $(LOCALBIN)/ginkgo
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
KUSTOMIZE = $(LOCALBIN)/kustomize
TYPOS = $(LOCALBIN)/typos

## Tool fixed versions.
GINKGO_VERSION ?= v2.27.2
GOLANGCI_LINT_VERSION ?= v2.8.0
KUSTOMIZE_VERSION ?= v5.5.0
TYPOS_VERSION ?= v1.34.0

## go-install-tool will 'go install' any package with custom target and version.
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(notdir $(1))-$(3) $(1)
endef


##@ Tools

.PHONY: install-tools
install-tools: install-ginkgo install-golangci-lint install-kustomize install-typos install-dependencies ## Install all development tools and dependencies
	@echo "All development tools and dependencies are installed."

.PHONY: install-ginkgo
install-ginkgo: $(GINKGO)
$(GINKGO): | $(LOCALBIN)
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

.PHONY: install-golangci-lint
install-golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): | $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: install-kustomize
install-kustomize: $(KUSTOMIZE)
$(KUSTOMIZE): | $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: install-typos
install-typos: $(TYPOS)
$(TYPOS): | $(LOCALBIN)
	@echo "Downloading typos $(TYPOS_VERSION)..."
	curl -L https://github.com/crate-ci/typos/releases/download/$(TYPOS_VERSION)/typos-$(TYPOS_VERSION)-$(TYPOS_ARCH).tar.gz | tar -xz -C $(LOCALBIN) $(TAR_OPTS)
	chmod +x $(TYPOS)
	@echo "typos installed successfully."

##@ Dependencies

.PHONY: check-dependencies
check-dependencies: ## Check if development dependencies are installed
	@if [ "$(TARGETOS)" = "linux" ]; then \
	  if [ -x "$$(command -v apt)" ]; then \
	    if ! dpkg -s libzmq3-dev >/dev/null 2>&1 || ! dpkg -s g++ >/dev/null 2>&1; then \
	      echo "ERROR: Missing dependencies. Please run 'sudo make install-dependencies'"; \
	      exit 1; \
	    fi; \
	  elif [ -x "$$(command -v dnf)" ]; then \
	    if ! rpm -q zeromq-devel >/dev/null 2>&1 || ! rpm -q gcc-c++ >/dev/null 2>&1; then \
	      echo "ERROR: Missing dependencies. Please run 'sudo make install-dependencies'"; \
	      exit 1; \
	    fi; \
	  else \
	    echo "WARNING: Unsupported Linux package manager. Cannot verify dependencies."; \
	  fi; \
	elif [ "$(TARGETOS)" = "darwin" ]; then \
	  if [ -x "$$(command -v brew)" ]; then \
	    if ! brew list zeromq pkg-config >/dev/null 2>&1; then \
	      echo "ERROR: Missing dependencies. Please run 'make install-dependencies'"; \
	      exit 1; \
	    fi; \
	  else \
	    echo "ERROR: Homebrew is not installed and is required. Install it from https://brew.sh/"; \
	    exit 1; \
	  fi; \
	fi
	@echo "✅ All dependencies are installed."

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
	    if ! rpm -q zeromq-devel >/dev/null 2>&1 || ! rpm -q gcc-c++ >/dev/null 2>&1; then \
	      echo "Installing dependencies with dnf..."; \
	      dnf install -y zeromq-devel gcc-c++; \
	    else \
	      echo "✅ ZMQ and gcc-c++ are already installed."; \
	    fi; \
	  else \
	    echo "ERROR: Unsupported Linux package manager. Install libzmq and g++/gcc-c++ manually."; \
	    exit 1; \
	  fi; \
	elif [ "$(TARGETOS)" = "darwin" ]; then \
	  if [ -x "$$(command -v brew)" ]; then \
	    if ! brew list zeromq pkg-config >/dev/null 2>&1; then \
	      echo "Installing dependencies with brew..."; \
	      brew install zeromq pkg-config; \
	    else \
	      echo "✅ ZeroMQ and pkg-config are already installed."; \
	    fi; \
	  else \
	    echo "ERROR: Homebrew is not installed and is required to install zeromq. Install it from https://brew.sh/"; \
	    exit 1; \
	  fi; \
	else \
	  echo "ERROR: Unsupported OS: $(TARGETOS). Install development dependencies manually."; \
	  exit 1; \
	fi

.PHONY: check-tools
check-tools: check-go check-ginkgo check-golangci-lint check-kustomize check-envsubst check-container-tool check-kubectl check-buildah check-typos ## Check that all required tools are installed
	@echo "All required tools are available."

.PHONY: check-go
check-go:
	@command -v go >/dev/null 2>&1 || { \
	  echo "ERROR: Go is not installed. Install it from https://golang.org/dl/"; exit 1; }

.PHONY: check-ginkgo
check-ginkgo:
	@command -v ginkgo >/dev/null 2>&1 || [ -f "$(GINKGO)" ] || { \
	  echo "ERROR: ginkgo is not installed."; \
	  echo "Run: make install-ginkgo (or install-tools)"; \
	  exit 1; }

.PHONY: check-golangci-lint
check-golangci-lint:
	@command -v golangci-lint >/dev/null 2>&1 || [ -f "$(GOLANGCI_LINT)" ] || { \
	  echo "ERROR: golangci-lint is not installed."; \
	  echo "Run: make install-golangci-lint (or install-tools)"; \
	  exit 1; }

.PHONY: check-kustomize
check-kustomize:
	@command -v kustomize >/dev/null 2>&1 || [ -f "$(KUSTOMIZE)" ] || { \
	  echo "ERROR: kustomize is not installed."; \
	  echo "Run: make install-kustomize (or install-tools)"; \
	  exit 1; }

.PHONY: check-envsubst
check-envsubst:
	@command -v envsubst >/dev/null 2>&1 || { \
	  echo "ERROR: envsubst is not installed. It is part of gettext."; \
	  echo "Try: sudo apt install gettext OR brew install gettext"; exit 1; }

.PHONY: check-container-tool
check-container-tool:
	@if [ -z "$(CONTAINER_RUNTIME)" ]; then \
		echo "ERROR: Error: No container tool detected. Please install docker or podman."; \
		exit 1; \
	else \
		echo "Container tool '$(CONTAINER_RUNTIME)' found."; \
	fi

.PHONY: check-kubectl
check-kubectl:
	@command -v kubectl >/dev/null 2>&1 || { \
	  echo "ERROR: kubectl is not installed. Install it from https://kubernetes.io/docs/tasks/tools/"; exit 1; }

.PHONY: check-builder
check-builder:
	@if [ -z "$(BUILDER)" ]; then \
		echo "ERROR: No container builder tool (buildah, docker, or podman) found."; \
		exit 1; \
	else \
		echo "Using builder: $(BUILDER)"; \
	fi

.PHONY: check-buildah
check-buildah:
	@command -v buildah >/dev/null 2>&1 || { \
	  echo "WARNING: buildah is not installed (optional - docker/podman can be used instead)."; }

.PHONY: check-typos
check-typos:
	@command -v typos >/dev/null 2>&1 || [ -f "$(TYPOS)" ] || { \
	  echo "ERROR: typos is not installed."; \
	  echo "Run: make install-typos (or install-tools)"; \
	  exit 1; }
	@echo "Checking for spelling errors with typos..."
	@$(TYPOS) --format brief
