package scorer

import (
	"fmt"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// Passthrough is an example scorer which processes the pods, but does not
// give them any score.
type Passthrough struct{}

var _ plugins.Scorer = &Passthrough{}

// Name provides the textual identifier for this scorer.
func (p *Passthrough) Name() string {
	return "passthrough-scorer"
}

// Score accepts a list of []types.Pod and processes them for scoring.
func (p *Passthrough) Score(ctx *types.SchedulingContext, pods []types.Pod) map[types.Pod]float64 {
	ctx.Logger.V(logutil.DEBUG).Info(fmt.Sprintf("Scoring pods passthrough was initialized %d candidates: %+v", len(pods), pods))

	scoredPods := make(map[types.Pod]float64, len(pods))
	for _, pod := range pods {
		scoredPods[pod] = 0.0
	}

	return scoredPods
}
