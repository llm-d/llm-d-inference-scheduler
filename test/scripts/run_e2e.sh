#!/bin/bash

set -euo pipefail

cleanup() {
    echo "Interrupted! Cleaning up kind cluster..."
    kind delete cluster --name e2e-tests 2>/dev/null || true
    exit 130  # SIGINT (Ctrl+C)
}

# Set trap only for interruption signals
# Normally kind cluster cleanup is done by AfterSuite
trap cleanup INT TERM

echo "Running end to end tests"

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
go test -v ${DIR}/../e2e/ -ginkgo.v
