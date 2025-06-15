package http

import (
	"maps"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// This file contains types that duplicate upstream types but include json marshaling annotations.
// This duplication allows sending these data structures to external http servers without requiring changes upstream.

// schedulingContext is types.SchedulingContext
type schedulingContext struct {
	Request request `json:"request"`
	Pods    []pod   `json:"pods"`
}

// request is types.LLMRequest
type request struct {
	TargetModel string            `json:"target_model"`
	RequestID   string            `json:"request_id"`
	Critical    bool              `json:"critical"`
	Prompt      string            `json:"prompt"`
	Headers     map[string]string `json:"headers"`
}

// pod is types.Pod
type pod struct {
	Pod     *podInfo    `json:"pod,omitempty"`
	Metrics *podMetrics `json:"metrics,omitempty"`
}

// podInfo is backend.Pod (sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend)
type podInfo struct {
	NamespacedName namespacedName    `json:"namespaced_name"`
	Address        string            `json:"address"`
	Labels         map[string]string `json:"labels"`
}

// namespacedName is k8stypes.NamespacedName (k8s.io/apimachinery/pkg/types)
type namespacedName struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// podMertics is backendmetrics.MetricsState ("sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics")
type podMetrics struct {
	ActiveModels            map[string]int `json:"active_models"`
	WaitingModels           map[string]int `json:"waiting_models"`
	MaxActiveModels         int            `json:"max_active_models"`
	RunningQueueSize        int            `json:"running_queue_size"`
	WaitingQueueSize        int            `json:"waiting_queue_size"`
	KVCacheUsagePercent     float64        `json:"kv_cache_usage_percent"`
	KVCacheMaxTokenCapacity int            `json:"kv_cache_max_token_capacity"`
	UpdateTime              string         `json:"update_time"`
}

// newSchedulingContext creates a new scheduling context according the given parameter
func newSchedulingContext(ctx *types.SchedulingContext) *schedulingContext {
	headers := map[string]string{}

	if ctx.Req.Headers != nil {
		headers = maps.Clone(ctx.Req.Headers)
	}

	return &schedulingContext{
		Pods: podsToExtPods(ctx.PodsSnapshot),
		Request: request{
			TargetModel: ctx.Req.TargetModel,
			RequestID:   ctx.Req.RequestId,
			Critical:    ctx.Req.Critical,
			Prompt:      ctx.Req.Prompt,
			Headers:     headers,
		},
	}
}

// Filter plugin

// filterPayload is the input parameter to a filter http call
type filterPayload struct {
	SchedContext *schedulingContext `json:"sched_context"`
	Pods         []pod              `json:"pods"`
}

// newFilterPayload creates a filterPayload object based on the given parameters
// returns the pointer to the created object
func newFilterPayload(ctx *types.SchedulingContext, pods []types.Pod) *filterPayload {
	externalPods := make([]pod, len(pods))

	for i, internalPod := range pods {
		externalPods[i] = pod{
			Pod:     podInfoFromBackend(internalPod.GetPod()),
			Metrics: metricsFromBackend(internalPod.GetMetrics()),
		}
	}

	return &filterPayload{SchedContext: newSchedulingContext(ctx), Pods: externalPods}
}
