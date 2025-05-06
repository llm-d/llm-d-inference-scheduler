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

## Development

### Temporary Fork

We're temporarily working with a fork of the [Gateway API Inference Extension
(GIE)] instead of upstream:

https://github.com/neuralmagic/gateway-api-inference-extension

We're specifically using a branch called `upstream-sync` which is being kept
up-to-date with upstream GIE, but includes some features that are being staged
there while PRs are being worked on with upstream.

To synchronize with this branch in the interim, you can't simply run `go get` as
it will complain that the module name doesn't match. The following will provide
a workaround to automatically synchronize the branch:

```console
make sync-gie-fork
```

This will fetch the latest `HEAD` of the `upstream-sync` branch, and will
update both the `go.mod` and `go.sum` accordingly, which can then be checked in
to git.

[Gateway API Inference Extension (GIE)]:https://github.com/kubernetes-sigs/gateway-api-inference-extension
