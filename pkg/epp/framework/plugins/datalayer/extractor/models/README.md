# Model Server Extractor

**Type:** `model-server-protocol-models` | **Implementation:** [extractor.go](extractor.go)

The Model Server Extractor converts the response from a [`models-data-source`](../../source/models/README.md) into endpoint attributes consumed by filters and scorers.

For setup, configuration, and the complete wiring example see the [Models Data Source](../../source/models/README.md).

## What it does

1. Receives the parsed API response forwarded by [`models-data-source`](../../source/models/README.md).
2. Converts it into a [`ModelInfoCollection`](extractor.go#L19) — a slice of [`ModelInfo`](extractor.go#L22) entries, each with:
   - `ID` (string): model identifier (e.g. `"llama-3-8b"`).
   - `Parent` (string, optional): base model the adapter derives from.
3. Stores the collection as an attribute on the corresponding endpoint.

## Inputs consumed

- Parsed API response from a [`models-data-source`](../../source/models/README.md).

## Attributes produced

- [`ModelInfoCollection`](extractor.go#L19) stored at attribute key `/v1/models` on each endpoint.

```go
attr, ok := endpoint.GetAttributes().Get("/v1/models")
if !ok || attr == nil {
    return fmt.Errorf("no models found")
}
models, ok := attr.(models.ModelInfoCollection)
```

## Configuration

No configuration parameters.

```yaml
- type: model-server-protocol-models
  name: my-models-extractor
```

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
- [Models Data Source](../../source/models/README.md)
