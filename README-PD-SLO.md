# PD-SLO Scheduling Quick Start

This document provides a quick start guide for SLO-aware Prefill-Decode disaggregated scheduling.

📖 **Full Documentation**: [docs/pd-slo-scheduling.md](docs/pd-slo-scheduling.md)

## What is PD-SLO Scheduling?

Joint optimization of (prefill, decode) pod pairs based on TTFT and TPOT SLOs.

**Key Insight**: TTFT depends on BOTH pods:
- TTFT = prefill_time + decode_queue_wait + transfer_overhead
- TPOT = decode_per_token_time (decode only)

## Quick Start

### 1. Deploy Predictor Services

Deploy 3 predictor services (or use heuristic predictors for MVP):
- `prefill-ttft-predictor-svc:8000/8001`
- `decode-ttft-predictor-svc:8000/8001`
- `decode-tpot-predictor-svc:8000/8001`

### 2. Set Environment Variables

```bash
export PREFILL_TTFT_TRAINING_URL=http://prefill-ttft-predictor-svc:8000
export PREFILL_TTFT_PREDICTION_URL=http://prefill-ttft-predictor-svc:8001
export DECODE_TTFT_TRAINING_URL=http://decode-ttft-predictor-svc:8000
export DECODE_TTFT_PREDICTION_URL=http://decode-ttft-predictor-svc:8001
export DECODE_TPOT_TRAINING_URL=http://decode-tpot-predictor-svc:8000
export DECODE_TPOT_PREDICTION_URL=http://decode-tpot-predictor-svc:8001
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
Request → PD-SLO Profile Handler → PD-SLO Pair Optimizer
                                         ↓
                        Evaluate all (prefill, decode) pairs
                                         ↓
                        3 Predictors × N×M pairs
                                         ↓
                        Select optimal pair (weighted random)
                                         ↓
                        Set x-prefiller-host-port header
```

## Components Created

| Component | Location | Purpose |
|-----------|----------|---------|
| PDPredictorSet | `pkg/predictors/pd_predictors.go` | 3 predictor clients |
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
llm_d_inference_scheduler_pd_slo_predictor_calls_total{predictor="prefill-ttft|decode-ttft|decode-tpot", status="success|error"}

# Pairs evaluated
llm_d_inference_scheduler_pd_slo_pairs_evaluated_total
```

## Configuration Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `ttftWeight` | 0.8 | Weight for TTFT in blended score |
| `tpotWeight` | 0.2 | Weight for TPOT in blended score |
| `transferOverheadMs` | 5.0 | KV transfer overhead (adjust for network) |
| `sloBufferFactor` | 0.9 | Safety margin (0.9 = 10% buffer) |
| `negativeHeadroomProb` | 0.01 | Exploration rate |

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
