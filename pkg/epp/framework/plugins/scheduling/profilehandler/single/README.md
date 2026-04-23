# SingleProfileHandler

**Type:** `single-profile-handler` | **Implementation:** [single_profile_handler.go](single_profile_handler.go)

Handles requests using a single scheduling profile, which is always selected as the primary profile. Auto-injected when exactly one `schedulingProfile` is defined and no profile handler is explicitly configured.

**Parameters:** None.

**Configuration Example:**
```yaml
plugins:
  - type: single-profile-handler
```

> Note: Normally you do not need to configure this plugin explicitly — it is auto-injected.

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
- [Disagg Profile Handler](../disagg/)
- [DataParallel Profile Handler](../dataparallel/)
