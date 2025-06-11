package scorer

import "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

// podToKey is a function type that converts a Pod to a string key.
// It returns the key and a boolean indicating success.
type podToKeyFunc func(pod types.Pod) (string, bool)

// indexedScoresToNormalizedScoredPods converts a map of pod scores to a map of
// normalized scores. The function takes a list of pods, a function to convert
// a pod to a key, and a map of scores indexed by those keys. It returns a map
// of pods to their normalized scores.
func indexedScoresToNormalizedScoredPods(pods []types.Pod, podToKey podToKeyFunc,
	scores map[string]int) map[types.Pod]float64 {
	scoredPods := make(map[types.Pod]float64)
	minScore, maxScore := getMinMax(scores)

	for _, pod := range pods {
		key, ok := podToKey(pod)
		if !ok {
			continue
		}

		if score, ok := scores[key]; ok {
			if minScore == maxScore {
				scoredPods[pod] = 1.0
				continue
			}

			scoredPods[pod] = float64(score-minScore) / float64(maxScore-minScore)
		} else {
			scoredPods[pod] = 0.0
		}
	}

	return scoredPods
}

func getMinMax(scores map[string]int) (int, int) {
	minScore := int(^uint(0) >> 1) // max int
	maxScore := -1

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
