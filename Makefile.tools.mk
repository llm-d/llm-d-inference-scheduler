LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)
PYTHON_VENV = $(LOCALBIN)/.venv
PYTHON_BIN = $(PYTHON_VENV)/bin

## Tool binary names.
CODESPELL = $(PYTHON_BIN)/codespell

## Tool versions.
CODESPELL_VERSION ?= v2.4.1

.PHONY: codespell
codespell: $(CODESPELL)
$(CODESPELL): $(PYTHON_VENV)
	$(PYTHON_BIN)/pip install codespell==$(CODESPELL_VERSION)


$(PYTHON_VENV): | $(LOCALBIN)
	python3 -m venv $(PYTHON_VENV)
	$(PYTHON_BIN)/pip install --upgrade pip
