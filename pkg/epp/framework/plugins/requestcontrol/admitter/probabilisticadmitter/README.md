# Probabilistic Admitter (`probabilistic-admitter`)

Probabilistically sheds sheddable requests under load while always admitting critical requests.

## Interface

Admitter

## Behavior

Critical and standard requests (priority >= 0) are always admitted.

For sheddable requests (priority < 0), the plugin computes cluster saturation using the
roofline formula and rejects with probability `p = min(sat^power * k, 1.0)`:

```
saturation = avg over pods of: max(WaitingQueueSize / queueDepthThreshold, KVCacheUsagePercent / kvCacheUtilThreshold)
```

| Condition | Behavior |
|-----------|----------|
| `priority >= 0` | Always admit |
| `len(pods) == 0` | Admit (safe default) |
| `request == nil` | Admit |
| Pod has nil metrics | Treated as fully saturated (score = 1.0) |

With defaults (`power=5, k=300`), shedding is ~1% at saturation 0.2 and reaches 100% at
saturation ≈ 0.34. This creates a dead zone at low load with a steep ramp at moderate overload.

## Config

| Parameter | Default | Description |
|-----------|---------|-------------|
| `queueDepthThreshold` | `5` | Queue depth at which pod saturation contribution = 1.0 |
| `kvCacheUtilThreshold` | `0.8` | KV cache fraction at which pod saturation contribution = 1.0 |
| `power` | `5.0` | Exponent of the probability ramp |
| `k` | `300.0` | Scale factor; 100% shed at saturation ≈ `(1/k)^(1/power)` ≈ 0.34 |

## Saturation Detector

When using this plugin, disable the system-level saturation gate by configuring
`utilization-detector` with effectively infinite thresholds. See
`deploy/config/probabilistic-admitter-epp-config.yaml` for the complete example.

## Dependencies

None. The plugin reads `WaitingQueueSize` and `KVCacheUsagePercent` directly from endpoint
metrics — no data producer prerequisite.
