# Precise Prefix Cache Scorer

## PrecisePrefixCacheScorer

**Type:** `precise-prefix-cache-scorer` | **Implementation:** [precise_prefix_cache.go](precise_prefix_cache.go)

Scores requests based on KV-cache locality using real-time KV-cache state tracked across vLLM instances. Unlike the history-based [`prefix-cache-scorer`](../prefix/), this plugin reads actual cache contents via [`llm-d-kv-cache`](https://github.com/llm-d/llm-d-kv-cache) for more accurate scoring.

Uses `kvcache.Indexer` to score pods by matching KV-cache blocks, and `kvevents.Pool` to subscribe to KV-events emitted by vLLM instances and keep cache state up-to-date in near-real time.

> **Important:** Block size and hash seed must match those used in the vLLM deployment.

**Parameters:**
- `tokenProcessorConfig`: Configuration for the `kvblock.TokenProcessor`.
- `indexerConfig`: Configuration for the `kvcache.Indexer`.
- `kvEventsConfig`: Configuration for the `kvevents.Pool`.

See the full parameter reference at [llm-d-kv-cache/docs/configuration.md](https://github.com/llm-d/llm-d-kv-cache/blob/main/docs/configuration.md).

> In most cases you only need to set:
> - Model name in `tokenizersPoolConfig` to match the vLLM deployment.
> - HuggingFace token via `tokenizersPoolConfig` or `tokenizersCacheDir` (or the `HF_TOKEN` environment variable).
> - Token processor `blockSize` and `hashSeed` to match the vLLM deployment.
> - `enableMetrics: true` in `kvBlockIndexConfig` to enable KV-block index metrics.

**Configuration Example — minimal:**
```yaml
plugins:
  - type: precise-prefix-cache-scorer
    parameters:
      tokenProcessorConfig:
        blockSize: 64                    # must match vLLM block size
        hashSeed: "12345"                # must match vLLM PYTHONHASHSEED env var
      indexerConfig:
        kvBlockIndexConfig:
          enableMetrics: true
        tokenizersPoolConfig:
          modelName: hf-repo/model-name
          hf:
            huggingFaceToken: your_hf_token_here    # or set HF_TOKEN env var
```

**Configuration Example — active-active multi-replica with auto pod discovery:**
```yaml
plugins:
  - type: precise-prefix-cache-scorer
    parameters:
      tokenProcessorConfig:
        blockSize: 64
        hashSeed: "42"
      indexerConfig:
        tokenizersPoolConfig:
          modelName: "Qwen/Qwen3-32B"
          hf:
            tokenizersCacheDir: "/tmp/tokenizers"
      kvEventsConfig:
        topicFilter: "kv@"
        concurrency: 4
        discoverPods: true
        podDiscoveryConfig:
          socketPort: 5556
```

vLLM engines configured to emit KV-events on port `5556`:
```yaml
--kv-events-config "{\"enable_kv_cache_events\":true,\"publisher\":\"zmq\",\"endpoint\":\"tcp://*:5556\",\"topic\":\"kv@${POD_IP}@Qwen/Qwen3-32B\"}"
```

**Configuration Example — all parameters:**
```yaml
plugins:
  - type: precise-prefix-cache-scorer
    parameters:
      tokenProcessorConfig:
        blockSize: 16
        hashSeed: "12345"
      kvEventsConfig:
        topicFilter: "kv@"
        concurrency: 4
        discoverPods: true
        podDiscoveryConfig:
          socketPort: 5556
      indexerConfig:
        prefixStoreConfig:
          cacheSize: 500000
          blockSize: 256
        kvBlockIndexConfig:
          inMemoryConfig:
            size: 100000000
            podCacheSize: 10
          enableMetrics: true
        tokenizersPoolConfig:
          modelName: hf-repo/model-name
          workersCount: 8
          hf:
            huggingFaceToken: your_hf_token_here
            tokenizersCacheDir: /tmp/tokenizers
```

### Speculative Indexing

Closes the blind spot between a routing decision and KV-event arrival by immediately writing a predicted cache entry to the prefix-cache index. Lets the next request with the same prefix hit the cache without waiting for engine confirmation.

Requires the `prepareDataPlugins` feature gate and KV events from vLLM.

**Parameters:**
- `speculativeIndexing` (bool, optional, default: `false`): Enable speculative index entries on routing decisions.
- `speculativeTTL` (duration string, optional, default: `"2s"`): TTL for speculative entries (e.g. `"2s"`, `"500ms"`).

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
- [Prefix Cache Scorer (history-based)](../prefix/)
- [No-Hit LRU Scorer](../nohitlru/)
