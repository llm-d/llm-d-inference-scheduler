# precise-prefix-cache-scorer

Scores pods based on real-time KV-cache block locality, providing more
accurate prefix cache affinity than estimation-based scorers.

## Type

`precise-prefix-cache-scorer`

## How it works

Unlike the approximate prefix cache scorer (which estimates cache state
from scheduling history), this plugin subscribes to KV-cache events
directly from vLLM instances and maintains a real-time index of which
KV blocks are present on each pod.

When scoring, it computes the overlap between the incoming request's
token blocks and each pod's cached blocks, giving higher scores to pods
with more matching blocks.

Supports speculative indexing: after a routing decision, the plugin
proactively adds predicted cache entries to close the blind spot between
routing and KV event arrival.

## Configuration

```yaml
- type: precise-prefix-cache-scorer
  parameters:
    tokenProcessorConfig:
      blockSize: 16          # must match vLLM block size
      modelName: "my-model"  # model name for tokenizer lookup
    indexerConfig:
      maxPrefixBlocksToMatch: 256
    kvEventsConfig: {}
    speculativeIndexing: true
```

> **Important:** `blockSize` and `hashSeed` in `tokenProcessorConfig`
> must match the values used in your vLLM deployment.

## When to use

Use instead of `prefix-cache-scorer` when you need accurate, real-time
KV-cache locality scoring. Requires vLLM instances to emit KV-cache
events. Recommended for production P/D disaggregated deployments.
