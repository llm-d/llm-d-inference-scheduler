package scorer

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
)

const (
	queueThresholdEnvName = "LOAD_AWARE_SCORER_QUEUE_THRESHOLD"
	queueThresholdDefault = 128
)

// compile-time type assertion
var _ framework.Scorer = &LoadAwareScorer{}

// NewLoadAwareScorer creates a new load based scorer
func NewLoadAwareScorer(ctx context.Context) framework.Scorer {
	return &LoadAwareScorer{
		queueThreshold: float64(env.GetEnvInt(queueThresholdEnvName, queueThresholdDefault, log.FromContext(ctx))),
	}
}

// LoadAwareScorer scorer that is based on load
type LoadAwareScorer struct {
	queueThreshold float64
}

// Type returns the type of the scorer.
func (s *LoadAwareScorer) Type() string {
	return "load-aware-scorer"
}

// Score scores the given pod in range of 0-1
// Currently metrics contains number of requests waiting in the queue, there is no information about number of requests
// that can be processed in the given pod immediately.
// Pod with empty waiting requests queue is scored with 0.5
// Pod with requests in the queue will get score between 0.5 and 0.
// Score 0 will get pod with number of requests in the queue equal to the threshold used in load-based filter (QueueingThresholdLoRA)
// In future pods with additional capacity will get score higher than 0.5
func (s *LoadAwareScorer) Score(_ context.Context, _ *types.CycleState, _ *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
	scoredPods := make(map[types.Pod]float64)

	for _, pod := range pods {
		waitingRequests := float64(pod.GetMetrics().WaitingQueueSize)

		if waitingRequests == 0 {
			scoredPods[pod] = 0.5
		} else {
			if waitingRequests > s.queueThreshold {
				waitingRequests = s.queueThreshold
			}
			scoredPods[pod] = 0.5 * (1.0 - (waitingRequests / s.queueThreshold))
		}
	}
	return scoredPods
}
