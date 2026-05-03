##@ Tool Checks

.PHONY: check-container-tool
check-container-tool:
	@if [ -z "$(CONTAINER_RUNTIME)" ]; then \
		echo "ERROR: No container tool detected. Please install docker or podman."; \
		exit 1; \
	else \
		echo "Container tool '$(CONTAINER_RUNTIME)' found."; \
	fi

.PHONY: check-kubectl
check-kubectl:
	@command -v kubectl >/dev/null 2>&1 || { \
	  echo "ERROR: kubectl is not installed. Install it from https://kubernetes.io/docs/tasks/tools/"; exit 1; }

.PHONY: check-kustomize
check-kustomize:
	@kubectl kustomize --help 2>&1 | grep -q -- '--enable-helm' || { \
	  echo "ERROR: kubectl kustomize does not support --enable-helm."; \
	  echo "Upgrade kubectl to v1.25 or later (https://kubernetes.io/docs/tasks/tools/)"; \
	  exit 1; }

.PHONY: check-envsubst
check-envsubst:
	@command -v envsubst >/dev/null 2>&1 || { \
	  echo "ERROR: envsubst is not installed. It is part of gettext."; \
	  echo "Try: sudo apt install gettext OR brew install gettext"; exit 1; }
