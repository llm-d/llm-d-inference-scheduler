package scorer

import (
	"context"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// LoadAwareType is the type of the LoadAware scorer
	LoadAwareType = "load-aware-scorer"

	// QueueThresholdDefault defines the default queue threshold value
	QueueThresholdDefault = 128
)

type loadAwareParameters struct {
	Threshold int `json:"threshold"`
}

// compile-time type assertion
var _ framework.Scorer = &LoadAware{}

// LoadAwareFactory defines the factory function for the LoadAware
func LoadAwareFactory(name string, rawParameters json.RawMessage, handle plugins.Handle) (plugins.Plugin, error) {
	parameters := loadAwareParameters{Threshold: QueueThresholdDefault}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' scorer - %w", LoadAwareType, err)
		}
	}

	return NewLoadAware(handle.Context(), parameters.Threshold).WithName(name), nil
}

// NewLoadAware creates a new load based scorer
func NewLoadAware(ctx context.Context, queueThreshold int) *LoadAware {
	if queueThreshold <= 0 {
		queueThreshold = QueueThresholdDefault
		log.FromContext(ctx).V(logutil.DEFAULT).Info(fmt.Sprintf("queueThreshold %d should be positive, using default queue threshold %d", queueThreshold, QueueThresholdDefault))
	}

	return &LoadAware{
		typedName:      plugins.TypedName{Type: LoadAwareType},
		queueThreshold: float64(queueThreshold),
	}
}

// LoadAware scorer that is based on load
type LoadAware struct {
	typedName      plugins.TypedName
	queueThreshold float64
}

// TypedName returns the typed name of the plugin.
func (s *LoadAware) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *LoadAware) WithName(name string) *LoadAware {
	s.typedName.Name = name
	return s
}

// Score scores the given pod in range of 0-1
// Currently metrics contains number of requests waiting in the queue, there is no information about number of requests
// that can be processed in the given pod immediately.
// Pod with empty waiting requests queue is scored with 0.5
// Pod with requests in the queue will get score between 0.5 and 0.
// Score 0 will get pod with number of requests in the queue equal to the threshold used in load-based filter
// In the future, pods with additional capacity will get score higher than 0.5
func (s *LoadAware) Score(_ context.Context, _ *types.CycleState, _ *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
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
