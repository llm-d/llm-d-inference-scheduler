# Speculative Indexing

Speculative indexing closes the blind spot between a routing decision and KV event arrival by immediately writing a predicted cache entry to the prefix-cache index. This lets the next request with the same prefix hit the cache without waiting for engine confirmation. See [#538](https://github.com/llm-d/llm-d-inference-scheduler/issues/538) for background.

## Configuration

Enable via `precise-prefix-cache-scorer` parameters:

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `speculativeIndexing` | bool | `false` | Enable speculative index entries on routing decisions. |
| `speculativeTTL` | duration string | `"2s"` | TTL for speculative entries. Accepts Go duration strings (e.g. `"2s"`, `"500ms"`). |

Requires the `prepareDataPlugins` feature gate and KV events from vLLM engines.

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
      speculativeTTL: "2s"
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
```
