# no-hit-lru-scorer

Penalizes pods that recently caused KV-cache misses, steering cold
requests away from pods that are unlikely to have cached prefixes.

## Type

`no-hit-lru-scorer`

## How it works

Tracks which pods served requests that resulted in a KV-cache miss using
an LRU cache. When a new request arrives, pods that recently had misses
receive a lower score. The plugin reads prefix cache hit/miss state from
a companion prefix cache scorer plugin.

Also implements `PreRequest` to record routing decisions for future
scoring rounds.

## Configuration

```yaml
- type: no-hit-lru-scorer
  parameters:
    prefixPluginType: "prefix-cache-scorer"   # type of the prefix cache plugin to read from
    prefixPluginName: "prefix-cache-scorer"   # name of the prefix cache plugin instance
    lruSize: 1024                             # max number of endpoints tracked (default: 1024)
```

## When to use

Use alongside a prefix cache scorer in P/D disaggregated deployments to
avoid routing cold requests to pods that have recently experienced cache
misses.
