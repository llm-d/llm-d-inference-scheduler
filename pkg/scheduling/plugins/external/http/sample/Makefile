# Makefile for the sample external plugins project

CONTAINER_RUNTIME ?= docker

SHELL := /usr/bin/env bash

# Defaults
PROJECT_NAME ?= sample-external-plugins
REGISTRY ?= ghcr.io/llm-d
IMAGE_TAG_BASE ?= $(REGISTRY)/$(PROJECT_NAME)
SAMPLE_TAG ?= dev
IMG = $(IMAGE_TAG_BASE):$(SAMPLE_TAG)
CONTAINER_TOOL := $(shell { command -v docker >/dev/null 2>&1 && echo docker; } || { command -v podman >/dev/null 2>&1 && echo podman; } || echo "")

# go source files
SRC = $(shell find . -type f -name '*.go')

##@ Container Build/Push

.PHONY:	image-build
image-build: check-container-tool ## Build Docker image ## Build Docker image using $(CONTAINER_TOOL)
	@printf "\033[33;1m==== Building Docker image $(IMG) ====\033[0m\n"
	$(CONTAINER_TOOL) build --build-arg TARGETOS=$(TARGETOS) --build-arg TARGETARCH=$(TARGETARCH) -t $(IMG) .

.PHONY: check-container-tool
check-container-tool:
	@command -v $(CONTAINER_TOOL) >/dev/null 2>&1 || { \
	  echo "❌ $(CONTAINER_TOOL) is not installed."; \
	  echo "🔧 Try: sudo apt install $(CONTAINER_TOOL) OR brew install $(CONTAINER_TOOL)"; exit 1; }
