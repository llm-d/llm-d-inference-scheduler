# SingleProfileHandler

**Type:** `single-profile-handler`

Handles requests using a single scheduling profile, which is always selected as the primary profile. Auto-injected when exactly one `schedulingProfile` is defined and no profile handler is explicitly configured.

**Parameters:** None.

> [!NOTE]
> This plugin is framework-injected by default when exactly one scheduling profile is defined and no profile handler is configured. You do not need to explicitly declare it in your configuration.

---

## Related Documentation
- [Disagg Profile Handler](../disagg/)
- [DataParallel Profile Handler](../dataparallel/)
