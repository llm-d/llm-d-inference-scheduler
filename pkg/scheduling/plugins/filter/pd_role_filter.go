package filter

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
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

// ByRoleLabel - filters out pods based on the role defined by RoleLabel label
type ByRoleLabel struct {
	// name defines the filter name
	name string
	// validRoles defines list of valid role header values
	validRoles map[string]bool
	// allowsNoRolesLabel - if true pods without role label will be considered as valid (not filtered out)
	allowsNoRolesLabel bool
}

var _ plugins.Filter = &ByRoleLabel{} // validate interface conformance

// NewByRoleLabel creates and returns an instance of the RoleBasedFilter based on the input parameters
// name - the filter name
// rolesArr - list of valid roles
func NewByRoleLabel(name string, allowsNoRolesLabel bool, rolesArr ...string) *ByRoleLabel {
	roles := map[string]bool{}

	for _, role := range rolesArr {
		roles[role] = true
	}

	return &ByRoleLabel{name: name, allowsNoRolesLabel: allowsNoRolesLabel, validRoles: roles}
}

// NewPrefillFilter creates and returns an instance of the Filter configured for prefill role
func NewPrefillFilter() plugins.Filter {
	return NewByRoleLabel("prefill-filter", false, RolePrefill)
}

// NewDecodeFilter creates and returns an instance of the Filter configured for decode role
func NewDecodeFilter() plugins.Filter {
	return NewByRoleLabel("decode-filter", true, RoleDecode, RoleBoth)
}

// Name returns the name of the filter
func (f *ByRoleLabel) Name() string {
	return f.name
}

// Filter filters out all pods that are not marked with one of roles from the validRoles collection
// or has no role label in case allowsNoRolesLabel is true
func (f *ByRoleLabel) Filter(_ *types.SchedulingContext, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		role, labelDefined := pod.GetPod().Labels[RoleLabel]
		_, roleExists := f.validRoles[role]

		if (!labelDefined && f.allowsNoRolesLabel) || roleExists {
			filteredPods = append(filteredPods, pod)
		}
	}

	return filteredPods
}
