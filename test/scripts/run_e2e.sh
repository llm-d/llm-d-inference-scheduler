#!/bin/bash

set -euo pipefail

cleanup() {
    echo "Interrupted! Cleaning up kind cluster..."
    if [ "${E2E_KEEP_CLUSTER_ON_FAILURE:-false}" = "true" ]; then
        echo "Keeping kind cluster 'e2e-tests' (E2E_KEEP_CLUSTER_ON_FAILURE=true)"
    else
        kind delete cluster --name e2e-tests 2>/dev/null || true
    fi
    exit 130  # SIGINT (Ctrl+C)
}

# Set trap only for interruption signals
# Normally kind cluster cleanup is done by AfterSuite
trap cleanup INT TERM

echo "Running end to end tests"

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
go test -v -timeout 20m ${DIR}/../e2e/ -ginkgo.v
