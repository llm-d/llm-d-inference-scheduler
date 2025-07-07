package filter

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// ByLabelFilterType is the type of the ByLabel filter
	ByLabelFilterType = "by-label"
)

type byLabelFilterParameters struct {
	Label         string   `json:"label"`
	ValidValues   []string `json:"validValues"`
	AllowsNoLabel bool     `json:"allowsNoLabel"`
}

var _ framework.Filter = &ByLabel{} // validate interface conformance

// ByLabelFilterFactory defines the factory function for the ByLabelFilter
func ByLabelFilterFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := byLabelFilterParameters{}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' filter - %w", ByLabelFilterType, err)
		}
	}
	return NewByLabel(name, parameters.Label, parameters.AllowsNoLabel, parameters.ValidValues...), nil
}

// NewByLabel creates and returns an instance of the RoleBasedFilter based on the input parameters
// name - the filter name
// labelName - the name of the label to use
// allowsNoLabel - if true pods without given label will be considered as valid (not filtered out)
// validValuesApp - list of valid values
func NewByLabel(name string, labelKey string, allowsNoLabel bool, validValues ...string) *ByLabel {
	return &ByLabel{
		typedName:      plugins.TypedName{Type: ByLabelFilterType, Name: name},
		labelKey:       labelKey,
		allowsNoLabel:  allowsNoLabel,
		validValuesSet: sets.New(validValues...),
	}
}

// ByLabel - filters out pods based on the values defined by the given label
type ByLabel struct {
	// name defines the filter typed name
	typedName plugins.TypedName
	// labelKey defines the key of the label to be checked
	labelKey string
	// validValues defines a set of valid label values
	validValuesSet sets.Set[string]
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
		labelValue, labelExists := pod.GetPod().Labels[f.labelKey] // labelValue is empty string if labelKey doesn't exist
		if f.validValuesSet.Has(labelValue) || (!labelExists && f.allowsNoLabel) {
			filteredPods = append(filteredPods, pod)
		}
	}

	return filteredPods
}
