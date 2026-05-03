# Load Aware Scorer

**Type:** `load-aware-scorer`

Scores pods based on their load, measured by the number of requests concurrently being processed. Uses a configurable threshold to determine when a pod is considered overloaded.

- Pods with an empty waiting-requests queue score `0.5`.
- Pods with requests in the queue score between `0.5` and `0.0` — the more queued, the lower the score.

**Parameters:**
- `threshold` (int, optional, default: `128`): Queue depth at which the score reaches `0.0`.

**Configuration Example:**
```yaml
plugins:
  - type: load-aware-scorer
    name: load-balancer
    parameters:
      threshold: 128
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: load-balancer
        weight: 5
```
