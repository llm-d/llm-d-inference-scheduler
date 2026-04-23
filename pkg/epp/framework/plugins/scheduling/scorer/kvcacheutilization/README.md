# KV Cache Utilization Scorer Plugin

This plugin scores candidate endpoints using each endpoint's current KV-cache utilization.

It is registered as type `kv-cache-utilization-scorer` and runs as a scheduling scorer.

> Note: This scorer is included in the default out-of-the-box configuration.

## What it does

**Type:** `kv-cache-utilization-scorer` | **Implementation:** [kvcache_utilization.go](kvcache_utilization.go)

For each candidate endpoint, the plugin computes:

```
  {score(endpoint)} = 1 - {kvCacheUsagePercent}
```

Where `kvCacheUsagePercent` is read from endpoint metrics.

This means:

- lower KV-cache usage -> higher score
- higher KV-cache usage -> lower score

## Scheduling intent

The scorer returns category `Distribution`, so it helps spread traffic away from endpoints with high KV-cache pressure.

## Inputs consumed

The plugin consumes:

- `metrics.KVCacheUsagePercentKey` (`float64`)

## Configuration

This scorer currently has no runtime parameters.

**Configuration Example:**
```yaml
plugins:
  - type: kv-cache-utilization-scorer
    name: kv-cache-util
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: kv-cache-util
        weight: 1
```

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
