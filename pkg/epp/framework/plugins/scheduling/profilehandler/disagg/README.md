# Disaggregated Profile Handler, PreRequest, and Decider Plugins

## Contents

- [Profile Handlers](#profile-handlers)
  - [DisaggProfileHandler](#disaggprofilehandler)
  - [PdProfileHandler (Deprecated)](#pdprofilehandler-deprecated)
- [PreRequest Plugins](#prerequest-plugins)
  - [DisaggHeadersHandler](#disaggheadershandler)
  - [PrefillHeaderHandler (Deprecated)](#prefillheaderhandler-deprecated)
- [Decider Plugins](#decider-plugins)
  - [PrefixBasedPDDecider](#prefixbasedpddecider)
  - [AlwaysDisaggPDDecider](#alwaysdisaggpddecider)
  - [AlwaysDisaggMultimodalDecider](#alwaysdisaggmultimodaldecider)

---

## Profile Handlers

### DisaggProfileHandler

**Type:** `disagg-profile-handler` | **Implementation:** [disagg_profile_handler.go](disagg_profile_handler.go)

Selects the scheduling profiles to use when running with disaggregation. Supports monolithic (D), two-stage (P/D), three-stage (E/P/D), and encode-prefill (E/PD) modes.

> Note: When using this plugin with P/D disaggregation, you must also have a PrefixCachePlugin configured in the prefill and decode scheduling profiles.

**Parameters:**
- `profiles` (optional): Names of scheduling profiles to use. Defaults match the profile names.
  - `decode` (string, optional, default: `"decode"`): Name of the decode scheduling profile.
  - `prefill` (string, optional, default: `"prefill"`): Name of the prefill scheduling profile.
  - `encode` (string, optional, default: `"encode"`): Name of the encode scheduling profile.
- `deciders` (optional): Decider plugins that control whether each disaggregation stage runs.
  - `prefill` (string, optional): Name of the prefill decider plugin. When set, enables P/D disaggregation.
  - `encode` (string, optional): Name of the encode decider plugin. When set, enables E disaggregation.

**Configuration Example — Decode-only (no disaggregation):**
```yaml
plugins:
  - type: disagg-profile-handler
```

**Configuration Example — P/D disaggregation:**
```yaml
plugins:
  - type: disagg-profile-handler
    parameters:
      deciders:
        prefill: prefix-based-pd-decider
```

**Configuration Example — E/P/D disaggregation:**
```yaml
plugins:
  - type: disagg-profile-handler
    parameters:
      deciders:
        prefill: prefix-based-pd-decider
        encode: always-disagg-multimodal-decider
```

### PdProfileHandler (Deprecated)

**Type:** `pd-profile-handler` | **Implementation:** [pd_profile_handler.go](pd_profile_handler.go)

> **Deprecated:** Use `disagg-profile-handler` instead.

---

## PreRequest Plugins

### DisaggHeadersHandler

**Type:** `disagg-headers-handler` | **Implementation:** [disagg_headers_handler.go](disagg_headers_handler.go)

Sets headers for use in disaggregated prefill/decode and encode/prefill/decode.

- **`x-prefiller-host-port`** — `<ip:port>` of the selected prefill pod. Absent when P/D disaggregation was skipped.
- **`x-encoder-hosts-ports`** — comma-separated `<ip:port>` list of selected encode pods. Absent when encode disaggregation was skipped.

**Parameters:**
- `prefillProfile` (string, optional): Name of the profile used for prefill scheduling. Only needed if the prefill profile is not named `prefill`.
- `encodeProfile` (string, optional): Name of the profile used for encode scheduling. Only needed if the encode profile is not named `encode`.

**Configuration Example:**
```yaml
plugins:
  - type: disagg-headers-handler
```

Custom profile names:
```yaml
plugins:
  - type: disagg-headers-handler
    parameters:
      prefillProfile: "my-prefill"
      encodeProfile: "my-encode"
```

### PrefillHeaderHandler (Deprecated)

**Type:** `prefill-header-handler` | **Implementation:** [disagg_headers_handler.go](disagg_headers_handler.go)

> **Deprecated:** Use `disagg-headers-handler` instead.

---

## Decider Plugins

### PrefixBasedPDDecider

**Type:** `prefix-based-pd-decider` | **Implementation:** [prefix_based_pd_decider.go](prefix_based_pd_decider.go)

Makes P/D disaggregation decisions based on KV cache prefix matching. Disaggregates only when the non-cached portion of the user input exceeds a threshold, avoiding disaggregation overhead for short or well-cached requests.

> Note: The `prepareDataPlugins` feature gate must be enabled.

**Parameters:**
- `nonCachedTokens` (int, required): Length in tokens of the uncached portion of the user input above which disaggregated P/D is triggered.

**Configuration Example:**
```yaml
featureGates:
- prepareDataPlugins
plugins:
  - type: prefix-based-pd-decider
    parameters:
      nonCachedTokens: 512
  - type: disagg-profile-handler
    parameters:
      deciders:
        prefill: prefix-based-pd-decider
```

In this example:
- P/D disaggregation is triggered only when 512 or more tokens of the input are uncached.
- The `disagg-profile-handler` references the decider by its type name.

### AlwaysDisaggPDDecider

**Type:** `always-disagg-pd-decider` | **Implementation:** [always_disagg_pd_decider.go](always_disagg_pd_decider.go)

Always approves P/D disaggregation for every request. Useful for testing or forcing disaggregation unconditionally.

**Parameters:** None.

**Configuration Example:**
```yaml
plugins:
  - type: always-disagg-pd-decider
    name: always-pd
  - type: disagg-profile-handler
    parameters:
      deciders:
        prefill: always-pd
```

### AlwaysDisaggMultimodalDecider

**Type:** `always-disagg-multimodal-decider` | **Implementation:** [always_disagg_mm_decider.go](always_disagg_mm_decider.go)

Approves encode disaggregation for requests that contain multimodal content (images, audio). Text-only requests are not disaggregated.

**Parameters:** None.

**Configuration Example:**
```yaml
plugins:
  - type: always-disagg-multimodal-decider
    name: mm-decider
  - type: disagg-profile-handler
    parameters:
      deciders:
        encode: mm-decider
```

In this example:
- Encode disaggregation is triggered only for requests containing multimodal content (image URLs, audio).
- Text-only requests proceed through decode-only scheduling.

---

## Related Documentation

- [Disaggregation Architecture](../../../../../../../docs/disaggregation.md)
- [Architecture Overview](../../../../../../../docs/architecture.md)
- [SingleProfileHandler](../single/)
- [Filter Plugins](../../filter/bylabel/README.md)
