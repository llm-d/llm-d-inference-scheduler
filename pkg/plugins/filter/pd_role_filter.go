package filter

import (
	"context"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// RoleLabel name
	RoleLabel = "llm-d.ai/role"
	// RolePrefill set for designated prefill workers
	RolePrefill = "prefill"
	// RoleDecode set for designated decode workers
	RoleDecode = "decode"
	// RoleBoth set for workers that can act as both prefill and decode
	RoleBoth = "both"
)

// compile-time type assertion
var _ framework.Filter = &PrefillFilter{}

// PrefillFilter - filters out pods that are not marked with role Prefill
type PrefillFilter struct{}

// Type returns the type of the filter
func (pf *PrefillFilter) Type() string {
	return "prefill-filter"
}

// Filter filters out all pods that are not marked as "prefill"
func (pf *PrefillFilter) Filter(_ context.Context, _ *types.CycleState, _ *types.LLMRequest, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		role := pod.GetPod().Labels[RoleLabel]
		if role == RolePrefill { // TODO: doesn't RoleBoth also imply Prefill?
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods
}

// compile-time type assertion
var _ framework.Filter = &DecodeFilter{}

// DecodeFilter - filters out pods that are not marked with role Decode or Both
type DecodeFilter struct{}

// Type returns the type of the filter
func (df *DecodeFilter) Type() string {
	return "decode-filter"
}

// Filter removes all pods that are not marked as "decode" or "both"
func (df *DecodeFilter) Filter(_ context.Context, _ *types.CycleState, _ *types.LLMRequest, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		role, defined := pod.GetPod().Labels[RoleLabel]
		if !defined || role == RoleDecode || role == RoleBoth {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods
}
