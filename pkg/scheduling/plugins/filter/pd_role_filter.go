package filter

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	roleLabel   = "llm-d.ai/role"
	rolePrefill = "prefill"
	roleDecode  = "decode"
	roleBoth    = "both"
)

// PrefillFilter - filters out pods that are not marked with role Prefill
type PrefillFilter struct{}

var _ plugins.Filter = &PrefillFilter{} // validate interface conformance

// Name returns the name of the filter
func (pf *PrefillFilter) Name() string {
	return "prefill-filter"
}

// Filter filters out all pods that are not marked as "prefill"
func (pf *PrefillFilter) Filter(_ *types.SchedulingContext, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		role := pod.GetPod().Labels[roleLabel]
		if role == rolePrefill {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods
}

// DecodeFilter - filters out pods that are not marked with role Decode or Both
type DecodeFilter struct{}

var _ plugins.Filter = &DecodeFilter{} // validate interface conformance

// Name returns the name of the filter
func (df *DecodeFilter) Name() string {
	return "decode-filter"
}

// Filter removes all pods that are not marked as "decode" or "both"
func (df *DecodeFilter) Filter(_ *types.SchedulingContext, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		role := pod.GetPod().Labels[roleLabel]
		if role == roleDecode || role == roleBoth {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods
}
