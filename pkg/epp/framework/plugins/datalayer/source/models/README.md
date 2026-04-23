# Models Data Source

**Type:** `models-data-source` | **Implementation:** [factories.go](factories.go)

The Models Data Source polls inference server pods for model information and passes the response to a paired [`model-server-protocol-models`](../../extractor/models/README.md) extractor.

The `"dataLayer"` feature gate must appear in the top-level `featureGates:` list — this is an experimental feature gate defined in [`ExperimentalDatalayerFeatureGate`](../../../../../datalayer/factory.go); without it the EPP ignores the `data:` section entirely, and with it `data: sources:` must be present (missing it causes a startup error).

## What it does

1. Iterates over every pod in the `InferencePool`.
2. Issues a `GET scheme://pod-ip:metricsPort/path` request to each pod.
3. Parses the OpenAI-compatible `/v1/models` response.
4. Forwards the parsed response to any extractors wired to this source via `data: sources:`.

## Inputs consumed

- Pod list from the `InferencePool` (polled individually on each scheduling cycle).

## Attributes produced

None directly. The parsed API response is forwarded to the attached extractors, which store it as endpoint attributes.

## Configuration

- `scheme` (string, optional, default: `"http"`): Protocol scheme: `"http"` or `"https"`.
- `path` (string, optional, default: `"/v1/models"`): URL path for the models API endpoint.
- `insecureSkipVerify` (bool, optional, default: `true`): Skip TLS certificate verification.

```yaml
- type: models-data-source
  name: my-models-source
  parameters:
    scheme: "http"
    path: "/v1/models"
    insecureSkipVerify: true
```

The data source expects responses in the OpenAI-compatible format:

```json
{
  "object": "list",
  "data": [
    { "id": "llama-3-8b", "parent": "llama-3" },
    { "id": "mistral-7b", "parent": "mistral" }
  ]
}
```

## Complete Configuration Example

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
featureGates:
- dataLayer
plugins:
- type: models-data-source
  name: vllm-models-source
  parameters:
    scheme: "https"
    path: "/v1/models"
    insecureSkipVerify: false
- type: model-server-protocol-models
  name: vllm-models-extractor
# ... other plugins (filters, scorers, profile handler, picker) ...
data:
  sources:
  - pluginRef: vllm-models-source
    extractors:
    - pluginRef: vllm-models-extractor
```

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
- [Model Server Extractor](../../extractor/models/README.md)
