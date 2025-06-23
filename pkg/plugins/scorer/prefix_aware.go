package scorer

import (
	"context"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	prefixAwareScorerName              = "prefix-aware-scorer"
	prefixAwareKeepAliveTime           = 60 * time.Minute // How long should an idle session be kept alive
	prefixAwareKeepAliveCheckFrequency = 15 * time.Minute // How often to check for overly idle sessions
)

// compile-time type assertion
var _ framework.Scorer = &PrefixAwareScorer{}

type promptHits struct {
	lastUpdate time.Time
	// hits map from string to int
	hits sync.Map
}

// NewPrefixAwareScorer creates a new PrefixAwareScorer with the given
// PrefixStoreConfig. If the config is nil, default is used.
func NewPrefixAwareScorer(ctx context.Context, config *PrefixStoreConfig) *PrefixAwareScorer {
	if config == nil {
		config = DefaultPrefixStoreConfig()
	}

	scorer := &PrefixAwareScorer{
		prefixStore:     NewPrefixStore(config),
		podToPromptHits: sync.Map{},
	}

	go scorer.cleanup(ctx, prefixAwareKeepAliveCheckFrequency, prefixAwareKeepAliveTime)

	return scorer
}

// PrefixAwareScorer is a routing scorer that scores pods based on the longest prefix match
// between the request's prompt and stored prefixes. The score is normalized between 0 and 1,
// where 1 represents the longest matching prefix.
type PrefixAwareScorer struct {
	prefixStore *PrefixStore

	// podToPromptHits map from podID(string) to promptHits
	podToPromptHits sync.Map
}

// Type returns the type of the scorer.
func (s *PrefixAwareScorer) Type() string {
	return "prefix-aware-scorer"
}

// Score scores the target pods based on the longest prefix match.
func (s *PrefixAwareScorer) Score(ctx context.Context, _ *types.CycleState, request *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
	loggerDebug := log.FromContext(ctx).WithName(prefixAwareScorerName).V(logutil.DEBUG)
	if request == nil {
		loggerDebug.Info("Request is nil, skipping scoring")
		return nil
	}

	scores := s.prefixStore.FindMatchingPods(request.Prompt, request.TargetModel)
	loggerDebug.Info("Got pod scores", "scores", scores)

	if len(scores) == 0 {
		loggerDebug.Info("No scores found for pods")
		return nil
	}

	for pod, score := range scores {
		if pod == "" {
			continue
		}

		rawPromptHitsInfo, _ := s.podToPromptHits.LoadOrStore(pod, &promptHits{lastUpdate: time.Now()})
		if promptHitsInfo, ok := rawPromptHitsInfo.(*promptHits); ok {
			promptHitsInfo.lastUpdate = time.Now()
			promptHitsInfo.hits.Store(request.Prompt, score)
		}
	}

	podToKey := func(pod types.Pod) (string, bool) {
		if pod.GetPod() == nil {
			return "", false
		}

		return pod.GetPod().NamespacedName.String(), true
	}

	return indexedScoresToNormalizedScoredPods(pods, podToKey, scores)
}

// PostResponse implements the PostResponse interface.
// It adds the prefix to the PrefixStore for the given pod.
func (s *PrefixAwareScorer) PostResponse(ctx context.Context, request *types.LLMRequest, _ *requestcontrol.Response, targetPod *backend.Pod) {
	debugLogger := log.FromContext(ctx).WithName(prefixAwareScorerName)
	debugLogger.Info("PostResponse called", "request", request, "pod", targetPod)

	if request == nil {
		debugLogger.Info("Request is nil, skipping PostResponse")
		return
	}

	if targetPod == nil {
		debugLogger.Info("Pod is nil, skipping PostResponse", "request", request)
		return
	}

	if err := s.prefixStore.AddEntry(request.TargetModel, request.Prompt, &targetPod.NamespacedName); err != nil {
		debugLogger.Error(err, "Failed to add entry to prefix store", "request", request, "pod", targetPod)
		return
	}
	// TODO should use response body as well. currently due to a bug in istio we do not get response body back.
	// should be handled once that bug is fixed.
}

// GetPrefixStore returns the scorer's PrefixStore.
func (s *PrefixAwareScorer) GetPrefixStore() *PrefixStore {
	return s.prefixStore
}

// GetCachedPercentage returns the percentage of the prompt that is cached for the given pod.
func (s *PrefixAwareScorer) GetCachedPercentage(pod, prompt string) float64 {
	rawHitsForPod, ok := s.podToPromptHits.Load(pod)
	if !ok {
		return 0.0
	}

	hitsForPod, ok := rawHitsForPod.(*promptHits)
	if !ok {
		return 0.0
	}

	rawVal, ok := hitsForPod.hits.Load(prompt)
	if !ok {
		return 0.0
	}

	intVal, _ := rawVal.(int)
	return float64(intVal*s.prefixStore.cacheBlockSize) / float64(len(prompt))
}

// cleanup Cleans up hits map
func (s *PrefixAwareScorer) cleanup(ctx context.Context, keepAliveCheckFrequency time.Duration, keepAliveDuration time.Duration) {
	logger := log.FromContext(ctx)

	logger.Info("Prefix aware scorer cleanup started")
	ticker := time.NewTicker(keepAliveCheckFrequency)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Prefix aware scorer cleanup stopped:")
			return
		case now := <-ticker.C:
			logger.Info("Prefix aware scorer cleanup")
			s.podToPromptHits.Range(
				func(podID any, rawPromptHit any) bool {
					if promptHitInfo, ok := rawPromptHit.(*promptHits); ok {
						if now.Sub(promptHitInfo.lastUpdate) > keepAliveDuration {
							// info is stale, remove it
							s.podToPromptHits.Delete(podID)
						}
					} else {
						// Value is not of the correct type, remove it
						s.podToPromptHits.Delete(podID)
					}
					return true
				})
		}
	}
}
