/*
Copyright 2025 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scorer

import (
	"context"
	"math"

	"sigs.k8s.io/controller-runtime/pkg/log"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// selectPodBasedOnStrategy implements tiered selection with support for composite modes
// Mirrors GAIE's selectPodBasedOnStrategy logic
func (s *PdSLOOptimizer) selectPodBasedOnStrategy(
	ctx context.Context,
	allPods []podWithHeadroom,
	positivePods []podWithHeadroom,
	negativePods []podWithHeadroom,
) *podWithHeadroom {
	logger := log.FromContext(ctx)

	// Case 1: composite-only mode - skip SLO headroom, use only pod metrics
	if s.config.HeadroomStrategy == "composite-only" {
		logger.V(logutil.DEBUG).Info("Selecting from composite scores only (no SLO predictions)")
		return s.selectFromCompositeScores(ctx, allPods, "composite-only")
	}

	// Case 2: Both positive and negative tiers exist - epsilon-greedy selection
	if len(positivePods) > 0 && len(negativePods) > 0 {
		if s.rand.Float64() < s.config.EpsilonExploreNeg {
			logger.V(logutil.DEBUG).Info("Epsilon-greedy: exploring negative tier",
				"epsilon", s.config.EpsilonExploreNeg)
			return s.selectFromNegativeTier(ctx, negativePods)
		}
		logger.V(logutil.DEBUG).Info("Epsilon-greedy: exploiting positive tier")
		return s.selectFromPositiveTier(ctx, positivePods)
	}

	// Case 3: Only positive tier exists
	if len(positivePods) > 0 {
		logger.V(logutil.DEBUG).Info("Only positive tier available")
		return s.selectFromPositiveTier(ctx, positivePods)
	}

	// Case 4: Only negative tier exists
	if len(negativePods) > 0 {
		logger.V(logutil.DEBUG).Info("Only negative tier available")
		return s.selectFromNegativeTier(ctx, negativePods)
	}

	// Case 5: No pods at all (shouldn't happen, but handle gracefully)
	if len(allPods) > 0 {
		logger.V(logutil.DEBUG).Info("No headroom pods available, selecting randomly from all pods")
		return &allPods[s.rand.Intn(len(allPods))]
	}

	logger.V(logutil.DEBUG).Info("No valid pods available")
	return nil
}

// selectFromCompositeScores selects a pod based on composite metrics (KV cache, queue, prefix cache)
// Mirrors GAIE's selectFromCompositeScores logic
func (s *PdSLOOptimizer) selectFromCompositeScores(
	ctx context.Context,
	pods []podWithHeadroom,
	strategy string,
) *podWithHeadroom {
	logger := log.FromContext(ctx)

	if len(pods) == 0 {
		return nil
	}
	if len(pods) == 1 {
		return &pods[0]
	}

	// Build composite choices
	choices, total := s.buildCompositeChoices(ctx, pods)

	// Invert weights for "least" strategy
	if strategy == "composite-least" {
		for i := range choices {
			choices[i].weight = minWeight + wMax - choices[i].weight
		}
		// Recalculate total
		total = 0
		for _, c := range choices {
			total += c.weight
		}
	}

	// Weighted random selection
	if total == 0 {
		logger.V(logutil.DEBUG).Info("Total weight is 0, selecting first pod")
		return &pods[0]
	}

	idx := s.rand.Intn(total)
	for _, c := range choices {
		if idx < c.weight {
			// Find and return the corresponding pod
			for i := range pods {
				if pods[i].pod.GetPod().String() == c.pod.GetPod().String() {
					logger.V(logutil.DEBUG).Info("Selected pod from composite scores",
						"pod", pods[i].pod.GetPod().String(),
						"weight", c.weight,
						"strategy", strategy)
					return &pods[i]
				}
			}
		}
		idx -= c.weight
	}

	// Fallback
	logger.V(logutil.DEBUG).Info("Fallback to first pod in composite selection")
	return &pods[0]
}

// buildCompositeChoices builds weighted choices based on composite metrics
// composite = wKV * (1 - kvUsage) + wQueue * (1 - normalizedQueue) + wPrefix * prefixCacheScore
func (s *PdSLOOptimizer) buildCompositeChoices(
	ctx context.Context,
	pods []podWithHeadroom,
) ([]choice, int) {
	logger := log.FromContext(ctx)

	// Normalize weights
	wkv := s.config.CompositeKVWeight
	wq := s.config.CompositeQueueWeight
	wpref := s.config.CompositePrefixWeight

	sumw := wkv + wq + wpref
	if sumw <= 0 {
		wkv, wq, wpref = 1, 0, 0
	} else {
		wkv /= sumw
		wq /= sumw
		wpref /= sumw
	}

	// Precompute queue stats for normalization
	minQ, maxQ := math.MaxInt32, -1
	queueCounts := make(map[string]int, len(pods))
	for _, p := range pods {
		q := p.pod.GetMetrics().WaitingQueueSize
		queueCounts[p.pod.GetPod().String()] = q
		if q < minQ {
			minQ = q
		}
		if q > maxQ {
			maxQ = q
		}
	}
	queueRange := float64(maxQ - minQ)

	logger.V(logutil.DEBUG).Info("Composite scoring parameters",
		"wkv", wkv, "wq", wq, "wprefix", wpref,
		"minQueue", minQ, "maxQueue", maxQ)

	choices := make([]choice, 0, len(pods))
	total := 0

	for _, p := range pods {
		q := queueCounts[p.pod.GetPod().String()]
		relQueue := 1.0
		if queueRange > 0 {
			// Normalize: lower queue = higher score
			relQueue = (float64(maxQ-q) / queueRange)
		}

		kvUsage := p.pod.GetMetrics().KVCacheUsagePercent
		prefix := p.prefixCacheScore

		// Composite score: higher is better
		// - Low KV usage is good (1 - kvUsage)
		// - Low queue is good (relQueue already inverted)
		// - High prefix cache score is good (prefix)
		composite := wkv*(1.0-kvUsage) + wq*relQueue + wpref*prefix

		// Convert to integer weight [minWeight, wMax]
		w := int(composite*float64(wMax-minWeight)) + minWeight
		if w < minWeight {
			w = minWeight
		}

		choices = append(choices, choice{pod: p.pod, weight: w})
		total += w

		logger.V(logutil.TRACE).Info("Composite pod score",
			"pod", p.pod.GetPod().String(),
			"kvUsage", kvUsage,
			"queue", q, "relQueue", relQueue,
			"prefix", prefix,
			"composite", composite,
			"weight", w)
	}

	return choices, total
}
