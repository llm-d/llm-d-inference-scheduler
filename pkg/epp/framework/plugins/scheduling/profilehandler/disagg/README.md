# Disaggregated Profile Handler Plugins

Profile handler plugins that orchestrate prefill/decode (P/D) and
encode/prefill/decode (E/P/D) disaggregated serving by selecting which
scheduling profiles to run for each request.

## Plugins

### `disagg-profile-handler`

Selects prefill, encode, and decode scheduling profiles based on a
configurable decider plugin. The decider determines whether a request
should be disaggregated or served entirely on decode pods.

**Type:** `disagg-profile-handler`

```yaml
- type: disagg-profile-handler
  parameters:
    deciders:
      prefill: prefix-based-pd-decider      # decider for P/D disaggregation
      encode: always-disagg-multimodal-decider  # decider for E/P/D
    decodeProfile: "decode"    # name of the decode scheduling profile (default: "decode")
    prefillProfile: "prefill"  # name of the prefill scheduling profile (default: "prefill")
    encodeProfile: "encode"    # name of the encode scheduling profile (default: "encode")
```

### `disagg-headers-handler`

PreRequest plugin that wires prefill and encode scheduling profile
results into HTTP headers, telling the decode worker which prefill
and encode pods to contact for KV-cache transfer.

Sets `x-prefiller-url` (prefill pod address) and encoder endpoint
headers based on the scheduling result.

**Type:** `disagg-headers-handler`

```yaml
- type: disagg-headers-handler
  parameters:
    prefillProfile: "prefill"  # scheduling profile name for prefill (default: "prefill")
    encodeProfile: "encode"    # scheduling profile name for encode (default: "encode")
```

### `prefix-based-pd-decider`

Decides whether to disaggregate a request into prefill and decode phases
based on the number of non-cached tokens. Requests below the threshold
are served on decode pods only (avoiding disaggregation overhead).

**Type:** `prefix-based-pd-decider`

```yaml
- type: prefix-based-pd-decider
  parameters:
    nonCachedTokens: 100   # minimum uncached tokens to trigger P/D disaggregation
```

### `always-disagg-multimodal-decider`

Always triggers encode disaggregation for multimodal requests
(those containing image, video, or audio inputs).

**Type:** `always-disagg-multimodal-decider`

```yaml
- type: always-disagg-multimodal-decider
```

## When to use

Use `disagg-profile-handler` + `disagg-headers-handler` together when
running P/D or E/P/D disaggregated deployments. Pair with
`prefix-based-pd-decider` to skip disaggregation for short or
fully-cached prompts where the overhead outweighs the benefit.
