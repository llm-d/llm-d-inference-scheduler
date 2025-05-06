/*
Copyright 2025 The Kubernetes Authors.

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
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const prefixAwareScorerName = "prefix-aware-scorer"

// PrefixAwareScorer is a routing scorer that scores pods based on the longest prefix match
// between the request's prompt and stored prefixes. The score is normalized between 0 and 1,
// where 1 represents the longest matching prefix.
type PrefixAwareScorer struct {
	prefixStore *PrefixStore
}

var _ plugins.Scorer = &PrefixAwareScorer{} // validate interface conformance

// NewPrefixAwareScorer creates a new PrefixAwareScorer with the given
// PrefixStoreConfig. If the config is nil, default is used.
func NewPrefixAwareScorer(config *PrefixStoreConfig) *PrefixAwareScorer {
	return &PrefixAwareScorer{
		prefixStore: NewPrefixStore(config),
	}
}

// Name returns the scorer's name
func (s *PrefixAwareScorer) Name() string {
	return "prefix-aware-scorer"
}

// Score scores the target pods based on the longest prefix match.
func (s *PrefixAwareScorer) Score(ctx *types.SchedulingContext, pods []types.Pod) map[types.Pod]float64 {
	loggerDebug := log.FromContext(ctx).WithName(prefixAwareScorerName).V(logutil.DEBUG)
	if ctx.Req == nil {
		loggerDebug.Info("Request is nil, skipping scoring")
		return nil
	}

	scores := s.prefixStore.FindMatchingPods(ctx.Req.Prompt, ctx.Req.Model)
	loggerDebug.Info("Got pod scores", "scores", scores)

	if len(scores) == 0 {
		loggerDebug.Info("No scores found for pods")
		return nil
	}

	podToKey := func(pod types.Pod) (string, bool) {
		if pod.GetPod() == nil {
			return "", false
		}

		return pod.GetPod().NamespacedName.String(), true
	}

	return indexedScoresToNormalizedScoredPods(pods, podToKey, scores)
}

// PostSchedule implements the PostSchedulePlugin interface.
// It adds the prefix to the PrefixStore for the given pod.
// TODO: switch to PostResponse.
func (s *PrefixAwareScorer) PostSchedule(ctx *types.SchedulingContext, res *types.Result) {
	pod := res.TargetPod

	debugLogger := log.FromContext(ctx).WithName(prefixAwareScorerName)
	debugLogger.Info("PostResponse called", "req", ctx.Req, "pod", pod)

	if ctx.Req == nil {
		debugLogger.Info("Request is nil, skipping PostResponse")
		return
	}

	if pod.GetPod() == nil {
		debugLogger.Info("Pod is nil, skipping PostResponse", "req", ctx.Req, "pod", pod)
		return
	}

	if err := s.prefixStore.AddEntry(ctx.Req.Model, ctx.Req.Prompt, &pod.GetPod().NamespacedName); err != nil {
		debugLogger.Error(err, "Failed to add entry to prefix store", "req", ctx.Req, "pod", pod)
		return
	}
}

// GetPrefixStore returns the scorer's PrefixStore.
func (s *PrefixAwareScorer) GetPrefixStore() *PrefixStore {
	return s.prefixStore
}

// podToKey is a function type that converts a Pod to a string key.
// It returns the key and a boolean indicating success.
type podToKeyFunc func(pod types.Pod) (string, bool)

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

// TODO: share with other scorers?
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
