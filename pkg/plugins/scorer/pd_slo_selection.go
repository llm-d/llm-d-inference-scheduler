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

const (
	wMax      = 100 // Maximum weight for weighted random selection
	minWeight = 1   // Minimum weight to ensure all pods have some probability
	eps       = 1e-9
)

// podWithHeadroom extends scoredPod with headroom breakdown
type podWithHeadroom struct {
	pod          schedulingtypes.Pod
	ttftHeadroom float64
	tpotHeadroom float64
	totalScore   float64 // Combined score (for positive tier)
}

// choice represents a weighted choice for random selection
type choice struct {
	pod    schedulingtypes.Pod
	weight int
}

// classifyPodsByHeadroom separates pods into positive and negative headroom groups
// Positive headroom: BOTH TTFT and TPOT have positive headroom (can meet SLOs)
// Negative headroom: EITHER TTFT or TPOT has negative/zero headroom (violates at least one SLO)
func classifyPodsByHeadroom(pods []podWithHeadroom) (positive, negative []podWithHeadroom) {
	for _, p := range pods {
		if p.ttftHeadroom > 0 && p.tpotHeadroom > 0 {
			positive = append(positive, p)
		} else {
			negative = append(negative, p)
		}
	}
	return
}

// selectPodTiered implements tiered selection strategy:
// - 99% (configurable): select from positive headroom pods
// - 1% (configurable): explore negative headroom pods
func (s *PdSLOOptimizer) selectPodTiered(
	ctx context.Context,
	allPods []podWithHeadroom,
) *podWithHeadroom {
	logger := log.FromContext(ctx)

	if len(allPods) == 0 {
		return nil
	}
	if len(allPods) == 1 {
		return &allPods[0]
	}

	positive, negative := classifyPodsByHeadroom(allPods)

	logger.V(logutil.DEBUG).Info("Tiered pod classification",
		"positive", len(positive),
		"negative", len(negative))

	// If we have both tiers, use epsilon-greedy tier selection
	if len(positive) > 0 && len(negative) > 0 {
		if s.rand.Float64() < s.config.EpsilonExploreNeg {
			logger.V(logutil.DEBUG).Info("Epsilon-greedy: exploring negative tier",
				"epsilon", s.config.EpsilonExploreNeg)
			return s.selectFromNegativeTier(ctx, negative)
		}
		logger.V(logutil.DEBUG).Info("Epsilon-greedy: exploiting positive tier")
		return s.selectFromPositiveTier(ctx, positive)
	}

	// Only one tier available
	if len(positive) > 0 {
		logger.V(logutil.DEBUG).Info("Only positive tier available")
		return s.selectFromPositiveTier(ctx, positive)
	}

	logger.V(logutil.DEBUG).Info("Only negative tier available")
	return s.selectFromNegativeTier(ctx, negative)
}

// selectFromPositiveTier selects a pod from the positive headroom tier
func (s *PdSLOOptimizer) selectFromPositiveTier(
	ctx context.Context,
	positivePods []podWithHeadroom,
) *podWithHeadroom {
	logger := log.FromContext(ctx)

	if len(positivePods) == 1 {
		return &positivePods[0]
	}

	// Calculate weights based on headroom strategy
	weights := s.calculatePositiveWeights(ctx, positivePods)

	// Weighted random selection
	selectedPod := s.performWeightedSelection(positivePods, weights)

	logger.V(logutil.DEBUG).Info("Selected from positive tier",
		"pod", selectedPod.pod.GetPod().String(),
		"ttftHeadroom", selectedPod.ttftHeadroom,
		"tpotHeadroom", selectedPod.tpotHeadroom)

	return selectedPod
}

// calculatePositiveWeights computes selection weights for positive headroom pods
func (s *PdSLOOptimizer) calculatePositiveWeights(
	ctx context.Context,
	pods []podWithHeadroom,
) []int {
	logger := log.FromContext(ctx)

	// Find min/max for normalization
	minTTFT, maxTTFT := math.MaxFloat64, -math.MaxFloat64
	minTPOT, maxTPOT := math.MaxFloat64, -math.MaxFloat64

	for _, p := range pods {
		if p.ttftHeadroom < minTTFT {
			minTTFT = p.ttftHeadroom
		}
		if p.ttftHeadroom > maxTTFT {
			maxTTFT = p.ttftHeadroom
		}
		if p.tpotHeadroom < minTPOT {
			minTPOT = p.tpotHeadroom
		}
		if p.tpotHeadroom > maxTPOT {
			maxTPOT = p.tpotHeadroom
		}
	}

	ttftRange := maxTTFT - minTTFT
	tpotRange := maxTPOT - minTPOT

	// Normalize weights
	alpha, beta := s.config.HeadroomTTFTWeight, s.config.HeadroomTPOTWeight
	if alpha+beta <= 0 {
		alpha, beta = 1.0, 0.0
	} else {
		sum := alpha + beta
		alpha /= sum
		beta /= sum
	}

	logger.V(logutil.DEBUG).Info("Positive headroom weighting",
		"ttftRange", ttftRange,
		"tpotRange", tpotRange,
		"alphaTTFT", alpha,
		"betaTPOT", beta,
		"strategy", s.config.HeadroomStrategy)

	weights := make([]int, len(pods))
	for i, p := range pods {
		// Normalize to [0, 1]
		normTTFT := 0.0
		if ttftRange > eps {
			normTTFT = (p.ttftHeadroom - minTTFT) / ttftRange
		}
		normTPOT := 0.0
		if tpotRange > eps {
			normTPOT = (p.tpotHeadroom - minTPOT) / tpotRange
		}

		// Blended score
		blended := alpha*normTTFT + beta*normTPOT

		// Apply strategy
		var finalScore float64
		switch s.config.HeadroomStrategy {
		case "least":
			// Prefer pods with LEAST headroom (better packing)
			finalScore = 1.0 - blended
		case "most":
			// Prefer pods with MOST headroom (conservative)
			finalScore = blended
		default: // "weighted" or any other
			// Use blended score directly
			finalScore = blended
		}

		// Convert to integer weight [minWeight, wMax]
		weights[i] = int(finalScore*float64(wMax-minWeight)) + minWeight

		logger.V(logutil.TRACE).Info("Positive pod weight",
			"pod", p.pod.GetPod().String(),
			"ttft", p.ttftHeadroom,
			"tpot", p.tpotHeadroom,
			"normTTFT", normTTFT,
			"normTPOT", normTPOT,
			"blended", blended,
			"finalScore", finalScore,
			"weight", weights[i])
	}

	return weights
}

// selectFromNegativeTier selects from negative headroom pods with hierarchical logic
func (s *PdSLOOptimizer) selectFromNegativeTier(
	ctx context.Context,
	negativePods []podWithHeadroom,
) *podWithHeadroom {
	logger := log.FromContext(ctx)

	if len(negativePods) == 1 {
		return &negativePods[0]
	}

	// Separate by running request count
	var zeroRunning, nonZeroRunning []podWithHeadroom

	for _, p := range negativePods {
		runningCount := s.getPodRunningRequestCount(p.pod)
		if runningCount == 0 {
			zeroRunning = append(zeroRunning, p)
		} else {
			nonZeroRunning = append(nonZeroRunning, p)
		}
	}

	logger.V(logutil.DEBUG).Info("Negative tier by running requests",
		"zeroRunning", len(zeroRunning),
		"nonZeroRunning", len(nonZeroRunning))

	// Strictly prefer pods with zero running requests
	candidates := negativePods
	if len(zeroRunning) > 0 {
		logger.V(logutil.DEBUG).Info("Selecting from zero-running-request pods")
		candidates = zeroRunning
	}

	if len(candidates) == 1 {
		return &candidates[0]
	}

	// Hierarchical categorization by which SLOs are violated
	var negTTFTNegTPOT, negTTFTNonNegTPOT, nonNegTTFTNegTPOT, nonNegTTFTNonNegTPOT []podWithHeadroom

	for _, p := range candidates {
		switch {
		case p.ttftHeadroom < 0 && p.tpotHeadroom < 0:
			negTTFTNegTPOT = append(negTTFTNegTPOT, p)
		case p.ttftHeadroom < 0 && p.tpotHeadroom >= 0:
			negTTFTNonNegTPOT = append(negTTFTNonNegTPOT, p)
		case p.ttftHeadroom >= 0 && p.tpotHeadroom < 0:
			nonNegTTFTNegTPOT = append(nonNegTTFTNegTPOT, p)
		default:
			nonNegTTFTNonNegTPOT = append(nonNegTTFTNonNegTPOT, p)
		}
	}

	logger.V(logutil.DEBUG).Info("Hierarchical negative categorization",
		"both_negative", len(negTTFTNegTPOT),
		"ttft_negative", len(negTTFTNonNegTPOT),
		"tpot_negative", len(nonNegTTFTNegTPOT),
		"neither_negative", len(nonNegTTFTNonNegTPOT))

	// Build weighted choices with hierarchical priorities
	allChoices := make([]choice, 0, len(candidates))
	totalWeight := 0

	// Priority 1: Both negative - blended deficit weighting
	if len(negTTFTNegTPOT) > 0 {
		weights := s.calculateNegativeDeficitWeights(ctx, negTTFTNegTPOT, "both_negative")
		for i, p := range negTTFTNegTPOT {
			allChoices = append(allChoices, choice{pod: p.pod, weight: weights[i]})
			totalWeight += weights[i]
		}
	}

	// Priority 2: TTFT negative
	if len(negTTFTNonNegTPOT) > 0 {
		weights := s.calculateNegativeDeficitWeights(ctx, negTTFTNonNegTPOT, "ttft_negative")
		for i, p := range negTTFTNonNegTPOT {
			allChoices = append(allChoices, choice{pod: p.pod, weight: weights[i]})
			totalWeight += weights[i]
		}
	}

	// Priority 3: TPOT negative
	if len(nonNegTTFTNegTPOT) > 0 {
		weights := s.calculateNegativeDeficitWeights(ctx, nonNegTTFTNegTPOT, "tpot_negative")
		for i, p := range nonNegTTFTNegTPOT {
			allChoices = append(allChoices, choice{pod: p.pod, weight: weights[i]})
			totalWeight += weights[i]
		}
	}

	// Priority 4: Edge case - neither negative (shouldn't happen in negative tier)
	for _, p := range nonNegTTFTNonNegTPOT {
		allChoices = append(allChoices, choice{pod: p.pod, weight: minWeight})
		totalWeight += minWeight
	}

	// Weighted random selection from choices
	if totalWeight == 0 {
		return &candidates[0]
	}

	idx := s.rand.Intn(totalWeight)
	for _, c := range allChoices {
		if idx < c.weight {
			// Find and return the corresponding pod
			for i := range candidates {
				if candidates[i].pod.GetPod().String() == c.pod.GetPod().String() {
					logger.V(logutil.DEBUG).Info("Selected from negative tier",
						"pod", candidates[i].pod.GetPod().String(),
						"ttftHeadroom", candidates[i].ttftHeadroom,
						"tpotHeadroom", candidates[i].tpotHeadroom,
						"weight", c.weight)
					return &candidates[i]
				}
			}
		}
		idx -= c.weight
	}

	// Fallback
	return &candidates[0]
}

// calculateNegativeDeficitWeights computes weights based on SLO deficits
// Lower deficit (less severe violation) => higher weight
func (s *PdSLOOptimizer) calculateNegativeDeficitWeights(
	ctx context.Context,
	pods []podWithHeadroom,
	category string,
) []int {
	logger := log.FromContext(ctx)

	if len(pods) == 0 {
		return nil
	}

	// Calculate deficits (only when headroom is negative)
	minTTFT, maxTTFT := math.MaxFloat64, -math.MaxFloat64
	minTPOT, maxTPOT := math.MaxFloat64, -math.MaxFloat64

	type deficit struct {
		ttft float64
		tpot float64
	}
	deficits := make([]deficit, len(pods))

	for i, p := range pods {
		ttftDef := 0.0
		if p.ttftHeadroom < 0 {
			ttftDef = -p.ttftHeadroom
		}
		tpotDef := 0.0
		if p.tpotHeadroom < 0 {
			tpotDef = -p.tpotHeadroom
		}

		deficits[i] = deficit{ttft: ttftDef, tpot: tpotDef}

		if ttftDef < minTTFT {
			minTTFT = ttftDef
		}
		if ttftDef > maxTTFT {
			maxTTFT = ttftDef
		}
		if tpotDef < minTPOT {
			minTPOT = tpotDef
		}
		if tpotDef > maxTPOT {
			maxTPOT = tpotDef
		}
	}

	ttftRange := maxTTFT - minTTFT
	tpotRange := maxTPOT - minTPOT

	// Normalize weights
	alpha, beta := s.config.NegHeadroomTTFTWeight, s.config.NegHeadroomTPOTWeight
	if alpha+beta <= 0 {
		alpha, beta = 1.0, 0.0
	} else {
		sum := alpha + beta
		alpha /= sum
		beta /= sum
	}

	logger.V(logutil.DEBUG).Info("Negative deficit weighting",
		"category", category,
		"ttftRange", ttftRange,
		"tpotRange", tpotRange,
		"alphaTTFT", alpha,
		"betaTPOT", beta)

	weights := make([]int, len(pods))
	for i, d := range deficits {
		// Normalize deficits to [0, 1] (0 = best/least violation)
		normTTFT := 0.0
		if ttftRange > eps {
			normTTFT = (d.ttft - minTTFT) / (ttftRange + eps)
		}
		normTPOT := 0.0
		if tpotRange > eps {
			normTPOT = (d.tpot - minTPOT) / (tpotRange + eps)
		}

		// Blended "badness": higher = worse violation
		blended := alpha*normTTFT + beta*normTPOT

		// Convert to weight: lower badness -> higher weight
		weights[i] = int((1.0-blended)*float64(wMax-minWeight)) + minWeight

		logger.V(logutil.TRACE).Info("Negative pod deficit weight",
			"pod", pods[i].pod.GetPod().String(),
			"ttftDef", d.ttft,
			"tpotDef", d.tpot,
			"normTTFT", normTTFT,
			"normTPOT", normTPOT,
			"blended", blended,
			"weight", weights[i])
	}

	return weights
}

// performWeightedSelection performs weighted random selection
func (s *PdSLOOptimizer) performWeightedSelection(
	pods []podWithHeadroom,
	weights []int,
) *podWithHeadroom {
	if len(pods) == 0 || len(weights) == 0 {
		return nil
	}

	totalWeight := 0
	for _, w := range weights {
		totalWeight += w
	}

	if totalWeight == 0 {
		return &pods[0]
	}

	idx := s.rand.Intn(totalWeight)
	for i, w := range weights {
		if idx < w {
			return &pods[i]
		}
		idx -= w
	}

	// Fallback
	return &pods[len(pods)-1]
}

// getPodRunningRequestCount returns the number of running requests on a pod
func (s *PdSLOOptimizer) getPodRunningRequestCount(pod schedulingtypes.Pod) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	podKey := pod.GetPod().NamespacedName.String()
	if queue, exists := s.runningRequestLists[podKey]; exists {
		return queue.GetSize()
	}
	return 0
}

// getPodMinTPOTSLO returns the strictest (minimum) TPOT SLO among running requests on a pod
func (s *PdSLOOptimizer) getPodMinTPOTSLO(pod schedulingtypes.Pod) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	podKey := pod.GetPod().NamespacedName.String()
	if queue, exists := s.runningRequestLists[podKey]; exists {
		if topReq := queue.Peek(); topReq != nil {
			return topReq.tpot
		}
	}
	return 0 // No running requests or no TPOT SLOs
}
