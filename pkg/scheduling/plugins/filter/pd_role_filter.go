package filter

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// RoleLabel name of the label that contains the pod's role
	RoleLabel = "llm-d.ai/role"
	// RolePrefill set for designated prefill workers
	RolePrefill = "prefill"
	// RoleDecode set for designated decode workers
	RoleDecode = "decode"
	// RoleBoth set for workers that can act as both prefill and decode
	RoleBoth = "both"
)

// RoleBasedFilter - filters out pods based on the role defiled by RoleLabel
type RoleBasedFilter struct {
	validRoles map[string]struct{}
	name       string
}

var _ plugins.Filter = &RoleBasedFilter{}

// NewPrefillFilter creates and returns an instance of the RoleBasedFilter configured for prefill
func NewPrefillFilter() *RoleBasedFilter {
	// TODO: doesn't RoleBoth also imply Prefill?
	return NewRoleBasedFilter("prefill-filter", RolePrefill)
}

// NewDecodeFilter creates and returns an instance of the RoleBasedFilter configured for decode
func NewDecodeFilter() *RoleBasedFilter {
	return NewRoleBasedFilter("decode-filter", RoleDecode, RoleBoth)
}

// NewRoleBasedFilter creates and returns an instance of the RoleBasedFilter based on the input parameters
// name - the filter name
// rolesArr - list of valid roles
func NewRoleBasedFilter(name string, rolesArr ...string) *RoleBasedFilter {
	roles := map[string]struct{}{}

	for _, role := range rolesArr {
		roles[role] = struct{}{}
	}

	return &RoleBasedFilter{name: name, validRoles: roles}
}

// Name returns the name of the filter
func (f *RoleBasedFilter) Name() string {
	return f.name
}

// Filter filters out all pods that are not marked with one of roles from the validRoles collection
func (f *RoleBasedFilter) Filter(_ *types.SchedulingContext, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		role := pod.GetPod().Labels[RoleLabel]
		if _, exists := f.validRoles[role]; exists {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods
}
