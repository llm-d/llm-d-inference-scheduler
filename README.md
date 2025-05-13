# Inference Scheduler

This project provides dynamic and pluggable scheduler components for AI
Inference requests for the LLM-d inference framework.

## About

This repository provides scheduler components for routing AI inference
requests, which get loaded into the Gateway component of LLM-d. In particular,
an "Endpoint Picker (EPP)" binary and container images are provided here which
can be configured via [Envoy]'s [ext-proc] feature to make optimized routing
decisions for AI Inference requests to backend model serving platforms (e.g.
[VLLM]).

This functionality is built upon [Gateway API] and the [Gateway API Inference
Extension (GIE)] projects for both the API resources and machinery, but extends
support beyond what's available in those projects by loading other custom
plugins needed by LLM-D (e.g. custom scorers, P/D Disaggregation, etc).

[Envoy]:https://github.com/envoyproxy/envoy
[ext-proc]:https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_proc_filter
[VLLM]:https://github.com/vllm-project/vllm
[Gateway API]:https://github.com/kubernetes-sigs/gateway-api
[Gateway API Inference Extension (GIE)]:https://github.com/kubernetes-sigs/gateway-api-inference-extension

## Contributing

Contributions are welcome! For large changes please [create an issue] first
describing the change so the maintainers can do an assessment, and work on the
details with you. See [DEVELOPMENT.md](/DEVELOPMENT.md) for details on how to
work with the codebase.

Note that in general features should go to the upstream [Gateway API Inference
Extension (GIE)] project _first_ if applicable. The GIE is a major dependency of
ours, and where most _general purpose_ inference features live. If you have
something that you feel is general purpose or use, it probably should go to the GIE. If you have
something that's _llm-d specific_ then it should go here. If you're not sure
whether your feature belongs here or in the GIE, feel free to create a
[discussion] or ask on [Slack].

[create an issue]:https://github.com/neuralmagic/llm-d-inference-scheduler/issues/new
[Gateway API Inference Extension (GIE)]:https://github.com/kubernetes-sigs/gateway-api-inference-extension
[discussion]:https://github.com/neuralmagic/llm-d-inference-scheduler/discussions/new?category=q-a
[Slack]:https://llm-d.slack.com/
