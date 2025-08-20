package scorer

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// ActiveRequestScorerType is the type of the ActiveRequestScorer
	ActiveRequestScorerType = "active-request-scorer"

	// defaultRequestTimeout defines the default timeout for requests in seconds
	defaultRequestTimeout = 300
)

// ActiveRequestScorerParameters defines the parameters for the
// ActiveRequestScorer.
type ActiveRequestScorerParameters struct {
	// RequestTimeout defines the timeout for requests in seconds.
	// Once the request is "in-flight" for this duration, it is considered to
	// be timed out and dropped.
	RequestTimeout int `json:"requestTimeout"`
}

// requestEntry represents a single request in the cache
type requestEntry struct {
	PodName   string
	RequestID string
}

// String returns a string representation of the request entry.
func (r *requestEntry) String() string {
	return fmt.Sprintf("%s.%s", r.PodName, r.RequestID)
}

// compile-time type assertion
var _ framework.Scorer = &ActiveRequestScorer{}

// ActiveRequestScorerFactory defines the factory function for the ActiveRequestScorer.
func ActiveRequestScorerFactory(name string, rawParameters json.RawMessage, handle plugins.Handle) (plugins.Plugin, error) {
	parameters := ActiveRequestScorerParameters{RequestTimeout: defaultRequestTimeout}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' scorer - %w", ActiveRequestScorerType, err)
		}
	}

	return NewActiveRequestScorer(handle.Context(), &parameters).WithName(name), nil
}

// NewActiveRequestScorer creates a new ActiveRequestScorer scorer.
func NewActiveRequestScorer(ctx context.Context, params *ActiveRequestScorerParameters) *ActiveRequestScorer {
	requestTimeout := defaultRequestTimeout

	if params != nil && params.RequestTimeout > 0 {
		requestTimeout = params.RequestTimeout
		log.FromContext(ctx).V(logutil.DEFAULT).Info("Request timeout should be positive, using default request timeout")
	}

	// cache for individual requests with their own TTL
	requestCache := ttlcache.New[string, *requestEntry](
		ttlcache.WithTTL[string, *requestEntry](time.Duration(requestTimeout)*time.Second),
		ttlcache.WithDisableTouchOnHit[string, *requestEntry](),
	)

	scorer := &ActiveRequestScorer{
		typedName:    plugins.TypedName{Type: ActiveRequestScorerType},
		requestCache: requestCache,
		podCounts:    make(map[string]int),
		mutex:        &sync.RWMutex{},
	}
	// callback to decrement count when requests expire
	// most requests will be removed in PostResponse, but this ensures
	// that we don't leak pod counts if PostResponse is not called
	requestCache.OnEviction(func(_ context.Context, reason ttlcache.EvictionReason,
		item *ttlcache.Item[string, *requestEntry]) {
		if reason == ttlcache.EvictionReasonExpired {
			scorer.decrementPodCount(item.Value().PodName)
		}
	})

	go cleanCachePeriodically(ctx, requestCache, requestTimeout)

	return scorer
}

// ActiveRequestScorer keeps track of individual requests being served
// per pod.
type ActiveRequestScorer struct {
	typedName plugins.TypedName

	// requestCache stores individual request entries with unique composite keys (podName.requestID)
	requestCache *ttlcache.Cache[string, *requestEntry]

	// podCounts maintains fast lookup for request counts per pod
	podCounts map[string]int
	mutex     *sync.RWMutex
}

// TypedName returns the typed name of the plugin.
func (s *ActiveRequestScorer) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *ActiveRequestScorer) WithName(name string) *ActiveRequestScorer {
	s.typedName.Name = name
	return s
}

// Score scores the given pods based on the number of active requests
// being served by each pod. The score is normalized to a range of 0-1.
func (s *ActiveRequestScorer) Score(ctx context.Context, _ *types.CycleState, _ *types.LLMRequest,
	pods []types.Pod) map[types.Pod]float64 {
	scoredPods := make(map[string]int)
	maxCount := 0
	s.mutex.RLock()
	for podName, count := range s.podCounts {
		scoredPods[podName] = count
		if count >= maxCount {
			maxCount = count
		}
	}
	s.mutex.RUnlock()

	scoredPodsMap := make(map[types.Pod]float64, len(pods))
	for _, pod := range pods {
		podName := pod.GetPod().NamespacedName.String()
		if count, exists := scoredPods[podName]; exists {
			if count == 0 {
				scoredPodsMap[pod] = 1.0 // no requests means highest score
			} else {
				scoredPodsMap[pod] = float64(maxCount-count) / float64(maxCount)
			}
		} else {
			scoredPodsMap[pod] = 1.0
		}
	}

	log.FromContext(ctx).V(logutil.DEBUG).Info("Scored pods", "scores", scoredPodsMap)
	return scoredPodsMap
}

// PreRequest is called before a request is sent to the target pod.
// It creates a new request entry in the cache with its own TTL and
// increments the pod count for fast lookup.
func (s *ActiveRequestScorer) PreRequest(ctx context.Context, request *types.LLMRequest,
	schedulingResult *types.SchedulingResult, _ int) {
	debugLogger := log.FromContext(ctx).V(logutil.DEBUG)

	for _, profileResult := range schedulingResult.ProfileResults { // schedulingResult guaranteed not to be nil
		if profileResult == nil || profileResult.TargetPods == nil || len(profileResult.TargetPods) == 0 {
			continue
		}

		// create request entry for first pod only. TODO: support fallback pods
		entry := &requestEntry{
			PodName:   profileResult.TargetPods[0].GetPod().NamespacedName.String(),
			RequestID: request.RequestId,
		}

		// add to request cache with TTL
		s.requestCache.Set(entry.String(), entry, 0) // Use default TTL
		s.incrementPodCount(entry.PodName)

		debugLogger.Info("Added request to cache", "requestEntry", entry.String())
	}
}

// PostResponse is called after a response is sent to the client.
// It removes the specific request entry from the cache and decrements
// the pod count.
func (s *ActiveRequestScorer) PostResponse(ctx context.Context, request *types.LLMRequest,
	_ *requestcontrol.Response, targetPod *backend.Pod) {
	debugLogger := log.FromContext(ctx).V(logutil.DEBUG).WithName("ActiveRequestScorer.PostResponse")
	if targetPod == nil {
		debugLogger.Info("Skipping PostResponse because targetPod is nil")
		return
	}

	entry := requestEntry{targetPod.NamespacedName.String(), request.RequestId}

	if _, found := s.requestCache.GetAndDelete(entry.String()); found {
		s.decrementPodCount(entry.PodName)
		debugLogger.Info("Removed request from cache", "requestEntry", entry.String())
	} else {
		debugLogger.Info("Request not found in cache", "requestEntry", entry.String())
	}
}

// incrementPodCount increments the request count for a pod.
func (s *ActiveRequestScorer) incrementPodCount(podName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.podCounts[podName]++
}

// decrementPodCount decrements the request count for a pod and removes
// the entry if count reaches zero.
func (s *ActiveRequestScorer) decrementPodCount(podName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if count, exists := s.podCounts[podName]; exists {
		if count <= 1 {
			delete(s.podCounts, podName)
		} else {
			s.podCounts[podName] = count - 1
		}
	}
}

func cleanCachePeriodically(ctx context.Context, cache *ttlcache.Cache[string, *requestEntry], requestTimeout int) {
	ticker := time.NewTicker(time.Duration(requestTimeout) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cache.DeleteExpired()
		}
	}
}
