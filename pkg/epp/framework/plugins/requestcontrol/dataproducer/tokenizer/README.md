# Tokenizer Plugin

**Type:** `tokenizer` 

Converts incoming LLM prompts (both standard text completions and multi-modal chat messages) into token IDs for downstream filters and scorers. Communicates via Unix Domain Socket (UDS) with a tokenizer service from [`github.com/llm-d/llm-d-kv-cache`](https://github.com/llm-d/llm-d-kv-cache), which runs as a separate sidecar container alongside the EPP pod. An embedded (in-process) alternative is also available in the same package. Fail-open: tokenization errors are logged and scheduling continues without token data.

The plugin supports two modes selected at build time:

- **Scorer mode** (default): hooks into the `Score` call to tokenize the request and share results via `CycleState`.
- **PrepareData mode** (`gaie_tokenized_prompt` build tag): runs in the PrepareData phase and stores token IDs directly on `request.TokenizedPrompt`.

## What it does

1. Receives the prompt from the incoming LLM request (text completion or multi-modal chat).
2. Sends the prompt to the tokenizer service over UDS and receives token IDs in return.
3. **Scorer mode**: writes the result into `CycleState` under `tokenizer.TokenizedPromptStateKey` for downstream scorers to read without re-tokenizing.
4. **PrepareData mode**: stores token IDs directly on `request.TokenizedPrompt`, available to all subsequent pipeline stages.

## Inputs consumed

- LLM request body: standard text prompt or multi-modal chat messages.
- Tokenizer service: a sidecar process (or in-process instance) reachable at the configured UDS socket path.

## Attributes produced

- **Scorer mode**: `TokenizedPromptState` written to `CycleState` under key `tokenizer.TokenizedPromptStateKey`.

  ```go
  state, err := scheduling.ReadCycleStateKey[*tokenizer.TokenizedPromptState](
      cycleState, tokenizer.TokenizedPromptStateKey,
  )
  ```

  > **Note:** Multi-modal features (`MMFeatures`) are only populated in scorer mode.

- **PrepareData mode**: token IDs stored on `request.TokenizedPrompt`.

## Configuration

- `modelName` (string, required): Model name whose tokenizer to load.
- `udsTokenizerConfig.socketFile` (string, optional, default: `"/tmp/tokenizer/tokenizer-uds.socket"`): Path to the Unix domain socket.
- `udsTokenizerConfig.timeout` (string, optional, default: `"5s"`): Timeout for tokenizer requests (Go duration string).
- `udsTokenizerConfig.maxRetries` (int, optional, default: `3`): Maximum retry attempts.

### Scorer Mode (default)

Registered under `scorers:` in config. Always returns zero scores — its sole purpose is to make token IDs available to downstream scorers (e.g. `precise-prefix-cache-scorer`, `context-length-aware`) without those scorers needing to re-tokenize.

```yaml
- type: tokenizer
  name: tokenizer
  weight: 0
  parameters:
    modelName: "llama-3-8b"
    udsTokenizerConfig:
      socketFile: "/tmp/tokenizer/tokenizer-uds.socket"
      timeout: "5s"
      maxRetries: 3
```

With cache-aware routing:

```yaml
plugins:
  - type: tokenizer
    parameters:
      modelName: "llama-3-8b"
  - type: precise-prefix-cache-scorer
    name: cache-scorer
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: tokenizer
        weight: 0
      - pluginRef: cache-scorer
        weight: 10
```

With context-length routing:

```yaml
plugins:
  - type: tokenizer
    parameters:
      modelName: "llama-3-8b"
  - type: context-length-aware
    name: context-router
    parameters:
      label: "llm-d.ai/context-length-range"
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: tokenizer
        weight: 0
      - pluginRef: context-router
        weight: 8
```

### PrepareData Mode (`gaie_tokenized_prompt` tag)

```bash
go build -tags gaie_tokenized_prompt
```

Implements `requestcontrol.DataProducer`; registered under `prepareData:` in config. Runs before filters and scorers. Use this mode when the framework version exposes `LLMRequest.TokenizedPrompt`.

```yaml
plugins:
  - type: precise-prefix-cache-scorer
    name: cache-scorer
prepareData:
  - type: tokenizer
    name: tokenizer
    parameters:
      modelName: "llama-3-8b"
      udsTokenizerConfig:
        socketFile: "/var/run/tokenizer/llama.socket"
        timeout: "10s"
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: cache-scorer
        weight: 10
```

## Related Documentation
- [Precise Prefix Cache Scorer](../../../scheduling/scorer/preciseprefixcache/README.md)
- [Context Length Aware Scorer](../../../scheduling/scorer/contextlengthaware/README.md)
