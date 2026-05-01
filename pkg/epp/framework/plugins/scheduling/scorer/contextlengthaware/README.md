# context-length-aware

Scores (and optionally filters) pods based on their supported context
length range, routing requests to pods optimized for the input length.

## Type

`context-length-aware`

## How it works

Reads a pod label (default: `llm-d.ai/context-length-range`) that
specifies the context length range the pod is optimized for, in the
format `min-max` (e.g., `0-2048` or `2048-131072`).

Scores pods higher when the incoming request's token count falls within
their declared range. When `enableFiltering` is true, pods whose range
does not cover the request length are filtered out entirely.

Token count is estimated from prompt length using a character-to-token
multiplier (0.25) when no tokenizer is configured.

## Configuration

```yaml
- type: context-length-aware
  parameters:
    label: "llm-d.ai/context-length-range"   # pod label to read (default)
    enableFiltering: false                    # also filter non-matching pods (default: false)
```

## Pod labeling

```yaml
metadata:
  labels:
    llm-d.ai/context-length-range: "0-8192"
```

## When to use

Use when serving heterogeneous hardware where some pods have larger KV
cache capacity (optimized for long contexts) and others are optimized for
short contexts. Improves GPU memory utilization by matching request length
to pod capability.
