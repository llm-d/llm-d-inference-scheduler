# Token Producer Plugin

**Type:** `token-producer`

Tokenizes the request prompt (text completions and multi-modal chat) and publishes the result on `InferenceRequestBody.TokenizedPrompt` for downstream consumers (scorers, filters, other data producers). Communicates over a Unix domain socket with a tokenizer sidecar from [`github.com/llm-d/llm-d-kv-cache`](https://github.com/llm-d/llm-d-kv-cache). Fail-open: tokenization errors are logged and scheduling continues with `TokenizedPrompt` left nil.

Implements `requestcontrol.DataProducer` and runs in the `PrepareRequestData` phase, before filters and scorers. The plugin is idempotent: if `InferenceRequestBody.TokenizedPrompt` is already populated by an earlier producer, tokenization is skipped. Multi-modal features are flattened into the upstream list shape, sorted by placeholder offset.

**Parameters:**
- `modelName` (string, required): Model name whose tokenizer to load.
- `udsTokenizerConfig.socketFile` (string, optional, default: `"/tmp/tokenizer/tokenizer-uds.socket"`): Path to the Unix domain socket exposed by the tokenizer sidecar.
- `udsTokenizerConfig.timeout` (string, optional, default: `"5s"`): Per-request timeout (Go duration string).
- `udsTokenizerConfig.maxRetries` (int, optional, default: `3`): Maximum retry attempts on transport errors.

Defaults shown above are the library defaults from `tokenization.UdsTokenizerConfig`.

> [!NOTE]
> Legacy alias `tokenizer` continues to work for backward compatibility and will be removed in a future release.

**Configuration Example:**
```yaml
plugins:
  - type: token-producer
    parameters:
      modelName: "llama-3-8b"
      udsTokenizerConfig:
        socketFile: "/tmp/tokenizer/tokenizer-uds.socket"
        timeout: "5s"
        maxRetries: 3
  - type: precise-prefix-cache-scorer
    name: cache-scorer
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: cache-scorer
        weight: 10
```

The framework auto-registers any plugin implementing `requestcontrol.DataProducer` into the `PrepareRequestData` phase; no separate `prepareData:` block is required.

---

## Related Documentation
- [Precise Prefix Cache Scorer](../../../scheduling/scorer/preciseprefixcache/README.md)
- [Context Length Aware Scorer](../../../scheduling/scorer/contextlengthaware/README.md)
