# Model Data Extractor

**Type:** `models-data-extractor`

The Model Server Extractor converts the response from a [`models-data-source`](../../source/models/README.md) into endpoint attributes consumed by filters and scorers.

For setup, configuration, and the complete wiring example see the [Models Data Source](../../source/models/README.md).

## What it does

1. Receives the parsed API response forwarded by [`models-data-source`](../../source/models/README.md).
2. Converts it into a `ModelDataCollection` — a slice of `ModelData` entries, each with:
   - `ID` (string): model identifier (e.g. `"llama-3-8b"`).
   - `Parent` (string, optional): base model the adapter derives from.
3. Stores the collection as an attribute on the corresponding endpoint.

## Inputs consumed

- Parsed API response from a [`models-data-source`](../../source/models/README.md).

## Attributes produced

- `ModelDataCollection` stored at attribute key [`ModelsAttributeKey`](extractor.go) (`"/v1/models"`) on each endpoint.

```go
attr, ok := endpoint.GetAttributes().Get(models.ModelsAttributeKey)
if !ok || attr == nil {
    return fmt.Errorf("no models found")
}
modelData, ok := attr.(models.ModelDataCollection)
```

## Configuration

No configuration parameters.
