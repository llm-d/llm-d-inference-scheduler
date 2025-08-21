package filter

import (
	"context"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// ByLabelType is the type of the ByLabel filter
	ByLabelType = "by-label"
)

type byLabelParameters struct {
	Label         string   `json:"label"`
	ValidValues   []string `json:"validValues"`
	AllowsNoLabel bool     `json:"allowsNoLabel"`
}

var _ framework.Filter = &ByLabel{} // validate interface conformance

// ByLabelFactory defines the factory function for the ByLabel filter.
func ByLabelFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := byLabelParameters{}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' filter - %w", ByLabelType, err)
		}
	}
	return NewByLabel(name, parameters.Label, parameters.AllowsNoLabel, parameters.ValidValues...), nil
}

// NewByLabel creates and returns an instance of the RoleBasedFilter based on the input parameters
// name - the filter name
// labelName - the name of the label to use
// allowsNoLabel - if true pods without given label will be considered as valid (not filtered out)
// validValuesApp - list of valid values
func NewByLabel(name string, labelName string, allowsNoLabel bool, validValues ...string) *ByLabel {
	validValuesMap := map[string]struct{}{}

	for _, v := range validValues {
		validValuesMap[v] = struct{}{}
	}

	return &ByLabel{
		typedName:     plugins.TypedName{Type: ByLabelType, Name: name},
		labelName:     labelName,
		allowsNoLabel: allowsNoLabel,
		validValues:   validValuesMap,
	}
}

// ByLabel - filters out pods based on the values defined by the given label
type ByLabel struct {
	// name defines the filter typed name
	typedName plugins.TypedName
	// labelName defines the name of the label to be checked
	labelName string
	// validValues defines list of valid label values
	validValues map[string]struct{}
	// allowsNoLabel - if true pods without given label will be considered as valid (not filtered out)
	allowsNoLabel bool
}

// TypedName returns the typed name of the plugin
func (f *ByLabel) TypedName() plugins.TypedName {
	return f.typedName
}

// WithName sets the name of the plugin.
func (f *ByLabel) WithName(name string) *ByLabel {
	f.typedName.Name = name
	return f
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
