# Token Producer Plugin

**Type:** `token-producer`

`DataProducer` plugin that renders the request prompt and publishes
`TokenIDs` (and a flat sorted `MultiModalFeatures` list) on
`InferenceRequestBody.TokenizedPrompt` for downstream consumers (scorers,
filters, other data producers).

Implements `requestcontrol.DataProducer` and runs in the `PrepareRequestData`
phase, before filters and scorers. The plugin is idempotent: if
`InferenceRequestBody.TokenizedPrompt` is already populated by an earlier
producer, tokenization is skipped. Multi-modal features are flattened into the
upstream list shape, sorted by placeholder offset.

> [!NOTE]
> Legacy alias `tokenizer` is still accepted but logs a deprecation warning at
> instantiation. Prefer `token-producer` in new configs.

## Backends

The plugin selects one of two backends based on which configuration block
is present.

| Backend              | Transport     | Sidecar image                            | Notes                                                                                           |
| -------------------- | ------------- | ---------------------------------------- |-------------------------------------------------------------------------------------------------|
| `vllmHTTP`           | HTTP          | any vLLM image with `vllm launch render` | Uses vLLM's exact preprocessing (template + tokenizer) — no separate sidecar image to maintain. |
| `udsTokenizerConfig` | gRPC over UDS | custom Python tokenizer service          | Lowest theoretical per-request overhead.                                                        |

Set exactly one block. Setting both is rejected by the factory. An empty
configuration falls back to `vllmHTTP` with `http://localhost:8000`.

## Config

| Parameter                              | Default                  | Description                                                       |
| -------------------------------------- | ------------------------ | ----------------------------------------------------------------- |
| `modelName`                            | – (required)             | Model whose tokenizer should be loaded / sent in render requests. |
| `vllmHTTP.url`                         | `http://localhost:8000`  | Base URL of the vLLM sidecar (no trailing slash).                 |
| `vllmHTTP.timeout`                     | `5s`                     | Per-request timeout for text-only requests.                       |
| `vllmHTTP.mmTimeout`                   | `30s`                    | Per-request timeout for multimodal requests.                      |
| `udsTokenizerConfig.socketFile`        | `/tmp/tokenizer/...sock` | UDS socket path.                                                  |
| `udsTokenizerConfig.modelTokenizerMap` | –                        | Optional model → tokenizer-data path map.                         |

## Failure mode

Per-request errors are returned to the Director, which currently logs and
continues; downstream scorers fall back to their own paths.

## Deployment — vLLM HTTP backend

The HTTP backend calls `POST {url}/v1/completions/render` and
`POST {url}/v1/chat/completions/render`, both of which are exposed by
`vllm serve <model>` and by the GPU-less `vllm launch render <model>`.

Recommended layout: co-locate a CPU-only render server as a sidecar in the
EPP pod and connect over loopback.

```yaml
# EPP pod spec
containers:
- name: vllm-render
  image: vllm/vllm-openai:latest          # any image shipping `vllm launch render`
  command: ["vllm", "launch", "render"]
  args: ["${MODEL_NAME}", "--port=8000"]
  ports: [{name: render-http, containerPort: 8000}]
  readinessProbe: {httpGet: {path: /health, port: 8000}, periodSeconds: 5}
```

```yaml
# EPP plugin config
- type: token-producer
  parameters:
    modelName: "${MODEL_NAME}"
    vllmHTTP:
      url: "http://localhost:8000"        # optional; this is the default
```

A complete sample config that pairs this with `precise-prefix-cache-scorer`
is at
[`deploy/config/sim-epp-tokenizer-vllm-http-config.yaml`](../../../../../../deploy/config/sim-epp-tokenizer-vllm-http-config.yaml).

---

## Related Documentation
- [Precise Prefix Cache Scorer](../../../scheduling/scorer/preciseprefixcache/README.md)
- [Context Length Aware Scorer](../../../scheduling/scorer/contextlengthaware/README.md)
