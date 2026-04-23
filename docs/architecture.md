# llm-d Inference Scheduler Architecture

## Table of Contents

- [Overview](#overview)
- [Core Goals](#core-goals)
- [Architecture Design](#architecture-design)
  - [Pluggability](#pluggability)
- [Filters, Scorers, and Scrapers](#filters-scorers-and-scrapers)
  - [Core Design Principles](#core-design-principles)
  - [Routing Flow](#routing-flow)
  - [Lifecycle Hooks](#lifecycle-hooks)
  - [Configuration](#configuration)
    - [`Plugins` Configuration](#plugins-configuration)
    - [`SchedulingProfiles` Configuration](#schedulingprofiles-configuration)
  - [Available Plugins](#available-plugins)
- [Metric Scraping](#metric-scraping)
- [Disaggregated Encode/Prefill/Decode (E/P/D)](#disaggregated-encodeprefilldecodesepd-epd)
- [InferencePool & InferenceModel Design](#inferencepool--inferencemodel-design)
  - [Current Assumptions](#current-assumptions)
- [References](#references)

---

## Overview

**llm-d** is an extensible architecture designed to schedule inference requests efficiently across model-serving pods.
 A central component of this architecture is the **Inference Gateway**, which builds on the Kubernetes-native
 **Gateway API Inference Extension** (GIE) to enable scalable, flexible, and pluggable request scheduling.

The design enables:

- Support for **multiple base models** within a shared cluster (see [serving multiple inference pools](#inferencepool--inferencemodel-design))
- Efficient routing based on **KV cache locality**, **session affinity**, **load**, and
**model metadata**
- Disaggregated **Prefill/Decode (P/D)** execution
  - We have introduced experimental **Encode/Prefill/Decode (E/P/D and all its permutations)** execution. For a detailed explanation, see [Disaggregated Inference Serving](./disaggregation.md)
- Pluggable **filters**, **scorers**, and **scrapers** for extensible scheduling

---

## Core Goals

- Schedule inference requests to optimal pods based on:
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

- The [Envoy] gateway, as a programmable data plane.
- An [EPP] (Endpoint Picker), making scheduling decisions, as the control plane.
  The llm-d inference scheduler extends the EPP in [GIE] with state of the art
  scheduling algorithms.
- An optional BBR (Body Based Routing) component, to associate requests with
  their corresponding model before the EPP is consulted.

[Envoy]:https://www.envoyproxy.io/
[EPP]:../cmd/epp/
[GIE]:../README.md#relation-to-gie-igw

---

### Pluggability

![Pluggability Architecture](./images/plugability.png)

Routing decisions are governed by dynamic components:

- **Profile Handlers**: Implement `scheduling.ProfileHandler` and control which scheduling profiles run and in what order
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

### Configuration

The inference scheduler relies on a YAML-based configuration—provided either as a file or an in-line parameter—to determine which lifecycle hooks (plugins) are active.

Specifically, this configuration establishes the following components:

- `Plugins`: The specific plugins to instantiate, along with their parameters. Because each instantiated plugin is assigned a unique name, you can configure the same plugin type multiple times if necessary.

- `SchedulingProfiles`: A collection of profiles that dictate the exact set of plugins invoked when scheduling a given request.

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

### `Plugins` Configuration

The `plugins` section in the configuration defines the set of plugins that will be instantiated and their parameters. Each entry in this section has the following form:

```yaml
- name: aName
  type: a-type
  parameters:
    param1: val1
    param2: val2
```

#### `Plugin` Fields:

The fields in a plugin entry are:

- **name** (optional): provides a name by which the plugin instance can be referenced. If this field is omitted, the plugin's type will be used as its name.
- **type**: specifies the type of the plugin to be instantiated.
- **parameters** (optional): defines the set of parameters used to configure the plugin in question. The actual set of parameters varies from plugin to plugin.

### `SchedulingProfiles` Configuration

The `schedulingProfiles` section defines the set of scheduling profiles that can be used in scheduling
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

#### `SchedulingProfile` Fields

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
- type: precise-prefix-cache-scorer
  parameters:
    indexerConfig:
      tokenProcessorConfig:
        blockSize: 5
      kvBlockIndexConfig:
        maxPrefixBlocksToMatch: 256
- type: decode-filter
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
  - pluginRef: precise-prefix-cache-scorer
    weight: 50
```

If the configuration is in a file, the EPP command line argument `--configFile` should be used
 to specify the full path of the file in question. If the configuration is passed as in-line
 text the EPP command line argument `--configText` should be used.

---

### Available plugins

The available plugins are grouped into five core categories based on their role within the request processing pipeline, as outlined below. For more detailed information, please refer to the README files located alongside each plugin's source code.

<img src="./images/plugins.png" alt="Plugin Execution Pipeline" width="100%"/>

#### 1. Preparation & Setup

- **[Data Layer](../pkg/epp/framework/plugins/datalayer/)**: Runs continuously in the background to monitor the health and stats of all the pods (servers) so the system knows what's available.
  - **Default:** [`metrics-data-source`](../pkg/epp/framework/plugins/datalayer/source/metrics/) + [`core-metrics-extractor`](../pkg/epp/framework/plugins/datalayer/extractor/metrics/) (framework-injected, no config needed)
  - **Interface:** [`DataSource`](../pkg/epp/framework/interface/datalayer/plugin.go) · [`Extractor`](../pkg/epp/framework/interface/datalayer/plugin.go)
  - **Reference**: [datalayer/source/](../pkg/epp/framework/plugins/datalayer/source/), [datalayer/extractor/](../pkg/epp/framework/plugins/datalayer/extractor/)

- **[Parsers](../pkg/epp/framework/plugins/requesthandling/parsers/) & [Producers](../pkg/epp/framework/plugins/requestcontrol/dataproducer/)**: Parsers inspect incoming HTTP and gRPC request payloads to extract the model name and prompt. Producers enrich the request cycle state with additional metadata (e.g., token counts, prefix hashes, latency predictions) consumed by downstream scheduling plugins like scorers and admitters.
  - **Default:** [`openai-parser`](../pkg/epp/framework/plugins/requesthandling/parsers/openai/) (framework-injected, no config needed)
  - **Interface:** [`Parser`](../pkg/epp/framework/interface/requesthandling/plugins.go) · [`DataProducer`](../pkg/epp/framework/interface/requestcontrol/plugins.go)
  - **Reference**: [requesthandling/parsers/](../pkg/epp/framework/plugins/requesthandling/parsers/), [requestcontrol/dataproducer/](../pkg/epp/framework/plugins/requestcontrol/dataproducer/)

- **[Flow Control](../pkg/epp/framework/plugins/flowcontrol/) & [Admitters](../pkg/epp/framework/plugins/requestcontrol/admitter/)**: Admitters act as the first line of defense, rejecting requests upfront if the endpoints cannot meet SLOs. Once a request enters the queue, flow control takes over to manage dispatching. It prevents system saturation by enforcing priority bands, per-flow fairness, FCFS ordering, and strict usage limits.
  - **Default:** [`utilization-detector`](../pkg/epp/framework/plugins/flowcontrol/saturationdetector/utilization/) + [`fcfs-ordering-policy`](../pkg/epp/framework/plugins/flowcontrol/ordering/fcfs/) + [`global-strict-fairness-policy`](../pkg/epp/framework/plugins/flowcontrol/fairness/globalstrict/) + [`static-usage-limit-policy`](../pkg/epp/framework/plugins/flowcontrol/usagelimits/) (framework-injected, no config needed)
  - **Interface:** [`Admitter`](../pkg/epp/framework/interface/requestcontrol/plugins.go) · [`SaturationDetector`](../pkg/epp/framework/interface/flowcontrol/plugins.go) · [`FairnessPolicy`](../pkg/epp/framework/interface/flowcontrol/plugins.go) · [`OrderingPolicy`](../pkg/epp/framework/interface/flowcontrol/plugins.go) · [`UsageLimitPolicy`](../pkg/epp/framework/interface/flowcontrol/plugins.go)
  - **Reference**: [flowcontrol/](../pkg/epp/framework/plugins/flowcontrol/), [requestcontrol/admitter/](../pkg/epp/framework/plugins/requestcontrol/admitter/)

#### 2. Routing Logic

- **[Profile Handlers & Deciders](../pkg/epp/framework/plugins/scheduling/profilehandler/)**: Orchestrates the selection and execution order of scheduling profiles. Every configuration must include exactly one handler.
  - **Default:** [`single-profile-handler`](../pkg/epp/framework/plugins/scheduling/profilehandler/single/) (framework-injected when exactly one scheduling profile is defined and no handler is specified)
  - **Interface:** [`ProfileHandler`](../pkg/epp/framework/interface/scheduling/plugins.go)
  - **Reference**: [scheduling/profilehandler/](../pkg/epp/framework/plugins/scheduling/profilehandler/)

#### 3. Filtering

- **[Filters](../pkg/epp/framework/plugins/scheduling/filter/)**: Excludes pods based on labels, label selectors, specific pod roles, prefix cache state, or SLO headroom.
  - **Interface:** [`Filter`](../pkg/epp/framework/interface/scheduling/plugins.go)
  - **Reference**: [scheduling/filter/bylabel/](../pkg/epp/framework/plugins/scheduling/filter/bylabel/), [scheduling/filter/prefixcacheaffinity/](../pkg/epp/framework/plugins/scheduling/filter/prefixcacheaffinity/), [scheduling/filter/sloheadroomtier/](../pkg/epp/framework/plugins/scheduling/filter/sloheadroomtier/)

#### 4. Scoring & Selection

- **[Scorers](../pkg/epp/framework/plugins/scheduling/scorer/)**: Scores pods using metrics such as [KV-cache prefix matching](../pkg/epp/framework/plugins/scheduling/scorer/preciseprefixcache/), [session affinity](../pkg/epp/framework/plugins/scheduling/scorer/sessionaffinity/), [current load](../pkg/epp/framework/plugins/scheduling/scorer/loadaware/), and [active request counts](../pkg/epp/framework/plugins/scheduling/scorer/activerequest/). Each scorer returns a value in `[0, 1]` per pod; that value is multiplied by the scorer's `weight` (set in `schedulingProfiles`) and accumulated across all scorers into a final score per pod. The pod with the highest total is selected. Weight controls each scorer's relative influence — omitting it defaults to `0`, meaning the scorer has no effect.
  - **Interface:** [`Scorer`](../pkg/epp/framework/interface/scheduling/plugins.go)
  - **Reference**: [scheduling/scorer/](../pkg/epp/framework/plugins/scheduling/scorer/)

- **[Pickers](../pkg/epp/framework/plugins/scheduling/picker/)**: Select one or more candidate endpoints from the scored set for the final routing decision.
  - **Default:** [`max-score-picker`](../pkg/epp/framework/plugins/scheduling/picker/maxscore/) (framework-injected, no config needed)
  - **Interface:** [`Picker`](../pkg/epp/framework/interface/scheduling/plugins.go)
  - **Reference**: [scheduling/picker/](../pkg/epp/framework/plugins/scheduling/picker/)

#### 5. Execution & Delivery

- **[PreRequest Plugins](../pkg/epp/framework/plugins/scheduling/profilehandler/disagg/)**: Run after scheduling and before the request is forwarded. Translate scheduling results into HTTP headers consumed by the vLLM sidecar.
  - **Interface:** [`PreRequest`](../pkg/epp/framework/interface/requestcontrol/plugins.go)
  - **Reference**: [scheduling/profilehandler/](../pkg/epp/framework/plugins/scheduling/profilehandler/)

- **[Response Processing](../pkg/epp/framework/plugins/requestcontrol/requestattributereporter/)**: Adds logging and metadata to the final generated text before handing it back to the user.
  - **Interface:** [`ResponseHeaderProcessor`](../pkg/epp/framework/interface/requestcontrol/plugins.go) · [`ResponseBodyProcessor`](../pkg/epp/framework/interface/requestcontrol/plugins.go)
  - **Reference**: [requestcontrol/requestattributereporter/](../pkg/epp/framework/plugins/requestcontrol/requestattributereporter/)


---

## Metric Scraping

- Scrapers collect metrics (e.g., memory usage, active adapters)
- Data is injected into the shared datastore for scorers
- Scoring can rely on numerical metrics or metadata (model ID, adapter tags)

---

## Disaggregated Encode/Prefill/Decode (E/P/D)

When enabled, the router:

- Selects one pod for **Prefill** (prompt processing)
- Selects another pod for **Decode** (token generation)

> [!NOTE] 
> Encode disaggregation is an experimental feature. When enabled, the router 
> identifies all pods capable of encoding, and the vLLM sidecar distributes multimedia 
> requests to randomly selected pods from that subset. More sophisticated selection 
> strategies are planned for future versions.

The **vLLM sidecar** handles orchestration between Encode, Prefill and Decode stages. It allows:

- Queuing
- Local memory management
- Experimental protocol compatibility

> [!NOTE]
> The detailed E/P/D design is available in this document:
> [Disaggregated Inference Serving in llm-d](./disaggregation.md)

---

## InferencePool & InferenceModel Design

### Current Assumptions

- Single `InferencePool` and single `EPP` due to Envoy limitations
- Model-based filtering can be handled within EPP
- Currently only one base model **per `InferencePool`** is supported.
  Multiple models are supported via multiple `InferencePools`.

> [!NOTE]
> The `InferenceModel` CRD is in the process of being significantly changed in IGW.
> Once finalized, these changes would be reflected in llm-d as well.

---

## References

- [GIE Spec](../README.md#relation-to-gie-igw)
- [Envoy External Processing](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_proc_filter)
