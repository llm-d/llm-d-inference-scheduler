# File Discovery Example

Run the full llm-d inference stack without a Kubernetes cluster using the
`file-discovery` plugin. See [docs/discovery.md](../../docs/discovery.md) for
architecture details.

## Files

Two sets of config files are provided -- one for docker-compose, one for running
components manually on localhost:

| File | Used by | Description |
|------|---------|-------------|
| `docker-compose.yaml` | docker-compose | Full stack: vLLM + EPP + Envoy |
| `epp-config.yaml` | docker-compose | EPP config, endpoints at `/etc/epp/endpoints.yaml` |
| `endpoints.yaml` | docker-compose | Endpoints using docker-compose service names |
| `envoy.yaml` | docker-compose | Envoy config, EPP at hostname `epp` |
| `epp-config-local.yaml` | manual | EPP config, endpoints at `./endpoints-local.yaml` |
| `endpoints-local.yaml` | manual | Endpoints using `127.0.0.1` |
| `envoy-local.yaml` | manual | Envoy config, EPP at `127.0.0.1` |

---

## Option 1 -- docker-compose (recommended)

Starts vLLM, the EPP, and Envoy as containers on a shared Docker network.
Service names resolve automatically so no address configuration is needed.

### Prerequisites

- Docker with GPU support (NVIDIA Container Toolkit installed)
- A Hugging Face token with access to the model

### Run

```bash
export HF_TOKEN=<your-hugging-face-token>
docker-compose up
```

### Test

```bash
curl http://localhost:8081/v1/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "Qwen/Qwen2.5-1.5B-Instruct", "prompt": "capital of france is", "max_tokens": 20}'
```

---

## Option 2 -- Running the components manually

Runs each component separately on the host network using the `*-local` files,
which use `127.0.0.1` instead of docker-compose service names. Run the steps
in order: vLLM first, then Envoy, then EPP.

### Running vLLM

**With a GPU:**

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

**Without a GPU (simulator):**

```bash
docker run -d --name vllm-sim \
  -p 8000:8000 \
  ghcr.io/llm-d/llm-d-inference-sim:v0.8.2 \
  --model Qwen/Qwen2.5-1.5B-Instruct
```

### Running Envoy

`envoy-local.yaml` points the ext_proc cluster at `127.0.0.1:9002` (the EPP).
Run from the `examples/file-discovery/` directory:

```bash
docker run -d --name envoy \
  --network host \
  -v $(pwd)/envoy-local.yaml:/etc/envoy/envoy.yaml \
  envoyproxy/envoy:distroless-v1.33.2 \
  -c /etc/envoy/envoy.yaml
```

`--network host` lets Envoy reach the EPP and vLLM on localhost.

Verify Envoy started:

```bash
curl http://localhost:19000/ready
```

Should return `LIVE`.

### Running the EPP

Build from the repository root:

```bash
go build -o ./epp ./cmd/epp
```

Run from the `examples/file-discovery/` directory so that relative paths in
`epp-config-local.yaml` resolve correctly:

```bash
cd examples/file-discovery
/path/to/repo/epp \
  --config-file ./epp-config-local.yaml \
  --grpc-port 9002 \
  --grpc-health-port 9003 \
  --metrics-port 9090 \
  --secure-serving=false
```

`epp-config-local.yaml` already references `./endpoints-local.yaml` so no
editing is needed.

### Test

```bash
curl http://localhost:8081/v1/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "Qwen/Qwen2.5-1.5B-Instruct", "prompt": "capital of france is", "max_tokens": 20}'
```

Note: the EPP logs `extract failed: metric family "vllm:kv_cache_usage_perc" not
found` against vLLM 0.7.3 -- this is a known metric naming mismatch and does not
affect routing. Requests are served correctly.

---

## Appendix -- Dynamic endpoint updates

With `watchFile: true` in the config, add or remove endpoints at runtime by
editing the endpoints file. The EPP reloads within milliseconds via `fsnotify`.

Example: add a second instance to `endpoints-local.yaml`:

```yaml
endpoints:
  - name: vllm-0
    address: 127.0.0.1
    port: "8000"
    labels:
      model: Qwen/Qwen2.5-1.5B-Instruct
  - name: vllm-1
    address: 127.0.0.2
    port: "8000"
    labels:
      model: Qwen/Qwen2.5-1.5B-Instruct
```

The EPP will immediately start routing requests to both instances.
