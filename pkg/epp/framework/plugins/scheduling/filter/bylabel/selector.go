package bylabel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

const (
	// ByLabelSelectorType is the type of the Selector filter
	ByLabelSelectorType = "by-label-selector"
)

// compile-time type assertion
var _ scheduling.Filter = &Selector{}

// SelectorFactory defines the factory function for the Selector filter
func SelectorFactory(name string, rawParameters json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	parameters := metav1.LabelSelector{}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' filter - %w", ByLabelSelectorType, err)
		}
	}
	return NewSelector(name, &parameters)
}

// NewSelector returns a new filter instance, configured with the provided
// name and label selector.
func NewSelector(name string, selector *metav1.LabelSelector) (*Selector, error) {
	if name == "" {
		return nil, errors.New("Selector: missing filter name")
	}
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, err
	}

	return &Selector{
		typedName: plugin.TypedName{Type: ByLabelSelectorType, Name: name},
		selector:  labelSelector,
	}, nil
}

// Selector filters out endpoints that do not match its label selector criteria
type Selector struct {
	typedName plugin.TypedName
	selector  labels.Selector
}

// TypedName returns the typed name of the plugin
func (blf *Selector) TypedName() plugin.TypedName {
	return blf.typedName
}

// Filter filters out all endpoints that do not satisfy the label selector
func (blf *Selector) Filter(_ context.Context, _ *scheduling.CycleState, _ *scheduling.InferenceRequest, endpoints []scheduling.Endpoint) []scheduling.Endpoint {
	filtered := []scheduling.Endpoint{}

	for _, endpoint := range endpoints {
		labels := labels.Set(endpoint.GetMetadata().Labels)
		if blf.selector.Matches(labels) {
			filtered = append(filtered, endpoint)
		}
	}
	return filtered
}
