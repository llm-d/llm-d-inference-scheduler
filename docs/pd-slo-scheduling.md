# SLO-Aware PD Disaggregated Scheduling

## Overview

This feature provides a **unified profile handler** that supports both SLO-aware and threshold-based Prefill-Decode (PD) disaggregated scheduling.

**Decision Flow**:
1. **Threshold check** (applies to ALL requests):
   - If non-cached suffix < `pdThreshold` → decode-only (skip prefill)
   - Otherwise → proceed to PD disaggregation

2. **PD Optimization Strategy** (for requests needing PD):
   - **With SLO headers** (`x-slo-ttft-ms`, `x-slo-tpot-ms`):
     - Joint optimization of (prefill, decode) pod pairs
     - **TTFT** = prefill_processing_time + decode_queue_wait + decode_startup + transfer_overhead
     - **TPOT** = decode_per_token_time (decode pod only)
     - Selects optimal pod pairs using blended headroom scoring: `0.8 × TTFT_headroom + 0.2 × TPOT_headroom`

   - **Without SLO headers**:
     - Uses pods selected by profiles directly
     - Compatible with existing non-SLO workloads

**Key Insight**: The threshold check ensures that short requests (where PD overhead exceeds benefit) always use decode-only, regardless of SLO headers.

## Architecture

### Request Flow

```
┌─────────────────────────────────────────────────────────────────┐
│  Client Request                                                  │
│  Headers: x-slo-ttft-ms: 500, x-slo-tpot-ms: 20                │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│  PD-SLO Profile Handler                                          │
│  ├─ Detect SLO headers                                          │
│  ├─ Run decode profile → decode pod candidates                  │
│  ├─ Run prefill profile → prefill pod candidates                │
│  └─ Coordinate joint optimization                               │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│  PD-SLO Pair Optimizer Scorer                                   │
│  For each (prefill, decode) pair:                               │
│    ├─ Call prefill-ttft-predictor → 320ms                       │
│    ├─ Call decode-ttft-predictor → 60ms                         │
│    ├─ Call decode-tpot-predictor → 15ms                         │
│    ├─ Calculate: joint_TTFT = 320 + 60 + 5 = 385ms             │
│    ├─ TTFT headroom = 500 - 385 = 115ms ✓                      │
│    ├─ TPOT headroom = 20 - 15 = 5ms ✓                          │
│    └─ Blended headroom = 0.8×115 + 0.2×5 = 93ms                │
│                                                                  │
│  Select: Weighted random (higher headroom = higher probability) │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│  Scheduling Result                                               │
│  ├─ Selected prefill pod: prefill-pod-2                         │
│  ├─ Selected decode pod: decode-pod-1                           │
│  └─ Set header: x-prefiller-host-port: prefill-pod-2:8000      │
└─────────────────────────────────────────────────────────────────┘
```

### Predictor Architecture

Three separate predictor services provide latency predictions:

```
┌──────────────────────────┐
│ Prefill TTFT Predictor   │  Predicts: Prefill processing time
│ Features:                │  - input_token_length (dominant)
│ - Input tokens           │  - kv_cache_percentage
│ - Prefix cache score     │  - num_requests_waiting
│ - Pod load               │  - num_requests_running
└──────────────────────────┘

┌──────────────────────────┐
│ Decode TTFT Predictor    │  Predicts: Queue wait + startup overhead
│ Features:                │  - num_requests_waiting (dominant)
│ - Queue depth            │  - num_requests_running (dominant)
│ - Pod load               │  - kv_cache_percentage
└──────────────────────────┘

┌──────────────────────────┐
│ Decode TPOT Predictor    │  Predicts: Per-token latency
│ Features:                │  - num_requests_running (dominant)
│ - Running requests       │  - kv_cache_percentage (dominant)
│ - KV cache usage         │  - output_token_length
└──────────────────────────┘
```

## Components

### New Files Created

1. **`pkg/predictors/pd_predictors.go`**
   - `PDPredictorSet`: Manages 3 predictor client instances
   - Environment-based configuration
   - Lifecycle management (start/stop)

2. **`pkg/plugins/scorer/pd_slo_pair_optimizer.go`**
   - Core scoring logic for (prefill, decode) pairs
   - Cartesian product evaluation (O(N×M) complexity)
   - Weighted random selection with exploration
   - Fallback heuristics when predictors unavailable

3. **`pkg/plugins/scorer/pd_slo_helpers.go`**
   - SLO header parsing
   - Pod role filtering
   - Pair classification (positive/negative headroom)
   - Cycle state utilities

4. **`pkg/plugins/profile/pd_slo_profile_handler.go`**
   - Profile coordination for SLO-aware PD scheduling
   - SLO header detection
   - Sets `x-prefiller-host-port` header

5. **`pkg/metrics/metrics.go`** (modified)
   - `PDSLOPairSelectionsTotal`: Selection outcomes
   - `PDSLOPredictorCallsTotal`: Predictor call statistics
   - `PDSLOPairsEvaluatedTotal`: Pairs evaluated per request

6. **`deploy/config/pd-slo-epp-config.yaml`**
   - Complete EPP configuration example
   - Documented parameters and environment variables

### Files Modified

- **`pkg/plugins/register.go`**: Added plugin registrations
- **`pkg/metrics/metrics.go`**: Added PD-SLO metrics

## Configuration

### EPP Configuration

Apply the PD-SLO configuration:

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig

plugins:
  - type: pd-slo-pair-optimizer
    name: pd-slo-pair-optimizer
    parameters:
      ttftWeight: 0.8              # Weight for TTFT in blended score
      tpotWeight: 0.2              # Weight for TPOT in blended score
      transferOverheadMs: 5.0      # KV transfer overhead (adjust for your network)
      sloBufferFactor: 0.9         # Safety margin (0.9 = 10% buffer)
      selectionStrategy: "weighted_random"
      negativeHeadroomProb: 0.01   # Exploration rate

profileHandler:
  type: pd-slo-profile-handler
  name: pd-slo-profile-handler
  parameters:
    decodeProfile: "decode"
    prefillProfile: "prefill"
    transferOverheadMs: 5.0

schedulingProfiles:
  - name: prefill
    plugins:
      - pluginRef: prefill-filter
      - pluginRef: prefix-cache-scorer
        weight: 2
      - pluginRef: pd-slo-pair-optimizer
        weight: 3
      - pluginRef: max-score-picker

  - name: decode
    plugins:
      - pluginRef: decode-filter
      - pluginRef: prefix-cache-scorer
        weight: 2
      - pluginRef: pd-slo-pair-optimizer
        weight: 3
      - pluginRef: max-score-picker
```

See full configuration: `deploy/config/pd-slo-epp-config.yaml`

### Environment Variables

Set these environment variables for the EPP deployment:

```bash
# Prefill TTFT Predictor
PREFILL_TTFT_TRAINING_URL=http://prefill-ttft-predictor-svc:8000
PREFILL_TTFT_PREDICTION_URL=http://prefill-ttft-predictor-svc:8001

# Decode TTFT Predictor (queue wait + startup)
DECODE_TTFT_TRAINING_URL=http://decode-ttft-predictor-svc:8000
DECODE_TTFT_PREDICTION_URL=http://decode-ttft-predictor-svc:8001

# Decode TPOT Predictor
DECODE_TPOT_TRAINING_URL=http://decode-tpot-predictor-svc:8000
DECODE_TPOT_PREDICTION_URL=http://decode-tpot-predictor-svc:8001
```

## Usage

### Request Types

**SLO-Aware Requests** (with SLO headers):

```bash
curl -X POST http://gateway-endpoint/v1/completions \
  -H "Content-Type: application/json" \
  -H "x-slo-ttft-ms: 500" \
  -H "x-slo-tpot-ms: 20" \
  -d '{
    "model": "llama-3-8b",
    "prompt": "Explain quantum computing",
    "max_tokens": 100
  }'
```

**Regular Requests** (without SLO headers):

```bash
curl -X POST http://gateway-endpoint/v1/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama-3-8b",
    "prompt": "Explain quantum computing",
    "max_tokens": 100
  }'
```

**SLO Headers**:
- `x-slo-ttft-ms`: Target time-to-first-token in milliseconds
- `x-slo-tpot-ms`: Target time-per-output-token in milliseconds

**Behavior**:
- **With SLO headers**: Joint pair optimization (PD-SLO path)
- **Without SLO headers**: Threshold-based PD scheduling (fallback path)

### Example Scenarios

**Latency-sensitive workload** (prioritize TTFT):
```bash
-H "x-slo-ttft-ms: 200"   # Strict TTFT requirement
-H "x-slo-tpot-ms: 50"    # Relaxed TPOT
```

**Throughput-focused workload** (prioritize TPOT):
```bash
-H "x-slo-ttft-ms: 1000"  # Relaxed TTFT
-H "x-slo-tpot-ms: 10"    # Strict TPOT requirement
```

**Balanced workload**:
```bash
-H "x-slo-ttft-ms: 500"
-H "x-slo-tpot-ms: 20"
```

## Predictor Services

### MVP: Heuristic Predictors

For initial deployment, use simple heuristic-based predictors:

**Prefill TTFT Heuristic**:
```python
def predict_prefill_ttft(input_token_length, prefix_cache_score):
    effective_tokens = input_token_length * (1 - prefix_cache_score)
    return effective_tokens * 0.5  # ~0.5ms per token
```

**Decode TTFT Heuristic**:
```python
def predict_decode_ttft(num_requests_waiting):
    return num_requests_waiting * 10  # ~10ms per queued request
```

**Decode TPOT Heuristic**:
```python
def predict_decode_tpot(num_requests_running, kv_cache_percentage):
    base_tpot = 20.0  # ms
    congestion = num_requests_running * 5.0
    kv_penalty = kv_cache_percentage * 10.0
    return base_tpot + congestion + kv_penalty
```

### Production: ML-based Predictors

For production, deploy trained ML models (reference: `gateway-api-inference-extension/sidecars/latencypredictor/training_server.py`):

1. Collect training data (requires sidecar instrumentation - future work)
2. Train separate models (XGBoost/LightGBM) for each predictor
3. Deploy trained models to replace heuristics

## Metrics

Monitor PD-SLO scheduling via Prometheus metrics:

### Key Metrics

```prometheus
# Pair selection outcomes
llm_d_inference_scheduler_pd_slo_pair_selections_total{outcome="positive_headroom"}
llm_d_inference_scheduler_pd_slo_pair_selections_total{outcome="negative_headroom"}

# Predictor calls
llm_d_inference_scheduler_pd_slo_predictor_calls_total{predictor="prefill-ttft", status="success"}
llm_d_inference_scheduler_pd_slo_predictor_calls_total{predictor="decode-ttft", status="success"}
llm_d_inference_scheduler_pd_slo_predictor_calls_total{predictor="decode-tpot", status="success"}
llm_d_inference_scheduler_pd_slo_predictor_calls_total{predictor="*", status="error"}

# Pairs evaluated
llm_d_inference_scheduler_pd_slo_pairs_evaluated_total
```

### Monitoring Queries

**SLO attainment rate**:
```promql
rate(llm_d_inference_scheduler_pd_slo_pair_selections_total{outcome="positive_headroom"}[5m])
/
rate(llm_d_inference_scheduler_pd_slo_pair_selections_total[5m])
```

**Predictor error rate**:
```promql
rate(llm_d_inference_scheduler_pd_slo_predictor_calls_total{status="error"}[5m])
/
rate(llm_d_inference_scheduler_pd_slo_predictor_calls_total[5m])
```

**Average pairs evaluated per request**:
```promql
rate(llm_d_inference_scheduler_pd_slo_pairs_evaluated_total[5m])
/
rate(llm_d_inference_scheduler_pd_slo_pair_selections_total[5m])
```

## Algorithm Details

### Pair Evaluation

For each (prefill, decode) combination:

1. **Get Predictions**:
   - `prefill_ttft` = PrefillTTFTPredictor.Predict(prefill_pod, request)
   - `decode_ttft` = DecodeTTFTPredictor.Predict(decode_pod, request)
   - `decode_tpot` = DecodeTPOTPredictor.Predict(decode_pod, request)

2. **Calculate Joint TTFT**:
   ```
   joint_ttft = prefill_ttft + decode_ttft + transfer_overhead
   ```

3. **Calculate Headrooms**:
   ```
   ttft_headroom = (ttft_slo × buffer_factor) - joint_ttft
   tpot_headroom = (tpot_slo × buffer_factor) - decode_tpot
   ```

4. **Blended Score**:
   ```
   blended_headroom = 0.8 × ttft_headroom + 0.2 × tpot_headroom
   ```

5. **Classification**:
   - **Positive headroom**: Both `ttft_headroom > 0` AND `tpot_headroom > 0`
   - **Negative headroom**: Either violates SLO

### Pair Selection

**Weighted Random Strategy**:
- 99% probability: Select from positive headroom pairs
- 1% probability: Explore negative headroom pairs (for learning)

Within each group, selection probability is proportional to blended headroom (higher headroom = higher probability).

### Complexity

- **Time Complexity**: O(N × M) where N = prefill pods, M = decode pods
- **Typical Case**: N=3, M=3 → 9 pair evaluations (~10-20ms overhead)
- **Large Scale**: N=10, M=10 → 100 pairs (~50-100ms overhead)
- **Optimization**: Can prune to top-K candidates if needed (future enhancement)

## Troubleshooting

### No SLO-aware routing happening

**Symptom**: Requests not using PD-SLO optimization

**Check**:
1. SLO headers present in request:
   ```bash
   curl -v ... -H "x-slo-ttft-ms: 500" -H "x-slo-tpot-ms: 20"
   ```

2. EPP configuration uses `pd-slo-profile-handler`:
   ```bash
   kubectl get endpointpickerconfig -o yaml | grep pd-slo
   ```

3. Plugins registered:
   ```bash
   # Check EPP logs for plugin registration
   kubectl logs -l app=epp | grep "pd-slo"
   ```

### Predictor connection failures

**Symptom**: High error rate in `pd_slo_predictor_calls_total{status="error"}`

**Check**:
1. Environment variables set correctly:
   ```bash
   kubectl get deployment epp -o yaml | grep PREDICTOR
   ```

2. Predictor services running:
   ```bash
   kubectl get svc | grep predictor
   kubectl get pods | grep predictor
   ```

3. Network connectivity:
   ```bash
   kubectl exec -it epp-pod -- curl http://prefill-ttft-predictor-svc:8001/health
   ```

**Fallback**: Scheduler uses heuristics when predictors fail (graceful degradation)

### All pairs have negative headroom

**Symptom**: `pd_slo_pair_selections_total{outcome="negative_headroom"}` consistently high

**Likely causes**:
1. **SLOs too strict**: Relax SLO targets
2. **System overloaded**: Scale up pods
3. **Predictor accuracy**: Check predictor calibration
4. **Transfer overhead**: Adjust `transferOverheadMs` parameter

**Investigation**:
```bash
# Check actual latencies vs SLOs
kubectl logs -l app=epp | grep "Selected optimal PD pair"
```

### High scheduling latency

**Symptom**: Requests take long to schedule

**Check**:
1. Number of pod pairs being evaluated:
   ```promql
   avg(llm_d_inference_scheduler_pd_slo_pairs_evaluated_total)
   ```

2. Predictor latency:
   - Should be <10ms per prediction
   - Check predictor service performance

**Optimization**:
- Reduce number of pods (if N×M > 100)
- Implement top-K candidate pruning
- Use faster predictor models

## Performance Tuning

### SLO Buffer Factor

Adjust `sloBufferFactor` to trade off between SLO violations and utilization:

- **Conservative** (`0.8`): More headroom, fewer violations, lower utilization
- **Balanced** (`0.9`): Default, good trade-off
- **Aggressive** (`0.95`): Tighter packing, higher utilization, more violations

### TTFT/TPOT Weights

Adjust weights based on workload characteristics:

- **Latency-critical** (`ttftWeight: 0.9, tpotWeight: 0.1`): Prioritize first token
- **Balanced** (`ttftWeight: 0.8, tpotWeight: 0.2`): Default
- **Throughput-focused** (`ttftWeight: 0.6, tpotWeight: 0.4`): More weight on TPOT

### Transfer Overhead

Adjust based on your network:

- **RDMA/InfiniBand**: `transferOverheadMs: 1.0` (very fast)
- **10GbE**: `transferOverheadMs: 5.0` (default)
- **1GbE**: `transferOverheadMs: 10.0` (slower network)

Measure actual transfer time and adjust accordingly.

## Future Enhancements

### Post-MVP Features

1. **Sidecar Timing Instrumentation**
   - Add timing to `connector_nixlv2.go` to collect actual TTFT/TPOT
   - Enable training data collection for ML models

2. **ML Model Training**
   - Train XGBoost/LightGBM models on production data
   - Replace heuristic predictors with learned models
   - Continuous model updates

3. **Advanced Metrics**
   - Predicted vs actual latencies
   - Per-model SLO violation tracking
   - Headroom distribution histograms

4. **Affinity-Based Selection**
   - Track successful (prefill, decode) pairs
   - Prefer pairs that historically work well together
   - Epsilon-greedy exploration

5. **Dynamic Weight Learning**
   - Learn optimal TTFT/TPOT weights per model
   - Adaptive based on workload patterns

6. **Top-K Candidate Pruning**
   - Reduce O(N×M) to O(K²) for large pod counts
   - Pre-filter candidates by prefix cache score / queue depth

## References

- **Plan**: `/home/rsaini/.claude/plans/humming-fluttering-wilkes.md`
- **Configuration**: `deploy/config/pd-slo-epp-config.yaml`
- **Metrics**: `pkg/metrics/metrics.go`
- **Existing SLO Router**: `gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/slo_aware_router/`
- **Predictor Reference**: `gateway-api-inference-extension/sidecars/latencypredictorasync/`

## Support

For issues or questions:
1. Check logs: `kubectl logs -l app=epp | grep -i "pd-slo"`
2. Check metrics: `curl http://epp-service:9090/metrics | grep pd_slo`
3. Review configuration: `kubectl get endpointpickerconfig -o yaml`
4. File issue with logs and configuration details
