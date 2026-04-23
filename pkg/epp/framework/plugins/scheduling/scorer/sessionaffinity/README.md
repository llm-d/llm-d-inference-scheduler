# Session Affinity Scorer

## SessionAffinityScorer

**Type:** `session-affinity-scorer` | **Implementation:** [session_affinity.go](session_affinity.go)

Scores candidate pods by giving a higher score to pods that were previously used for the same session. Enables sticky routing for stateful workloads where reusing the same pod reduces latency or preserves context.

**Parameters:** None.

**Configuration Example:**
```yaml
plugins:
  - type: session-affinity-scorer
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: session-affinity-scorer
        weight: 5
```

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
