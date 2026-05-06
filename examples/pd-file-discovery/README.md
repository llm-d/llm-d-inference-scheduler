# P/D Disaggregation with File Discovery

Run a prefill/decode disaggregated inference stack without a Kubernetes cluster using
the `file-discovery` plugin. See [docs/disaggregation.md](../../docs/disaggregation.md)
for architecture details.

## How it works

Each inference request is handled in two stages:

1. **Decode** -- the EPP selects a decode worker and sets `x-gateway-destination-endpoint`
   so Envoy routes the request to the sidecar on that worker.
2. **Prefill** -- the EPP also selects a prefill worker and sets `x-prefiller-host-port`.
   The sidecar on the decode worker reads this header, forwards the prefill request to the
   prefill worker (with `max_tokens=1`), then runs decode locally.

Endpoint roles are declared via the `llm-d.ai/role` label in the endpoints file.
The `always-disagg-pd-decider` ensures every request goes through both stages.

## Files

| File | Used by | Description |
|------|---------|-------------|
| `docker-compose.yaml` | docker-compose | Full stack: prefill sim + decode sim + sidecar + EPP + Envoy |
| `epp-config.yaml` | docker-compose | EPP config, endpoints at `/etc/epp/endpoints.yaml` |
| `endpoints.yaml` | docker-compose | Endpoints using docker-compose service names |
| `envoy.yaml` | docker-compose | Envoy config, EPP at hostname `epp` |
| `epp-config-local.yaml` | manual | EPP config, endpoints at `./endpoints-local.yaml` |
| `endpoints-local.yaml` | manual | Endpoints using `127.0.0.1` |
| `envoy-local.yaml` | manual | Envoy config, EPP at `127.0.0.1` |

---

## Option 1 -- docker-compose (recommended)

Starts both simulators, the sidecar, the EPP, and Envoy on a shared Docker network.

### Prerequisites

- Docker

### Run

```bash
docker-compose up
```

### Test

```bash
curl http://localhost:8081/v1/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "Qwen/Qwen2.5-1.5B-Instruct", "prompt": "capital of france is", "max_tokens": 20}'
```

To confirm P/D routing is active, check the EPP logs for `run_decode` followed by
`run_prefill` decisions, and the sidecar logs for `running Shared Storage protocol`.

---

## Option 2 -- Running the components manually

Runs each component separately on the host network using the `*-local` files.
Run in order: sim-prefill, sim-decode, sidecar-decode, Envoy, EPP.

### Port layout

| Component | Port |
|-----------|------|
| sim-prefill | 8000 |
| sim-decode | 8001 |
| sidecar-decode | 8002 (forwards to localhost:8001) |
| EPP | 9002 (gRPC), 9003 (health), 9090 (metrics) |
| Envoy | 8081 |

### Running sim-prefill

```bash
docker run -d --name sim-prefill \
  -p 8000:8000 \
  ghcr.io/llm-d/llm-d-inference-sim:v0.8.2 \
  --model Qwen/Qwen2.5-1.5B-Instruct
```

### Running sim-decode

```bash
docker run -d --name sim-decode \
  -p 8001:8000 \
  ghcr.io/llm-d/llm-d-inference-sim:v0.8.2 \
  --model Qwen/Qwen2.5-1.5B-Instruct
```

### Running the sidecar

```bash
docker run -d --name sidecar-decode \
  --network host \
  ghcr.io/llm-d/llm-d-routing-sidecar:latest \
  --port 8002 \
  --vllm-port 8001 \
  --kv-connector shared-storage \
  --secure-proxy=false
```

`--network host` lets the sidecar reach sim-decode on `localhost:8001` and sim-prefill
on `localhost:8000`.

### Running Envoy

Run from the `examples/pd-file-discovery/` directory:

```bash
docker run -d --name envoy \
  --network host \
  -v $(pwd)/envoy-local.yaml:/etc/envoy/envoy.yaml \
  envoyproxy/envoy:distroless-v1.33.2 \
  -c /etc/envoy/envoy.yaml
```

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

Run from the `examples/pd-file-discovery/` directory so that the relative path in
`epp-config-local.yaml` resolves correctly:

```bash
cd examples/pd-file-discovery
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
