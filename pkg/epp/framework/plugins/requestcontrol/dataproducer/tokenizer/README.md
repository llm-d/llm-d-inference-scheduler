# Tokenizer (`tokenizer`)

`DataProducer` plugin that renders the request prompt and publishes
`TokenIDs` (and a flat sorted `MultiModalFeatures` list) on
`request.Body.TokenizedPrompt` for downstream scorers and filters.

The plugin selects one of two backends based on which configuration block
is present. Each backend is self-contained — they share the same plugin
contract but are independent implementations.

| Backend                | Transport       | Sidecar image                                | Notes                                                                |
| ---------------------- | --------------- | -------------------------------------------- | -------------------------------------------------------------------- |
| `udsTokenizerConfig`   | gRPC over UDS   | custom Python tokenizer service              | Lowest per-request overhead.                                         |
| `vllmHTTPRenderConfig` | HTTP            | any vLLM image with `vllm launch render`     | Uses vLLM's exact preprocessing (template + tokenizer) — no separate sidecar image to maintain. |

Set exactly one block. Setting both is rejected by the factory. An empty
configuration falls back to `udsTokenizerConfig` with the default socket
path for backward compatibility.

## Config

| Parameter                                | Default                  | Description                                                       |
| ---------------------------------------- | ------------------------ | ----------------------------------------------------------------- |
| `modelName`                              | – (required)             | Model whose tokenizer should be loaded / sent in render requests. |
| `udsTokenizerConfig.socketFile`          | `/tmp/tokenizer/...sock` | UDS socket path.                                                  |
| `udsTokenizerConfig.modelTokenizerMap`   | –                        | Optional model → tokenizer-data path map.                         |
| `vllmHTTPRenderConfig.url`               | `http://localhost:8000`  | Base URL of the vLLM sidecar (no trailing slash).                 |
| `vllmHTTPRenderConfig.timeout`           | `5s`                     | Per-request timeout for text-only requests.                       |
| `vllmHTTPRenderConfig.mmTimeout`         | `30s`                    | Per-request timeout for multimodal requests.                      |

## Failure mode

Fail-open. Per-request errors are logged and `TokenizedPrompt` is left nil;
downstream scorers fall back to their own paths.

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
- type: tokenizer
  parameters:
    modelName: "${MODEL_NAME}"
    vllmHTTPRenderConfig:
      url: "http://localhost:8000"        # optional; this is the default
```

A complete sample config that pairs this with `precise-prefix-cache-scorer`
is at
[`deploy/config/sim-epp-tokenizer-vllm-http-config.yaml`](../../../../../../deploy/config/sim-epp-tokenizer-vllm-http-config.yaml).
