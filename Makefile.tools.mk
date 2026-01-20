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
## Dependencies
TOKENIZER_LIB = $(LOCALLIB)/libtokenizers.a

## Tool fixed versions.
GINKGO_VERSION ?= v2.27.2
GOLANGCI_LINT_VERSION ?= v2.1.6
KUSTOMIZE_VERSION ?= v5.5.0
TYPOS_VERSION ?= v1.34.0

## Python Configuration
PYTHON_VERSION ?= 3.12
# Extract RELEASE_VERSION from Dockerfile
TOKENIZER_VERSION := $(shell grep '^ARG RELEASE_VERSION=' Dockerfile.epp | cut -d'=' -f2)

# Python executable for creating venv
PYTHON_EXE := $(shell command -v python$(PYTHON_VERSION) || command -v python3)
VENV_DIR := $(shell pwd)/build/venv
VENV_BIN := $(VENV_DIR)/bin

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
install-tools: install-ginkgo install-golangci-lint install-kustomize install-typos install-dependencies download-tokenizer ## Install all development tools and dependencies
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
	    if ! dpkg -s libzmq3-dev >/dev/null 2>&1 || ! dpkg -s g++ >/dev/null 2>&1 || ! dpkg -s python$(PYTHON_VERSION)-dev >/dev/null 2>&1; then \
	      echo "ERROR: Missing dependencies. Please run 'sudo make install-dependencies'"; \
	      exit 1; \
	    fi; \
	  elif [ -x "$$(command -v dnf)" ]; then \
	    if ! rpm -q zeromq-devel >/dev/null 2>&1 || ! rpm -q gcc-c++ >/dev/null 2>&1 || ! rpm -q python$(PYTHON_VERSION)-devel >/dev/null 2>&1; then \
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
	    if ! dpkg -s libzmq3-dev >/dev/null 2>&1 || ! dpkg -s g++ >/dev/null 2>&1 || ! dpkg -s python$(PYTHON_VERSION)-dev >/dev/null 2>&1; then \
	      echo "Installing dependencies with apt..."; \
	      apt-get update && apt-get install -y libzmq3-dev g++ python$(PYTHON_VERSION)-dev; \
	    else \
	      echo "✅ ZMQ, g++, and Python dev headers are already installed."; \
	    fi; \
	  elif [ -x "$$(command -v dnf)" ]; then \
	    if ! rpm -q zeromq-devel >/dev/null 2>&1 || ! rpm -q gcc-c++ >/dev/null 2>&1 || ! rpm -q python$(PYTHON_VERSION)-devel >/dev/null 2>&1; then \
	      echo "Installing dependencies with dnf..."; \
	      dnf install -y zeromq-devel gcc-c++ python$(PYTHON_VERSION)-devel; \
	    else \
	      echo "✅ ZMQ, gcc-c++, and Python dev headers are already installed."; \
	    fi; \
	  else \
	    echo "ERROR: Unsupported Linux package manager. Install libzmq, g++/gcc-c++, and python-devel manually."; \
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
	    echo "ERROR: Homebrew is not installed and is required to install zeromq. Install it from https://brew.sh/"; \
	    exit 1; \
	  fi; \
	else \
	  echo "ERROR: Unsupported OS: $(TARGETOS). Install development dependencies manually."; \
	  exit 1; \
	fi

.PHONY: download-tokenizer
download-tokenizer: $(TOKENIZER_LIB)
$(TOKENIZER_LIB): | $(LOCALLIB)
	## Download the HuggingFace tokenizer bindings.
	@echo "Downloading HuggingFace tokenizer bindings for version $(TOKENIZER_VERSION)..."
	@curl -L https://github.com/daulet/tokenizers/releases/download/$(TOKENIZER_VERSION)/libtokenizers.$(TARGETOS)-$(TOKENIZER_ARCH).tar.gz | tar -xz -C $(LOCALLIB)
	@ranlib $(LOCALLIB)/*.a
	@echo "Tokenizer bindings downloaded successfully."


.PHONY: install-python-deps
install-python-deps: ## Sets up Python virtual environment and installs dependencies
	@printf "\033[33;1m==== Setting up Python virtual environment in $(VENV_DIR) ====\033[0m\n"
	@if [ -z "$(PYTHON_EXE)" ]; then \
		echo "ERROR: Python 3 not found in PATH."; \
		exit 1; \
	fi
	@if [ ! -f "$(VENV_BIN)/pip" ]; then \
		echo "Creating virtual environment..."; \
		$(PYTHON_EXE) -m venv $(VENV_DIR) || { \
			echo "ERROR: Failed to create virtual environment."; \
			echo "Your Python installation may be missing the 'venv' module."; \
			exit 1; \
		}; \
	fi
	@echo "Upgrading pip and installing dependencies..."
	@$(VENV_BIN)/pip install --upgrade pip --quiet
	@KV_CACHE_PKG=$$(go list -m -f '{{.Dir}}' github.com/llm-d/llm-d-kv-cache 2>/dev/null); \
	chmod +x $$KV_CACHE_PKG/pkg/preprocessing/chat_completions/setup.sh; $$KV_CACHE_PKG/pkg/preprocessing/chat_completions/setup.sh;
	@echo "✅ Python dependencies installed in venv"

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
