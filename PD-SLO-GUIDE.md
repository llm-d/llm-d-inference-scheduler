# PD-SLO Scheduling

SLO-aware scheduling for Prefill-Decode disaggregated architecture with dual latency predictors.

## Overview

Unified profile handler supporting both SLO-aware and threshold-based PD scheduling:

**With SLO headers** (`x-slo-ttft-ms`, `x-slo-tpot-ms`):
- Joint optimization of (prefill, decode) pod pairs using two predictors
- `jointTTFT = prefillTTFT + decodeTTFT + transferOverhead`
- Blended headroom: `0.8×TTFT_headroom + 0.2×TPOT_headroom`

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
   PD-SLO Pair Optimizer    Use best pod from
   - Evaluate N×M pairs      each profile
   - Call 2 predictors       (fallback)
   - Blended headroom
   - Weighted random
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
| PdSLOPairOptimizer | `pkg/plugins/scorer/pd_slo_pair_optimizer.go` | Evaluates all (prefill, decode) pairs, selects optimal |
| Helpers | `pkg/plugins/scorer/pd_slo_helpers.go` | SLO parsing, weighted selection, cycle state utilities |

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

  # PD-SLO Pair Optimizer
  - type: pd-slo-pair-optimizer
    name: pd-slo-pair-optimizer
    parameters:
      ttftWeight: 0.8                # Weight for TTFT headroom
      tpotWeight: 0.2                # Weight for TPOT headroom
      transferOverheadMs: 5.0        # KV transfer overhead
      sloBufferFactor: 0.9           # Safety margin (0.9 = 10% buffer)
      selectionStrategy: "weighted_random"
      negativeHeadroomProb: 0.01     # Exploration rate

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
      transferOverheadMs: 5.0
      pdThreshold: 5                 # Skip prefill if < 5 non-cached tokens

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

### Key Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `pdThreshold` | 5 | Non-cached tokens threshold for decode-only vs PD (0 = always PD) |
| `ttftWeight` | 0.8 | TTFT weight in blended headroom |
| `tpotWeight` | 0.2 | TPOT weight in blended headroom |
| `transferOverheadMs` | 5.0 | KV transfer overhead (tune for network) |
| `sloBufferFactor` | 0.9 | Safety margin (0.9 = aim for 90% of SLO) |
| `negativeHeadroomProb` | 0.01 | Exploration rate for negative headroom pairs |

## Behavior

**Example 1: Short request (threshold blocks PD)**
- Input: 50 tokens, 80% prefix cache hit, SLO headers present
- Non-cached suffix: 50 × (1 - 0.8) = 10 tokens
- `pdThreshold = 5`: **Decode-only** (10 > 5 but PD overhead > benefit)
- SLO headers ignored, no pair optimization

**Example 2: Long request with SLO (joint optimization)**
- Input: 500 tokens, 20% prefix cache hit, SLO headers present
- Non-cached suffix: 500 × (1 - 0.2) = 400 tokens
- `pdThreshold = 5`: **PD-SLO optimization** (400 >> 5, SLO present)
- Evaluates all (prefill, decode) pairs, selects optimal via blended headroom

**Example 3: Long request without SLO (fallback)**
- Input: 500 tokens, 20% prefix cache hit, no SLO headers
- Non-cached suffix: 400 tokens
- `pdThreshold = 5`: **Regular PD scheduling** (400 >> 5, no SLO)
- Uses best pod from each profile (scored by prefix-cache-scorer)

## Metrics

```prometheus
# Pair selection outcomes
llm_d_inference_scheduler_pd_slo_pair_selections_total{outcome="positive_headroom|negative_headroom"}

# Predictor calls
llm_d_inference_scheduler_pd_slo_predictor_calls_total{predictor="prefill|decode", status="success|error"}

# Total pairs evaluated
llm_d_inference_scheduler_pd_slo_pairs_evaluated_total
```

## Troubleshooting

| Issue | Check | Solution |
|-------|-------|----------|
| No SLO routing | Headers present? | Add `x-slo-ttft-ms` and `x-slo-tpot-ms` |
| Predictor errors | Sidecars running? | Check `kubectl get pods -n llm-d`, verify 5 containers (1 EPP + 4 sidecars) |
| All decode-only | `pdThreshold` too high? | Reduce to 5 or 0 (always PD) |
| All negative headroom | SLOs too strict? | Relax targets or scale up pods |
| Fallback used | Pair optimizer skipped? | Verify both profiles ran, check EPP logs for "PD-SLO optimization" |

## Status

**Current**: Uses fallback (best pod from each profile). Pair optimizer skipped due to sequential profile execution.
**Future**: Fix joint optimization or replace with heuristic-based selection if fallback proves sufficient.
