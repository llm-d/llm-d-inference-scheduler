# PD-SLO Scheduling Quick Start

This document provides a quick start guide for SLO-aware Prefill-Decode disaggregated scheduling.

📖 **Full Documentation**: [docs/pd-slo-scheduling.md](docs/pd-slo-scheduling.md)

## What is PD-SLO Scheduling?

A unified profile handler that supports **both** SLO-aware and threshold-based PD scheduling:

**With SLO headers** (`x-slo-ttft-ms`, `x-slo-tpot-ms`):
- Joint optimization of (prefill, decode) pod pairs
- TTFT = prefill_time + decode_queue_wait + transfer_overhead
- TPOT = decode_per_token_time (decode only)
- Selects optimal pair using blended headroom scoring

**Without SLO headers**:
- Falls back to threshold-based PD scheduling
- Uses prefix cache hit percentage to decide decode-only vs PD
- Compatible with existing non-SLO workloads

## Quick Start

### 1. Deploy Predictor Services

Deploy 2 predictor services (or use heuristic predictors for MVP):
- `prefill-predictor-svc:8000/8001` (predicts prefill TTFT)
- `decode-predictor-svc:8000/8001` (predicts decode TTFT + TPOT)

### 2. Set Environment Variables

```bash
export PREFILL_TRAINING_URL=http://prefill-predictor-svc:8000
export PREFILL_PREDICTION_URL=http://prefill-predictor-svc:8001
export DECODE_TRAINING_URL=http://decode-predictor-svc:8000
export DECODE_PREDICTION_URL=http://decode-predictor-svc:8001
```

### 3. Apply Configuration

```bash
kubectl apply -f deploy/config/pd-slo-epp-config.yaml
```

### 4. Send Request with SLO Headers

```bash
curl -X POST http://gateway/v1/completions \
  -H "Content-Type: application/json" \
  -H "x-slo-ttft-ms: 500" \
  -H "x-slo-tpot-ms: 20" \
  -d '{"model": "llama-3", "prompt": "Hello", "max_tokens": 100}'
```

### 5. Monitor Metrics

```bash
curl http://epp-service:9090/metrics | grep pd_slo
```

## Architecture Summary

```
Request → PD-SLO Profile Handler
            ↓
      Run decode profile
            ↓
      Threshold check (applies to ALL requests)
      Non-cached suffix < pdThreshold?
            ↓
          YES → Decode-only (no prefill)
            ↓
           NO → PD disaggregation needed
            ↓
      Has SLO headers?
            ↓                    ↓
          YES                  NO
            ↓                    ↓
    PD-SLO Pair Optimizer    Use profile
    Evaluate all pairs       results directly
    2 Predictors × N×M       (no prediction)
    (prefill, decode)
    Blended headroom
    Weighted random
            ↓                    ↓
    Set x-prefiller-host-port header
```

**Key Behavior**:
- **Threshold check applies to ALL requests** (both with and without SLO headers)
- Short requests (non-cached suffix < `pdThreshold`) always use decode-only
- Only requests needing PD disaggregation proceed to SLO-aware or regular optimization

## Components Created

| Component | Location | Purpose |
|-----------|----------|---------|
| PDPredictorSet | `pkg/predictors/pd_predictors.go` | 2 predictor clients (prefill, decode) |
| PdSLOPairOptimizer | `pkg/plugins/scorer/pd_slo_pair_optimizer.go` | Core scoring logic |
| PdSLOProfileHandler | `pkg/plugins/profile/pd_slo_profile_handler.go` | Profile coordination |
| Helpers | `pkg/plugins/scorer/pd_slo_helpers.go` | Utility functions |
| Metrics | `pkg/metrics/metrics.go` | PD-SLO metrics |
| Config | `deploy/config/pd-slo-epp-config.yaml` | EPP configuration |

## Key Metrics

```prometheus
# Selection outcomes
llm_d_inference_scheduler_pd_slo_pair_selections_total{outcome="positive_headroom|negative_headroom"}

# Predictor calls
llm_d_inference_scheduler_pd_slo_predictor_calls_total{predictor="prefill|decode", status="success|error"}

# Pairs evaluated
llm_d_inference_scheduler_pd_slo_pairs_evaluated_total
```

## Behavior Examples

**Example 1: Short request with SLO headers**
- Request: 50 tokens, 80% prefix cache hit, SLO headers present
- Non-cached suffix: 50 × (1 - 0.8) = 10 tokens
- If `pdThreshold = 100`: **Decode-only** (threshold check fails)
- SLO headers ignored because request too short for PD

**Example 2: Long request with SLO headers**
- Request: 500 tokens, 20% prefix cache hit, SLO headers present
- Non-cached suffix: 500 × (1 - 0.2) = 400 tokens
- If `pdThreshold = 100`: **PD-SLO optimization** (threshold passes → SLO path)
- Evaluates all (prefill, decode) pairs, selects optimal

**Example 3: Long request without SLO headers**
- Request: 500 tokens, 20% prefix cache hit, no SLO headers
- Non-cached suffix: 400 tokens
- If `pdThreshold = 100`: **Regular PD scheduling** (threshold passes → non-SLO path)
- Uses pods selected by profiles directly

**Example 4: Always use PD (disable threshold)**
- Set `pdThreshold = 0`
- All requests needing prefill will use PD disaggregation
- SLO headers determine optimization strategy (pair optimizer vs direct)

## Configuration Parameters

**PD-SLO Pair Optimizer** (for SLO requests):
| Parameter | Default | Description |
|-----------|---------|-------------|
| `ttftWeight` | 0.8 | Weight for TTFT in blended score |
| `tpotWeight` | 0.2 | Weight for TPOT in blended score |
| `transferOverheadMs` | 5.0 | KV transfer overhead (adjust for network) |
| `sloBufferFactor` | 0.9 | Safety margin (0.9 = 10% buffer) |
| `negativeHeadroomProb` | 0.01 | Exploration rate |

**PD-SLO Profile Handler** (unified behavior):
| Parameter | Default | Description |
|-----------|---------|-------------|
| `pdThreshold` | 0 | **Applies to ALL requests**: Non-cached tokens threshold for decode-only vs PD (0 = always PD) |
| `hashBlockSize` | 16 | Hash block size for prefix cache |
| `transferOverheadMs` | 5.0 | KV transfer overhead |

## Troubleshooting

| Issue | Check | Solution |
|-------|-------|----------|
| No SLO routing | SLO headers present? | Add `x-slo-ttft-ms` and `x-slo-tpot-ms` |
| Predictor errors | Services running? | Check `kubectl get svc \| grep predictor` |
| All negative headroom | SLOs too strict? | Relax targets or scale up pods |
| High latency | Too many pairs? | Reduce pod count or implement pruning |

## Next Steps

1. ✅ Deploy and test with heuristic predictors
2. 📊 Monitor metrics and SLO attainment
3. 🔧 Tune parameters (weights, buffer factor, overhead)
4. 📈 Collect training data (requires sidecar instrumentation)
5. 🤖 Train ML models and replace heuristics
6. 🚀 Production deployment

## Full Documentation

See [docs/pd-slo-scheduling.md](docs/pd-slo-scheduling.md) for:
- Detailed architecture
- Algorithm explanations
- Predictor service details
- Performance tuning
- Future enhancements

---

**Status**: ✅ MVP Implementation Complete
**Next**: Deploy predictor services and test end-to-end
