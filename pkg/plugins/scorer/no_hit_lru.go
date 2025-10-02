package scorer

import (
	"context"
	"encoding/json"
	"fmt"

	lru "github.com/hashicorp/golang-lru/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// NoHitLRUType is the type of the NoHitLRU scorer
	NoHitLRUType = "no-hit-lru-scorer"

	// defaultLRUSize is the maximum number of pods we'll consider in the cache
	defaultLRUSize = 1024
)

// compile-time type assertions
var _ framework.Scorer = &NoHitLRU{}
var _ requestcontrol.PreRequest = &NoHitLRU{}

// NoHitLRUParameters defines the parameters for the NoHitLRU scorer.
type NoHitLRUParameters struct {
	// PrefixPluginName defines the name of the prefix cache plugin to read state from.
	// Defaults to "prefix-cache-scorer".
	PrefixPluginName string `json:"prefixPluginName"`

	// LRUSize defines the maximum number of pods to track in the LRU cache.
	LRUSize int `json:"lruSize"`
}

// coldRequestState tracks whether a request triggered a KV cache hit
// when the cache is missed, isCold is true.
type coldRequestState struct {
	isCold bool
}

// Clone implements the plugins.StateData interface
func (c *coldRequestState) Clone() plugins.StateData {
	return &coldRequestState{isCold: c.isCold}
}

// NoHitLRUFactory defines the factory function for the NoHitLRU
func NoHitLRUFactory(name string, rawParameters json.RawMessage, handle plugins.Handle) (plugins.Plugin, error) {
	parameters := NoHitLRUParameters{}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' scorer - %w", NoHitLRUType, err)
		}
	}

	if parameters.PrefixPluginName == "" {
		parameters.PrefixPluginName = prefix.PrefixCachePluginType
	}

	// Note: We don't enforce that the prefix plugin exists here
	// The scorer will gracefully handle missing prefix cache state as an optimization

	return NewNoHitLRU(handle.Context(), &parameters).WithName(name), nil
}

// NewNoHitLRU creates a new NoHitLRU scorer
func NewNoHitLRU(ctx context.Context, params *NoHitLRUParameters) *NoHitLRU {
	prefixPluginName := prefix.PrefixCachePluginType
	lruSize := defaultLRUSize

	if params != nil {
		if params.PrefixPluginName != "" {
			prefixPluginName = params.PrefixPluginName
		}
		if params.LRUSize > 0 {
			lruSize = params.LRUSize
		}
	}

	lruCache, err := lru.New[string, struct{}](lruSize)
	if err != nil {
		log.FromContext(ctx).Error(err, fmt.Sprintf("failed to initialize NoHitLRU scorer: could not create LRU cache with size %d: %v", lruSize, err))
		return nil
	}

	return &NoHitLRU{
		typedName:        plugins.TypedName{Type: NoHitLRUType},
		lruCache:         lruCache,
		prefixPluginName: prefixPluginName,
		pluginState:      plugins.NewPluginState(ctx),
	}
}

// NoHitLRU scorer that favors pods that were least recently used for cold requests.
// This can help evenly distribute cache growth, since cold requests result in more
// new KV blocks.
type NoHitLRU struct {
	typedName        plugins.TypedName
	lruCache         *lru.Cache[string, struct{}] // pod name -> dummy value (we only care about order)
	prefixPluginName string
	pluginState      *plugins.PluginState
}

// TypedName returns the typed name of the plugin.
func (s *NoHitLRU) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *NoHitLRU) WithName(name string) *NoHitLRU {
	s.typedName.Name = name
	return s
}

// isColdRequest determines if a request is cold by reading the prefix cache state.
// Returns true if no prefix cache hits were found, or if prefix cache state is unavailable.
func (s *NoHitLRU) isColdRequest(ctx context.Context, cycleState *types.CycleState) bool {
	logger := log.FromContext(ctx).V(logutil.DEBUG)

	// Read prefix cache state to determine if this is a cold request
	// This is treated as an optimization - if the state isn't available, we assume cold request
	prefixState, err := types.ReadCycleStateKey[*prefix.SchedulingContextState](cycleState, plugins.StateKey(s.prefixPluginName))

	if err != nil {
		logger.Info("No prefix cache state found, treating as cold request for LRU optimization", "error", err)
		return true
	}

	// Check if this is a cold request (no prefix cache hits)
	return len(prefixState.PrefixCacheServers) == 0
}

// scoreNeutral returns neutral scores (0.5) for all pods.
// Used when a request has cache hits and LRU optimization should not apply.
func (s *NoHitLRU) scoreNeutral(pods []types.Pod) map[types.Pod]float64 {
	scoredPods := make(map[types.Pod]float64, len(pods))
	for _, pod := range pods {
		scoredPods[pod] = 0.5
	}
	return scoredPods
}

// getLRUPositions returns a map of pod names to their LRU position.
// Position 0 represents the oldest (least recently used) entry.
func (s *NoHitLRU) getLRUPositions() map[string]int {
	// Get all keys from LRU cache in order (oldest first)
	// https://pkg.go.dev/github.com/hashicorp/golang-lru/v2#Cache.Keys
	lruKeys := s.lruCache.Keys()

	lruPosition := make(map[string]int, len(lruKeys))
	for i, key := range lruKeys {
		lruPosition[key] = i
	}
	return lruPosition
}

// partitionPodsByUsage separates pods into those that have received cold requests
// (usedPods) and those that have never received cold requests (neverUsedPods).
func (s *NoHitLRU) partitionPodsByUsage(pods []types.Pod, lruPosition map[string]int) (usedPods, neverUsedPods []types.Pod) {
	for _, pod := range pods {
		podName := pod.GetPod().NamespacedName.String()
		if _, exists := lruPosition[podName]; exists {
			usedPods = append(usedPods, pod)
		} else {
			neverUsedPods = append(neverUsedPods, pod)
		}
	}
	return usedPods, neverUsedPods
}

// scoreNeverUsedPods assigns scores to pods that have never received a cold request.
// The first never-used pod gets the highest score (1.0), with subsequent pods
// receiving progressively lower scores.
func (s *NoHitLRU) scoreNeverUsedPods(scoredPods map[types.Pod]float64, neverUsedPods []types.Pod, totalPods int) {
	// Avoid possibility of dividing by zero.
	if totalPods <= 1 {
		return
	}
	for i, pod := range neverUsedPods {
		score := 1.0 - float64(i)/float64(totalPods-1)
		scoredPods[pod] = score
	}
}

// scoreUsedPods assigns scores to pods based on their LRU position.
// Pods that were least recently used for cold requests receive higher scores.
func (s *NoHitLRU) scoreUsedPods(scoredPods map[types.Pod]float64, usedPods []types.Pod, lruPosition map[string]int, neverUsedCount, totalPods int) {
	// Avoid possibility of dividing by zero.
	if totalPods <= 1 {
		return
	}
	for _, pod := range usedPods {
		podName := pod.GetPod().NamespacedName.String()
		lruPos := lruPosition[podName]
		// LRU keys are oldest to newest so rank 0 = oldest
		// The never used pod count is added to the rank so that
		// a never-used pod will always have the highest score.
		rank := neverUsedCount + lruPos
		score := 1.0 - float64(rank)/float64(totalPods-1)
		if score < 0 {
			score = 0
		}
		scoredPods[pod] = score
	}
}

// scoreColdRequestByLRU scores pods based on their LRU position for cold requests.
// Pods that have never received a cold request get the highest scores.
// Among previously used pods, least recently used ones get higher scores.
func (s *NoHitLRU) scoreColdRequestByLRU(pods []types.Pod) map[types.Pod]float64 {
	scoredPods := make(map[types.Pod]float64, len(pods))
	totalPods := len(pods)

	// Avoid possibility of dividing by zero.
	if totalPods == 1 {
		scoredPods[pods[0]] = 1.0
		return scoredPods
	}

	lruPosition := s.getLRUPositions()
	usedPods, neverUsedPods := s.partitionPodsByUsage(pods, lruPosition)

	s.scoreNeverUsedPods(scoredPods, neverUsedPods, totalPods)
	s.scoreUsedPods(scoredPods, usedPods, lruPosition, len(neverUsedPods), totalPods)

	return scoredPods
}

// Score scores the given pods based on LRU for cold requests.
// For cache hits, returns neutral scores (0.5) for all pods.
// For cache misses, ranks pods by their LRU order.
// - LRU ordering is with respect to when a pod last received a cold request.
// - Least recently used (or never used) pods get highest score (1.0)
// - Most recently used pods get lowest score (approaching 0.0)
func (s *NoHitLRU) Score(ctx context.Context, cycleState *types.CycleState, request *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
	logger := log.FromContext(ctx).V(logutil.DEBUG)

	isCold := s.isColdRequest(ctx, cycleState)

	// Store the cold request state in plugin state for PreRequest to use
	coldState := &coldRequestState{isCold: isCold}
	s.pluginState.Write(request.RequestId, plugins.StateKey(s.typedName.String()), coldState)

	if !isCold {
		logger.Info("Cache hit detected, returning neutral scores")
		return s.scoreNeutral(pods)
	}

	logger.Info("Cold request detected, scoring pods by LRU")
	return s.scoreColdRequestByLRU(pods)
}

// PreRequest is called before a request is sent to the target pod.
// For cold requests, it updates the LRU cache to track which pods have been used recently.
func (s *NoHitLRU) PreRequest(ctx context.Context, request *types.LLMRequest, schedulingResult *types.SchedulingResult, _ int) {
	logger := log.FromContext(ctx).V(logutil.DEBUG)

	if schedulingResult == nil || len(schedulingResult.ProfileResults) == 0 {
		logger.Info("No scheduling result available")
		return
	}

	// Read the cold request state we stored in Score
	coldState, err := plugins.ReadPluginStateKey[*coldRequestState](s.pluginState, request.RequestId, plugins.StateKey(s.typedName.String()))
	// After fetching the cold state, drop it from the plugin state immediately (otherwise it will hang around until it becomes stale).
	s.pluginState.Delete(request.RequestId)

	if err != nil {
		logger.Info("No cold request state found, treating as non-cold request", "error", err)
		return
	}

	if !coldState.isCold {
		logger.Info("Not a cold request, skipping LRU update")
		return
	}

	// Get the primary profile's target pod
	primaryProfile := schedulingResult.ProfileResults[schedulingResult.PrimaryProfileName]
	if primaryProfile == nil || len(primaryProfile.TargetPods) == 0 {
		logger.Info("No target pod in primary profile")
		return
	}

	targetPod := primaryProfile.TargetPods[0]
	podName := targetPod.GetPod().NamespacedName.String()

	// Move the pod to the front of the LRU.
	var present struct{} // dummy value
	s.lruCache.Add(podName, present)

	logger.Info("Updated LRU cache for cold request", "pod", podName, "requestId", request.RequestId)
}
