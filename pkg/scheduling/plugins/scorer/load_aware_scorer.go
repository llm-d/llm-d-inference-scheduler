package scorer

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/config"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// LoadAwareScorer scorer that is based on load
type LoadAwareScorer struct{}

var _ plugins.Scorer = &LoadAwareScorer{} // validate interface conformance

// NewLoadAwareScorer creates a new load based scorer
func NewLoadAwareScorer() plugins.Scorer {
	return &LoadAwareScorer{}
}

// Name returns the scorer's name
func (s *LoadAwareScorer) Name() string {
	return "load-aware-scorer"
}

// Score scores the given pod in range of 0-1
// Currently metrics contains number of requests waiting in the queue, there is no information about number of requests
// that can be processed in the given pod immediately.
// Pod with empty waiting requests queue is scored with 0.5
// Pod with requests in the queue will get score between 0.5 and 0.
// Score 0 will get pod with number of requests in the queue equal to the threshold used in load-based filter (QueueingThresholdLoRA)
// In future pods with additional capacity will get score higher than 0.5
func (s *LoadAwareScorer) Score(_ *types.SchedulingContext, pods []types.Pod) map[types.Pod]float64 {
	scoredPods := make(map[types.Pod]float64)

	for _, pod := range pods {
		waitingRequests := float64(pod.GetMetrics().WaitingQueueSize)

		if waitingRequests == 0 {
			scoredPods[pod] = 0.5
		} else {
			scoredPods[pod] = 0.5 * (1.0 - (waitingRequests / float64(config.Conf.QueueingThresholdLoRA)))
		}
	}
	return scoredPods
}
