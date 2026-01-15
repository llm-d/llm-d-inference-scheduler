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

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/profile"
)

const (
	// ProximityScorerType is the type of the ProximityScorer
	ProximityScorerType = "proximity-scorer"
)

// ProximityScorerParams defines configuration parameters for the proximity scorer
type ProximityScorerParams struct {
	SameNodeScore float64 `json:"sameNodeScore"` // Score for same node (NVLink ~900GB/s)
	DefaultScore  float64 `json:"defaultScore"`  // Score for different node (InfiniBand ~400GB/s)
}

// compile-time type assertion
var _ framework.Scorer = &ProximityScorer{}

// ProximityScorerFactory defines the factory function for the ProximityScorer
func ProximityScorerFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	params := ProximityScorerParams{
		SameNodeScore: 1.0,
		DefaultScore:  0.0,
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &params); err != nil {
			return nil, fmt.Errorf("failed to parse proximity scorer parameters: %w", err)
		}
	}

	return NewProximityScorer(params).WithName(name), nil
}

// NewProximityScorer creates a new ProximityScorer instance
func NewProximityScorer(params ProximityScorerParams) *ProximityScorer {
	return &ProximityScorer{
		typedName: plugins.TypedName{Type: ProximityScorerType},
		params:    params,
	}
}

// ProximityScorer is a scorer that prefers prefill pods on the same node as the decode pod
type ProximityScorer struct {
	typedName plugins.TypedName
	params    ProximityScorerParams
}

// TypedName returns the typed name of the plugin
func (s *ProximityScorer) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin
func (s *ProximityScorer) WithName(name string) *ProximityScorer {
	s.typedName.Name = name
	return s
}

// Category returns the scorer category (Affinity to prefer locality)
func (s *ProximityScorer) Category() framework.ScorerCategory {
	return framework.Affinity
}

// Score assigns scores to prefill pods based on their proximity to the decode pod
func (s *ProximityScorer) Score(ctx context.Context, cycleState *types.CycleState,
	request *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {

	scores := make(map[types.Pod]float64, len(pods))

	// Read decode pod node name from CycleState
	proximityState, err := types.ReadCycleStateKey[*profile.ProximityState](
		cycleState,
		plugins.StateKey(profile.ProximityStateKey),
	)

	if err != nil {
		// If decode pod node name not available, give all pods default score
		log.FromContext(ctx).V(logutil.DEBUG).Info(
			"Proximity state not found in CycleState, using default scores",
			"error", err,
		)
		for _, pod := range pods {
			scores[pod] = s.params.DefaultScore
		}
		return scores
	}

	// Score each prefill pod based on proximity to decode pod
	for _, pod := range pods {
		metadata := pod.GetPod()

		// Get prefill pod's node name
		prefillNodeName := metadata.NodeName

		// Simple binary scoring: same node = 1.0, different node = 0.0
		score := s.params.DefaultScore
		if prefillNodeName != "" && prefillNodeName == proximityState.DecodeNodeName {
			score = s.params.SameNodeScore // Same node - NVLink (~900GB/s)
		}
		// else: Different node - InfiniBand (~400GB/s)

		scores[pod] = score

		log.FromContext(ctx).V(logutil.DEBUG).Info("Proximity score calculated",
			"pod", metadata.NamespacedName.String(),
			"prefillNode", prefillNodeName,
			"decodeNode", proximityState.DecodeNodeName,
			"score", score,
		)
	}

	return scores
}
