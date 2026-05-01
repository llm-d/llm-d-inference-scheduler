# Label-Based Filter Plugins

Filters inference requests to pods based on Kubernetes labels. This
package provides five filter plugins: three role-based filters for
disaggregated serving, and two generic label filters.

## Role-based filters

### `decode-filter`

Passes only pods designated as decode workers.

**Type:** `decode-filter`

**Accepted `llm-d.ai/role` values:** `decode`, `prefill-decode`, `both`, `encode-prefill-decode`

**Pods with no role label:** excluded

```yaml
- type: decode-filter
```

### `prefill-filter`

Passes only pods designated as prefill workers.

**Type:** `prefill-filter`

**Accepted `llm-d.ai/role` values:** `prefill`, `encode-prefill`, `prefill-decode`, `both`, `encode-prefill-decode`

**Pods with no role label:** excluded

```yaml
- type: prefill-filter
```

### `encode-filter`

Passes only pods designated as encode workers (first stage in E/P/D pipeline).

**Type:** `encode-filter`

**Accepted `llm-d.ai/role` values:** `encode`, `encode-prefill`, `encode-prefill-decode`

**Pods with no role label:** excluded

```yaml
- type: encode-filter
```

## Generic label filters

### `by-label`

Passes pods whose label value matches one of a configured set of values.

**Type:** `by-label`

```yaml
- type: by-label
  parameters:
    label: "my-label"
    validValues: ["value1", "value2"]
    allowsNoLabel: false   # if true, pods without the label are also passed
```

### `by-label-selector`

Passes pods matching a Kubernetes label selector expression (supports
`matchLabels` and `matchExpressions` with AND logic).

**Type:** `by-label-selector`

```yaml
- type: by-label-selector
  parameters:
    matchLabels:
      app: my-app
    matchExpressions:
      - key: tier
        operator: In
        values: ["fast", "medium"]
```

## When to use

Use `decode-filter` and `prefill-filter` together in P/D disaggregated
scheduling profiles. Use `encode-filter` for E/P/D multimodal pipelines.
Use `by-label` or `by-label-selector` for custom routing requirements
beyond the built-in role system.
