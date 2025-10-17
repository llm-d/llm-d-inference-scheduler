#!/bin/bash

# Use the CONTAINER_TOOL from the environment, or default to docker if it's not set.
CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"
echo "Using container tool: ${CONTAINER_TOOL}"

export EPP_IMAGE="${EPP_IMAGE:-ghcr.io/llm-d/llm-d-inference-scheduler:latest}"
export VLLM_SIMULATOR_IMAGE="${VLLM_SIMULATOR_IMAGE:-ghcr.io/llm-d/llm-d-inference-sim:v0.4.0}"
export ROUTING_SIDECAR_IMAGE="${ROUTING_SIDECAR_IMAGE:-ghcr.io/llm-d/llm-d-routing-sidecar:v0.2.0}"

# --- Helper Function to Ensure Image Availability ---
# This function checks the registry first, then falls back to a local-only check.
ensure_image() {
  local image_name="$1"
  echo "Checking for image: ${image_name}"

  # Attempt to inspect the image manifest on the remote registry.
  if ${CONTAINER_TOOL} manifest inspect "${image_name}" > /dev/null 2>&1; then
    echo " -> Image found on registry. Pulling..."
    if ! ${CONTAINER_TOOL} pull "${image_name}"; then
        echo "    ❌ ERROR: Failed to pull image '${image_name}'."
        exit 1
    fi
    echo "    ✅ Successfully pulled image."
  else
    # If the image is not on the registry, check if it's already available locally.
    echo " -> Image not found on registry. Checking for a local version..."
    if [ -z "$(${CONTAINER_TOOL} images -q "${image_name}")" ]; then
      # If it's not on the registry AND not local, it's an error.
      echo "    ❌ ERROR: Image '${image_name}' is not available locally and could not be found on the registry."
      exit 1
    fi
    echo " -> Found local-only image. Proceeding."
  fi
}

# --- Print Final Images and Pull Dependencies ---
echo "--- Using the following images ---"
echo "Scheduler Image:     ${EPP_IMAGE}"
echo "Simulator Image:     ${VLLM_SIMULATOR_IMAGE}"
echo "Sidecar Image:       ${ROUTING_SIDECAR_IMAGE}"
echo "----------------------------------------------------"

echo "Pulling dependencies..."
ensure_image "${EPP_IMAGE}"
ensure_image "${VLLM_SIMULATOR_IMAGE}"
ensure_image "${ROUTING_SIDECAR_IMAGE}"
echo "Successfully pulled dependencies"
