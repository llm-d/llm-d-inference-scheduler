package scorer

import (
	"math"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// endpointToKey is a function type that converts a Pod to a string key.
// It returns the key and a boolean indicating success.
type endpointToKeyFunc func(endpoint scheduling.Endpoint) (string, bool)

// indexedScoresToNormalizedScoredPods converts a map of pod scores to a map of
// normalized scores. The function takes a list of pods, a function to convert
// a pod to a key, and a map of scores indexed by those keys. It returns a map
// of pods to their normalized scores.
func indexedScoresToNormalizedScoredPods(endpoints []scheduling.Endpoint, endpointToKey endpointToKeyFunc,
	scores map[string]float64) map[scheduling.Endpoint]float64 {
	scoredEndpoints := make(map[scheduling.Endpoint]float64)
	minScore, maxScore := getMinMax(scores)

	for _, endpoint := range endpoints {
		key, ok := endpointToKey(endpoint)
		if !ok {
			continue
		}

		if score, ok := scores[key]; ok {
			if minScore == maxScore {
				scoredEndpoints[endpoint] = 1.0
				continue
			}

			scoredEndpoints[endpoint] = (score - minScore) / (maxScore - minScore)
		} else {
			scoredEndpoints[endpoint] = 0.0
		}
	}

	return scoredEndpoints
}

func getMinMax(scores map[string]float64) (float64, float64) {
	minScore := math.MaxFloat64
	maxScore := math.Inf(-1)

	for _, score := range scores {
		if score < minScore {
			minScore = score
		}
		if score > maxScore {
			maxScore = score
		}
	}

	return minScore, maxScore
}
