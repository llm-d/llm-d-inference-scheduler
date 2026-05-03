# File Discovery Example

Run the full llm-d inference stack without a Kubernetes cluster using the
`file-discovery` plugin. See [docs/discovery.md](../../docs/discovery.md) for
architecture details.

## Prerequisites

- Docker with GPU support (NVIDIA Container Toolkit installed)
- A Hugging Face token with access to the model

## Quick start (docker-compose)

```bash
export HF_TOKEN=<your-hugging-face-token>
docker-compose up
```

Send a request:

```bash
curl http://localhost:8081/v1/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "Qwen/Qwen2.5-1.5B-Instruct", "prompt": "capital of france is", "max_tokens": 20}'
```

## Files

| File | Purpose |
|------|---------|
| `epp-config.yaml` | EPP configuration referencing file-discovery |
| `endpoints.yaml` | List of inference endpoints the EPP will serve |
| `envoy.yaml` | Envoy configuration with ext_proc and ORIGINAL_DST |
| `docker-compose.yaml` | Full stack: vLLM + EPP + Envoy |

---

## Appendix A -- EPP flags

```bash
epp \
  --config-file ./epp-config.yaml \
  --grpc-port 9002 \
  --grpc-health-port 9003 \
  --metrics-port 9090 \
  --secure-serving=false
```

`--pool-name` and `--pool-namespace` are optional in file-discovery mode; they
default to `epp` and `default` respectively and are informational only.

---

## Appendix B -- Envoy

Envoy uses two clusters:

- **`ext_proc`** -- gRPC connection to the EPP (port 9002). The EPP processes
  every request header and sets `x-gateway-destination-endpoint`.
- **`original_destination_cluster`** -- forwards to whatever IP:port the EPP
  put in the `x-gateway-destination-endpoint` header. Envoy never needs to
  know the list of backends.

For bare-metal (no docker-compose), replace the service names `epp` and `vllm-0`
in `envoy.yaml` with the actual hostnames or IPs.

Run Envoy as a container:

```bash
docker run -d --name envoy \
  --network host \
  -v $(pwd)/envoy.yaml:/etc/envoy/envoy.yaml \
  envoyproxy/envoy:distroless-v1.33.2 \
  -c /etc/envoy/envoy.yaml
```

`--network host` lets Envoy reach the EPP and vLLM on localhost.

---

## Appendix C -- vLLM

### GPU (recommended)

```bash
docker run -d --name vllm \
  --gpus all \
  -p 8000:8000 \
  -e HUGGING_FACE_HUB_TOKEN=${HF_TOKEN} \
  vllm/vllm-openai:v0.7.3 \
  --model Qwen/Qwen2.5-1.5B-Instruct \
  --max-model-len 4096
```

Requires NVIDIA Container Toolkit. Verify Docker can see the GPU first:

```bash
docker run --rm --gpus all nvidia/cuda:12.8.0-base-ubuntu22.04 nvidia-smi
```

### Simulator (no GPU required)

For testing the EPP routing without a real model:

```bash
docker run -d --name vllm-sim \
  -p 8000:8000 \
  ghcr.io/llm-d/llm-d-inference-sim:v0.8.2 \
  --model Qwen/Qwen2.5-1.5B-Instruct
```

Responses are random text but all routing, metrics polling, and EPP scheduling
work correctly.

---

## Appendix D -- Dynamic endpoint updates

With `watchFile: true` in `epp-config.yaml`, add or remove endpoints at runtime
by editing `endpoints.yaml`. The EPP reloads within milliseconds via `fsnotify`.

Add a second instance:

```yaml
endpoints:
  - name: vllm-0
    address: vllm-0
    port: "8000"
    labels:
      model: Qwen/Qwen2.5-1.5B-Instruct
  - name: vllm-1
    address: vllm-1
    port: "8000"
    labels:
      model: Qwen/Qwen2.5-1.5B-Instruct
```

The EPP will immediately start routing requests to both instances.
