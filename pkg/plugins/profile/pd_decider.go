package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// pdDecider interface for pd profile handler diceder
type pdDecider interface {
	// isDisaggregationRequired checks if disaggregated PD is required for the given request and pod.
	isDisaggregationRequired(ctx context.Context, inputTokens int, pod types.Pod) bool
}

// pdDeciderParams parameters for pdDecider creation
type pdDeciderParams struct {
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
}
