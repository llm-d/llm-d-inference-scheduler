package filter

import (
	"context"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// DecodeFilterType is the type of the DecodeFilter
	DecodeFilterType = "decode-filter"

	// PrefillFilterType is the type of the PrefillFilter
	PrefillFilterType = "prefill-filter"

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

// NewPrefillFilter creates a new instance of the DecodeFilter
func NewPrefillFilter() *PrefillFilter {
	return &PrefillFilter{
		name: PrefillFilterType,
	}
}

// PrefillFilter - filters out pods that are not marked with role Prefill
type PrefillFilter struct {
	name string
}

// Type returns the type of the filter
func (pf *PrefillFilter) Type() string {
	return PrefillFilterType
}

// Name returns the name of the instance of the filter.
func (pf *PrefillFilter) Name() string {
	return pf.name
}

// WithName sets the name of the filter.
func (pf *PrefillFilter) WithName(name string) *PrefillFilter {
	pf.name = name
	return pf
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

// NewDecodeFilter creates a new instance of the DecodeFilter
func NewDecodeFilter() *DecodeFilter {
	return &DecodeFilter{
		name: DecodeFilterType,
	}
}

// DecodeFilter - filters out pods that are not marked with role Decode or Both
type DecodeFilter struct {
	name string
}

// Type returns the type of the filter
func (df *DecodeFilter) Type() string {
	return DecodeFilterType
}

// Name returns the name of the instance of the filter.
func (df *DecodeFilter) Name() string {
	return df.name
}

// WithName sets the name of the filter.
func (df *DecodeFilter) WithName(name string) *DecodeFilter {
	df.name = name
	return df
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
