#!/usr/bin/env bash
# test-squid-kind.sh — deploys the Squid implementation to a temporary kind cluster.
#
# Usage:
#   ./deploy/components/multimedia-downloader/implementations/squid/http/test-squid-kind.sh [--keep-cluster]
#
# Flags:
#   --keep-cluster   Keep the kind cluster on exit (useful for debugging).
#
# Requirements: kind, kubectl, docker

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="squid-smoke"
KEEP_CLUSTER=false

# The base service.yaml is three levels up from the squid/http/ subdirectory.
BASE_DIR="${SCRIPT_DIR}/../../.."
SERVICE_YAML="${BASE_DIR}/service.yaml"

# --- Colors / helpers ----------------------------------------------------------
YELLOW='\033[1;33m'; NC='\033[0m'
section() { echo -e "\n${YELLOW}==> $*${NC}"; }

# --- Argument parsing ----------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --keep-cluster) KEEP_CLUSTER=true; shift ;;
        --help|-h)
            sed -n '2,/^set /p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "ERROR: Unknown argument: $1" >&2; exit 1 ;;
    esac
done

# --- Cleanup -------------------------------------------------------------------
cleanup() {
    set +e
    section "Cleaning up"
    kubectl delete pod curl-test-pod --ignore-not-found --wait=false 2>/dev/null
    if [[ "${KEEP_CLUSTER}" == "false" ]]; then
        kind delete cluster --name "${CLUSTER_NAME}" 2>/dev/null
        echo "  Cluster '${CLUSTER_NAME}' deleted."
    else
        echo "  Cluster kept. Resources left in place for inspection."
        echo "  To delete: kind delete cluster --name ${CLUSTER_NAME}"
    fi
}
trap cleanup EXIT

# --- Setup kind cluster --------------------------------------------------------
section "Setting up kind cluster '${CLUSTER_NAME}'"
if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
    echo "  Reusing existing cluster."
else
    kind create cluster --name "${CLUSTER_NAME}" --wait 90s
    echo "  Cluster created."
fi
section "Deploying squid implementation from ${SCRIPT_DIR}"
kubectl apply -k "${SCRIPT_DIR}"
kubectl apply -f "${SERVICE_YAML}"
kubectl rollout status deployment/multimedia-downloader --timeout=120s
echo "  multimedia-downloader is ready."

# --- Verification ---
TEST_URL="http://images.cocodataset.org/val2017/000000039769.jpg"
PROXY="http://multimedia-downloader:80"

section "Creating test pod for curl commands"
kubectl run curl-test-pod --image=curlimages/curl:8.11.1 --restart=Never -- sleep 30

# Wait for pod to be ready
kubectl wait --for=condition=Ready pod/curl-test-pod --timeout=60s
echo "  Test pod ready."

section "Testing Cache (2 requests to verify TCP_MISS then TCP_HIT)"
for i in {1..2}; do
    echo "Request $i to $TEST_URL via $PROXY..."
    kubectl exec curl-test-pod -- curl -s -x "$PROXY" "$TEST_URL" -o /dev/null
    sleep 3
done

section "Results (Checking TCP_MISS/TCP_HIT)"
kubectl logs -l app=multimedia-downloader -c log-tailer --tail=20 | grep -E "TCP_.*_HIT|TCP_MISS" || echo "No logs found."

exit 0
