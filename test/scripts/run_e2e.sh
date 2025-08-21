#!/bin/bash

# Set a default VLLM_SIMULATOR_TAG if not provided
export VLLM_SIMULATOR_TAG="${VLLM_SIMULATOR_TAG:-v0.4.0}"

# Set the default routing side car image tag
export ROUTING_SIDECAR_TAG="${ROUTING_SIDECAR_TAG:-v0.2.0}"

if [[ "${VLLM_SIMULATOR_TAG}" != "dev" ]]; then
  docker pull ghcr.io/llm-d/llm-d-inference-sim:${VLLM_SIMULATOR_TAG}
  if [[ $? != 0 ]]; then
    tput setaf 1
    echo "Failed to pull ghcr.io/llm-d/llm-d-inference-sim:${VLLM_SIMULATOR_TAG}"
    tput sgr0
    exit 1
  fi
else
  SIMTAG=$(docker images | grep ghcr.io/llm-d/llm-d-inference-sim | awk '{print $2}' | grep dev)
  if [[ "${SIMTAG}" != "dev" ]]; then
    tput setaf 1
    echo "Missing image ghcr.io/llm-d/llm-d-inference-sim:dev"
    tput sgr0
    exit 1
  fi
fi

EPPTAG=$(docker images | grep ghcr.io/llm-d/llm-d-inference-scheduler | awk '{print $2}' | grep dev)
if [[ "${EPPTAG}" != "dev" ]]; then
  tput setaf 1
  echo "Missing image ghcr.io/llm-d/llm-d-inference-scheduler:dev"
  tput sgr0
  exit 1
fi

docker pull ghcr.io/llm-d/llm-d-routing-sidecar:${ROUTING_SIDECAR_TAG}

tput bold
echo "Running end to end tests"
tput sgr0

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
go test -v ${DIR}/../e2e/inference-scheduler/ -ginkgo.v