# Active Request Scorer

**Type:** `active-request-scorer` | **Implementation:** [active_request.go](active_request.go)

Scores pods based on the number of active requests being served per pod. Each request is tracked individually with its own TTL to ensure accurate timeout handling.

Pods at or below `idleThreshold` active requests receive the maximum score (`1.0`). Busier pods are scored proportionally in the range `[0, maxBusyScore]`, with the most-loaded pod scoring lowest.

**Parameters:**
- `idleThreshold` (int, optional, default: `0`): Request count at or below which a pod is considered idle and scores `1.0`.
- `maxBusyScore` (float, optional, default: `1.0`): Maximum score assigned when a pod is busy.
- `requestTimeout` (duration string, optional, default: `"2m"`): Timeout after which an in-flight request is automatically removed from tracking.

**Configuration Example:**
```yaml
plugins:
  - type: active-request-scorer
    name: active-tracker
    parameters:
      idleThreshold: 2
      maxBusyScore: 0.5
      requestTimeout: "2m"
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: active-tracker
        weight: 5
```

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
