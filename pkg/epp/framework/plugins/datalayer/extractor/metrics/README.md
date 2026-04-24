# Core Metrics Extractor

The Core Metrics Extractor is a data layer plugin responsible for extracting model server metrics from a data source and storing them as endpoint attributes. It supports multiple inference engines and can be configured to map engine-specific metric names to a standard set of internal keys.

It is registered as type `core-metrics-extractor` and runs as a data layer extractor.

## What it does

1.  Receives a `PrometheusMetricMap` from a metrics data source (e.g., `metrics-data-source`).
2.  Identifies the inference engine type of the endpoint (e.g., vLLM, SGLang, Triton) using a Pod label.
3.  Looks up the metric specifications for that engine.
4.  Extracts values for standard metrics:
    -   **Waiting Queue Size**: Number of requests waiting in the engine's queue.
    -   **Running Requests Size**: Number of requests currently being processed.
    -   **KV Cache Usage**: Percentage of KV cache currently utilized.
    -   **LoRA Adapters**: Information about active and waiting LoRA adapters.
    -   **Cache Configuration**: Block size and total number of GPU blocks.
5.  Stores these values as attributes on the endpoint, making them available to scheduling plugins.

## Inputs consumed

-   `PrometheusMetricMap`: A map of Prometheus metric families, typically produced by `metrics-data-source`.

## Attributes produced

The plugin populates several standard keys on the endpoint:

-   `WaitingQueueSize` (int)
-   `RunningRequestsSize` (int)
-   `KVCacheUsagePercent` (float64)
-   `MaxActiveModels` (int)
-   `ActiveModels` (int)
-   `WaitingModels` (int)
-   `UpdateTime` (time.Time)

## Configuration

The plugin config supports:

-   `engineLabelKey`: The Pod label key used to identify the engine type. Defaults to `inference.networking.k8s.io/engine-type`.
-   `defaultEngine`: The engine type to use if the label is missing. Defaults to `vllm`.
-   `engineConfigs`: A list of engine-specific metric specifications.

### Built-in Engine Configurations

The plugin comes with built-in support for the following engines:
-   `vllm`
-   `sglang`
-   `trtllm-serve`
-   `triton-tensorrt-llm`

To correctly establish the mapping, model server Pods should be labeled using the `engineLabelKey` with the engine type as follows:

```yaml
metadata:
  labels:
    inference.networking.k8s.io/engine-type: vllm # other options: sglang, trtllm-serve, triton-tensorrt-llm 

```


### Custom Engine Configuration Example

```yaml
type: core-metrics-extractor
parameters:
  engineConfigs:
    - name: "my-custom-engine"
      queuedRequestsSpec: "custom_queue_size{status=waiting}"
      runningRequestsSpec: "custom_running_size"
      kvUsageSpec: "custom_cache_utilization"
```

and the model server deployment Pods should have the label:

```yaml
metadata:
  labels:
    inference.networking.k8s.io/engine-type: my-custom-engine

```

