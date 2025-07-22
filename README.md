[![Go Report Card](https://goreportcard.com/badge/github.com/llm-d/llm-d-inference-scheduler)](https://goreportcard.com/report/github.com/llm-d/llm-d-inference-scheduler)
[![Go Reference](https://pkg.go.dev/badge/github.com/llm-d/llm-d-inference-scheduler.svg)](https://pkg.go.dev/github.com/llm-d/llm-d-inference-scheduler)
[![License](https://img.shields.io/github/license/llm-d/llm-d-inference-scheduler)](/LICENSE)
[![Join Slack](https://img.shields.io/badge/Join_Slack-blue?logo=slack)](https://llm-d.slack.com/archives/C08SBNRRSBD)

# Inference Scheduler

This scheduler makes optimized routing decisions for inference requests to
the llm-d inference framework.

## About

This provides an "Endpoint Picker (EPP)" component to the llm-d inference
framework which schedules incoming inference requests to the platform via a
[Kubernetes] Gateway according to scheduler plugins. For more details on the llm-d inference scheduler architecture, routing logic, and different plugins (filters and scorers), including plugin configuration, see the [Architecture Documentation]).

The EPP extends the [Gateway API Inference Extension (GIE)] project,
which provides the API resources and machinery for scheduling. We add some
custom features that are specific to llm-d here, such as [P/D Disaggregation].

A compatible [Gateway API] implementation is used as the Gateway. The Gateway
API implementation must utilize [Envoy] and support [ext-proc], as this is the
callback mechanism the EPP relies on to make routing decisions to model serving
workloads currently.

[Kubernetes]:https://kubernetes.io
[Architecture Documentation]:docs/architecture.md
[Gateway API Inference Extension (GIE)]:https://github.com/kubernetes-sigs/gateway-api-inference-extension
[P/D Disaggregation]:docs/dp.md
[Gateway API]:https://github.com/kubernetes-sigs/gateway-api
[Envoy]:https://github.com/envoyproxy/envoy
[ext-proc]:https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_proc_filter

## Contributing

Our community meeting is weekly at Wednesday 10AM PDT ([Google Meet], [Meeting Notes]).

We currently utilize the [#sig-inference-scheduler] channel in llm-d Slack workspace for communications.

For large changes please [create an issue] first describing the change so the
maintainers can do an assessment, and work on the details with you. See
[DEVELOPMENT.md](DEVELOPMENT.md) for details on how to work with the codebase.

Note that in general features should go to the upstream [Gateway API Inference
Extension (GIE)] project _first_ if applicable. The GIE is a major dependency of
ours, and where most _general purpose_ inference features live. If you have
something that you feel is general purpose or use, it probably should go to the
GIE. If you have something that's _llm-d specific_ then it should go here. If
you're not sure whether your feature belongs here or in the GIE, feel free to
create a [discussion] or ask on [Slack].

Contributions are welcome!

[create an issue]:https://github.com/llm-d/llm-d-inference-scheduler/issues/new
[Gateway API Inference Extension (GIE)]:https://github.com/kubernetes-sigs/gateway-api-inference-extension
[discussion]:https://github.com/llm-d/llm-d-inference-scheduler/discussions/new?category=q-a
[Slack]:https://llm-d.slack.com/
[Google Meet]:https://meet.google.com/uin-yncz-rvg
[Meeting Notes]:https://docs.google.com/document/d/1Pf3x7ZM8nNpU56nt6CzePAOmFZ24NXDeXyaYb565Wq4
[#sig-inference-scheduler]:https://llm-d.slack.com/?redir=%2Fmessages%2Fsig-inference-scheduler
