package scorer

import (
	"context"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	prefixAwareScorerName              = "prefix-aware-scorer"
	prefixAwareKeepAliveTime           = 60 * time.Minute // How long should an idle session be kept alive
	prefixAwareKeepAliveCheckFrequency = 15 * time.Minute // How often to check for overly idle sessions
)

type promptHits struct {
	lastUpdate time.Time
	hits       map[string]int
}

// PrefixAwareScorer is a routing scorer that scores pods based on the longest prefix match
// between the request's prompt and stored prefixes. The score is normalized between 0 and 1,
// where 1 represents the longest matching prefix.
type PrefixAwareScorer struct {
	prefixStore *PrefixStore

	// podToPromptHits map from podID(string) to *promptHits
	podToPromptHits sync.Map
}

var _ plugins.Scorer = &PrefixAwareScorer{} // validate interface conformance

// NewPrefixAwareScorer creates a new PrefixAwareScorer with the given
// PrefixStoreConfig. If the config is nil, default is used.
func NewPrefixAwareScorer(ctx context.Context, config *PrefixStoreConfig) *PrefixAwareScorer {
	scorer := &PrefixAwareScorer{
		prefixStore:     NewPrefixStore(config),
		podToPromptHits: sync.Map{},
	}

	go scorer.cleanup(ctx, prefixAwareKeepAliveCheckFrequency, prefixAwareKeepAliveTime)

	return scorer
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

	scores := s.prefixStore.FindMatchingPods(ctx.Req.Prompt, ctx.Req.TargetModel)
	loggerDebug.Info("Got pod scores", "scores", scores)

	if len(scores) == 0 {
		loggerDebug.Info("No scores found for pods")
		return nil
	}

	for pod, score := range scores {
		if pod == "" {
			continue
		}

		rawPromptHitsInfo, _ := s.podToPromptHits.LoadOrStore(pod, &promptHits{
			lastUpdate: time.Now(),
			hits:       map[string]int{},
		})

		if promptHitsInfo, ok := rawPromptHitsInfo.(promptHits); ok {
			promptHitsInfo.hits[ctx.Req.Prompt] = score
			promptHitsInfo.lastUpdate = time.Now()
			s.podToPromptHits.Store(pod, promptHitsInfo)
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

	if err := s.prefixStore.AddEntry(ctx.Req.TargetModel, ctx.Req.Prompt, &pod.GetPod().NamespacedName); err != nil {
		debugLogger.Error(err, "Failed to add entry to prefix store", "req", ctx.Req, "pod", pod)
		return
	}
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

	hitsForPod, ok := rawHitsForPod.(promptHits)
	if !ok {
		return 0.0
	}

	if _, ok := hitsForPod.hits[prompt]; !ok {
		return 0.0
	}

	return float64(hitsForPod.hits[prompt]*s.prefixStore.blockSize) / float64(len(prompt))
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
