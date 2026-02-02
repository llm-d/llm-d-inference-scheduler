package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// compile-time type assertion
var _ pdDecider = &alwaysDisaggregatedDecider{}

const alwaysDisaggregatedName = "always-disaggregated-decider"

// newAlwaysDisaggregatedDecider initializes a new AlwaysDisaggregationDecider and returns its pointer.
func newAlwaysDisaggregatedDecider(_ json.RawMessage) (*alwaysDisaggregatedDecider, error) {
	return &alwaysDisaggregatedDecider{}, nil
}

// alwaysDisaggregatedDecider is a PD decision that forces to always disaggregate.
type alwaysDisaggregatedDecider struct{}

// shouldDisaggregate checks if disaggregated PD is required for the given request and pod.
func (_ *alwaysDisaggregatedDecider) shouldDisaggregate(_ context.Context, _ int, _ scheduling.Endpoint) bool {
	return true
}
