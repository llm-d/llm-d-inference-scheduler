# DataParallel Profile Handler

**Type:** `data-parallel-profile-handler`

> **Deprecated:** Use `single-profile-handler` with Istio >= 1.28.1 instead. See [single/](../single/).

Provides a profile handler for data-parallel inference routing, where a request is scheduled to one pod among multiple replicas serving the same model. Injects the `X-Data-Parallel-Endpoint` header pointing to the selected pod and rewrites the target port to `primaryPort`.

**Constraints:**
- Requires exactly one scheduling profile in the config.

**Parameters:**
- `primaryPort` (int, optional, default: `8000`): Primary service port (1–65535).

**Configuration Example:**
```yaml
plugins:
  - type: data-parallel-profile-handler
    name: dp-handler
    parameters:
      primaryPort: 8000
```

**Migration:** Replace with `single-profile-handler` (requires Istio >= 1.28.1):

**Before:**
```yaml
plugins:
  - type: data-parallel-profile-handler
    parameters:
      primaryPort: 8000
```

**After:**
```yaml
plugins:
  - type: single-profile-handler
```

---

## Related Documentation
- [SingleProfileHandler](../single/)
- [Disagg Profile Handler](../disagg/)
