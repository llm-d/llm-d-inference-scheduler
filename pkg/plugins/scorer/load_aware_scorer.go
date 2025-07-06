package scorer

import (
	"context"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// LoadAwareScorerType is the type of the LoadAwareScorer
	LoadAwareScorerType = "load-aware-scorer"

	// QueueThresholdDefault defines the default queue threshold value
	QueueThresholdDefault = 128
)

type loadAwareScorerParameters struct {
	Threshold int `json:"threshold"`
}

// compile-time type assertion
var _ framework.Scorer = &LoadAwareScorer{}

// LoadAwareScorerFactory defines the factory function for the LoadAwareScorer
func LoadAwareScorerFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := loadAwareScorerParameters{Threshold: QueueThresholdDefault}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' scorer - %w", LoadAwareScorerType, err)
		}
	}

	return NewLoadAwareScorer(parameters.Threshold).WithName(name), nil
}

// NewLoadAwareScorer creates a new load based scorer
func NewLoadAwareScorer(queueThreshold int) *LoadAwareScorer {
	return &LoadAwareScorer{
		typedName:      plugins.TypedName{Type: LoadAwareScorerType},
		queueThreshold: float64(queueThreshold),
	}
}

// LoadAwareScorer scorer that is based on load
type LoadAwareScorer struct {
	typedName      plugins.TypedName
	queueThreshold float64
}

// TypedName returns the typed name of the plugin.
func (s *LoadAwareScorer) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *LoadAwareScorer) WithName(name string) *LoadAwareScorer {
	s.typedName.Name = name
	return s
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
