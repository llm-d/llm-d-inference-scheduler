# ByLabel Filter Plugins

Label-based filters that retain or remove candidate pods based on Kubernetes label values. Includes two general-purpose filters and three pre-configured role filters for disaggregated inference architectures.

## ByLabel

**Type:** `by-label`

### Behavior

Filters out pods that do not have a specific label with one of the allowed values. Pods missing the label are either filtered out or retained based on the `allowsNoLabel` setting.

### Config

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `label` | `string` | Yes | — | The name of the Kubernetes label to inspect on each pod. |
| `validValues` | `[]string` | Yes (unless `allowsNoLabel=true`) | — | List of acceptable label values. A pod is kept if its value matches any entry. |
| `allowsNoLabel` | `bool` | No | `false` | If `true`, pods that lack the label entirely are included. If `false`, they are filtered out. |

### Inputs

- Pod Kubernetes labels (read from the candidate pod's metadata).

**Configuration Example:**
```yaml
plugins:
  - type: by-label
    parameters:
      label: "gpu.type"
      validValues: ["a100"]
      allowsNoLabel: false
```

In this example:
- Only pods labeled with the specific GPU type (`gpu.type=a100`) are selected.
- Pods missing the `gpu.type` label are not considered for scheduling.

---

## ByLabelSelector

**Type:** `by-label-selector`

### Behavior

Filters out pods using a standard Kubernetes label selector.

> [!NOTE]
> Only the matching labels feature of Kubernetes label selectors is supported.

### Config

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `matchLabels` | `map[string]string` | Yes | — | Map of `{key: value}` pairs. All pairs must match (AND logic). |

### Inputs

- Pod Kubernetes labels (read from the candidate pod's metadata).

**Configuration Example:**
```yaml
plugins:
  - type: by-label-selector
    parameters:
      matchLabels:
        inference-role: decode
        hardware-type: H100
```

In this example:
- Only pods that have **both** labels `inference-role=decode` **and** `hardware-type=H100` will be selected.
- Pods missing either label, or having a different value, are **filtered out**.
- All key-value pairs in `matchLabels` must match (**AND** logic).

---

## Role-Based Filters

Pre-configured `by-label` filters for disaggregated inference architectures. Each checks the `llm-d.ai/role` label on candidate pods.

**Example Target Pod:**
```yaml
apiVersion: v1
kind: Pod
metadata:
  labels:
    llm-d.ai/role: "decode"
spec:
  # ... pod specification
```

#### Inference Roles

| Role | Description |
|------|-------------|
| `encode` | Encode stage only |
| `prefill` | Prefill stage only |
| `decode` | Decode stage only |
| `both` | Prefill + Decode (alias for `prefill-decode`) |
| `encode-prefill` | Encode + Prefill |
| `prefill-decode` | Prefill + Decode |
| `encode-prefill-decode` | All stages (monolithic) |

### EncodeRole Filter

#### Behavior

**Type:** `encode-filter`

Filters out pods not marked as encode. Retains pods whose `llm-d.ai/role` value is `encode`, `encode-prefill`, or `encode-prefill-decode`.

#### Config

No parameters.

#### Inputs

- `llm-d.ai/role` pod label.

**Configuration Example:**
```yaml
plugins:
  - type: encode-filter
```

### PrefillRole Filter

#### Behavior

**Type:** `prefill-filter`

Filters out pods not marked as prefill. Retains pods whose `llm-d.ai/role` value is `prefill`, `encode-prefill`, `prefill-decode`, `both`, or `encode-prefill-decode`.

#### Config

No parameters.

#### Inputs

- `llm-d.ai/role` pod label.

**Configuration Example:**
```yaml
plugins:
  - type: prefill-filter
```

### DecodeRole Filter

#### Behavior

**Type:** `decode-filter`

Filters out pods not authorized for the decode stage. Retains pods whose `llm-d.ai/role` value is `decode`, `prefill-decode`, `both`, or `encode-prefill-decode`. Pods that completely lack the `llm-d.ai/role` label are not filtered out.

#### Config

No parameters.

#### Inputs

- `llm-d.ai/role` pod label.

**Configuration Example:**
```yaml
plugins:
  - type: decode-filter
```

---

## Related Documentation
- [Creating a Custom Filter](../../../../../../../docs/create_new_filter.md)
