# Program-Aware Plugin: Deployment Guide

This document covers deploying and testing the **program-aware fairness plugin** on a local KIND cluster using the `llm-d-inference-scheduler`.

## Table of Contents

- [Prerequisites](#prerequisites)
- [1. Initial Setup](#1-initial-setup)
- [2. Build the EPP Image](#2-build-the-epp-image)
- [3. Deploy to KIND](#3-deploy-to-kind)
- [4. Verify Deployment](#4-verify-deployment)
- [5. Send Test Requests](#5-send-test-requests)
- [6. View Prometheus Metrics](#6-view-prometheus-metrics)
- [7. Roll Out Updates](#7-roll-out-updates)
- [8. Cleanup](#8-cleanup)
- [Troubleshooting](#troubleshooting)

---

## Prerequisites

| Tool      | Minimum Version | Check Command         |
|-----------|-----------------|-----------------------|
| Go        | 1.24+           | `go version`          |
| Docker    | 20+             | `docker --version`    |
| kind      | 0.20+           | `kind --version`      |
| kubectl   | 1.28+           | `kubectl version`     |
| kustomize | 5+              | `kustomize version`   |

Ensure sufficient inotify limits (required for KIND):

```bash
sudo sysctl fs.inotify.max_user_watches=524288
sudo sysctl fs.inotify.max_user_instances=512
```

---

## 1. Initial Setup

Clone the repository and ensure Go modules download properly:

```bash
cd /path/to/llm-d-inference-scheduler

# Ensure private Go modules resolve
export GOPRIVATE=github.com/llm-d/*
go mod download
```

### GIE Dependency

The program-aware plugin requires a GIE version that includes `fairnessPolicyRef` in `PriorityBandConfig`. If the build fails with `unknown field "flowControl.defaultPriorityBand.fairnessPolicyRef"`, update the GIE dependency:

```bash
go get sigs.k8s.io/gateway-api-inference-extension@08fc9b098204edf50dca24b0b5a98f3a0c600e41
go mod tidy
```

### EPP Configuration

The plugin config is at `deploy/config/sim-program-aware-config.yaml`:

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: program-aware-fairness
- type: queue-scorer
- type: max-score-picker
- type: single-profile-handler

featureGates:
- flowControl
- prepareDataPlugins

flowControl:
  defaultPriorityBand:
    fairnessPolicyRef: program-aware-fairness

schedulingProfiles:
- name: default
  plugins:
  - pluginRef: queue-scorer
  - pluginRef: max-score-picker
```

Key points:
- `flowControl` and `prepareDataPlugins` feature gates must be enabled.
- `fairnessPolicyRef: program-aware-fairness` wires the plugin as the FairnessPolicy.
- The plugin type name `program-aware-fairness` must match the registered `ProgramAwarePluginType`.

---

## 2. Build the EPP Image

Build with a custom tag to distinguish from upstream:

```bash
EPP_TAG=program-aware make image-build-epp
```

This produces: `ghcr.io/llm-d/llm-d-inference-scheduler:program-aware`

To verify:

```bash
docker images | grep program-aware
```

---

## 3. Deploy to KIND

### Full deployment (first time)

This creates the KIND cluster, loads images, installs CRDs, Istio, vllm simulator, and the EPP:

```bash
EPP_TAG=program-aware \
EPP_CONFIG=deploy/config/sim-program-aware-config.yaml \
make env-dev-kind
```

The script will:
1. Create a KIND cluster named `llm-d-inference-scheduler-dev`
2. Load EPP, sidecar, and UDS tokenizer images into the cluster
3. Install Gateway API, GIE, and Istio CRDs
4. Deploy Istio control plane, vllm simulator, and EPP
5. Wait for all deployments to become ready

### UDS Tokenizer Image

The deployment includes a UDS tokenizer sidecar. If the image is unavailable:

```bash
# Option A: Pull and re-tag an existing version
docker pull ghcr.io/llm-d/llm-d-uds-tokenizer:latest
docker tag ghcr.io/llm-d/llm-d-uds-tokenizer:latest ghcr.io/llm-d/llm-d-uds-tokenizer:dev

# Option B: Build from the kv-cache Go module
make image-build-uds-tokenizer UDS_TOKENIZER_TAG=dev
```

---

## 4. Verify Deployment

```bash
# Check all pods are Running
kubectl get pods -n default

# Expected output:
# inference-gateway-istio-...          1/1   Running
# tinyllama-...-endpoint-picker-...    2/2   Running   (epp + uds-tokenizer)
# tinyllama-...-vllm-sim-...           2/2   Running   (vllm-sim + sidecar)

# Check gateway is programmed
kubectl get gateway inference-gateway

# Check EPP startup logs (look for "program-aware-fairness" plugin loaded)
kubectl logs -l app=tinyllama-1-1b-chat-v1-0-endpoint-picker -c epp --tail=20
```

---

## 5. Send Test Requests

### Basic request (no fairness ID)

```bash
curl -s http://localhost:30080/v1/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "TinyLlama/TinyLlama-1.1B-Chat-v1.0",
    "prompt": "hello",
    "max_tokens": 10,
    "temperature": 0
  }' | jq
```

### Request with program ID

The program ID is specified via the `x-gateway-inference-fairness-id` header:

```bash
curl -s http://localhost:30080/v1/completions \
  -H 'Content-Type: application/json' \
  -H 'x-gateway-inference-fairness-id: program-1' \
  -d '{
    "model": "TinyLlama/TinyLlama-1.1B-Chat-v1.0",
    "prompt": "hello",
    "max_tokens": 10,
    "temperature": 0
  }' | jq
```

### Multi-program test

```bash
for prog in program-1 program-2 program-3; do
  for i in $(seq 1 5); do
    curl -s http://localhost:30080/v1/completions \
      -H 'Content-Type: application/json' \
      -H "x-gateway-inference-fairness-id: $prog" \
      -d '{
        "model": "TinyLlama/TinyLlama-1.1B-Chat-v1.0",
        "prompt": "hello",
        "max_tokens": 10,
        "temperature": 0
      }' > /dev/null &
  done
done
wait
echo "All requests sent"
```

### Verify in EPP logs

```bash
# Flow queues provisioned per program
kubectl logs -l app=tinyllama-1-1b-chat-v1-0-endpoint-picker -c epp --tail=100 | grep "JIT provisioned"

# Plugin lifecycle hooks (increase verbosity to 5 for TRACE)
kubectl logs -l app=tinyllama-1-1b-chat-v1-0-endpoint-picker -c epp --tail=100 | grep "program-aware"
```

To enable TRACE-level logs:

```bash
kubectl patch deployment tinyllama-1-1b-chat-v1-0-endpoint-picker \
  --type='json' \
  -p='[{"op":"replace","path":"/spec/template/spec/containers/0/args/5","value":"5"}]'
```

---

## 6. View Prometheus Metrics

### Disable metrics authentication (for local testing)

By default, the metrics endpoint requires authentication. Disable it for local testing:

```bash
kubectl patch deployment tinyllama-1-1b-chat-v1-0-endpoint-picker \
  --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--metrics-endpoint-auth=false"}]'
```

### Port-forward and query

```bash
# Start port-forward (background)
kubectl port-forward deployment/tinyllama-1-1b-chat-v1-0-endpoint-picker 9090:9090 &

# Query program-aware metrics
curl -s http://localhost:9090/metrics | grep program_aware
```

### Available metrics

| Metric | Type | Description |
|--------|------|-------------|
| `program_aware_requests_total{program_id}` | Counter | Total requests received per program |
| `program_aware_dispatched_total{program_id}` | Counter | Total requests dispatched per program |
| `program_aware_wait_time_milliseconds_bucket{program_id}` | Histogram | Flow control queue wait time distribution |
| `program_aware_input_tokens_total{program_id}` | Counter | Prompt tokens consumed per program |
| `program_aware_output_tokens_total{program_id}` | Counter | Completion tokens produced per program |

### Example output

```
program_aware_requests_total{program_id="program-1"} 5
program_aware_dispatched_total{program_id="program-1"} 5
program_aware_wait_time_milliseconds_bucket{program_id="program-1",le="10"} 4
program_aware_wait_time_milliseconds_bucket{program_id="program-1",le="25"} 5
program_aware_input_tokens_total{program_id="program-1"} 25
program_aware_output_tokens_total{program_id="program-1"} 50
```

---

## 7. Roll Out Updates

After making code changes to the plugin:

```bash
# 1. Rebuild the EPP image (same tag)
EPP_TAG=program-aware make image-build-epp

# 2. Load into KIND
kind --name llm-d-inference-scheduler-dev load docker-image \
  ghcr.io/llm-d/llm-d-inference-scheduler:program-aware

# 3. Restart the EPP to pick up the new image
kubectl rollout restart deployment tinyllama-1-1b-chat-v1-0-endpoint-picker

# 4. Wait for rollout
kubectl rollout status deployment tinyllama-1-1b-chat-v1-0-endpoint-picker --timeout=120s

# 5. Verify pod is healthy
kubectl get pods -l app=tinyllama-1-1b-chat-v1-0-endpoint-picker
```

### Update the config

If you change the EPP config YAML:

```bash
# Delete and recreate the configmap
kubectl delete configmap epp-config
kubectl create configmap epp-config \
  --from-file=epp-config.yaml=deploy/config/sim-program-aware-config.yaml

# Restart EPP to reload config
kubectl rollout restart deployment tinyllama-1-1b-chat-v1-0-endpoint-picker
```

### One-liner for rebuild + deploy

```bash
EPP_TAG=program-aware make image-build-epp && \
kind --name llm-d-inference-scheduler-dev load docker-image ghcr.io/llm-d/llm-d-inference-scheduler:program-aware && \
kubectl rollout restart deployment tinyllama-1-1b-chat-v1-0-endpoint-picker && \
kubectl rollout status deployment tinyllama-1-1b-chat-v1-0-endpoint-picker --timeout=120s
```

---

## 8. Cleanup

```bash
# Delete the KIND cluster
make clean-env-dev-kind

# Or manually
kind delete cluster --name llm-d-inference-scheduler-dev
```

---

## Troubleshooting

### EPP CrashLoopBackOff

Check logs for the crash reason:

```bash
kubectl logs <epp-pod-name> -c epp --previous
```

Common causes:
- **`unknown field` in config**: GIE dependency too old — update with `go get sigs.k8s.io/gateway-api-inference-extension@<newer-commit>`
- **Plugin not registered**: Ensure `register.go` includes `plugin.Register(programaware.ProgramAwarePluginType, programaware.ProgramAwarePluginFactory)`

### kube-proxy CrashLoopBackOff

Symptom: pods can't reach the API server (dial tcp 10.96.0.1:443 timeout).

```bash
# Fix inotify limits
sudo sysctl fs.inotify.max_user_watches=524288
sudo sysctl fs.inotify.max_user_instances=512

# Recreate the cluster
make clean-env-dev-kind
EPP_TAG=program-aware EPP_CONFIG=deploy/config/sim-program-aware-config.yaml make env-dev-kind
```

### UDS tokenizer image not found

If `kind load docker-image` fails for the tokenizer:

```bash
# Check available tags
gh api /orgs/llm-d/packages/container/llm-d-uds-tokenizer/versions \
  --paginate --jq '.[].metadata.container.tags[]' | head -20

# Pull and re-tag
docker pull ghcr.io/llm-d/llm-d-uds-tokenizer:<available-tag>
docker tag ghcr.io/llm-d/llm-d-uds-tokenizer:<available-tag> ghcr.io/llm-d/llm-d-uds-tokenizer:dev
```

### Metrics endpoint returns "Unauthorized"

```bash
kubectl patch deployment tinyllama-1-1b-chat-v1-0-endpoint-picker \
  --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--metrics-endpoint-auth=false"}]'
```

### No program_aware metrics appear

1. Ensure you sent requests with the `x-gateway-inference-fairness-id` header.
2. Verify the plugin is loaded in logs: `grep "program-aware" <epp-logs>`.
3. Metrics only appear after at least one labeled request has been processed.
