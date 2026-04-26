# Static Usage Limit Policy Plugin

**Type:** `static-usage-limit-policy` | **Implementation:** [usagelimitpolicy.go](usagelimitpolicy.go)

A usage limit policy that applies a fixed admission ceiling across all priority levels. The Flow Controller uses this ceiling to gate how much of the pool's capacity can be consumed before requests are queued.

A `threshold` of `1.0` (the default) means no gating — all capacity is available. A lower value (e.g., `0.8`) reserves headroom by capping admission at 80% of pool capacity, providing a safety margin before saturation.

This policy is auto-injected when flow control is enabled.

**Parameters:**
- `threshold` (float64, optional, default: `1.0`): Fixed admission ceiling applied uniformly to all priorities. Must be in `(0.0, 1.0]`.

**Configuration Example:**
```yaml
plugins:
  - type: static-usage-limit-policy
    name: my-usage-limit
    parameters:
      threshold: 0.8
flowControl:
  usageLimitPolicyPluginRef: my-usage-limit
```

---

## Related Documentation

- [Architecture Overview](../../../../../../docs/architecture.md)
- [Flow Control Overview](../fairness/README.md)
