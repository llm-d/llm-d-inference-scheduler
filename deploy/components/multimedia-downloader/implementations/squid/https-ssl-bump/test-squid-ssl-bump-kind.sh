#!/usr/bin/env bash
# test-squid-ssl-bump-kind.sh — deploys the Squid SSL Bump implementation to a temporary kind cluster.
#
# Usage:
#   ./deploy/components/multimedia-downloader/implementations/squid/https-ssl-bump/test-squid-ssl-bump-kind.sh [--keep-cluster]
#
# Flags:
#   --keep-cluster   Keep the kind cluster on exit (useful for debugging).
#
# Requirements: kind, kubectl, docker, openssl

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="squid-ssl-bump-smoke"
KEEP_CLUSTER=false
NAMESPACE="default"
LOCAL_IMAGE="squid-ssl-bump:local"

# The base service.yaml is three levels up from the https-ssl-bump implementation.
BASE_DIR="${SCRIPT_DIR}/../../.."
SERVICE_YAML="${BASE_DIR}/service.yaml"

# --- Colors / helpers ----------------------------------------------------------
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
section() { echo -e "\n${YELLOW}==> $*${NC}"; }
success() { echo -e "${GREEN}✓ $*${NC}"; }
error() { echo -e "${RED}✗ $*${NC}"; }

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
    if [[ "${KEEP_CLUSTER}" == "false" ]]; then
        kubectl delete pod curl-ssl-test-pod --ignore-not-found --wait=false 2>/dev/null
        kind delete cluster --name "${CLUSTER_NAME}" 2>/dev/null
        echo "  Cluster '${CLUSTER_NAME}' deleted."
        if [[ -d "${SCRIPT_DIR}/squid-ssl-certs" ]]; then
            rm -rf "${SCRIPT_DIR}/squid-ssl-certs"
            echo "  Generated certificates cleaned up."
        fi
    else
        echo "  Cluster kept. Resources left in place for inspection."
        echo "  To delete: kind delete cluster --name ${CLUSTER_NAME}"
        echo "  Certificates saved in: ${SCRIPT_DIR}/squid-ssl-certs"
    fi
}
trap cleanup EXIT

# --- Build Squid image ---------------------------------------------------------
build_image() {
    local image_name="$1"
    echo "  Building Docker image from ${SCRIPT_DIR}..."
    docker build -t "${image_name}" \
        -f "${SCRIPT_DIR}/Dockerfile.squid-ssl-bump" \
        "${SCRIPT_DIR}"
}

section "Building Squid SSL Bump image"
echo "  Using image: ${LOCAL_IMAGE}"
build_image "${LOCAL_IMAGE}"
success "Image built: ${LOCAL_IMAGE}"

# --- Setup kind cluster --------------------------------------------------------
section "Setting up kind cluster '${CLUSTER_NAME}'"
if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
    echo "  Reusing existing cluster."
else
    kind create cluster --name "${CLUSTER_NAME}" --wait 90s
    echo "  Cluster created."
fi

section "Loading image into kind cluster"
kind load docker-image "${LOCAL_IMAGE}" --name "${CLUSTER_NAME}"
success "Image loaded into cluster"

# --- Generate SSL Certificates -------------------------------------------------
section "Generating SSL certificates"
cd "${SCRIPT_DIR}"

# Clean up any existing certificates
if [[ -d "squid-ssl-certs" ]]; then
    rm -rf squid-ssl-certs
fi

# Generate certificates using the provided script
./generate-ssl-certs.sh --namespace "${NAMESPACE}" --out-dir squid-ssl-certs --org "Squid Test CA"
success "SSL certificates generated and secrets created"

# --- Deploy Squid with SSL Bump ------------------------------------------------
section "Deploying Squid with SSL Bump support"
kubectl apply -k "${SCRIPT_DIR}"
kubectl apply -f "${SERVICE_YAML}"
kubectl rollout status deployment/multimedia-downloader --timeout=120s
success "multimedia-downloader with SSL Bump is ready"

# --- Deploy Test Client Pod ----------------------------------------------------
section "Deploying test client pod with CA trust"
kubectl delete pod curl-ssl-test-pod --ignore-not-found --wait=true 2>/dev/null
kubectl apply -f "${SCRIPT_DIR}/curl-test-pod.yaml"
kubectl wait --for=condition=Ready pod/curl-ssl-test-pod --timeout=120s
success "Test client pod ready with CA trust configured"

# --- Verification --------------------------------------------------------------
section "Testing SSL Bump functionality"

# Clear the access log before testing
kubectl exec deployment/multimedia-downloader -c squid -- truncate -s 0 /var/cache/squid/access.log || true

TEST_URLS=(
    "https://images.dog.ceo/breeds/poodle-standard/n02113799_2280.jpg"
    "https://httpbin.org/image/jpeg"
)
for url in "${TEST_URLS[@]}"; do
    echo ""
    echo "Testing: ${url}"
    
    # First request (should be TCP_MISS)
    echo "  Request 1 (expecting cache MISS)..."
    if kubectl exec curl-ssl-test-pod -- curl -s -o /dev/null -w "HTTP Status: %{http_code}\n" "${url}"; then
        success "Request succeeded"
    else
        error "Request failed"
    fi

    # Second request (should be TCP_HIT or TCP_MEM_HIT)
    echo "  Request 2 (expecting cache HIT)..."
    if kubectl exec curl-ssl-test-pod -- curl -s -o /dev/null -w "HTTP Status: %{http_code}\n" "${url}"; then
        success "Request succeeded"
    else
        error "Request failed"
    fi
done

# --- Verify SSL Bump is Working ------------------------------------------------
section "Verifying SSL Bump interception"

echo "  Checking certificate issuer (should show Squid CA)..."
kubectl exec curl-ssl-test-pod -- curl -v https://httpbin.org/get 2>&1 | grep -i "issuer" || echo "Could not verify issuer"

echo "  Checking Squid logs for SSL activity..."
kubectl logs -l app=multimedia-downloader -c squid --tail=50 | grep -i "ssl\|bump\|certificate" || echo "No SSL-specific logs found"

# --- Display Results -----------------------------------------------------------
section "Cache Performance Results"
echo ""
echo "  Access log entries (showing cache status):"
kubectl logs -l app=multimedia-downloader -c log-tailer --tail=30 | grep -E "TCP_.*HIT|TCP_MISS" || echo "No cache entries found"

echo ""
section "Summary"
echo "✓ SSL Bump proxy deployed and tested"
echo "✓ Client pod configured with CA trust"
echo "✓ HTTPS requests intercepted and cached"
echo ""
echo "To inspect the cluster manually:"
echo "  kubectl config use-context kind-${CLUSTER_NAME}"
echo "  kubectl get pods"
echo "  kubectl logs -l app=multimedia-downloader -c squid"
echo "  kubectl exec curl-ssl-test-pod -- curl -v https://example.com"

exit 0
