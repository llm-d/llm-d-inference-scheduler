#!/bin/bash

# Use the CONTAINER_TOOL from the environment, or default to docker if it's not set.
CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"
echo "Using container tool: ${CONTAINER_TOOL}"

export EPP_IMAGE="${EPP_IMAGE:-ghcr.io/llm-d/llm-d-inference-scheduler:dev}"
export VLLM_SIMULATOR_IMAGE="${VLLM_SIMULATOR_IMAGE:-ghcr.io/llm-d/llm-d-inference-sim:v0.4.0}"
export ROUTING_SIDECAR_IMAGE="${ROUTING_SIDECAR_IMAGE:-ghcr.io/llm-d/llm-d-routing-sidecar:v0.2.0}"

# --- Print Final Images and Pull Dependencies ---
echo "--- Using the following images for the E2E test ---"
echo "Scheduler Image:     ${EPP_IMAGE}"
echo "Simulator Image:     ${VLLM_SIMULATOR_IMAGE}"
echo "Sidecar Image:       ${ROUTING_SIDECAR_IMAGE}"
echo "----------------------------------------------------"

echo "Pulling dependencies..."
${CONTAINER_TOOL} pull ${EPP_IMAGE}
if [[ $? != 0 ]]; then
  echo "Failed to pull ${EPP_IMAGE}"
  exit 1
fi

${CONTAINER_TOOL} pull ${VLLM_SIMULATOR_IMAGE}
if [[ $? != 0 ]]; then
  echo "Failed to pull ${VLLM_SIMULATOR_IMAGE}"
  exit 1
fi

${CONTAINER_TOOL} pull ${ROUTING_SIDECAR_IMAGE}
if [[ $? != 0 ]]; then
  echo "Failed to pull ${ROUTING_SIDECAR_IMAGE}"
  exit 1
fi
echo "Successfully pulled dependencies"
