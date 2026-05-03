# Eviction Priority-Then-Time Ordering Plugin

**Type:** `eviction-priority-then-time-ordering`

An eviction ordering policy that selects which queued request to evict when the system is overloaded. It prioritizes evicting the lowest-priority request first. When two requests share the same priority, the most recently dispatched one is evicted first, minimizing wasted KV-cache investment.

Eviction ordering:
1. **Lowest priority first** — requests with more negative priority are evicted before those with less negative or zero priority.
2. **Newest dispatch time first** (tie-breaker) — among equal-priority requests, the one dispatched most recently is evicted, as it has the least sunk cost in KV-cache memory.

**Parameters:** None.

---

## Related Documentation
- [Eviction Filtering](../filtering/README.md)
