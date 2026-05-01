# Probabilistic Admitter

The `probabilistic-admitter` plugin implements binary-tier probabilistic load shedding for
llm-d inference workloads. It protects critical traffic while gracefully degrading under load
by probabilistically rejecting lower-priority (sheddable) requests before they consume capacity.

## How It Works

The plugin computes cluster saturation each time a sheddable request arrives:

```
saturation = avg over pods of: max(QueueDepth / queueDepthThreshold, KVCache / kvCacheUtilThreshold)
```

Shedding probability follows a quintic ramp:

```
p = min(saturation^power × k, 1.0)
```

With default parameters (`power=5`, `k=300`):
- At saturation 0.20: ~1% of sheddable requests are shed
- At saturation 0.34: 100% of sheddable requests are shed

This dead zone at low load means normal traffic is unaffected, while the steep ramp at
moderate overload provides decisive protection before the cluster is overwhelmed.

## Priority Tiers

| Priority | Tier | Behavior |
|----------|------|----------|
| `>= 0` | Critical / standard | Always admitted |
| `< 0` | Sheddable | Probabilistically rejected under load |

Priorities are set on `InferenceObjective` resources.

## Deploying

### 1. Apply the EPP config

Use `deploy/config/probabilistic-admitter-epp-config.yaml` as the `EndpointPickerConfig`
for your EPP deployment.

The config disables the system-level saturation gate (by setting the `utilization-detector`
to effectively infinite thresholds) so that all admission decisions are made by the
`probabilistic-admitter`.

### 2. Create InferenceObjective resources

Define one `InferenceObjective` per traffic tier pointing to the same model and pool.

**Sheddable tier** — batch jobs, background processing, non-interactive workloads:

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha2
kind: InferenceObjective
metadata:
  name: batch-workload
  namespace: default
spec:
  modelName: my-model
  poolRef:
    name: my-inference-pool
  priority: -1
```

**Critical tier** — interactive workloads, SLO-bound traffic:

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha2
kind: InferenceObjective
metadata:
  name: interactive-workload
  namespace: default
spec:
  modelName: my-model
  poolRef:
    name: my-inference-pool
  priority: 1
```

Requests routed through the `batch-workload` objective (priority -1) will be probabilistically
shed when the cluster saturates. Requests routed through `interactive-workload` (priority 1)
are never shed.

## Parameters

All parameters are optional. Defaults match the simulation-validated configuration.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `queueDepthThreshold` | `5` | Per-pod waiting queue depth at which saturation contribution = 1.0 |
| `kvCacheUtilThreshold` | `0.8` | Per-pod KV cache fraction (0.0–1.0) at which saturation contribution = 1.0 |
| `power` | `5.0` | Quintic exponent of the probability ramp |
| `k` | `300.0` | Scale factor — 100% shed when `saturation^power × k ≥ 1` |

## Notes

- Pods with missing or stale metrics are treated as fully saturated (score 1.0).
- With zero pods, all requests are admitted (safe default).
- The `probabilistic-admitter` reads metrics directly — no data producer plugin required.
