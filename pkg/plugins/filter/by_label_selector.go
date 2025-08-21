package filter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// ByLabelSelectorType is the type of the ByLabelSelector filter
	ByLabelSelectorType = "by-label-selector"
)

// compile-time type assertion
var _ framework.Filter = &ByLabelSelector{}

// ByLabelSelectorFactory defines the factory function for the ByLabelSelector filter
func ByLabelSelectorFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := metav1.LabelSelector{}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' filter - %w", ByLabelSelectorType, err)
		}
	}
	return NewByLabelSelector(name, &parameters)
}

// NewByLabelSelector returns a new filter instance, configured with the provided
// name and label selector.
func NewByLabelSelector(name string, selector *metav1.LabelSelector) (*ByLabelSelector, error) {
	if name == "" {
		return nil, errors.New("ByLabelSelector: missing filter name")
	}
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, err
	}

	return &ByLabelSelector{
		typedName: plugins.TypedName{Type: ByLabelSelectorType, Name: name},
		selector:  labelSelector,
	}, nil
}

// ByLabelSelector filters out pods that do not match its label selector criteria
type ByLabelSelector struct {
	typedName plugins.TypedName
	selector  labels.Selector
}

// TypedName returns the typed name of the plugin
func (blf *ByLabelSelector) TypedName() plugins.TypedName {
	return blf.typedName
}

// Filter filters out all pods that do not satisfy the label selector
func (blf *ByLabelSelector) Filter(_ context.Context, _ *types.CycleState, _ *types.LLMRequest, pods []types.Pod) []types.Pod {
	filtered := []types.Pod{}

	for _, pod := range pods {
		labels := labels.Set(pod.GetPod().Labels)
		if blf.selector.Matches(labels) {
			filtered = append(filtered, pod)
		}
	}
	return filtered
}
