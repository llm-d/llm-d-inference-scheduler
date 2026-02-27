# Speculative Indexing Configuration

## Overview

Speculative indexing solves the "blind spot" between a routing decision and the arrival of a KV event.
When the scheduler picks a pod, it immediately writes a speculative entry to the prefix-cache index
so that the *next* request with the same prefix is routed to the same pod — before the engine confirms
via KV events.

This improves TTFT (Time To First Token) by enabling cache-aware routing from the very first request,
rather than waiting for KV event confirmation which can take hundreds of milliseconds.

## Prerequisites

- **Feature gate**: `prepareDataPlugins` must be enabled in the `EndpointPickerConfig`.
  This feature gate enables the `PrepareRequestData` and `PreRequest` lifecycle hooks
  used by speculative indexing.
- **KV events**: The vLLM engines must be configured to emit KV-cache events so that
  confirmed entries eventually replace speculative ones.

## Configuration Parameters

### PrecisePrefixCacheScorer

The following parameters are added to `precise-prefix-cache-scorer`:

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `speculativeIndexing` | bool | `false` | When true, the plugin proactively adds predicted cache entries to the index immediately after a routing decision, closing the blind spot between routing and KV event arrival. |
| `speculativeTTL` | duration (nanoseconds) | `2000000000` (2s) | Time-to-live for speculative index entries. After this duration, speculative entries are automatically evicted from the index. Only used when `speculativeIndexing` is true. |

### NoHitLRUScorer

The following parameter is added to `no-hit-lru-scorer`:

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `prefixPluginType` | string | `prefix-cache-scorer` | The type of the prefix cache plugin to read state from. Set to `precise-prefix-cache-scorer` when using speculative indexing. |

## Example Configuration

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
featureGates:
  - prepareDataPlugins                    # required for speculative indexing
plugins:
  - type: precise-prefix-cache-scorer
    parameters:
      tokenProcessorConfig:
        blockSize: 32                     # must match vLLM --block-size
        hashSeed: "12345"
      speculativeIndexing: true           # enable speculative indexing
      speculativeTTL: 2000000000          # 2s in nanoseconds
      indexerConfig:
        kvBlockIndexConfig:
          inMemoryConfig:
            size: 100000000
            podCacheSize: 10
          enableMetrics: true
        tokenizersPoolConfig:
          modelName: hf-repo/model-name
          workersCount: 4
      kvEventsConfig:
        zmqEndpoint: "tcp://*:5557"
        topicFilter: "kv@"
        concurrency: 16
  - type: no-hit-lru-scorer
    parameters:
      prefixPluginType: precise-prefix-cache-scorer
      prefixPluginName: precise-prefix-cache-scorer
      lruSize: 2048
  - type: queue-scorer
  - type: kv-cache-utilization-scorer
  - type: prefill-filter
  - type: decode-filter
  - type: max-score-picker
  - type: always-disagg-pd-decider
  - type: pd-profile-handler
    parameters:
      prefixPluginType: precise-prefix-cache-scorer
      prefixPluginName: precise-prefix-cache-scorer
      primaryPort: 8000
schedulingProfiles:
  - name: prefill
    plugins:
      - pluginRef: prefill-filter
      - pluginRef: precise-prefix-cache-scorer
        weight: 2
      - pluginRef: no-hit-lru-scorer
        weight: 2
      - pluginRef: queue-scorer
        weight: 2
      - pluginRef: kv-cache-utilization-scorer
        weight: 1
      - pluginRef: max-score-picker
  - name: decode
    plugins:
      - pluginRef: decode-filter
      - pluginRef: precise-prefix-cache-scorer
        weight: 2
      - pluginRef: no-hit-lru-scorer
        weight: 2
      - pluginRef: queue-scorer
        weight: 2
      - pluginRef: kv-cache-utilization-scorer
        weight: 1
      - pluginRef: max-score-picker
```

## How It Works

```
PrepareRequestData → compute blockKeys, lookup index, store scores & matchInfo
    ↓
Score → reuse pre-computed scores (or fallback to direct computation)
    ↓
PreRequest → inject speculative entries for selected pod(s), register in TTL cache
    ↓
[KV event arrives] → confirmed entry added
    ↓
[TTL expires] → speculative entry evicted, confirmed entry remains
```

1. **PrepareRequestData**: Tokenizes the prompt, computes block keys, and looks up the
   KV-cache index. Scores and match info are stored in plugin state for reuse by
   downstream hooks.
2. **Score**: Reuses pre-computed scores from `PrepareRequestData` when available,
   falling back to direct computation for backward compatibility.
3. **PreRequest**: After the scheduler picks a pod, injects speculative `PodEntry`
   records into the index for the selected endpoint(s). In P/D disaggregation mode,
   entries are added for both the decode and prefill endpoints.
4. **TTL eviction**: Speculative entries are automatically removed after `speculativeTTL`.
   Confirmed entries from KV events are unaffected.
