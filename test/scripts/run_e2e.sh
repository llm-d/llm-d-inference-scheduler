#!/bin/bash

# Set a default VLLM_SIMULATOR_TAG if not provided
export VLLM_SIMULATOR_TAG="${VLLM_SIMULATOR_TAG:-v0.4.0}"

# Set the default routing side car image tag
export ROUTING_SIDECAR_TAG="${ROUTING_SIDECAR_TAG:-v0.2.0}"

if [[ "${VLLM_SIMULATOR_TAG}" != "dev" ]]; then
  docker pull ghcr.io/llm-d/llm-d-inference-sim:${VLLM_SIMULATOR_TAG}
  if [[ $? != 0 ]]; then
    echo "Failed to pull ghcr.io/llm-d/llm-d-inference-sim:${VLLM_SIMULATOR_TAG}"
    exit 1
  fi
else
  SIMTAG=$(docker images | grep ghcr.io/llm-d/llm-d-inference-sim | awk '{print $2}' | grep dev)
  if [[ "${SIMTAG}" != "dev" ]]; then
    echo "Missing image ghcr.io/llm-d/llm-d-inference-sim:dev"
    exit 1
  fi
fi

EPPTAG=$(docker images | grep ghcr.io/llm-d/llm-d-inference-scheduler | awk '{print $2}' | grep dev)
if [[ "${EPPTAG}" != "dev" ]]; then
  echo "Missing image ghcr.io/llm-d/llm-d-inference-scheduler:dev"
  exit 1
fi

docker pull ghcr.io/llm-d/llm-d-routing-sidecar:${ROUTING_SIDECAR_TAG}

echo "Running end to end tests"

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
go test -v ${DIR}/../e2e/inference-scheduler/ -ginkgo.v
