package scorer

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// PendingTokensType is the type of the PendingTokens scorer.
	PendingTokensType = "pending-tokens-scorer"
)

// PendingTokensParameters defines configuration for the PendingTokens scorer.
type PendingTokensParameters struct {
	// RequestTimeout defines the timeout for requests in seconds.
	// once the request has been in-flight for this duration, it is considered to
	// be timed out and dropped.
	// This field accepts duration strings like "30s", "1m", "2h".
	RequestTimeout string `json:"requestTimeout"`
}

// compile-time type assertion
var _ scheduling.Scorer = &PendingTokens{}
var _ requestcontrol.PreRequest = &PendingTokens{}
var _ requestcontrol.ResponseComplete = &PendingTokens{}

// PendingTokensFactory defines the factory function for the PendingTokens scorer.
func PendingTokensFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	parameters := PendingTokensParameters{}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' scorer - %w", PendingTokensType, err)
		}
	}

	return NewPendingTokens(handle.Context(), &parameters).WithName(name), nil
}

// NewPendingTokens creates a new PendingTokens scorer.
func NewPendingTokens(ctx context.Context, params *PendingTokensParameters) *PendingTokens {
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

	scorer := &PendingTokens{
		typedName:      plugin.TypedName{Type: PendingTokensType},
		requestCache:   requestCache,
		endpointTokens: make(map[string]int),
		requestTokens:  make(map[string]int),
		mutex:          &sync.RWMutex{},
	}
	// callback to decrement count when requests expire
	// most requests will be removed in ResponseComplete, but this ensures
	// that we don't leak endpoint counts if ResponseComplete is not called
	requestCache.OnEviction(func(_ context.Context, reason ttlcache.EvictionReason,
		item *ttlcache.Item[string, *requestEntry]) {
		if reason == ttlcache.EvictionReasonExpired {
			entry := item.Value()

			scorer.mutex.RLock()
			tokenCount := scorer.requestTokens[entry.RequestID]
			scorer.mutex.RUnlock()

			for _, endpointName := range entry.PodNames {
				scorer.decrementPodTokens(endpointName, tokenCount)
			}

			scorer.mutex.Lock()
			delete(scorer.requestTokens, entry.RequestID)
			scorer.mutex.Unlock()
		}
	})

	go cleanCachePeriodically(ctx, requestCache, requestTimeout)

	return scorer
}

// PendingTokens keeps track of pending input tokens per endpoint
// to enable token-aware load balancing.
type PendingTokens struct {
	typedName plugin.TypedName

	// requestCache stores individual request entries keyed by requestID
	requestCache *ttlcache.Cache[string, *requestEntry]

	// endpointTokens maintains fast lookup for request tokens per endpoint
	endpointTokens map[string]int
	requestTokens  map[string]int // requestID -> token count
	mutex          *sync.RWMutex
}

// TypedName returns the typed name of the plugin.
func (s *PendingTokens) TypedName() plugin.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *PendingTokens) WithName(name string) *PendingTokens {
	s.typedName.Name = name
	return s
}

// Category returns how this scorer influences endpoint selection.
func (s *PendingTokens) Category() scheduling.ScorerCategory {
	return scheduling.Distribution
}

// Score scores endpoints based on their pending input token load.
// normalized to a range of 0-1. Endpoints with fewer pending tokens
// receive higher scores.
func (s *PendingTokens) Score(ctx context.Context, _ *scheduling.CycleState, _ *scheduling.LLMRequest,
	endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {

	pendingTokensByEndpoint := make(map[string]int)
	maxTokens := 0
	s.mutex.RLock()
	for endpointName, tokens := range s.endpointTokens {
		pendingTokensByEndpoint[endpointName] = tokens
		if tokens > maxTokens {
			maxTokens = tokens
		}
	}
	s.mutex.RUnlock()

	log.FromContext(ctx).V(logutil.DEBUG).Info("Pending token counts", "pendingTokens", pendingTokensByEndpoint, "maxTokens", maxTokens)

	scoredEndpointsMap := make(map[scheduling.Endpoint]float64, len(endpoints))
	for _, endpoint := range endpoints {
		endpointName := endpoint.GetMetadata().NamespacedName.String()
		tokens, exists := pendingTokensByEndpoint[endpointName]
		if !exists {
			scoredEndpointsMap[endpoint] = 1.0
			continue
		}
		if maxTokens == 0 {
			scoredEndpointsMap[endpoint] = 1.0
			continue
		}
		scoredEndpointsMap[endpoint] = 1.0 - (float64(tokens) / float64(maxTokens))
	}

	log.FromContext(ctx).V(logutil.DEBUG).Info("Scored endpoints", "scores", endpointScores(scoredEndpointsMap))
	return scoredEndpointsMap
}

// PreRequest is called before a request is sent to the target endpoint.
// It creates a new request entry in the cache with its own TTL and
// increments the token count for all selected endpoints for fast lookup.
func (s *PendingTokens) PreRequest(
	ctx context.Context,
	request *scheduling.LLMRequest,
	schedulingResult *scheduling.SchedulingResult,
) {
	debugLogger := log.FromContext(ctx).V(logutil.DEBUG)
	tokenCount := estimateTokenCount(request)

	endpointNames := make([]string, 0, len(schedulingResult.ProfileResults))
	for profileName, profileResult := range schedulingResult.ProfileResults {
		if profileResult == nil || len(profileResult.TargetEndpoints) == 0 {
			continue
		}

		endpointName := profileResult.TargetEndpoints[0].GetMetadata().NamespacedName.String()
		endpointNames = append(endpointNames, endpointName)
		s.incrementPodTokens(endpointName, tokenCount)
		debugLogger.Info(
			"Added request to cache",
			"requestId", request.RequestId,
			"endpointName", endpointName,
			"tokenCount", tokenCount,
			"profileName", profileName,
		)
	}

	s.mutex.Lock()
	s.requestTokens[request.RequestId] = tokenCount
	s.mutex.Unlock()

	// add to request cache
	s.requestCache.Set(request.RequestId, &requestEntry{PodNames: endpointNames, RequestID: request.RequestId}, 0) // Use default TTL
}

// ResponseComplete is called after a response is sent to the client.
// It removes the request entry from the cache and decrements
// the token counts for all associated endpoints.
func (s *PendingTokens) ResponseComplete(
	ctx context.Context,
	request *scheduling.LLMRequest,
	_ *requestcontrol.Response,
	targetPod *datalayer.EndpointMetadata,
) {
	debugLogger := log.FromContext(ctx).V(logutil.DEBUG).WithName("PendingTokens.ResponseComplete")
	if targetPod == nil {
		debugLogger.Info("Skipping ResponseComplete because targetPod is nil")
		return
	}

	if item, found := s.requestCache.GetAndDelete(request.RequestId); found {
		entry := item.Value()
		if entry != nil {
			s.mutex.RLock()
			tokenCount := s.requestTokens[request.RequestId]
			s.mutex.RUnlock()

			for _, endpointName := range entry.PodNames {
				s.decrementPodTokens(endpointName, tokenCount)
			}

			s.mutex.Lock()
			delete(s.requestTokens, request.RequestId)
			s.mutex.Unlock()
		}
	} else {
		debugLogger.Info("Request not found in cache", "requestId", request.RequestId)
	}
}

// incrementPodTokens increments the number of tokens for an endpoint.
func (s *PendingTokens) incrementPodTokens(endpointName string, count int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.endpointTokens[endpointName] += count
}

// decrementPodTokens decrements the pending token count for an endpoint and removes
// the entry if count reaches zero.
func (s *PendingTokens) decrementPodTokens(endpointName string, count int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if current, exists := s.endpointTokens[endpointName]; exists {
		if current <= count {
			delete(s.endpointTokens, endpointName)
		} else {
			s.endpointTokens[endpointName] = current - count
		}
	}
}

// estimateTokenCount estimates the number of input tokens for a request.
// Prefers actual token IDs if available, otherwise falls back to prompt size as a proxy.
func estimateTokenCount(request *scheduling.LLMRequest) int {
	if request == nil {
		return 0
	}
	if request.TokenizedPrompt != nil && len(request.TokenizedPrompt.TokenIDs) > 0 {
		return len(request.TokenizedPrompt.TokenIDs)
	}
	if request.Body == nil {
		return 0
	}
	return len(request.Body.PromptText())
}
