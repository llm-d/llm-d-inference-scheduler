package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// compile-time type assertion
var _ pdDecider = &AlwaysDisaggregationDecider{}

const alwaysDeciderName = "always-disaggregation-decider"

// NewAlwaysDisaggregationDecider initializes a new AlwaysDisaggregationDecider and returns its pointer.
func NewAlwaysDisaggregationDecider(_ json.RawMessage) (*AlwaysDisaggregationDecider, error) {
	return &AlwaysDisaggregationDecider{}, nil
}

// AlwaysDisaggregationDecider handles scheduler profiles for PD.
type AlwaysDisaggregationDecider struct {
}

// isDisaggregationRequired checks if disaggregated PD is required for the given request and pod.
func (d *AlwaysDisaggregationDecider) isDisaggregationRequired(_ context.Context, _ int, _ scheduling.Endpoint) bool {
	return true
}
