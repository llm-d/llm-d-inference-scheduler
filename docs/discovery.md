# Discovery Plugin Architecture

## Overview

The EPP discovers inference endpoints through an **EndpointDiscovery** plugin -- a
pluggable abstraction that populates and maintains the endpoint datastore independently
of the underlying infrastructure. By default the EPP uses Kubernetes CRD reconcilers
to discover endpoints. When an `EndpointDiscovery` plugin is configured, the plugin
becomes the sole source of truth for endpoint lifecycle.

This enables the EPP to run without a Kubernetes cluster, which is valuable for RL
training and inference workloads on Slurm, Ray, and other non-Kubernetes infrastructure.

---

## Core Interfaces

All discovery interfaces live in `pkg/epp/framework/interface/datalayer/discovery.go`,
co-located with the other datalayer interfaces.

### `EndpointDiscovery`

```go
type EndpointDiscovery interface {
    fwkplugin.Plugin
    Start(ctx context.Context, notifier DiscoveryNotifier) error
}
```

`Start` is the plugin's entry point. It blocks in the caller's goroutine until
`ctx` is cancelled or a fatal error occurs. The caller (the runner's errgroup) is
responsible for starting it in a dedicated goroutine.

Implementations SHOULD enumerate all currently known endpoints via
`notifier.Upsert` before entering the watch loop, to avoid serving an empty
datastore at startup. Implementations that guarantee no missed events through their
watch mechanism (e.g. a Kubernetes list+watch) may fold the initial enumeration into
the watch sequence instead.

### `DiscoveryNotifier`

```go
type DiscoveryNotifier interface {
    Upsert(endpoint *EndpointMetadata)
    Delete(id types.NamespacedName)
}
```

The callback through which the plugin drives the datastore.

**Not goroutine-safe.** All calls must be made sequentially from a single goroutine.

**Ordering contract:** Upsert and Delete calls are processed in the order received.
An Upsert followed by a Delete for the same endpoint must arrive in that order, or
the endpoint will be incorrectly left in the datastore.

---

## Integration with the Datalayer

Discovery is intentionally placed under the datalayer component because:

- The datalayer already contains K8s binding code (`k8s_bind.go`) and push-based
  metrics mechanisms. Discovery is a natural peer.
- Both K8s discovery plugins use `datalayer.BindNotificationSource` with a shared
  `podDiscoveryExtractor` (defined in `pod_extractor.go` in the `k8s` plugin package)
  to translate pod lifecycle events into `DiscoveryNotifier` calls, eliminating
  duplicated controller-runtime wiring.
- When a K8s discovery plugin is configured, `dlRuntime.Start(ctx, mgr)` is called
  on the manager the plugin owns, binding any configured notification sources
  (push-based metrics) to that manager.

---

## Plugin Registration

An `EndpointDiscovery` implementation must also satisfy `fwkplugin.Plugin` (enforced
at compile time by the interface embedding). Register via:

```go
fwkplugin.Register(MyPluginType, MyFactory)
```

Reference the plugin in the config:

```yaml
discovery:
  pluginRef: my-plugin-name
```

---

## Built-in Plugins

### `file-discovery`

**Package:** `pkg/epp/framework/plugins/datalayer/discovery/file`

Reads a YAML (or JSON) file listing inference endpoints. Optionally watches for
file changes at runtime using `fsnotify`.

#### Parameters

| Parameter   | Type   | Required | Default | Description |
|-------------|--------|----------|---------|-------------|
| `path`      | string | yes      | --      | Path to the endpoints file |
| `watchFile` | bool   | no       | `false` | Reload when file changes on disk |

#### Endpoints file format

```yaml
endpoints:
  - name: vllm-0
    namespace: default       # optional, defaults to "default"
    address: 10.0.0.1
    port: "8080"
    metricsHost: 10.0.0.1:8080   # optional; derived as address:port if absent
    labels:
      model: llama-3-8b
```

#### Reload behaviour

When `watchFile: true`, on any `Write` or `Create` event:
- All endpoints in the new file are upserted (add or update).
- Endpoints absent from the new file but present in the previous load are deleted.
- All upserts are delivered before any deletes (ordering contract preserved).

### `inference-pool-discovery`

**Package:** `pkg/epp/framework/plugins/datalayer/discovery/k8s`

Discovers endpoints by watching Kubernetes pods whose labels match an `InferencePool`
CRD. Also optionally watches `InferenceObjective` and `InferenceModelRewrite` CRDs
when those are installed. Owns its own `ctrl.Manager` internally.

#### Parameters

| Parameter        | Type   | Required | Default                          | Description |
|------------------|--------|----------|----------------------------------|-------------|
| `poolName`       | string | yes*     | from `--pool-name` flag          | InferencePool name |
| `poolNamespace`  | string | no       | `"default"`                      | InferencePool namespace |
| `poolGroup`      | string | no       | `"inference.networking.k8s.io"`  | API group |
| `leaderElection` | bool   | no       | `false`                          | Enable leader election |

*When no `discovery` section is in the config, pool name comes from `--pool-name`.

### `static-selector-discovery`

**Package:** `pkg/epp/framework/plugins/datalayer/discovery/k8s`

Discovers endpoints by watching pods matching a fixed label selector from plugin
parameters. No InferencePool CRD required. Owns its own `ctrl.Manager` internally.

#### Parameters

| Parameter             | Type     | Required | Default     | Description |
|-----------------------|----------|----------|-------------|-------------|
| `endpointSelector`    | string   | yes      | --          | Pod label selector (e.g. `"app=vllm"`) |
| `endpointTargetPorts` | []int    | yes      | --          | Ports to create endpoints for |
| `namespace`           | string   | no       | `"default"` | Namespace to watch |

---

## Running without Kubernetes (file-discovery mode)

See the [file-discovery example](../examples/file-discovery/README.md) for a complete
runnable setup.

The full stack is:

```
Client
  --> Envoy (port 8081)
        --[ext_proc]--> EPP (port 9002)
                          picks endpoint, sets x-gateway-destination-endpoint
        --[ORIGINAL_DST]--> vLLM (address from header)
```

---

## Implementing a Custom Discovery Plugin

1. Create a struct that embeds or satisfies `fwkdl.EndpointDiscovery` (which embeds
   `fwkplugin.Plugin`).
2. Write a factory: `func(name string, params json.RawMessage, handle fwkplugin.Handle) (fwkplugin.Plugin, error)`
3. Register it: `fwkplugin.Register(MyPluginType, MyFactory)`
4. Reference it in `EndpointPickerConfig` via `discovery.pluginRef`

The `file-discovery` plugin is the reference implementation for non-K8s sources.
The `inference-pool-discovery` plugin is the reference for K8s sources that use
`datalayer.BindNotificationSource` with a pod discovery extractor.
