// Package scorer provides scorer plugins for the scheduler.
package scorer

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// compile-time type assertion
var _ framework.Scorer = &Passthrough{}

// Passthrough is an example scorer which processes the pods, but does not
// give them any score.
type Passthrough struct{}

// Name provides the textual identifier for this scorer.
func (p *Passthrough) Name() string {
	return "passthrough-scorer"
}

// Score accepts a list of []types.Pod and processes them for scoring.
func (p *Passthrough) Score(ctx context.Context, _ *types.LLMRequest, _ *types.CycleState, pods []types.Pod) map[types.Pod]float64 {
	log.FromContext(ctx).V(logutil.DEBUG).Info(fmt.Sprintf("Scoring pods passthrough was initialized %d candidates: %+v", len(pods), pods))

	scoredPods := make(map[types.Pod]float64, len(pods))
	for _, pod := range pods {
		scoredPods[pod] = 0.0
	}

	return scoredPods
}
