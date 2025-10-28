# LLM-D Inference Scheduler with chat completions preprocessing

## Overview

This repository contains a custom fork of the [llm-d-inference-scheduler](https://github.com/llm-d/llm-d-inference-scheduler) with modifications to add **chat completions preprocessing** functionality for KV-cache aware routing.

## Repository Information

- **Original Repository**: `https://github.com/llm-d/llm-d-inference-scheduler.git`
- **Upstream Merged**: Commit including 31 upstream commits plus local chat completions changes
- **Custom Features**: Chat completions preprocessing integration with `llm-d-kv-cache-manager` v0.3.2

---

## Quick Start

### Prerequisites
- Docker/Podman
- Go 1.25+
- Python 3.12 development headers

### Building the Image
```bash
cd /Users/guygirmonsky/llm-d-build/llm-d-inference-scheduler
TARGETARCH=amd64 TARGETOS=linux make image-build
```

---

## Changes Made

### 1. Dependencies Update

**Upgraded to upstream versions:**
- `llm-d-kv-cache-manager` v0.3.2 (from v0.2.1) - includes chat completions preprocessing
- `gateway-api-inference-extension` v1.1.0-rc.1 (from v0.5.1)
- Updated Kubernetes dependencies to v0.34.1
- Updated controller-runtime to v0.22.3

**API Changes:**
- Adapted code to use `request.Body` instead of `request.Data` (v1.1.0 API change)
- Updated ldflags path from `pkg/epp/metrics` to `version` package

### 2. Dockerfile Modifications

#### Builder Stage Changes
- Added Python 3.12 development headers (`python3.12-devel`) for CGO compilation
- Downloads Python dependencies from upstream `llm-d-kv-cache-manager` v0.3.2
- Configured CGO environment variables for Python integration
- Preserved upstream image size optimizations (cleaning dnf cache)

#### Runtime Stage Changes
- Installed Python 3.12 runtime in final image
- Downloads and installs Python dependencies for chat completions preprocessing
- Sets PYTHONPATH and HF_HOME environment variables

### 3. Go Code Modifications

#### Precise Prefix Cache Scorer (`pkg/plugins/scorer/precise_prefix_cache.go`)
- Adapts chat completions preprocessing to new `request.Body` API
- Uses upstream `llm-d-kv-cache-manager/pkg/preprocessing/chat_completions` module
- Preprocesses chat completion requests to get flattened prompts for KV-cache lookup

#### Profile Handler (`pkg/plugins/profile/pd_profile_handler.go`)
- Applies same preprocessing functionality for PD (Prefill/Decode) profile selection
- Uses `request.Body` API structure
- Calculates prompt length using preprocessed output for cache hit percentage

---

## Troubleshooting

### Common Issues

1. **Python.h not found during local build**
   - Local builds require Python 3.12 development headers: `brew install python@3.12` (macOS)
   - Alternatively, use Docker build which includes all dependencies: `make image-build`
   - The Docker build is the recommended approach as it matches production environment

2. **Import errors during Go build**
   - Run `go mod tidy` to ensure dependencies are properly resolved
   - The project now uses upstream `llm-d-kv-cache-manager` v0.3.2 which includes chat completions preprocessing
   - No local repository clones or replace directives are needed

3. **Runtime Python errors**
   - Verify Python 3.12 runtime is installed in the final image (automatically included in Dockerfile)
   - Check that PYTHONPATH and HF_HOME environment variables are set correctly (included in Dockerfile)
   - Python dependencies are automatically installed during Docker image build
