# Development

Documentation for developing the inference scheduler.

## Table of Contents

- [Development](#development)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Requirements](#requirements)
  - [Kind Development Environment](#kind-development-environment)
    - [Accessing the Gateway](#accessing-the-gateway)
    - [Prometheus Monitoring](#prometheus-monitoring)
    - [Grafana Dashboard](#grafana-dashboard)
    - [Development Cycle](#development-cycle)
    - [Inference Disaggregation Modes](#inference-disaggregation-modes)
    - [Cleanup](#cleanup)
  - [Running Tests](#running-tests)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [Filtered Tests](#filtered-tests)
    - [End-to-End Tests](#end-to-end-tests)
    - [Coverage](#coverage)
  - [Tokenization Architecture](#tokenization-architecture)
  - [Kubernetes Development Environment](#kubernetes-development-environment)
    - [Infrastructure Setup](#infrastructure-setup)
    - [RBAC and Permissions](#rbac-and-permissions)
    - [Developer Setup](#developer-setup)
    - [Environment Configuration](#environment-configuration)
    - [Deploying Changes](#deploying-changes)
    - [Cleanup Environment](#cleanup-environment)
  - [Submitting Changes](#submitting-changes)

## Overview

This repo builds the **Endpoint Picker Plugin (EPP)**, the inference scheduling component
that routes requests to vLLM backends. The EPP runs alongside a Gateway API implementation
and picks backends based on KV cache state, prefill locality, and load. A second binary,
the **P/D sidecar** (`cmd/pd-sidecar/`), handles prefill/decode disaggregation routing.

The KIND environment is the easiest way to get started: one command, no cloud account.
A real Kubernetes cluster setup is covered later for shared or production-like testing.

## Requirements

- [Make] `v4`+
- [Golang] `v1.24`+
- [Docker] (or [Podman])
- [Kubernetes in Docker (KIND)]
- [Kubectl] `v1.25`+

[Make]:https://www.gnu.org/software/make/
[Golang]:https://go.dev/
[Docker]:https://www.docker.com/
[Podman]:https://podman.io/
[Kubernetes in Docker (KIND)]:https://github.com/kubernetes-sigs/kind
[Kubectl]:https://kubectl.docs.kubernetes.io/installation/kubectl/

## Kind Development Environment

Deploys the EPP, vLLM simulator, and Gateway API implementation into a local KIND cluster:

```bash
make env-dev-kind
```

Creates a new `kind` cluster (or reuses an existing one) in the `default` namespace.

> [!NOTE]
> You can pre-pull external images to avoid slow downloads:
> ```
> docker pull ghcr.io/llm-d/llm-d-inference-sim:v0.8.2
> docker pull ghcr.io/llm-d/llm-d-uds-tokenizer:dev
> ```

### Accessing the Gateway

Use port-forward for local development:

```bash
kubectl --context kind-llm-d-inference-scheduler-dev \
  port-forward service/inference-gateway-istio 8080:80
```

The simulator runs with a model named `TinyLlama/TinyLlama-1.1B-Chat-v1.0`.
To confirm what model is available:

```bash
curl -s http://localhost:8080/v1/models | jq
```

Make a request:

```bash
curl -s -w '\n' http://localhost:8080/v1/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"hi","max_tokens":10,"temperature":0}' | jq
```

<details>
<summary>Alternative access methods (NodePort, LoadBalancer)</summary>

**NodePort**

The gateway is also exposed as a NodePort and is exposed on your development machine on port 30080 by default. Override this at
cluster creation time with any free port in the range 30000-32767:

```bash
KIND_GATEWAY_HOST_PORT=<selected-port> make env-dev-kind
```

The service is then accessible at `http://localhost:30080`.

**LoadBalancer**

```bash
# Install and run cloud-provider-kind:
go install sigs.k8s.io/cloud-provider-kind@latest && cloud-provider-kind &
kubectl --context kind-llm-d-inference-scheduler-dev get service inference-gateway-istio
# Wait for the LoadBalancer External-IP to become available.
# The service is accessible over port 80.
```

</details>

### Prometheus Monitoring

To deploy Prometheus alongside the dev environment:

```bash
PROM_ENABLED=true make env-dev-kind
```

Prometheus will be accessible at `http://localhost:30090`. To use a different host port:

```bash
PROM_ENABLED=true KIND_PROM_HOST_PORT=30091 make env-dev-kind
```

### Grafana Dashboard

The upstream [Inference Gateway dashboard] covers EPP, inference pool, and vLLM metrics.

Add a Prometheus datasource at `http://localhost:30090`, then import the JSON via
**Dashboards > New > Import**. See the
[Grafana installation docs](https://grafana.com/docs/grafana/latest/setup-grafana/installation/)
for setup.

[Inference Gateway dashboard]:https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/tools/dashboards/inference_gateway.json

> [!NOTE]
> For significant customization beyond the standard deployment, use the `deploy/components`
> directory with `kubectl kustomize`. The `deploy/environments/kind` deployment is a useful
> reference.

### Development Cycle

Edit your code, then rebuild and reload into the cluster:

```bash
make env-dev-kind
```

This rebuilds the EPP image (tagged `dev` by default) and loads it into the cluster.
To use a specific tag:

```bash
EPP_TAG=0.0.4 make env-dev-kind
```

Then restart the deployment to pick up the new image:

```bash
kubectl rollout restart deployment tinyllama-1-1b-chat-v1-0-endpoint-picker
```

> [!NOTE]
> Images are built with debug symbols stripped (`-s -w`) by default. To produce a
> debuggable image for use with `dlv`, override `LDFLAGS`:
> ```bash
> LDFLAGS="" make image-build-epp
> ```
> To load a different vLLM simulator tag, set `VLLM_SIMULATOR_TAG`:
> ```bash
> VLLM_SIMULATOR_TAG=<tag> make env-dev-kind
> ```

### Inference Disaggregation Modes

You can deploy the inference stack in disaggregated modes to optimize performance by separating specific stages of the LLM pipeline into dedicated pods. For technical details on the advanced disaggregation strategies, refer to [docs/disaggregation.md](docs/disaggregation.md).

#### 1. Prefill/Decode (P/D) Disaggregation

In this mode, Prefill and Decode run on independent deployments.

To deploy a P/D-enabled Kind environment:

```bash
PD_ENABLED=true make env-dev-kind
```

To verify the setup, follow the same steps described in the [Accessing the Gateway](#accessing-the-gateway) section above.

#### 2. Encode/Prefill/Decode (E/P/D) Disaggregation

This multimodal configuration introduces a standalone Encoder pod for compute-intensive image and video embeddings, while decoupling Prefill and Decode into specialized deployments.

To deploy an E/P/D-enabled Kind environment:

```bash
EPD_ENABLED=true make env-dev-kind
```

<details>
<summary>E/P/D Setup Verification</summary>

1. Port-forward the Gateway:

```bash
kubectl --context kind-llm-d-inference-scheduler-dev port-forward service/inference-gateway-istio-nodeport 8080:80
```
2. Test the Pipeline:
Run an image-based inference request to ensure the E/P/D components are communicating correctly:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen3-VL-2B-Instruct",
    "messages": [
      {
        "role": "user",
        "content": [
          { "type": "image_url", "image_url": { "url": "https://upload.wikimedia.org/wikipedia/commons/thumb/3/3a/Cat03.jpg/1200px-Cat03.jpg" } },
          { "type": "text", "text": "What is in this image?" }
        ]
      }
    ],
    "max_tokens": 100
  }'
```
</details>

### Cleanup

```bash
make clean-env-dev-kind
```

> [!NOTE]
> Port mappings (`KIND_GATEWAY_HOST_PORT`, `KIND_PROM_HOST_PORT`) are baked into the cluster
> at creation time. To change them, run `make clean-env-dev-kind` first, then recreate.

## Running Tests

### Unit Tests

Coverage and race detection are always enabled.

```bash
make test-unit          # run all unit tests (epp + sidecar)
make test-unit-epp      # epp only
make test-unit-sidecar  # sidecar only
```

### Integration Tests

Requires the KIND development environment to be running (`make env-dev-kind`).

```bash
make test-integration   # coverage and race detection always enabled
```

### Filtered Tests

```bash
make test-filter PATTERN=TestName           # epp tests matching pattern
make test-filter PATTERN=TestName TYPE=sidecar
```

### End-to-End Tests

```bash
make test-e2e
```

This creates a temporary Kind cluster named `e2e-tests`, runs the full test suite against it, and deletes the cluster on completion.

**Keeping the cluster on failure**

Set `E2E_KEEP_CLUSTER_ON_FAILURE=true` to preserve the cluster (and, when using a real cluster, all created Kubernetes objects) when any test fails. This is useful for inspecting pod logs, events, or cluster state after a failure.

```bash
E2E_KEEP_CLUSTER_ON_FAILURE=true make test-e2e
```

When set, a successful run still cleans up normally — the cluster is only kept if there is at least one test failure.

**Accessing the cluster after a failure**

E2E tests do not update the host's kubeconfig to point at the `e2e-tests` Kind cluster. After a preserved failure, export the kubeconfig manually:

```bash
# Merge into the default kubeconfig ($HOME/.kube/config or $KUBECONFIG)
kind export kubeconfig --name e2e-tests

# Or write to a specific file
kind export kubeconfig --name e2e-tests --kubeconfig /path/to/kubeconfig
```

Then use it as normal:

```bash
kubectl --context kind-e2e-tests get pods
```

**Environment variables**

| Variable | Default | Description |
|---|---|---|
| `E2E_KEEP_CLUSTER_ON_FAILURE` | `false` | Preserve the Kind cluster (or Kubernetes objects) when the suite fails |
| `E2E_PORT` | `30080` | Host port mapped to the gateway NodePort |
| `E2E_METRICS_PORT` | `32090` | Host port mapped to the EPP metrics NodePort |
| `K8S_CONTEXT` | _(empty)_ | Use an existing cluster context instead of creating a Kind cluster |
| `NAMESPACE` | `default` | Namespace to deploy test resources into |
| `CONTAINER_RUNTIME` | `docker` | Container runtime used to load images into Kind (`docker` or `podman`) |
| `READY_TIMEOUT` | `3m` | How long to wait for resources to become ready |
| `EPP_IMAGE` | `ghcr.io/llm-d/llm-d-inference-scheduler:dev` | EPP image loaded into the Kind cluster |
| `VLLM_SIMULATOR_IMAGE` | `ghcr.io/llm-d/llm-d-inference-sim:v0.8.2` | vLLM simulator image loaded into the Kind cluster |
| `SIDECAR_IMAGE` | `ghcr.io/llm-d/llm-d-routing-sidecar:dev` | Routing sidecar image loaded into the Kind cluster |
| `UDS_TOKENIZER_IMAGE` | `ghcr.io/llm-d/llm-d-uds-tokenizer:dev` | UDS tokenizer image loaded into the Kind cluster |

### Coverage

Coverage profiles are written to `coverage/` (gitignored). To generate an HTML report:

```bash
make coverage-report
open coverage/epp.html
```

To compare coverage against `main`:

```bash
make test-unit          # run tests on your branch first
make coverage-compare   # builds a baseline from main in a temp worktree, then diffs
```

To compare against a different ref:

```bash
make coverage-compare BASE_REF=release-0.5
```

## Tokenization Architecture

> [!NOTE]
> **Python is NOT required**. Previous EPP versions (before v0.5.1) used embedded Python
> tokenizers.

The project uses **UDS (Unix Domain Socket)** tokenization. A separate UDS tokenizer
sidecar container handles tokenization; the EPP itself does not. Previous approaches
(daulet/tokenizers, direct Python/vLLM linking) are deprecated and no longer used.

The UDS tokenizer image is built and published by the
[llm-d-kv-cache](https://github.com/llm-d/llm-d-kv-cache) repository.
Published images are available at `ghcr.io/llm-d/llm-d-uds-tokenizer:<tag>`

- The `:dev` tag tracks the kv-cache `main` branch.
- To pin a specific release, set `UDS_TOKENIZER_TAG` (or `UDS_TOKENIZER_IMAGE` for a
  fully custom reference):

  ```bash
  UDS_TOKENIZER_TAG=v0.7.0 make env-dev-kind
  ```

- To use a different registry, set `IMAGE_REGISTRY` (shared with all other images):

  ```bash
  IMAGE_REGISTRY=quay.io/my-org make env-dev-kind
  ```

- To build the image from source, run `make image-build-uds` in the `llm-d-kv-cache` repo.

## Kubernetes Development Environment

A real Kubernetes cluster can be used for development and testing. Setup has two layers:

- **Cluster infrastructure**: CRDs and operators, installed once by a cluster admin.
- **Developer environment**: your namespace and workloads.

On a shared cluster, each developer uses a separate namespace. On a personal cluster,
`default` works fine.

### Infrastructure Setup

> [!CAUTION]
> Only run this if you are the cluster admin. Applying CRDs and operators can be disruptive
> to other developers sharing the cluster.

Install Gateway API and GIE CRDs:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/standard-install.yaml
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/latest/download/manifests.yaml
```

Install kgateway:

```bash
KGTW_VERSION=v2.0.2
helm upgrade -i --create-namespace --namespace kgateway-system --version $KGTW_VERSION \
  kgateway-crds oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds
helm upgrade -i --namespace kgateway-system --version $KGTW_VERSION \
  kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway \
  --set inferenceExtension.enabled=true
```

For more details, see the Gateway API Inference Extension
[getting started guide](https://gateway-api-inference-extension.sigs.k8s.io/guides/).

### RBAC and Permissions

EPP is namespace-scoped. Its `Role` grants `get/watch/list` on `inferencepools` and `pods`,
plus `create` on `tokenreviews`/`subjectaccessreviews` for metrics auth
(`--metrics-endpoint-auth=true`, the default). To disable metrics auth and avoid the
cluster-scoped RBAC requirement, use `--metrics-endpoint-auth=false`.

### Developer Setup

> [!NOTE]
> This setup requires building and pushing container images to your own private registry.

**1. Set your namespace.**

```bash
export NAMESPACE=your-dev-namespace
kubectl create namespace ${NAMESPACE}
kubectl config set-context --current --namespace="${NAMESPACE}"
```

> [!NOTE]
> If you are using OpenShift, use `oc project "${NAMESPACE}"` instead.

**2. Set your Hugging Face token.**

Required to pull model weights. Get one at [huggingface.co/settings/tokens](https://huggingface.co/settings/tokens):

```bash
export HF_TOKEN="<your-token>"
```

**3. Clone the `llm-d-kv-cache` repository.**

The Makefile expects it as a sibling of this repo at `../llm-d-kv-cache`:

```bash
git clone git@github.com:llm-d/llm-d-kv-cache.git ../llm-d-kv-cache
```

If you clone it elsewhere, set:

```bash
export VLLM_CHART_DIR=<path>/llm-d-kv-cache/vllm-setup-helm
```

**4. Deploy:**

```bash
make env-dev-kubernetes
```

> [!NOTE]
> The model and images of each component can be replaced. See
> [Environment Configuration](#environment-configuration) for details.

**5. Test the deployment.**

Expose the gateway via port-forward:

```bash
kubectl port-forward service/inference-gateway 8080:80 -n "${NAMESPACE}"
```

Make a request:

```bash
curl -s -w '\n' http://localhost:8080/v1/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"hi","max_tokens":10,"temperature":0}' \
  | jq
```

> [!NOTE]
> If the response is empty or contains an error, jq may output a cryptic message.
> Drop the `| jq` to see the raw response.

### Environment Configuration

**1. EPP image registry and tag:**

```bash
export IMAGE_REGISTRY="<YOUR_REGISTRY>"
export EPP_TAG="<YOUR_TAG>"
```

> [!NOTE]
> The full image reference is `${IMAGE_REGISTRY}/llm-d-inference-scheduler:${EPP_TAG}`.
> For example, with `IMAGE_REGISTRY=quay.io/<my-id>` and `EPP_TAG=v1.0.0`, the image
> will be `quay.io/<my-id>/llm-d-inference-scheduler:v1.0.0`.

**2. vLLM replica count:**

```bash
export VLLM_REPLICA_COUNT=2
```

**3. Model name:**

```bash
export MODEL_NAME=mistralai/Mistral-7B-Instruct-v0.2
```

For larger models, set additional vLLM parameters:

```bash
export MODEL_NAME=meta-llama/Llama-3.1-70B-Instruct
export PVC_SIZE=200Gi
export VLLM_MEMORY_RESOURCES=100Gi
export VLLM_GPU_MEMORY_UTILIZATION=0.95
export VLLM_TENSOR_PARALLEL_SIZE=2
export VLLM_GPU_COUNT_PER_INSTANCE=2
```

**4. Additional settings:**

More environment variables are documented in `scripts/kubernetes-dev-env.sh`.

### Deploying Changes

> [!WARNING]
> This requires manual image builds and pushes to your private registry.

Build and push a new image:

```bash
export EPP_TAG=$(git rev-parse HEAD)
export IMAGE_REGISTRY="quay.io/<my-id>"
make image-build
make image-push
```

Redeploy:

```bash
make env-dev-kubernetes
```

Test with a request:

```bash
kubectl port-forward service/inference-gateway 8080:80 -n "${NAMESPACE}"
curl -s -w '\n' http://localhost:8080/v1/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"hi","max_tokens":10,"temperature":0}' \
  | jq
```

### Cleanup Environment

Remove all deployed resources in your namespace:

```bash
make clean-env-dev-kubernetes
```

To remove the namespace too:

```bash
kubectl delete namespace ${NAMESPACE}
```

To uninstall the cluster infrastructure:

Uninstall GIE CRDs:

```bash
kubectl delete -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/latest/download/manifests.yaml \
  --ignore-not-found
```

Uninstall kgateway:

```bash
helm uninstall kgateway -n kgateway-system
helm uninstall kgateway-crds -n kgateway-system
```

For more details, see the Gateway API Inference Extension
[getting started guide](https://gateway-api-inference-extension.sigs.k8s.io/guides/).

## Submitting Changes

Before opening a PR, run:

```bash
make presubmit
```

This runs the same lint, vet, and test checks as the CI pipeline. Fixing failures locally
saves a round-trip through GitHub Actions.