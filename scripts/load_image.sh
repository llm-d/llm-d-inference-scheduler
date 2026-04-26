#!/bin/bash

# Use the CONTAINER_RUNTIME from the environment, or default to docker if it's not set.
CONTAINER_RUNTIME="${CONTAINER_RUNTIME:-docker}"
echo "Using container tool: ${CONTAINER_RUNTIME}"

for IMAGE in $@; do
  if [[ "${CONTAINER_RUNTIME}" == "docker" ]]; then
    kind load docker-image $IMAGE --name ${KIND_CLUSTER_NAME}
  else
    "${CONTAINER_RUNTIME}" save --format=docker-archive "${IMAGE}" | kind --name "${KIND_CLUSTER_NAME}" load image-archive /dev/stdin
  fi
done
