# LLM-D Inference Scheduler Custom Fork

## Overview

This repository contains a custom fork of the [llm-d-inference-scheduler](https://github.com/llm-d/llm-d-inference-scheduler) with significant modifications to add **chat completions preprocessing** functionality. The fork enables the inference scheduler to intelligently route chat completion requests by converting them to flattened templated prompts for improved KV-cache aware routing.

## Repository Information

- **Original Repository**: `https://github.com/llm-d/llm-d-inference-scheduler.git`
- **Fork Location**: `/Users/guygirmonsky/llm-d-build/llm-d-inference-scheduler`
- **Custom Image**: `ghcr.io/guygir/llm-d-inference-scheduler:latest`
- **Fork Date**: September 2025
- **Base Commit**: `82f0cf2` (Makefile fixes #322)

---

## What This Fork Adds

This custom fork adds **chat completions preprocessing** capabilities to the LLM-D Inference Scheduler, enabling it to:

1. **Convert chat completion requests** to flattened templated prompts
2. **Improve KV-cache hit rates** by using consistent prompt formatting
3. **Maintain backward compatibility** with existing completion requests
4. **Support Python-based preprocessing** through CGO integration

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

### Tagging and Pushing
```bash
podman tag ghcr.io/llm-d/llm-d-inference-scheduler:dev ghcr.io/guygir/llm-d-inference-scheduler:latest
podman push ghcr.io/guygir/llm-d-inference-scheduler:latest
```

---

## Architecture

### Request Processing Flow

1. **Incoming Request**: Chat completion request arrives at the scheduler
2. **Preprocessing**: The `preprocessRequest` function converts chat messages to a flattened prompt using Jinja2 templating
3. **Cache Lookup**: The flattened prompt is used to find similar cached prompts in the KV-cache indexer
4. **Scoring**: Pods are scored based on cache similarity using the preprocessed prompt
5. **Routing Decision**: Request is routed to the pod with the highest cache similarity
6. **Response**: Model generates response using the optimized cache

### Key Components

- **Chat Completions Preprocessing**: Converts structured chat messages to templated prompts
- **KV-Cache Indexer**: Maintains an index of cached prompts for similarity matching
- **Precise Prefix Cache Scorer**: Scores pods based on cache similarity
- **Profile Handler**: Manages routing profiles and cache hit calculations
- **Python Integration**: CGO-based integration with Python libraries for preprocessing

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

## Benefits

### 1. Improved Cache Hit Rates
- Chat completions are properly preprocessed for better cache matching
- Consistent prompt formatting across similar requests
- Better utilization of existing cached data

### 2. Reduced Latency
- Similar requests are routed to pods with relevant cached data
- Faster response times for repeated or similar conversations
- Reduced GPU memory allocation overhead

### 3. Better Resource Utilization
- More efficient use of GPU memory through intelligent routing
- Reduced redundant computation across pods
- Better load distribution based on cache availability

### 4. Production Ready
- Built with proper security and containerization practices
- Non-root user execution for security
- Comprehensive error handling and logging

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

4. **CRD deployment errors**
   - Ensure cluster-admin privileges are available
   - Check that InferenceObjective and InferencePool CRDs exist
   - Verify the CRDs are deployed before running the scheduler

### Build Error Examples

**Without Local Clones (What We Encountered):**
```bash
# Error 1: Package structure mismatch
go: cannot find package "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/common/config/loader"

# Error 2: Missing API version
go: cannot find package "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"

# Error 3: Missing preprocessing code
go: cannot find package "github.com/llm-d/llm-d-kv-cache-manager/pkg/preprocessing/chat_completions"
```

**With Local Clones (Current Solution):**
```bash
# All imports resolve correctly
✅ sigs.k8s.io/gateway-api-inference-extension/pkg/epp/common/config/loader
✅ sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2  
✅ github.com/llm-d/llm-d-kv-cache-manager/pkg/preprocessing/chat_completions
✅ Consistent, reproducible builds
```

---

## Deployment Requirements

- CRDs must be deployed to the cluster (InferenceObjective, InferencePool)
- Cluster-admin privileges required for CRD deployment
- Compatible with linux/amd64 platform

---

## Future Enhancements

1. **Dynamic Template Loading**: Support for model-specific chat templates
2. **Advanced Preprocessing**: More sophisticated prompt optimization techniques
3. **Metrics and Monitoring**: Enhanced observability for cache hit rates and preprocessing performance
4. **Configuration Management**: Runtime configuration for preprocessing parameters

---

## Maintenance and Updates

### Updating from Upstream
1. **Fetch upstream changes**: `git fetch origin && git merge origin/main`
2. **Resolve conflicts**: Review changes in modified files, ensure preprocessing functionality is preserved
3. **Test changes**: Build the image locally and verify preprocessing still works correctly

---

## Conclusion

This custom fork of the LLM-D Inference Scheduler successfully adds chat completions preprocessing capabilities while maintaining backward compatibility and production readiness. The modifications enable intelligent routing of chat completion requests through KV-cache aware scheduling, resulting in improved performance and resource utilization.

The fork is ready for deployment and can be used as a drop-in replacement for the standard inference scheduler in environments that require chat completion support.

---

**Fork Created**: September 2025  
**Base Commit**: `82f0cf2` (Makefile fixes #322)  
**Custom Image**: `ghcr.io/guygir/llm-d-inference-scheduler:latest`  
**Status**: Production Ready
