## Local directories are defined in main Makefile
$(LOCALBIN):
	[ -d $@ ] || mkdir -p $@

## Tool binary names.
GINKGO = $(LOCALBIN)/ginkgo
KUSTOMIZE = $(LOCALBIN)/kustomize
TYPOS = $(LOCALBIN)/typos

## Tool fixed versions.
GINKGO_VERSION ?= v2.27.2
KUSTOMIZE_VERSION ?= v5.5.0
TYPOS_VERSION ?= v1.34.0

##@ Tools

.PHONY: install-tools
install-tools: install-ginkgo install-kustomize install-typos ## Install all development tools
	@echo "All development tools are installed."

.PHONY: install-ginkgo
install-ginkgo: $(GINKGO)
$(GINKGO): | $(LOCALBIN)
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

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

.PHONY: check-tools
check-tools: check-go check-ginkgo check-kustomize check-envsubst check-container-tool check-kubectl check-buildah check-typos ## Check that all required tools are installed
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
