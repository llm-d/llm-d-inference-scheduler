# Precise Prefix Cache Scorer

**Type:** `precise-prefix-cache-scorer`

Pure attribute reader. Reads `PrefixCacheMatchInfo` written by the
[`precise-prefix-cache-producer`](../../../requestcontrol/dataproducer/preciseprefixcache/README.md)
and returns `matchBlocks / totalBlocks` per endpoint.

Takes no parameters in split configuration.

## Split configuration (recommended)

```yaml
plugins:
  - type: token-producer
    parameters:
      modelName: hf-repo/model-name
      vllm:
        http: http://localhost:8000
  - type: precise-prefix-cache-producer
    parameters:
      tokenProcessorConfig:
        blockSize: 64
      indexerConfig:
        kvBlockIndexConfig:
          enableMetrics: true
  - type: precise-prefix-cache-scorer
```

The data-layer DAG orders the three plugins via Produces/Consumes — no
manual ordering required.

For active-active pod discovery, wire the producer to the
endpoint-notification-source:

```yaml
plugins:
  - type: endpoint-notification-source
  - type: precise-prefix-cache-producer
    parameters:
      tokenProcessorConfig:
        blockSize: 64
      kvEventsConfig:
        topicFilter: "kv@"
        concurrency: 4
        discoverPods: true
        podDiscoveryConfig:
          socketPort: 5556
  - type: precise-prefix-cache-scorer
dataLayer:
  sources:
    - pluginRef: endpoint-notification-source
      extractors:
        - pluginRef: precise-prefix-cache-producer
```

## Legacy all-in-one configuration

Old YAML that puts the producer parameters on `precise-prefix-cache-scorer`
continues to work — the factory instantiates an internal producer and logs
a deprecation notice. Legacy mode will be removed in a future release.

```yaml
plugins:
  - type: precise-prefix-cache-scorer
    parameters:
      tokenProcessorConfig:
        blockSize: 64
      indexerConfig:
        kvBlockIndexConfig:
          enableMetrics: true
```

## Score semantics

Returns `matchBlocks / totalBlocks` in `[0, 1]`, matching `prefix-cache-scorer`.
Replaces the historical min-max normalization, which returned 1.0 on cold
clusters and amplified small absolute hits to 1.0 whenever they were
best-in-set.
