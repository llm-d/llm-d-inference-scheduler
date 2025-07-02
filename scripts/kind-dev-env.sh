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

# Set a default VLLM_SIMULATOR_IMAGE if not provided
: "${VLLM_SIMULATOR_IMAGE:=llm-d-inference-sim}"

# Set a default VLLM_SIMULATOR_TAG if not provided
export VLLM_SIMULATOR_TAG="${VLLM_SIMULATOR_TAG:-latest}"

# Set a default EPP_IMAGE if not provided
: "${EPP_IMAGE:=llm-d-inference-scheduler}"

# Set a default EPP_TAG if not provided
export EPP_TAG="${EPP_TAG:-dev}"

# Set the model name to deploy
export MODEL_NAME="${MODEL_NAME:-food-review}"
# Safe model name for Kubernetes resources (lowercase, hyphenated)
export MODEL_NAME_SAFE=$(echo "${MODEL_NAME}" | tr '[:upper:]' '[:lower:]' | tr ' /_.' '-')
# Set the endpoint-picker to deploy
export EPP_NAME="${EPP_NAME:-${MODEL_NAME}-endpoint-picker}"

# Set the default routing side car image tag
export ROUTING_SIDECAR_TAG="${ROUTING_SIDECAR_TAG:-0.0.6}"

# Set the inference pool name for the deployment
export POOL_NAME="${POOL_NAME:-${MODEL_NAME}-inference-pool}"

# vLLM replica count (without PD)
export VLLM_REPLICA_COUNT="${VLLM_REPLICA_COUNT:-1}"

# By default we are not setting up for PD
export PD_ENABLED="\"${PD_ENABLED:-false}\""

# Replica counts for P and D
export VLLM_REPLICA_COUNT_P="${VLLM_REPLICA_COUNT_P:-1}"
export VLLM_REPLICA_COUNT_D="${VLLM_REPLICA_COUNT_D:-2}"

if [ "${PD_ENABLED}" != "\"true\"" ]; then
  DEFAULT_EPP_CONFIG="deploy/config/epp-config.yaml"
else
  DEFAULT_EPP_CONFIG="deploy/config/pd-epp-config.yaml"
fi
export EPP_CONFIG="${EPP_CONFIG:-${DEFAULT_EPP_CONFIG}}"
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
for cmd in kind kubectl kustomize ${CONTAINER_RUNTIME}; do
    if ! command -v "$cmd" &> /dev/null; then
        echo "Error: $cmd is not installed or not in the PATH."
        exit 1
    fi
done

# ------------------------------------------------------------------------------
# Cluster Deployment
# ------------------------------------------------------------------------------

# Check if the cluster already exists
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "Cluster '${CLUSTER_NAME}' already exists, re-using"
else
    kind create cluster --name "${CLUSTER_NAME}" --config - << EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30080
    hostPort: ${GATEWAY_HOST_PORT}
    protocol: TCP
EOF
fi

# Set the kubectl context to the kind cluster
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
kubectl config set-context ${KUBE_CONTEXT} --namespace=default

set -x

# Hotfix for https://github.com/kubernetes-sigs/kind/issues/3880
CONTAINER_NAME="${CLUSTER_NAME}-control-plane"
${CONTAINER_RUNTIME} exec -it ${CONTAINER_NAME} /bin/bash -c "sysctl net.ipv4.conf.all.arp_ignore=0"

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

# Load the vllm simulator image into the cluster
if [ "${CONTAINER_RUNTIME}" == "podman" ]; then
	podman save ${IMAGE_REGISTRY}/${VLLM_SIMULATOR_IMAGE}:${VLLM_SIMULATOR_TAG} -o /dev/stdout | kind --name ${CLUSTER_NAME} load image-archive /dev/stdin
else
	if docker image inspect "${IMAGE_REGISTRY}/${VLLM_SIMULATOR_IMAGE}:${VLLM_SIMULATOR_TAG}" > /dev/null 2>&1; then
		echo "INFO: Loading image into KIND cluster..."
		kind --name ${CLUSTER_NAME} load docker-image ${IMAGE_REGISTRY}/${VLLM_SIMULATOR_IMAGE}:${VLLM_SIMULATOR_TAG}
	fi
fi

# Load the ext_proc endpoint-picker image into the cluster
if [ "${CONTAINER_RUNTIME}" == "podman" ]; then
	podman save ${IMAGE_REGISTRY}/${EPP_IMAGE}:${EPP_TAG} -o /dev/stdout | kind --name ${CLUSTER_NAME} load image-archive /dev/stdin
else
	kind --name ${CLUSTER_NAME} load docker-image ${IMAGE_REGISTRY}/${EPP_IMAGE}:${EPP_TAG}
fi
# ------------------------------------------------------------------------------
# CRD Deployment (Gateway API + GIE)
# ------------------------------------------------------------------------------

kustomize build deploy/components/crds-gateway-api |
	kubectl --context ${KUBE_CONTEXT} apply --server-side --force-conflicts -f -

kustomize build deploy/components/crds-gie |
	kubectl --context ${KUBE_CONTEXT} apply --server-side --force-conflicts -f -

kustomize build --enable-helm deploy/components/crds-istio |
	kubectl --context ${KUBE_CONTEXT} apply --server-side --force-conflicts -f -

# ------------------------------------------------------------------------------
# Development Environment
# ------------------------------------------------------------------------------

# Deploy the environment to the "default" namespace
if [ "${PD_ENABLED}" != "\"true\"" ]; then
  KUSTOMIZE_DIR="deploy/environments/dev/kind-istio"
else
  KUSTOMIZE_DIR="deploy/environments/dev/kind-istio-pd"
fi 

kubectl --context ${KUBE_CONTEXT} create configmap epp-config --from-file=epp-config.yaml=${EPP_CONFIG}

kustomize build --enable-helm  ${KUSTOMIZE_DIR} \
	| envsubst '${POOL_NAME} ${MODEL_NAME} ${MODEL_NAME_SAFE} ${EPP_NAME} ${EPP_TAG} ${VLLM_SIMULATOR_TAG} \
  ${PD_ENABLED} ${ROUTING_SIDECAR_TAG} ${VLLM_REPLICA_COUNT} ${VLLM_REPLICA_COUNT_P} ${VLLM_REPLICA_COUNT_D}' \
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
