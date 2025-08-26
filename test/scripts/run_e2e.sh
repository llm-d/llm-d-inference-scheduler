#!/bin/bash

# Set a default EPP_TAG if not provided
export EPP_TAG="${EPP_TAG:-dev}"

# Set a default VLLM_SIMULATOR_TAG if not provided
export VLLM_SIMULATOR_TAG="${VLLM_SIMULATOR_TAG:-v0.4.0}"

# Set the default routing side car image tag
export ROUTING_SIDECAR_TAG="${ROUTING_SIDECAR_TAG:-v0.2.0}"

SIMTAG=$(docker images | grep ghcr.io/llm-d/llm-d-inference-sim | awk '{print $2}' | grep ${VLLM_SIMULATOR_TAG})
if [[ "${SIMTAG}" != "${VLLM_SIMULATOR_TAG}" ]]; then
  docker pull ghcr.io/llm-d/llm-d-inference-sim:${VLLM_SIMULATOR_TAG}
  if [[ $? != 0 ]]; then
    echo "Failed to pull ghcr.io/llm-d/llm-d-inference-sim:${VLLM_SIMULATOR_TAG}"
    exit 1
  fi
fi

EPPTAG=$(docker images | grep ghcr.io/llm-d/llm-d-inference-scheduler | awk '{print $2}' | grep ${EPP_TAG})
if [[ "${EPPTAG}" != "${EPP_TAG}" ]]; then
  docker pull ghcr.io/llm-d/llm-d-inference-scheduler:${EPP_TAG}
  if [[ $? != 0 ]]; then
    echo "Failed to pull ghcr.io/llm-d/llm-d-inference-scheduler:${EPP_TAG}"
    exit 1
  fi
fi

SIDECARTAG=$(docker images | grep ghcr.io/llm-d/llm-d-routing-sidecar | awk '{print $2}' | grep ${ROUTING_SIDECAR_TAG})
if [[ "${SIDECARTAG}" != "${ROUTING_SIDECAR_TAG}" ]]; then
  docker pull ghcr.io/llm-d/llm-d-routing-sidecar:${ROUTING_SIDECAR_TAG}
  if [[ $? != 0 ]]; then
    echo "Failed to pull ghcr.io/llm-d/llm-d-routing-sidecar:${ROUTING_SIDECAR_TAG}"
    exit 1
  fi
fi

echo "Running end to end tests"

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
go test -v ${DIR}/../e2e/ -ginkgo.v
