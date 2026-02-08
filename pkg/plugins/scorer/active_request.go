package scorer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// ActiveRequestType is the type of the ActiveRequest scorer.
	ActiveRequestType = "active-request-scorer"

	// defaultRequestTimeout defines the default timeout for open requests to be
	// considered stale and removed from the cache.
	defaultRequestTimeout = 2 * time.Minute
)

// ActiveRequestParameters defines the parameters for the
// ActiveRequest.
type ActiveRequestParameters struct {
	// RequestTimeout defines the timeout for requests in seconds.
	// Once the request is "in-flight" for this duration, it is considered to
	// be timed out and dropped.
	// This field accepts duration strings like "30s", "1m", "2h".
	RequestTimeout string `json:"requestTimeout"`
}

// requestEntry represents a single request in the cache
type requestEntry struct {
	PodNames  []string
	RequestID string
}

// String returns a string representation of the request entry.
func (r requestEntry) String() string {
	return fmt.Sprintf("%s:%s", r.RequestID, strings.Join(r.PodNames, "."))
}

// endpointScores implements logr.Marshaler to lazily convert endpoint keys
// to strings only when the log line is actually written.
type endpointScores map[scheduling.Endpoint]float64

func (s endpointScores) MarshalLog() interface{} {
	result := make(map[string]float64, len(s))
	for ep, score := range s {
		result[ep.GetMetadata().NamespacedName.String()] = score
	}
	return result
}

// compile-time type assertion
var _ scheduling.Scorer = &ActiveRequest{}
var _ requestcontrol.PreRequest = &ActiveRequest{}
var _ requestcontrol.ResponseComplete = &ActiveRequest{}

// ActiveRequestFactory defines the factory function for the ActiveRequest scorer.
func ActiveRequestFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	parameters := ActiveRequestParameters{}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' scorer - %w", ActiveRequestType, err)
		}
	}

	return NewActiveRequest(handle.Context(), &parameters).WithName(name), nil
}

// NewActiveRequest creates a new ActiveRequest scorer.
func NewActiveRequest(ctx context.Context, params *ActiveRequestParameters) *ActiveRequest {
	requestTimeout := defaultRequestTimeout
	logger := log.FromContext(ctx)

	if params != nil && params.RequestTimeout != "" {
		paramsRequestTimeout, err := time.ParseDuration(params.RequestTimeout)
		if err != nil || paramsRequestTimeout <= 0 {
			logger.Error(err, "Invalid request timeout duration, using default request timeout")
		} else {
			requestTimeout = paramsRequestTimeout
			logger.Info("Using request timeout", "requestTimeout", requestTimeout)
		}
	}

	// cache for individual requests with their own TTL
	requestCache := ttlcache.New[string, *requestEntry](
		ttlcache.WithTTL[string, *requestEntry](requestTimeout),
		ttlcache.WithDisableTouchOnHit[string, *requestEntry](),
	)

	scorer := &ActiveRequest{
		typedName:      plugin.TypedName{Type: ActiveRequestType},
		requestCache:   requestCache,
		endpointCounts: make(map[string]int),
		mutex:          &sync.RWMutex{},
	}
	// callback to decrement count when requests expire
	// most requests will be removed in ResponseComplete, but this ensures
	// that we don't leak endpoint counts if ResponseComplete is not called
	requestCache.OnEviction(func(_ context.Context, reason ttlcache.EvictionReason,
		item *ttlcache.Item[string, *requestEntry]) {
		if reason == ttlcache.EvictionReasonExpired {
			for _, endpointName := range item.Value().PodNames {
				scorer.decrementPodCount(endpointName)
			}
		}
	})

	go cleanCachePeriodically(ctx, requestCache, requestTimeout)

	return scorer
}

// ActiveRequest keeps track of individual requests being served
// per endpoint.
type ActiveRequest struct {
	typedName plugin.TypedName

	// requestCache stores individual request entries with unique composite keys (endpointName.requestID)
	requestCache *ttlcache.Cache[string, *requestEntry]

	// endpointCounts maintains fast lookup for request counts per endpoint
	endpointCounts map[string]int
	mutex          *sync.RWMutex
}

// TypedName returns the typed name of the plugin.
func (s *ActiveRequest) TypedName() plugin.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *ActiveRequest) WithName(name string) *ActiveRequest {
	s.typedName.Name = name
	return s
}

// Category returns the preference the scorer applies when scoring candidate endpoints.
func (s *ActiveRequest) Category() scheduling.ScorerCategory {
	return scheduling.Distribution
}

// Score scores the given endpoints based on the number of active requests
// being served by each endpoint. The score is normalized to a range of 0-1.
func (s *ActiveRequest) Score(ctx context.Context, _ *scheduling.CycleState, _ *scheduling.LLMRequest,
	endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	scoredEndpoints := make(map[string]int)
	maxCount := 0
	s.mutex.RLock()
	for endpointName, count := range s.endpointCounts {
		scoredEndpoints[endpointName] = count
		if count >= maxCount {
			maxCount = count
		}
	}
	s.mutex.RUnlock()

	log.FromContext(ctx).V(logutil.DEBUG).Info("Active request counts", "endpointCounts", scoredEndpoints, "maxCount", maxCount)

	scoredEndpointsMap := make(map[scheduling.Endpoint]float64, len(endpoints))
	for _, endpoint := range endpoints {
		endpointName := endpoint.GetMetadata().NamespacedName.String()
		if count, exists := scoredEndpoints[endpointName]; exists {
			if count == 0 || maxCount == 0 {
				scoredEndpointsMap[endpoint] = 1.0 // no requests means highest score
			} else {
				scoredEndpointsMap[endpoint] = float64(maxCount-count) / float64(maxCount)
			}
		} else {
			scoredEndpointsMap[endpoint] = 1.0
		}
	}

	log.FromContext(ctx).V(logutil.DEBUG).Info("Scored endpoints", "scores", endpointScores(scoredEndpointsMap))
	return scoredEndpointsMap
}

// PreRequest is called before a request is sent to the target endpoint.
// It creates a new request entry in the cache with its own TTL and
// increments the endpoint count for fast lookup.
func (s *ActiveRequest) PreRequest(
	ctx context.Context,
	request *scheduling.LLMRequest,
	schedulingResult *scheduling.SchedulingResult,
) {
	debugLogger := log.FromContext(ctx).V(logutil.DEBUG)

	endpointNames := make([]string, 0, len(schedulingResult.ProfileResults))
	for profileName, profileResult := range schedulingResult.ProfileResults {
		if profileResult == nil || len(profileResult.TargetEndpoints) == 0 {
			continue
		}

		endpointName := profileResult.TargetEndpoints[0].GetMetadata().NamespacedName.String()
		endpointNames = append(endpointNames, endpointName)
		s.incrementPodCount(endpointName)
		debugLogger.Info(
			"Added request to cache",
			"requestId", request.RequestId,
			"endpointName", endpointName,
			"profileName", profileName,
		)
	}

	// add to request cache
	s.requestCache.Set(request.RequestId, &requestEntry{PodNames: endpointNames, RequestID: request.RequestId}, 0) // Use default TTL
}

// ResponseComplete is called after a response is sent to the client.
// It removes the specific request entry from the cache and decrements
// the endpoint count.
func (s *ActiveRequest) ResponseComplete(
	ctx context.Context,
	request *scheduling.LLMRequest,
	_ *requestcontrol.Response,
	targetPod *datalayer.EndpointMetadata,
) {
	debugLogger := log.FromContext(ctx).V(logutil.DEBUG).WithName("ActiveRequest.ResponseComplete")
	if targetPod == nil {
		debugLogger.Info("Skipping ResponseComplete because targetPod is nil")
		return
	}

	if item, found := s.requestCache.GetAndDelete(request.RequestId); found {
		entry := item.Value()
		if entry != nil {
			for _, endpointName := range entry.PodNames {
				s.decrementPodCount(endpointName)
			}
			debugLogger.Info("Removed request from cache", "requestEntry", entry.String())
		} else {
			debugLogger.Info("Request entry value is nil", "requestId", request.RequestId)
		}
	} else {
		debugLogger.Info("Request not found in cache", "requestId", request.RequestId)
	}
}

// incrementPodCount increments the request count for a endpoint.
func (s *ActiveRequest) incrementPodCount(endpointName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.endpointCounts[endpointName]++
}

// decrementPodCount decrements the request count for a endpoint and removes
// the entry if count reaches zero.
func (s *ActiveRequest) decrementPodCount(endpointName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if count, exists := s.endpointCounts[endpointName]; exists {
		if count <= 1 {
			delete(s.endpointCounts, endpointName)
		} else {
			s.endpointCounts[endpointName] = count - 1
		}
	}
}

func cleanCachePeriodically[K comparable, V any](ctx context.Context, cache *ttlcache.Cache[K, V], requestTimeout time.Duration) {
	ticker := time.NewTicker(requestTimeout)
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
