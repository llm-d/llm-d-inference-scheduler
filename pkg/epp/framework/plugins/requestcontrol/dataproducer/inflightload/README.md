# In-Flight Load Producer Plugin

**Type:** `inflight-load-producer` | **Implementation:** [producer.go](producer.go)

Tracks real-time in-flight request and token counts per endpoint by hooking into the request lifecycle. Writes an `InFlightLoad` attribute onto each endpoint in the `PrepareRequestData` phase, consumed by the [`token-load-scorer`](../../../scheduling/scorer/tokenload/README.md) and the `concurrency-detector`.

The producer hooks three lifecycle phases:
- **PrepareRequestData**: Writes current in-flight counts to each endpoint's attributes.
- **PreRequest**: Increments counters when a request is dispatched to an endpoint.
- **ResponseBody**: Decrements counters when a response completes or the request is aborted.

Endpoint departure events (pod removed from the pool) are handled via the `EndpointExtractor` interface to clean up stale counters.

**Parameters:** None.

**Configuration Example:**
```yaml
plugins:
  - type: inflight-load-producer
    name: inflight-load
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: inflight-load
        weight: 0
```

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
- [Token Load Scorer](../../../scheduling/scorer/tokenload/README.md)
- [Concurrency Attributes](../../../datalayer/attribute/concurrency/README.md)
