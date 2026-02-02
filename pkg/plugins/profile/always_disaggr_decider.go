package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// compile-time type assertion
var _ pdDecider = &alwaysDisaggregationDecider{}

const alwaysDeciderName = "always-disaggregation-decider"

// newAlwaysDisaggregationDecider initializes a new AlwaysDisaggregationDecider and returns its pointer.
func newAlwaysDisaggregationDecider(_ json.RawMessage) (*alwaysDisaggregationDecider, error) {
	return &alwaysDisaggregationDecider{}, nil
}

// alwaysDisaggregationDecider is a PD decision that forces to always disaggregate.
type alwaysDisaggregationDecider struct { }

// isDisaggregationRequired checks if disaggregated PD is required for the given request and pod.
func (_ *alwaysDisaggregationDecider) isDisaggregationRequired(_ context.Context, _ int, _ scheduling.Endpoint) bool {
	return true
}
