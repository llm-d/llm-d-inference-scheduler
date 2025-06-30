package filter

import (
	"context"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// ByLabelFilterType is the type of the ByLabel filter
	ByLabelFilterType = "by-label"
)

// ByLabel - filters out pods based on the values defined by the given label
type ByLabel struct {
	// name defines the filter name
	name string
	// labelName defines the name of the label to be checked
	labelName string
	// validValues defines list of valid label values
	validValues map[string]struct{}
	// allowsNoLabel - if true pods without given label will be considered as valid (not filtered out)
	allowsNoLabel bool
}

var _ framework.Filter = &ByLabel{} // validate interface conformance

// NewByLabel creates and returns an instance of the RoleBasedFilter based on the input parameters
// name - the filter name
// labelName - the name of the label to use
// allowsNoLabel - if true pods without given label will be considered as valid (not filtered out)
// validValuesApp - list of valid values
func NewByLabel(name string, labelName string, allowsNoLabel bool, validValuesApp ...string) *ByLabel {
	validValues := map[string]struct{}{}

	for _, v := range validValuesApp {
		validValues[v] = struct{}{}
	}

	return &ByLabel{name: name, labelName: labelName, allowsNoLabel: allowsNoLabel, validValues: validValues}
}

// Type returns the type of the filter
func (f *ByLabel) Type() string {
	return ByLabelFilterType
}

// Name returns the name of the filter
func (f *ByLabel) Name() string {
	return f.name
}

// Filter filters out all pods that are not marked with one of roles from the validRoles collection
// or has no role label in case allowsNoRolesLabel is true
func (f *ByLabel) Filter(_ context.Context, _ *types.CycleState, _ *types.LLMRequest, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		val, labelDefined := pod.GetPod().Labels[f.labelName]
		_, valueExists := f.validValues[val]

		if (!labelDefined && f.allowsNoLabel) || valueExists {
			filteredPods = append(filteredPods, pod)
		}
	}

	return filteredPods
}
