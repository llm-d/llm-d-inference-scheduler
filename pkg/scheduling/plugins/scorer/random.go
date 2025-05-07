// Package scorer provides scorer plugins for the scheduler.
package scorer

import (
	"fmt"
	"math/rand"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// Random is an example scorer which processes the pods, giving each a random score.
type Random struct{}

var _ plugins.Scorer = &Random{}

// Name provides the textual identifier for this scorer.
func (r *Random) Name() string {
	return "random-scorer"
}

// Score accepts a list of []types.Pod and processes them for scoring.
func (r *Random) Score(ctx *types.SchedulingContext, pods []types.Pod) map[types.Pod]float64 {
	ctx.Logger.V(logutil.DEBUG).Info(fmt.Sprintf("Scoring pods randomly called with %d candidates: %+v",
		len(pods), pods))

	scores := make(map[types.Pod]float64, len(pods))
	for _, pod := range pods {
		scores[pod] = rand.Float64()
	}

	return scores
}
