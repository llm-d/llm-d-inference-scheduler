# Development Environment Overlays

Kustomize overlays for deploying vLLM inference in different disaggregation scenarios.
Each scenario directory selects which atomic components (`vllm-encode`, `vllm-prefill`,
`vllm-decode`) to deploy and applies scenario-specific patches.

These overlays are used by both `scripts/kind-dev-env.sh` (for local KIND clusters)
and e2e tests (via `kustomize build` + env var substitution).

## Scenario Directories

| Directory | Disaggregation | Components | Description |
|-----------|---------------|------------|-------------|
| `epd/` | EPD (default) | decode | No disaggregation, single deployment (no routing sidecar, vLLM on port 8000) |
| `p-d/` | P/D | prefill + decode | Separate prefill and decode deployments with KV cache transfer |
| `e-pd/` | E/PD | encode + decode | Separate encoder, combined prefill-decode with EC transfer |
| `e-p-d/` | E/P/D | encode + prefill + decode | Fully disaggregated: encoder, prefill, and decode |

Data parallel (`VLLM_DATA_PARALLEL_SIZE`) and KV cache (`KV_CACHE_ENABLED`) are independent
options that combine with any disaggregation mode — they are not separate scenarios.

## Shared Infrastructure

| Directory | Description |
|-----------|-------------|
| `base-kind-istio/` | Istio control plane + inference gateway. Applied separately by `kind-dev-env.sh` before the scenario overlay |
| `e2e-infra/` | Test-specific infrastructure: NodePort services for health/metrics and envoy proxy config |
| `kubernetes-kgateway/` | Alternative gateway configuration using Kubernetes Gateway API (instead of Istio) |

## Scenario Selection

Set `DISAGG_MODE` environment variable when running `kind-dev-env.sh`:

```bash
# Default (no disaggregation) - uses vllm-decode component directly
DISAGG_MODE=epd ./scripts/kind-dev-env.sh

# Prefill/Decode
DISAGG_P=true ./scripts/kind-dev-env.sh

# Encode / Prefill-Decode
DISAGG_E=true ./scripts/kind-dev-env.sh

# Encode / Prefill / Decode (fully disaggregated)
DISAGG_E=true DISAGG_P=true ./scripts/kind-dev-env.sh

# Data Parallel (any mode)
VLLM_DATA_PARALLEL_SIZE=2 ./scripts/kind-dev-env.sh
```

## Key Environment Variables

Variables substituted at deploy time via `envsubst` or Go test `substituteMany`:

| Variable | Description | Example |
|----------|-------------|---------|
| `VLLM_IMAGE` | vLLM container image (simulator or real) | `ghcr.io/llm-d/llm-d-inference-sim:v0.8.2` |
| `SIDECAR_IMAGE` | Routing sidecar image | `ghcr.io/llm-d/llm-d-routing-sidecar:dev` |
| `UDS_TOKENIZER_IMAGE` | UDS tokenizer sidecar image | `ghcr.io/llm-d/llm-d-uds-tokenizer:dev` |
| `MODEL_NAME` | HuggingFace model name | `food-review` |
| `POOL_NAME` | InferencePool name | `food-review-inference-pool` |
| `VLLM_REPLICA_COUNT_E` | Encode deployment replicas | `1` |
| `VLLM_REPLICA_COUNT_P` | Prefill deployment replicas | `1` |
| `VLLM_REPLICA_COUNT_D` | Decode deployment replicas | `2` |
| `VLLM_DATA_PARALLEL_SIZE` | Data parallel rank count per vLLM pod — applies to ALL pod types (encode, prefill, decode) | `1` |
| `CONNECTOR_TYPE` | KV connector for P/D | `nixlv2` |
| `EC_CONNECTOR_TYPE` | EC connector for E scenarios | `ec-example` |
| `KV_CACHE_ENABLED` | Enable KV cache scoring | `false` |
