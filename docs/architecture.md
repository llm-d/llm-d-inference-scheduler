# llm-d Inference Router Architecture

---

## Overview

**llm-d** is an extensible architecture designed to route inference requests efficiently across model-serving pods.
 A central component of this architecture is the **Inference Gateway**, which builds on the Kubernetes-native
 **Gateway API Inference Extension** to enable scalable, flexible, and pluggable routing of requests.

The design enables:

- Support for **multiple base models** within a shared cluster [Not supported in
Phase1]
- Efficient routing based on **KV cache locality**, **session affinity**, **load**, and
**model metadata**
- Disaggregated **Prefill/Decode (P/D)** execution
- Pluggable **filters**, **scorers**, and **scrapers** for extensible routing

---

## Core Goals

- Route inference requests to optimal pods based on:
  - Base model compatibility
  - KV cache reuse
  - Load balancing
- Support multi-model deployments on heterogeneous hardware
- Enable runtime extensibility with pluggable logic (filters, scorers, scrapers)
- Community-aligned implementation using GIE and Envoy + External Processing (EPP)

---

## Architecture Design

![Inference Gateway Architecture](./images/architecture.png)

The inference scheduler is built on top of:

- **Envoy** as a programmable data plane
- **EPP (External Processing Plugin)** using **GIE**

---

### Pluggability

![Pluggability Architecture](./images/plugability.png)

Routing decisions are governed by dynamic components:

- **Filters**: Exclude pods based on static or dynamic criteria
- **Scorers**: Assign scores to candidate pods
- **Scrapers**: Collect pod metadata and metrics for scorers

These components are maintained in the `llm-d-inference-scheduler` repository and can evolve independently.
A [sample filter plugin guide](./create_new_filter.md) is provided to illustrate how one could extend the
 Inference Gateway functionality to address unique requirements.

---

## Filters, Scorers, and Scrapers

### Core Design Principles

- **Pluggability**: No core changes are needed to add new scorers or filters
- **Isolation**: Each component operates independently

---

### Routing Flow

1. **Filtering**
   - Pods in an `InferencePool` go through a sequential chain of filters
   - Pods may be excluded based on criteria like model compatibility, resource usage, or custom logic

2. **Scoring**
   - Filtered pods are scored using a weighted set of scorers
   - Scorers currently run sequentially (future: parallel execution)
   - Scorers access a shared datastore populated by scrapers

3. **Pod Selection**
   - The highest-scored pod is selected
   - If multiple pods share the same score, one is selected at random

---

### Lifecycle Hooks

- `Pre-call`
- `Scoring`
- `Post-choice`
- `After-response`

---

## Configuration

The set of lifecycle hooks (plugins) that are used by the inference scheduler is determined by how
 it is configured. The configuration is in the form of YAML text, which can either be in a file or
 specified in-line as a parameter. The configuration defines the set of plugins to be instantiated
 along with their parameters. Each plugin is also given a name, enabling the same plugin type to be
 instantiated multiple times, if needed. Also defined is a set of SchedulingProfiles, which determine
 the set of plugins to be used when scheduling a request. The set of plugins instantiated must also
 include a Profile Handler, which determines which SchedulingProfiles will be used for a particular
 request and how their results will be processed.

The configuration text has the following form:

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- ....
- ....
schedulingProfiles:
- ....
- ....
```

The first two lines of the configuration are constant and must appear as is.

The plugins section defines the set of plugins that will be instantiated and their parameters.
 Each entry in this section has the following form:

```yaml
- name: aName
  type: a-type
  parameters:
    param1: val1
    param2: val2
```

The fields in a plugin entry are:
- **name** (optional): provides a name by which the plugin instance can be referenced. If this
field is omitted, the plugin's type will be used as its name.
- **type**: specifies the type of the plugin to be instantiated.
- **parameters** (optional): defines the set of parameters used to configure the plugin in question.
The actual set of parameters varies from plugin to plugin.

The schedulingProfiles section defines the set of scheduling profiles that can be used in scheduling
requests to pods. The number of scheduling profiles one defines, depends on the use case. For simple
serving of requests, one is enough. For disaggregated prefill, two profiles are required. Each entry
in this section has the following form:

```yaml
- name: aName
  plugins:
  - pluginRef: plugin1
  - pluginRef: plugin2
    weight: 50
```
The fields in a schedulingProfile entry are:
- **name**: specifies the scheduling profile's name.
- **plugins**: specifies the set of plugins to be used when this scheduling profile is chosen for a request.
  - **pluginRef**: reference to the name of the plugin instance to be used
  - **weight**: weight to be used if the referenced plugin is a scorer.

A complete configuration might look like this:

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: prefix-cache-scorer
  parameters:
    hashBlockSize: 5
    maxPrefixBlocksToMatch: 256
    lruCapacityPerServer: 31250
- type: decode-filter
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
  - pluginRef: prefix-cache-scorer
    weight: 50
```

If the configuration is in a file, the EPP command line argument `--configFile` should be used
 to specify the full path of the file in question. If the configuration is passed as in-line
 text the EPP command line argument `--configText` should be used.

---

### Plugin Configuration

This section describes how to setup the various plugins available with the llm-d-inference-scheduler

#### PrefillHeader

Sets a header for use in disaggregated prefill/decode

- **Type**: `prefill-header-handler`
- **Parameters**:
  - `prefillProfile`: specifies the name of the profile used for the prefill scheduling. Only needed if the prefill profile is not named `prefill`.

---

#### PdProfileHandler

Selects the profiles to use when running with disaggregated prefill/decode

- **Type**: `pd-profile-handler`
- **Parameters**:
  - `threshold`: specifies the threshold at which there are enough new input tokens to send the request to prefill and then decode, vs just to decode.
  - `hashBlockSize`: specifies the length of the prompt chunk that a block is keyed by. This must the same value used for the PrefixCachePlugin.
  - `decodeProfile`: specifies the name of the profile used for the decode scheduling. Only needed if the decode profile is not named `decode`.
  - `prefillProfile`: specifies the name of the profile used for the prefill scheduling. Only needed if the prefill profile is not named `prefill`.

**Note:** When using this plugin you must also have a PrefixCachePlugin configured in the prefill and decode scheduling profiles.

---

#### ByLabelSelector

Filters out pods using a standard Kubernetes label selector.

**Note:** Only the matching labels feature of Kubernetes label selectors is supported.

- **Type**: `by-label-selector`
- **Parameters**: A standard Kubernetes label selector.
  - `matchLabels`: map of `{key,value}` pairs. If more than one pair are in the map, all of the keys are checked and the results are combined with AND logic.

---

#### DecodeFilter

Filters out pods that are not marked either as decode or both prefill and decode. The filter looks for
 the label `llm-d.ai/role`, with a value of either `decode` or `both`. In addition pods that are missing
 the label will not be filtered out.

- **Type**: `decode-filter`
- **Parameters**: None

---

#### PrefillFilter

Filters out pods that are not marked as prefill. The filter looks for the label `llm-d.ai/role`, with a value of `prefill`.

- **Type**: `prefill-filter`
- **Parameters**: None

---

#### PrecisePrefixCacheScorer

The `precise-prefix-cache-scorer` scores a request based on KV-cache localities.
Similarly to the IGW `prefix-cache-scorer`, it provides a score based on the number of
 matching KV-cache blocks between the request's prompt and the KV-cache contents of each pod.
 However, unlike the IGW `prefix-cache-scorer`, which relies on estimations based on scheduling history,
 the `precise-prefix-cache-scorer` tracks the real-time KV-cache states across the vLLM instances to
 provide more accurate scoring.

When enabled, the scorer will use the `llm-d-kv-cache-manager` to track the KV-cache states
 across the vLLM instances. It will use the `kvcache.Indexer` to score the pods based on the
 number of matching blocks in the KV-cache. It will also use the `kvevents.Pool` to subscribe
 to the KV-Events emitted by the vLLM instances and update the KV-cache states in near-real-time.

Configuration:

- **Type**: `precise-prefix-cache-scorer`
- **Parameters**:
  - `indexerConfig`: Configuration for the `kvcache.Indexer`.
  - `kvEventsConfig`: Configuration for the `kvevents.Pool`.

See list of parameters at [llm-d-kv-cache-manager/docs/configuration.md](https://github.com/llm-d/llm-d-kv-cache-manager/blob/fa85b60207ba0a09daf23071e10ccb62d7977b40/docs/configuration.md).

Note that in most cases you will only need to set:
- HuggingFace token for the `tokenizersPoolConfig` or the `tokenizersCacheDir` to a mounted directory containing the tokenizers.
  - For the HuggingFace token, the inference-scheduler also accepts the environment variable `HF_TOKEN` - this is the practical option for security. 
- **IMPORTANT**: Token processor's block-size and hash-seed to match those used in the vLLM deployment.
- `KVBlockIndex` metrics to true if you wish to enable metrics for the KV-Block Index (admissions, evictions, lookups and hits).

Example configuration with the above parameters set:

```yaml
plugins:
  - type: precise-prefix-cache-scorer
    parameters:
      indexerConfig:
        tokenProcessorConfig:
          blockSize: 64
          hashSeed: "12345"
      tokenizersPoolConfig:
        huggingFaceToken: your_hf_token_here    # automatically set by `HF_TOKEN` environment variable
      kvBlockIndexConfig:
        enableMetrics: true
```

Example configuration with all parameters set:

```yaml
plugins:
  - type: precise-prefix-cache-scorer
    parameters:
        kvEventsConfig:
          zmqEndpoint: tcp://*:5557
          topicFilter: kv@
          concurrency: 8
        kvCacheIndexerConfig:
          prefixStoreConfig:
            cacheSize: 500000
            blockSize: 256
          tokenProcessorConfig:
            blockSize: 16
            hashSeed: "12345"
          kvBlockIndexConfig:
            inMemoryConfig:
              size: 100000000
              podCacheSize: 10
            enableMetrics: true
          tokenizersPoolConfig:
            workersCount: 8
            huggingFaceToken: your_hf_token_here    # automatically set by `HF_TOKEN` environment variable
            tokenizersCacheDir: /tmp/tokenizers
```

---

#### LoadAwareScorer

Scores pods based on their load, based on the number of requests concurrently being processed.
A threshold is provided which is used to determine what is considered an overloaded pod.

Scores are given to the pods in the range of 0-1. Currently the metrics contain the number of
requests waiting in the queue, there is no information about number of requests that can be
processed in the given pod immediately.

Pods with an empty waiting requests queue are scored with 0.5.

Pods with requests in the queue will get score between 0.5 and 0.

- **Type**: `load-aware-scorer`
- **Parameters**:
  - `threshold`: specifies the threshold at which a pod is considered overloaded.

---

#### ActiveRequestScorer

Scores pods based on the number of active requests being served per pod. Each request is tracked 
individually with its own TTL to ensure accurate timeout handling. Pods with fewer active 
requests receive higher scores.

Scores are normalized to a range of 0-1, where pods with fewer active requests get higher scores.

- **Type**: `active-request-scorer`
- **Parameters**:
  - `requestTimeout`: specifies the timeout for requests in seconds. Once a request is "in-flight" 
    for this duration, it is considered timed out and automatically removed.

---

#### SessionAffinity

Scores the candidate pods by giving a higher score to the pods that were previously
used for the same session.

- **Type**: `session-affinity-scorer`
- **Parameters**: None

---

#### NoHitLRUScorer

Scores pods based on least recently used (LRU) ordering for cold requests (requests with no KV cache hits).
This helps evenly distribute cache growth across pods, since cold requests result in new KV blocks being created.

The scorer integrates with a prefix cache plugin to determine if a request has cache hits:
- For cold requests (no cache hits): Ranks pods by LRU order, with never-used or least recently used pods
  receiving higher scores (up to 1.0) and most recently used pods receiving lower scores (approaching 0.0)
- For warm requests (cache hits): Returns neutral scores (0.5) for all pods to avoid interfering with
  cache locality optimization

The LRU tracking is specific to cold requests only - pods are added to the LRU cache when they serve
a cold request, not when they serve requests with cache hits.

- **Type**: `no-hit-lru-scorer`
- **Parameters**:
  - `prefixPluginName` (optional): The name of the prefix cache plugin to read state from. Defaults to `prefix-cache-scorer`.
  - `lruSize` (optional): The maximum number of pods to track in the LRU cache. Defaults to 1024.

Example configuration:

```yaml
plugins:
  - type: prefix-cache-scorer
    parameters:
      hashBlockSize: 5
      maxPrefixBlocksToMatch: 256
      lruCapacityPerServer: 31250
  - type: no-hit-lru-scorer
    parameters:
      lruSize: 2048
  - type: decode-filter
  - type: max-score-picker
  - type: single-profile-handler
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: decode-filter
      - pluginRef: max-score-picker
      - pluginRef: prefix-cache-scorer
        weight: 2
      - pluginRef: no-hit-lru-scorer
        weight: 1
```

**Note:** This scorer is designed to work alongside a prefix cache scorer (such as `prefix-cache-scorer` or
`precise-prefix-cache-scorer`). If no prefix cache state is available, all requests are treated as cold.
When integrating with a prefix-cache scorer, the prefix-cache scorer should be defined first in the scheduling profile.

---

### Sample Disaggregated Prefill/Decode Configuration

The following is an example of what a configuration for disaggregated Prefill/Decode might look like:

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: prefill-header-handler
- type: prefix-cache-scorer
  parameters:
    hashBlockSize: 5
    maxPrefixBlocksToMatch: 256
    lruCapacityPerServer: 31250
- type: prefill-filter
- type: decode-filter
- type: max-score-picker
- type: pd-profile-handler
  parameters:
    threshold: 10
    hashBlockSize: 5
schedulingProfiles:
- name: prefill
  plugins:
  - pluginRef: prefill-filter
  - pluginRef: max-score-picker
  - pluginRef: prefix-cache-scorer
    weight: 50
- name: decode
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
  - pluginRef: prefix-cache-scorer
    weight: 50
```

Several things should be noted:
1. The `PrefillHeader`, `PdProfileHandler`, `DecodeFilter`, `PrefillFilter` and the `PrefixCachePlugin`
 plugins must be in the list of plugins instantiated.
2. There must be two scheduler profiles defined.
3. The scheduler profile for prefill, must include the `PrefillFilter`
4. The scheduler profile for decode, must include the `DecodeFilter`

---

## Metric Scraping

- Scrapers collect metrics (e.g., memory usage, active adapters)
- Data is injected into the shared datastore for scorers
- Scoring can rely on numerical metrics or metadata (model ID, adapter tags)

---

## Disaggregated Prefill/Decode (P/D)

When enabled, the router:

- Selects one pod for **Prefill** (prompt processing)
- Selects another pod for **Decode** (token generation)

The **vLLM sidecar** handles orchestration between Prefill and Decode stages. It allows:

- Queuing
- Local memory management
- Experimental protocol compatibility

> **Note**: The detailed P/D design is available in this document: [Disaggregated Prefill/Decode in llm-d](./dp.md)

---

## InferencePool & InferenceModel Design

### Current Assumptions

- Single `InferencePool` and single `EPP` due to Envoy limitations
- Model-based filtering can be handled within EPP
- Currently only one base model is supported

> [!NOTE]
> The `InferenceModel` CRD is in the process of being significantly changed in IGW.
> Once finalized, these changes would be reflected in llm-d as well.

---

## References

- [GIE Spec](https://gateway-api-inference-extension.sigs.k8s.io/)
- [Envoy External Processing](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_proc_filter)
