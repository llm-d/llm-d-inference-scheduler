package http

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// Scorer implementation of the external http scorer
type Scorer struct {
	Plugin
}

var _ plugins.Scorer = &Scorer{} // validate interface conformance

// NewScorer creates a new instance of external http filter based on the given parameters
func NewScorer(_ context.Context, name string, url string) plugins.Scorer {
	return &Scorer{Plugin{name: name, url: url}}
}

// Score scores the given pods in range of 0-1
func (s *Scorer) Score(ctx *types.SchedulingContext, pods []types.Pod) map[types.Pod]float64 {
	logger := log.FromContext(ctx).WithName(s.Name())
	logger.Info(">>> external scorer call - TODO implement it!", "name", s.Name())

	result := map[types.Pod]float64{}

	// TODO - send http request to the url
	for _, pod := range pods {
		result[pod] = 1.0
	}

	return result
}
