# PD-SLO Scheduling

SLO-aware scheduling for Prefill-Decode disaggregated architecture with dual latency predictors.

## Overview

Unified profile handler supporting both SLO-aware and threshold-based PD scheduling:

**With SLO headers** (`x-slo-ttft-ms`, `x-slo-tpot-ms`):
- Independent optimization of prefill and decode pods using trained predictors
- Prefill: Select pod with best TTFT headroom (SLO - predictedTTFT)
- Decode: Select pod with best combined TTFT+TPOT headroom

**Without SLO headers**:
- Threshold-based PD scheduling (prefix cache hit percentage)
- Uses best pod from each profile (scored by prefix-cache-scorer)

## Architecture

### Dual Predictor Design

Two independent predictor services (or heuristics for MVP):

| Predictor | Predicts | Key Features | Environment Vars |
|-----------|----------|--------------|------------------|
| **Prefill** | TTFT for prompt processing | input_tokens (dominant), prefix_cache_score, pod_load | `PREFILL_TRAINING_URL`, `PREFILL_PREDICTION_URL` |
| **Decode** | TTFT + TPOT for token generation | queue_depth (dominant), running_requests, kv_cache_usage | `DECODE_TRAINING_URL`, `DECODE_PREDICTION_URL` |

### Request Flow

```
Request → PD-SLO Profile Handler
           ↓
     Run decode profile (get decode candidates)
           ↓
     Threshold check: non-cached suffix < pdThreshold?
           ↓
         YES → Decode-only (skip prefill)
           ↓
          NO → Run prefill profile (get prefill candidates)
           ↓
     Has SLO headers?
           ↓                    ↓
         YES                  NO
           ↓                    ↓
   PD-SLO Optimizer         Use best pod from
   - Score prefill pods      each profile
     independently           (fallback)
   - Score decode pods
     independently
   - Use trained predictors
           ↓                    ↓
     Set x-prefiller-host-port header
           ↓
     Route to decode pod
```

## Components

| Component | File | Purpose |
|-----------|------|---------|
| PDPredictorSet | `pkg/predictors/pd_predictors.go` | Two predictor clients (prefill + decode) |
| PdSLOProfileHandler | `pkg/plugins/profile/pd_slo_profile_handler.go` | Coordinates dual-profile execution, threshold logic |
| PdSLOOptimizer | `pkg/plugins/scorer/pd_slo_optimizer.go` | Scores prefill/decode pods independently using trained predictors |
| Helpers | `pkg/plugins/scorer/pd_slo_helpers.go` | SLO parsing, telemetry tracking, pod filtering utilities |

## Configuration

### Environment Variables (Auto-injected by GAIE chart)

```bash
PREFILL_TRAINING_URL=http://localhost:8000
PREFILL_PREDICTION_URL=http://localhost:8001
DECODE_TRAINING_URL=http://localhost:8010
DECODE_PREDICTION_URL=http://localhost:8011
```

### EPP Plugin Configuration

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig

plugins:
  # Filters
  - type: prefill-filter
    name: prefill-filter
  - type: decode-filter
    name: decode-filter

  # Scorers
  - type: prefix-cache-scorer
    name: prefix-cache-scorer
    parameters:
      hashBlockSize: 5
      maxPrefixBlocksToMatch: 256
      lruCapacityPerServer: 31250

  # PD-SLO Optimizer (independent scoring)
  - type: pd-slo-optimizer
    name: pd-slo-optimizer
    parameters:
      sloBufferFactor: 0.9           # Safety margin (0.9 = 10% buffer)

  # Picker
  - type: max-score-picker
    name: max-score-picker

  # PD-SLO Profile Handler
  - type: pd-slo-profile-handler
    name: pd-slo-profile-handler
    parameters:
      decodeProfile: "decode"
      prefillProfile: "prefill"
      prefixPluginName: "prefix-cache-scorer"
      hashBlockSize: 5
      pdThreshold: 5                 # Skip prefill if < 5 non-cached tokens

schedulingProfiles:
  - name: prefill
    plugins:
      - pluginRef: prefill-filter
      - pluginRef: prefix-cache-scorer
        weight: 2
      - pluginRef: pd-slo-optimizer
        weight: 3
      - pluginRef: max-score-picker

  - name: decode
    plugins:
      - pluginRef: decode-filter
      - pluginRef: prefix-cache-scorer
        weight: 2
      - pluginRef: pd-slo-optimizer
        weight: 3
      - pluginRef: max-score-picker
```

### Key Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `pdThreshold` | 5 | Non-cached tokens threshold for decode-only vs PD (0 = always PD) |
| `sloBufferFactor` | 0.9 | Safety margin (0.9 = aim for 90% of SLO) |

## Behavior

**Example 1: Short request (threshold blocks PD)**
- Input: 50 tokens, 80% prefix cache hit, SLO headers present
- Non-cached suffix: 50 × (1 - 0.8) = 10 tokens
- `pdThreshold = 5`: **Decode-only** (10 > 5 but PD overhead > benefit)
- SLO headers ignored, no independent optimization

**Example 2: Long request with SLO (independent optimization)**
- Input: 500 tokens, 20% prefix cache hit, SLO headers present
- Non-cached suffix: 500 × (1 - 0.2) = 400 tokens
- `pdThreshold = 5`: **PD-SLO optimization** (400 >> 5, SLO present)
- Prefill pods scored independently by prefill predictor (TTFT headroom)
- Decode pods scored independently by decode predictor (TTFT+TPOT headroom)

**Example 3: Long request without SLO (fallback)**
- Input: 500 tokens, 20% prefix cache hit, no SLO headers
- Non-cached suffix: 400 tokens
- `pdThreshold = 5`: **Regular PD scheduling** (400 >> 5, no SLO)
- Uses best pod from each profile (scored by prefix-cache-scorer)

## Metrics

```prometheus
# PD disaggregation decisions
llm_d_inference_scheduler_pd_decision_total{decision_type="decode-only|prefill-decode"}

# Pod selection outcomes (headroom-based)
llm_d_inference_scheduler_pd_slo_pod_selections_total{pod_type="prefill|decode", outcome="positive_headroom|negative_headroom"}

# Actual headroom distribution (histogram in seconds)
# Positive values = pod can meet SLO, negative values = SLO violation
llm_d_inference_scheduler_pd_slo_headroom_seconds{pod_type="prefill|decode", metric_type="ttft|tpot"}

# Predictor calls
llm_d_inference_scheduler_pd_slo_predictor_calls_total{predictor="prefill|decode", status="success|error"}

# Telemetry collection
llm_d_inference_scheduler_pd_slo_telemetry_recorded_total{pod_type="prefill|decode"}
```

**Example Queries:**
```promql
# SLO violation rate (negative headroom) for decode pods
rate(llm_d_inference_scheduler_pd_slo_pod_selections_total{pod_type="decode", outcome="negative_headroom"}[5m])
/ rate(llm_d_inference_scheduler_pd_slo_pod_selections_total{pod_type="decode"}[5m])

# Median TTFT headroom for decode pods (how much headroom do we typically have?)
histogram_quantile(0.5, rate(llm_d_inference_scheduler_pd_slo_headroom_seconds_bucket{pod_type="decode", metric_type="ttft"}[5m]))

# P90 TPOT headroom (90% of decode pods have this much headroom or better)
histogram_quantile(0.9, rate(llm_d_inference_scheduler_pd_slo_headroom_seconds_bucket{pod_type="decode", metric_type="tpot"}[5m]))

# Percent of prefill selections with negative headroom (violating SLO)
sum(rate(llm_d_inference_scheduler_pd_slo_headroom_seconds_bucket{pod_type="prefill", metric_type="ttft", le="0"}[5m]))
/ sum(rate(llm_d_inference_scheduler_pd_slo_headroom_seconds_count{pod_type="prefill", metric_type="ttft"}[5m]))

# Predictor error rate
rate(llm_d_inference_scheduler_pd_slo_predictor_calls_total{status="error"}[5m])
/ rate(llm_d_inference_scheduler_pd_slo_predictor_calls_total[5m])

# Telemetry collection rate
rate(llm_d_inference_scheduler_pd_slo_telemetry_recorded_total[1m])
```

**Interpreting Headroom Metrics:**
- **Positive headroom** (e.g., +0.05s = +50ms): Pod has 50ms of headroom, can meet SLO comfortably
- **Zero headroom** (0s): Pod exactly meets SLO, no margin for error
- **Negative headroom** (e.g., -0.1s = -100ms): Pod will violate SLO by 100ms
- **Use P50/P90 to tune `sloBufferFactor`**: If P90 headroom is very negative, increase buffer; if very positive, decrease buffer

## Troubleshooting

| Issue | Check | Solution |
|-------|-------|----------|
| No SLO routing | Headers present? | Add `x-slo-ttft-ms` and `x-slo-tpot-ms` |
| Predictor errors | Sidecars running? | Check `kubectl get pods -n llm-d`, verify 5 containers (1 EPP + 4 sidecars) |
| All decode-only | `pdThreshold` too high? | Reduce to 5 or 0 (always PD) |
| All negative headroom | SLOs too strict? | Check metrics: `pd_slo_pod_selections_total{outcome="negative_headroom"}`, relax targets or scale up pods |
| Optimizer not scoring | Plugin configured? | Verify both profiles include `pd-slo-optimizer`, check EPP logs for "Decode-only scoring mode" or "Prefill-only scoring mode" |
| No telemetry collected | Requestcontrol hooks working? | Check EPP logs for "Received prefill TTFT from sidecar" and "Sent prefill/decode TTFT telemetry", verify metric: `pd_slo_telemetry_recorded_total` |
| High predictor errors | Models not loading? | Check metric: `pd_slo_predictor_calls_total{status="error"}`, verify training/prediction server logs |

## Status

**Current**: Independent optimization mode - prefill and decode pods are scored separately using trained latency predictors. Joint optimization is not implemented.
