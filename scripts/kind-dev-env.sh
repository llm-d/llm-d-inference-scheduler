#!/bin/bash

# This shell script deploys a kind cluster with an Istio-based Gateway API
# implementation fully configured. It deploys the vllm simulator, which it
# exposes with a Gateway -> HTTPRoute -> InferencePool. The Gateway is
# configured with the a filter for the ext_proc endpoint picker.

set -eo pipefail

# ------------------------------------------------------------------------------
# Variables
# ------------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Set a default CLUSTER_NAME if not provided
: "${CLUSTER_NAME:=llm-d-inference-scheduler-dev}"

# Set the host port to map to the Gateway's inbound port (30080)
: "${GATEWAY_HOST_PORT:=30080}"

# Set the default IMAGE_REGISTRY if not provided
: "${IMAGE_REGISTRY:=ghcr.io/llm-d}"

# Set a default VLLM_SIMULATOR_TAG if not provided
export VLLM_SIMULATOR_TAG="${VLLM_SIMULATOR_TAG:-v0.8.2}"

# Set a default VLLM_SIMULATOR_IMAGE if not provided
VLLM_SIMULATOR_IMAGE="${VLLM_SIMULATOR_IMAGE:-${IMAGE_REGISTRY}/llm-d-inference-sim:${VLLM_SIMULATOR_TAG}}"
export VLLM_SIMULATOR_IMAGE

# Set a default EPP_TAG if not provided
export EPP_TAG="${EPP_TAG:-dev}"

# Set a default EPP_IMAGE if not provided
EPP_IMAGE="${EPP_IMAGE:-${IMAGE_REGISTRY}/llm-d-inference-scheduler:${EPP_TAG}}"
export EPP_IMAGE

# Set the model name to deploy (EPD defaults to a multimodal model)
if [ -z "${MODEL_NAME}" ] && [ "${EPD_ENABLED}" == "true" ]; then
  export MODEL_NAME="Qwen/Qwen3-VL-2B-Instruct"
else
  export MODEL_NAME="${MODEL_NAME:-TinyLlama/TinyLlama-1.1B-Chat-v1.0}"
fi
# Extract model family (e.g., "meta-llama" from "meta-llama/Llama-3.1-8B-Instruct")
export MODEL_FAMILY="${MODEL_NAME%%/*}"
# Extract model ID (e.g., "Llama-3.1-8B-Instruct")
export MODEL_ID="${MODEL_NAME##*/}"
# Safe model name for Kubernetes resources (lowercase, hyphenated)
export MODEL_NAME_SAFE=$(echo "${MODEL_ID}" | tr '[:upper:]' '[:lower:]' | tr ' /_.' '-')

# Set the endpoint-picker to deploy
export EPP_NAME="${EPP_NAME:-${MODEL_NAME_SAFE}-endpoint-picker}"

# Set the default routing side car image tag
export SIDECAR_TAG="${SIDECAR_TAG:-dev}"

# Set a default SIDECAR_IMAGE if not provided
SIDECAR_IMAGE="${SIDECAR_IMAGE:-${IMAGE_REGISTRY}/llm-d-routing-sidecar:${SIDECAR_TAG}}"
export SIDECAR_IMAGE

# Set the default UDS tokenizer image tag
export UDS_TOKENIZER_TAG="${UDS_TOKENIZER_TAG:-dev}"

# Set a default UDS_TOKENIZER_IMAGE if not provided
UDS_TOKENIZER_IMAGE="${UDS_TOKENIZER_IMAGE:-${IMAGE_REGISTRY}/llm-d-uds-tokenizer:${UDS_TOKENIZER_TAG}}"
export UDS_TOKENIZER_IMAGE

# Set the inference pool name for the deployment
export POOL_NAME="${POOL_NAME:-${MODEL_NAME_SAFE}-inference-pool}"

# vLLM replica count (without PD/EPD)
export VLLM_REPLICA_COUNT="${VLLM_REPLICA_COUNT:-1}"

# By default we are not setting up for PD (Prefill/Decode)
export PD_ENABLED="\"${PD_ENABLED:-false}\""

# By default we are not deploying Prometheus monitoring
export PROM_ENABLED="${PROM_ENABLED:-false}"

# Set the host port to map to the Prometheus NodePort (30090)
: "${PROM_HOST_PORT:=30090}"

# By default we are not setting up for E/P/D (separate Encode, Prefill, and Decode deployments)
export EPD_ENABLED="\"${EPD_ENABLED:-false}\""

if [ "${PD_ENABLED}" == "\"true\"" ] && [ "${EPD_ENABLED}" == "\"true\"" ]; then
  echo "Error: PD_ENABLED and EPD_ENABLED cannot both be set to true." >&2
  exit 1
fi

# By default we are not setting up for KV cache
export KV_CACHE_ENABLED="${KV_CACHE_ENABLED:-false}"

# By default we are not setting up for external tokenizer
export EXTERNAL_TOKENIZER_ENABLED="${EXTERNAL_TOKENIZER_ENABLED:-false}"

# Replica counts for E (Encode), P (Prefill), and D (Decode)
export VLLM_REPLICA_COUNT_E="${VLLM_REPLICA_COUNT_E:-1}"
export VLLM_REPLICA_COUNT_P="${VLLM_REPLICA_COUNT_P:-1}"
if [ "${EPD_ENABLED}" == "\"true\"" ]; then
  export VLLM_REPLICA_COUNT_D="${VLLM_REPLICA_COUNT_D:-1}"
else
  export VLLM_REPLICA_COUNT_D="${VLLM_REPLICA_COUNT_D:-2}"
fi

# Data Parallel size
export VLLM_DATA_PARALLEL_SIZE="${VLLM_DATA_PARALLEL_SIZE:-1}"

# Validate configuration compatibility
if [ "${KV_CACHE_ENABLED}" == "true" ] && ([ "${PD_ENABLED}" == "\"true\"" ] || [ "${EPD_ENABLED}" == "\"true\"" ] || [ ${VLLM_DATA_PARALLEL_SIZE} -ne 1 ]); then
  echo "Error: KV_CACHE_ENABLED=true is not supported with PD_ENABLED=true, EPD_ENABLED=true, or VLLM_DATA_PARALLEL_SIZE != 1." >&2
  exit 1
fi


PRIMARY_PORT="0"

# Determine EPP config file based on feature flags
if [ "${EXTERNAL_TOKENIZER_ENABLED}" == "true" ]; then
  # External tokenizer mode (uses precise-prefix-cache with UDS tokenizer sidecar)
  DEFAULT_EPP_CONFIG="deploy/config/sim-epp-external-tokenizer-config.yaml"

elif [ "${KV_CACHE_ENABLED}" == "true" ]; then
  # KV cache mode (simple mode only)
  DEFAULT_EPP_CONFIG="deploy/config/sim-epp-kvcache-config.yaml"

elif [ "${EPD_ENABLED}" == "\"true\"" ]; then
  # E/P/D mode (separate Encode, Prefill, and Decode deployments)
  DEFAULT_EPP_CONFIG="deploy/config/sim-epd-epp-config.yaml"
  [ ${VLLM_DATA_PARALLEL_SIZE} -ne 1 ] && PRIMARY_PORT="8000"

elif [ "${PD_ENABLED}" == "\"true\"" ]; then
  # Prefill-Decode mode
  DEFAULT_EPP_CONFIG="deploy/config/sim-pd-epp-config.yaml"
  [ ${VLLM_DATA_PARALLEL_SIZE} -ne 1 ] && PRIMARY_PORT="8000"

elif [ ${VLLM_DATA_PARALLEL_SIZE} -ne 1 ]; then
  # Data Parallel mode (only needed for Istio pre-1.28.1)
  # Not really called in kind(docker.io/istio/pilot:1.28.1) by "make env-dev-kind"
  DEFAULT_EPP_CONFIG="deploy/config/dp-epp-config.yaml"

else
  # Simple mode
  DEFAULT_EPP_CONFIG="deploy/config/sim-epp-config.yaml"
fi

export EPP_CONFIG="${EPP_CONFIG:-${DEFAULT_EPP_CONFIG}}"
export PRIMARY_PORT

# ------------------------------------------------------------------------------
# Setup & Requirement Checks
# ------------------------------------------------------------------------------

# Check for a supported container runtime if an explicit one was not set
if [ -z "${CONTAINER_RUNTIME}" ]; then
  if command -v docker &> /dev/null; then
    CONTAINER_RUNTIME="docker"
  elif command -v podman &> /dev/null; then
    CONTAINER_RUNTIME="podman"
  else
    echo "Neither docker nor podman could be found in PATH" >&2
    exit 1
  fi
fi

set -u

# Check for required programs
for cmd in kind kubectl ${CONTAINER_RUNTIME}; do
    if ! command -v "$cmd" &> /dev/null; then
        echo "Error: $cmd is not installed or not in the PATH."
        exit 1
    fi
done

# Prometheus config-reloader needs sufficient inotify resources
if [ "${PROM_ENABLED}" == "true" ]; then
  INOTIFY_INSTANCES=$(cat /proc/sys/fs/inotify/max_user_instances)
  if [ "${INOTIFY_INSTANCES}" -lt 512 ]; then
    echo "Error: fs.inotify.max_user_instances is ${INOTIFY_INSTANCES} (need >= 512) for Prometheus."
    echo ""
    echo "  sudo sysctl -w fs.inotify.max_user_instances=512"
    echo ""
    echo "To persist: echo 'fs.inotify.max_user_instances=512' | sudo tee /etc/sysctl.d/99-inotify.conf"
    exit 1
  fi
fi

TARGET_PORTS="8000"

NEW_LINE=$'\n'
for ((i = 1; i < VLLM_DATA_PARALLEL_SIZE; ++i)); do
    EXTRA_PORT=$((8000 + i))
    TARGET_PORTS="${TARGET_PORTS}${NEW_LINE}  - number: ${EXTRA_PORT}"
done

export TARGET_PORTS

# ------------------------------------------------------------------------------
# Cluster Deployment
# ------------------------------------------------------------------------------

# Check if the cluster already exists
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "Cluster '${CLUSTER_NAME}' already exists, re-using"
else
    EXTRA_PORT_MAPPINGS=""
    if [ "${PROM_ENABLED}" == "true" ]; then
      EXTRA_PORT_MAPPINGS="  - containerPort: 30090
    hostPort: ${PROM_HOST_PORT}
    protocol: TCP"
    fi

    kind create cluster --name "${CLUSTER_NAME}" --config - << EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30080
    hostPort: ${GATEWAY_HOST_PORT}
    protocol: TCP
${EXTRA_PORT_MAPPINGS}
EOF
fi

# Set the kubectl context to the kind cluster
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
kubectl config set-context ${KUBE_CONTEXT} --namespace=default

set -x

# Hotfix for https://github.com/kubernetes-sigs/kind/issues/3880
CONTAINER_NAME="${CLUSTER_NAME}-control-plane"
${CONTAINER_RUNTIME} exec ${CONTAINER_NAME} /bin/bash -c "sysctl net.ipv4.conf.all.arp_ignore=0"

# Wait for all pods to be ready
kubectl --context ${KUBE_CONTEXT} -n kube-system wait --for=condition=Ready --all pods --timeout=300s

echo "Waiting for local-path-storage pods to be created..."
until kubectl --context ${KUBE_CONTEXT} -n local-path-storage get pods -o name | grep -q pod/; do
  sleep 2
done
kubectl --context ${KUBE_CONTEXT} -n local-path-storage wait --for=condition=Ready --all pods --timeout=300s

# ------------------------------------------------------------------------------
# Load Container Images
# ------------------------------------------------------------------------------

LINUX_ARCH="$(uname -m)"
case "${LINUX_ARCH}" in
    x86_64) LINUX_ARCH="amd64" ;;
    aarch64|arm64) LINUX_ARCH="arm64" ;;
esac

PLATFORM_ARGS=()
if [ "${CONTAINER_RUNTIME}" == "docker" ]; then
    PLATFORM_ARGS=("--platform" "linux/${LINUX_ARCH}")
fi

for IMAGE in "${VLLM_SIMULATOR_IMAGE}" "${EPP_IMAGE}" "${SIDECAR_IMAGE}" "${UDS_TOKENIZER_IMAGE}"; do
    if ! "${CONTAINER_RUNTIME}" image inspect "${IMAGE}" > /dev/null 2>&1; then
        echo "Image ${IMAGE} not found locally, pulling..."
        "${CONTAINER_RUNTIME}" pull "${PLATFORM_ARGS[@]}" "${IMAGE}"
    fi
    echo "Loading ${IMAGE} into kind cluster..."
    "${CONTAINER_RUNTIME}" save "${PLATFORM_ARGS[@]}" "${IMAGE}" | kind --name "${CLUSTER_NAME}" load image-archive /dev/stdin
done

# ------------------------------------------------------------------------------
# CRD Deployment (Gateway API + GIE)
# ------------------------------------------------------------------------------

kubectl kustomize deploy/components/crds-gateway-api |
	kubectl --context ${KUBE_CONTEXT} apply --server-side --force-conflicts -f -

kubectl kustomize deploy/components/crds-gie |
	kubectl --context ${KUBE_CONTEXT} apply --server-side --force-conflicts -f -

kubectl kustomize --enable-helm deploy/components/crds-istio |
	kubectl --context ${KUBE_CONTEXT} apply --server-side --force-conflicts -f -

# ------------------------------------------------------------------------------
# Development Environment
# ------------------------------------------------------------------------------

# Deploy the environment to the "default" namespace
if [ "${EPD_ENABLED}" == "\"true\"" ]; then
  KUSTOMIZE_DIR="deploy/environments/dev/kind-istio-epd"
elif [ "${PD_ENABLED}" == "\"true\"" ]; then
  KUSTOMIZE_DIR="deploy/environments/dev/kind-istio-pd"
else
  KUSTOMIZE_DIR="deploy/environments/dev/kind-istio"
fi

TEMP_FILE=$(mktemp)
# Ensure that the temporary file is deleted now matter what happens in the script
trap "rm -f \"${TEMP_FILE}\"" EXIT

kubectl --context ${KUBE_CONTEXT} delete configmap epp-config --ignore-not-found
envsubst '$MODEL_NAME' < ${EPP_CONFIG} > ${TEMP_FILE}
kubectl --context ${KUBE_CONTEXT} create configmap epp-config --from-file=epp-config.yaml=${TEMP_FILE}

kubectl kustomize --enable-helm  ${KUSTOMIZE_DIR} \
	| envsubst '${POOL_NAME} ${MODEL_NAME} ${MODEL_NAME_SAFE} ${EPP_NAME} ${EPP_IMAGE} ${VLLM_SIMULATOR_IMAGE} \
  ${PD_ENABLED} ${KV_CACHE_ENABLED} ${SIDECAR_IMAGE} ${UDS_TOKENIZER_IMAGE} ${TARGET_PORTS} \
  ${VLLM_REPLICA_COUNT} ${VLLM_REPLICA_COUNT_E} ${VLLM_REPLICA_COUNT_P} ${VLLM_REPLICA_COUNT_D} ${VLLM_DATA_PARALLEL_SIZE}' \
  | kubectl --context ${KUBE_CONTEXT} apply -f -

# ------------------------------------------------------------------------------
# Check & Verify
# ------------------------------------------------------------------------------

# Wait for all control-plane deployments to be ready
kubectl --context ${KUBE_CONTEXT} -n llm-d-istio-system wait --for=condition=available --timeout=300s deployment --all

# Wait for all deployments to be ready
kubectl --context ${KUBE_CONTEXT} -n default wait --for=condition=available --timeout=300s deployment --all

# Wait for the gateway to be ready
kubectl --context ${KUBE_CONTEXT} wait gateway/inference-gateway --for=condition=Programmed --timeout=300s

# ------------------------------------------------------------------------------
# Prometheus Monitoring (optional)
# ------------------------------------------------------------------------------

if [ "${PROM_ENABLED}" == "true" ]; then
  echo "Deploying Prometheus monitoring stack..."

  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
  helm repo update prometheus-community

  # Install kube-prometheus-stack (Prometheus only)
  helm upgrade --install prometheus prometheus-community/kube-prometheus-stack \
    --namespace monitoring --create-namespace \
    --set grafana.enabled=false \
    --set alertmanager.enabled=false \
    --set kubeControllerManager.enabled=false \
    --set kubeEtcd.enabled=false \
    --set kubeProxy.enabled=false \
    --set kubeScheduler.enabled=false \
    --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
    --set prometheus.prometheusSpec.podMonitorSelectorNilUsesHelmValues=false \
    --set prometheus.prometheusSpec.resources.requests.memory=512Mi \
    --set prometheus.prometheusSpec.resources.limits.memory=1Gi \
    --set prometheus.service.type=NodePort \
    --set prometheus.service.nodePort=30090 \
    --kube-context ${KUBE_CONTEXT} \
    --wait --timeout 300s

  kubectl kustomize deploy/components/monitoring \
    | envsubst '${EPP_NAME} ${POOL_NAME}' \
    | kubectl --context ${KUBE_CONTEXT} apply -f -

  echo "Prometheus monitoring deployed."
fi

cat <<EOF
-----------------------------------------
Deployment completed!

* Kind Cluster Name: ${CLUSTER_NAME}
* Kubectl Context: ${KUBE_CONTEXT}

Status:

* The vllm simulator is running and exposed via InferencePool
* The Gateway is exposing the InferencePool via HTTPRoute
* The Endpoint Picker is loaded into the Gateway via ext_proc

You can watch the Endpoint Picker logs with:

  $ kubectl --context ${KUBE_CONTEXT} logs -f deployments/${EPP_NAME}

With that running in the background, you can make requests:

  $ curl -s -w '\n' http://localhost:${GATEWAY_HOST_PORT}/v1/completions -H 'Content-Type: application/json' -d '{"model":"${MODEL_NAME}","prompt":"hi","max_tokens":10,"temperature":0}' | jq

See DEVELOPMENT.md for additional access methods if the above fails.

-----------------------------------------
EOF

if [ "${PROM_ENABLED}" == "true" ]; then
cat <<EOF

Monitoring:

* Prometheus: http://localhost:${PROM_HOST_PORT}

EOF
fi
