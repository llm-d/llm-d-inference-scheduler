# data-parallel-profile-handler

> **Deprecated:** Use `simple-profile-handler` with Istio >= 1.28.1.

Handles scheduler profile selection for data-parallel (DP) serving
topologies where multiple identical pods share the same model.

## Type

`data-parallel-profile-handler`

## Configuration

```yaml
- type: data-parallel-profile-handler
  parameters:
    primaryPort: 8000   # base port for data-parallel communication (default: 8000)
```

## When to use

Only use this handler if you are running Istio < 1.28.1. For all new
deployments, use `simple-profile-handler` instead.
