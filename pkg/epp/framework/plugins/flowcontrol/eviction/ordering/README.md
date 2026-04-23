# Eviction Priority-Then-Time Ordering Plugin

## PriorityThenTimeOrdering

**Type:** `eviction-priority-then-time-ordering` | **Implementation:** [priority_time.go](priority_time.go)

An eviction ordering policy that selects which queued request to evict when the system is overloaded. It prioritizes evicting the lowest-priority request first. When two requests share the same priority, the most recently dispatched one is evicted first, minimizing wasted KV-cache investment.

Eviction ordering:
1. **Lowest priority first** — requests with more negative priority are evicted before those with less negative or zero priority.
2. **Newest dispatch time first** (tie-breaker) — among equal-priority requests, the one dispatched most recently is evicted, as it has the least sunk cost in KV-cache memory.

**Parameters:** None.

**Configuration Example:**
```yaml
plugins:
  - type: eviction-priority-then-time-ordering
    name: eviction-priority-then-time-ordering
```

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
- [Eviction Filtering](../filtering/README.md)
