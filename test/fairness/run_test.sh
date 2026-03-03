#!/usr/bin/env bash
#
# Fairness A/B Test Orchestrator
#
# Runs baseline (no fairness) and program-aware (EWMA fairness) load tests,
# then analyzes the comparison.
#
# Prerequisites:
#   - vLLM + EPP already deployed (via make env-dev-kubernetes or similar)
#   - kubectl port-forward to gateway on $GATEWAY_PORT (default 8080)
#   - kubectl port-forward to EPP metrics on $METRICS_PORT (default 9090)
#   - Python 3.9+ with aiohttp, numpy, matplotlib
#
# Usage:
#   ./run_test.sh                     # Use defaults
#   GATEWAY_URL=http://10.0.0.5:8080 DURATION=300 ./run_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# --- Configuration (override via environment) ---
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
METRICS_URL="${METRICS_URL:-http://localhost:9090}"
MODEL="${MODEL:-Qwen/Qwen2-0.5B-Instruct}"
PROGRAMS="${PROGRAMS:-prog-heavy:10,prog-medium:3,prog-light:1}"
DURATION="${DURATION:-180}"
WARMUP="${WARMUP:-30}"
MAX_TOKENS="${MAX_TOKENS:-100}"
TIMEOUT="${TIMEOUT:-60}"
CONCURRENCY="${CONCURRENCY:-50}"
NAMESPACE="${NAMESPACE:-fairness-test}"

# EPP deployment name — derived from model name.
MODEL_SLUG="$(echo "${MODEL##*/}" | tr '[:upper:]' '[:lower:]' | tr ' /_.' '-')"
EPP_NAME="${EPP_NAME:-${MODEL_SLUG}-endpoint-picker}"

# Configs.
BASELINE_CONFIG="$REPO_DIR/deploy/config/sim-epp-config.yaml"
PA_CONFIG="$REPO_DIR/deploy/config/sim-program-aware-config.yaml"

# Output directories.
RESULTS_DIR="$SCRIPT_DIR/results"
BASELINE_DIR="$RESULTS_DIR/baseline"
PA_DIR="$RESULTS_DIR/program-aware"
COMPARISON_DIR="$RESULTS_DIR/comparison"

# --- Helpers ---
log() { echo "[$(date +%H:%M:%S)] $*"; }
die() { echo "ERROR: $*" >&2; exit 1; }

check_gateway() {
    log "Checking gateway at $GATEWAY_URL ..."
    if ! curl -sf --max-time 10 "$GATEWAY_URL/v1/completions" \
        -H 'Content-Type: application/json' \
        -d "{\"model\":\"$MODEL\",\"prompt\":\"hello\",\"max_tokens\":1,\"temperature\":0}" > /dev/null 2>&1; then
        die "Gateway not responding at $GATEWAY_URL. Ensure port-forward is active."
    fi
    log "Gateway OK."
}

snapshot_metrics() {
    local output="$1"
    log "Snapshotting metrics to $output ..."
    curl -sf --max-time 10 "$METRICS_URL/metrics" > "$output" 2>/dev/null || \
        log "WARNING: Could not fetch metrics from $METRICS_URL (is port-forward active?)"
}

switch_config() {
    local config_file="$1"
    local config_name="$2"
    log "Switching EPP config to $config_name ..."

    # Update ConfigMap.
    kubectl -n "$NAMESPACE" create configmap epp-config \
        --from-file=epp-config.yaml="$config_file" \
        --dry-run=client -o yaml | kubectl apply -f -

    # Restart EPP.
    kubectl -n "$NAMESPACE" rollout restart deployment/"$EPP_NAME"
    kubectl -n "$NAMESPACE" rollout status deployment/"$EPP_NAME" --timeout=120s

    # Wait for EPP to be ready.
    log "Waiting 10s for EPP to stabilize ..."
    sleep 10

    # Verify gateway still responds after config switch.
    check_gateway
    log "Config switched to $config_name."
}

run_loadgen() {
    local output_file="$1"
    local label="$2"

    log "Starting $label load test (warmup=${WARMUP}s, duration=${DURATION}s) ..."
    python3 "$SCRIPT_DIR/fairness_loadgen.py" \
        --gateway-url "$GATEWAY_URL" \
        --model "$MODEL" \
        --programs "$PROGRAMS" \
        --duration "$DURATION" \
        --warmup "$WARMUP" \
        --max-tokens "$MAX_TOKENS" \
        --timeout "$TIMEOUT" \
        --concurrency "$CONCURRENCY" \
        --output "$output_file"
    log "$label load test complete."
}

# --- Main ---
main() {
    log "=== Fairness A/B Test ==="
    log "Gateway:  $GATEWAY_URL"
    log "Model:    $MODEL"
    log "Programs: $PROGRAMS"
    log "Duration: ${WARMUP}s warmup + ${DURATION}s measurement"
    log ""

    # Create output directories.
    mkdir -p "$BASELINE_DIR" "$PA_DIR" "$COMPARISON_DIR"

    # Pre-flight check.
    check_gateway

    # --- Phase 1: Baseline ---
    log ""
    log "========================================="
    log "  PHASE 1: BASELINE (No Fairness)"
    log "========================================="
    switch_config "$BASELINE_CONFIG" "baseline (sim-epp-config)"
    run_loadgen "$BASELINE_DIR/results.jsonl" "Baseline"
    snapshot_metrics "$BASELINE_DIR/metrics_final.txt"

    # Brief pause between tests.
    log "Pausing 15s between tests ..."
    sleep 15

    # --- Phase 2: Program-Aware ---
    log ""
    log "========================================="
    log "  PHASE 2: PROGRAM-AWARE FAIRNESS"
    log "========================================="
    switch_config "$PA_CONFIG" "program-aware (sim-program-aware-config)"

    # Verify plugin loaded.
    log "Verifying program-aware plugin loaded ..."
    kubectl -n "$NAMESPACE" logs deployment/"$EPP_NAME" -c epp --tail=30 | grep -i "program-aware" || \
        log "WARNING: Could not confirm program-aware plugin in logs."

    run_loadgen "$PA_DIR/results.jsonl" "Program-Aware"
    snapshot_metrics "$PA_DIR/metrics_final.txt"

    # --- Phase 3: Analysis ---
    log ""
    log "========================================="
    log "  PHASE 3: ANALYSIS"
    log "========================================="
    python3 "$SCRIPT_DIR/analyze_results.py" \
        --baseline "$BASELINE_DIR/results.jsonl" \
        --program-aware "$PA_DIR/results.jsonl" \
        --output "$COMPARISON_DIR/"

    log ""
    log "=== Test complete! ==="
    log "Results:    $RESULTS_DIR"
    log "Baseline:   $BASELINE_DIR/results.jsonl"
    log "Prog-Aware: $PA_DIR/results.jsonl"
    log "Comparison: $COMPARISON_DIR/"
}

main "$@"
