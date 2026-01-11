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

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// epsilonGreedyAffinityGate filters pods by prefix cache score using epsilon-greedy exploration
// Mirrors GAIE's epsilonGreedyAffinityGate logic
// Returns: (filtered pods, sticky flag)
// - sticky=true means we're exploiting high prefix cache pods
// - sticky=false means we're exploring all pods
func (s *PdSLOOptimizer) epsilonGreedyAffinityGate(
	ctx context.Context,
	candidates []podWithHeadroom,
	label string, // e.g. "overall" or "positive"
	prefixStickyThreshold float64,
) ([]podWithHeadroom, bool) {
	logger := log.FromContext(ctx)

	if prefixStickyThreshold <= 0 {
		// Affinity gating disabled
		logger.V(logutil.DEBUG).Info("Affinity gating disabled (threshold <= 0)", "path", label)
		return candidates, false
	}

	// Filter to pods with prefix cache score >= threshold
	eligible := make([]podWithHeadroom, 0, len(candidates))
	for _, p := range candidates {
		if p.prefixCacheScore >= prefixStickyThreshold {
			eligible = append(eligible, p)
		}
	}

	// No eligible sticky pods? Explore (no gating).
	if len(eligible) == 0 {
		logger.V(logutil.DEBUG).Info("No eligible sticky pods, exploring all",
			"path", label,
			"threshold", prefixStickyThreshold,
			"total", len(candidates))
		return candidates, false
	}

	// ε-exploration branch
	if s.rand.Float64() < s.config.EpsilonExploreSticky {
		logger.V(logutil.DEBUG).Info("ε-greedy: exploring (ignoring affinity gate)",
			"path", label,
			"epsilon", s.config.EpsilonExploreSticky,
			"eligibleCount", len(eligible),
			"total", len(candidates))
		return candidates, false
	}

	// ε-exploitation branch: use only sticky pods
	logger.V(logutil.DEBUG).Info("ε-greedy: exploiting (applying affinity gate)",
		"path", label,
		"threshold", prefixStickyThreshold,
		"eligibleCount", len(eligible),
		"total", len(candidates))
	return eligible, true
}
