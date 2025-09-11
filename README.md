# LLM-D Inference Scheduler Custom Fork

## Overview

This repository contains a custom fork of the [llm-d-inference-scheduler](https://github.com/llm-d/llm-d-inference-scheduler) with modifications to add **chat completions preprocessing** functionality.

## Repository Information

- **Original Repository**: `https://github.com/llm-d/llm-d-inference-scheduler.git`
- **Fork Location**: `/Users/guygirmonsky/llm-d-build/llm-d-inference-scheduler`
- **Custom Image**: `ghcr.io/guygir/llm-d-inference-scheduler:latest`
- **Base Commit**: `82f0cf2` (Makefile fixes #322)

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

### 1. Repository Structure Changes

#### Added Local Repository Clones
- `gateway-api-inference-extension/` - Local clone of the Gateway API Inference Extension
- `llm-d-kv-cache-manager/` - Local clone of the KV Cache Manager with preprocessing capabilities

**Why Local Clones Were Necessary:**
The upstream repositories had structural differences, missing functionality, and version compatibility issues that prevented successful builds:

- **Package Structure Mismatch**: Upstream had `pkg/epp/config/loader` but code expected `pkg/epp/common/config/loader`
- **API Version Issues**: Code expected `api/v1alpha2` but upstream had `apix/v1alpha2`
- **Missing Preprocessing Code**: Chat completions preprocessing code wasn't available in upstream
- **Version Compatibility**: Upstream v0.5.1 didn't have required functionality

#### Go Module Configuration (`go.mod`)
```go
// Added replace directives to point to local clones:
replace github.com/llm-d/llm-d-kv-cache-manager => ./llm-d-kv-cache-manager
replace sigs.k8s.io/gateway-api-inference-extension => ./gateway-api-inference-extension
```

### 2. Dockerfile Modifications

#### Builder Stage Changes
- Added Python 3.12 development tools and runtime
- Integrated Python dependencies for chat completions preprocessing
- Added CGO environment variables for Python integration
- Modified build process to include Python libraries

#### Runtime Stage Changes
- Installed Python 3.12 runtime in final image
- Copied Python wrapper files and dependencies
- Set proper environment variables for Python library discovery

### 3. Go Code Modifications

#### Main Application Entry Point (`cmd/epp/main.go`)
- Fixed plugin registration to use custom plugins
- Removed call to non-existent `runner.RegisterAllPlugins()`

#### Precise Prefix Cache Scorer (`pkg/plugins/scorer/precise_prefix_cache.go`)
- Added chat completions preprocessing import
- Modified `Score` function to preprocess requests before cache lookup
- Added `preprocessRequest` function for chat completion handling

#### Profile Handler (`pkg/plugins/profile/pd_profile_handler.go`)
- Added same preprocessing functionality as the scorer
- Modified `Pick` function to use preprocessed prompts for calculations
- Ensures consistent preprocessing across all components

---

## Troubleshooting

### Common Issues

1. **Python.h not found during build**
   - Ensure Python 3.12 development headers are installed
   - Check that python3.12-devel package is available

2. **Import errors during Go build**
   - Verify local repository clones are present
   - Check that go.mod replace directives are correct
   - Ensure all required files are copied to the build context

3. **Runtime Python errors**
   - Verify Python 3.12 runtime is installed in the final image
   - Check that PYTHONPATH environment variable is set correctly
   - Ensure all Python dependencies are properly installed
