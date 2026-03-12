package scorer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

// Test helper functions

func newTestEndpoint(name string, queueSize int) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: name, Namespace: "default"}},
		&fwkdl.Metrics{
			WaitingQueueSize: queueSize,
		},
		nil,
	)
}

func newTestRequest(id string) *scheduling.LLMRequest {
	return &scheduling.LLMRequest{
		RequestId: id,
	}
}

func newTestSchedulingResult(profileEndpoints map[string]scheduling.Endpoint) *scheduling.SchedulingResult {
	profileResults := make(map[string]*scheduling.ProfileRunResult)
	for profile, endpoint := range profileEndpoints {
		profileResults[profile] = &scheduling.ProfileRunResult{
			TargetEndpoints: []scheduling.Endpoint{endpoint},
		}
	}
	return &scheduling.SchedulingResult{
		ProfileResults: profileResults,
	}
}

func (s *ActiveRequest) getPodCount(endpointName string) int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.endpointCounts[endpointName]
}

func (s *ActiveRequest) hasPodCount(endpointName string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	_, exists := s.endpointCounts[endpointName]
	return exists
}

func TestActiveRequestScorer_Score(t *testing.T) {
	endpointA := newTestEndpoint("pod-a", 2)
	endpointB := newTestEndpoint("pod-b", 0)
	endpointC := newTestEndpoint("pod-c", 15)

	tests := []struct {
		name       string
		setupCache func(*ActiveRequest)
		input      []scheduling.Endpoint
		wantScores map[scheduling.Endpoint]float64
	}{
		{
			name: "no endpoints in cache",
			setupCache: func(_ *ActiveRequest) {
				// Cache is empty
			},
			input: []scheduling.Endpoint{endpointA, endpointB, endpointC},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 1,
				endpointB: 1,
				endpointC: 1,
			},
		},
		{
			name: "all endpoints in cache with different request counts",
			setupCache: func(s *ActiveRequest) {
				s.mutex.Lock()
				s.endpointCounts["default/pod-a"] = 3
				s.endpointCounts["default/pod-b"] = 0
				s.endpointCounts["default/pod-c"] = 6
				s.mutex.Unlock()
			},
			input: []scheduling.Endpoint{endpointA, endpointB, endpointC},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.5,
				endpointB: 1.0,
				endpointC: 0.0,
			},
		},
		{
			name: "some endpoints in cache",
			setupCache: func(s *ActiveRequest) {
				s.mutex.Lock()
				s.endpointCounts["default/pod-a"] = 4
				s.endpointCounts["default/pod-c"] = 1
				// pod-b not in cache
				s.mutex.Unlock()
			},
			input: []scheduling.Endpoint{endpointA, endpointB, endpointC},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.0,
				endpointB: 1.0,
				endpointC: 0.75,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := utils.NewTestContext(t)

			scorer := NewActiveRequest(ctx, nil)
			test.setupCache(scorer)

			got := scorer.Score(ctx, nil, nil, test.input)

			assert.Equal(t, test.wantScores, got)
		})
	}
}

func TestActiveRequestScorer_PreRequest(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := NewActiveRequest(ctx, nil)

	endpointA := newTestEndpoint("pod-a", 2)
	endpointB := newTestEndpoint("pod-b", 0)

	testProfile := "test-profile"

	t.Run("First request", func(t *testing.T) {
		request := newTestRequest("test-request-1")
		schedulingResult := newTestSchedulingResult(map[string]scheduling.Endpoint{
			testProfile: endpointA,
		})

		scorer.PreRequest(ctx, request, schedulingResult)

		assert.True(t, scorer.requestCache.Has(request.RequestId), "Expected request to be in cache")
		assert.Equal(t, 1, scorer.getPodCount(endpointA.GetMetadata().NamespacedName.String()))
	})

	t.Run("Second request to multiple endpoints", func(t *testing.T) {
		request := newTestRequest("test-request-2")
		schedulingResult := newTestSchedulingResult(map[string]scheduling.Endpoint{
			testProfile: endpointA,
			"prefill":   endpointB,
		})

		scorer.PreRequest(ctx, request, schedulingResult)

		assert.True(t, scorer.requestCache.Has(request.RequestId), "Expected request to be in cache")
		assert.Equal(t, 2, scorer.getPodCount(endpointA.GetMetadata().NamespacedName.String()))
		assert.Equal(t, 1, scorer.getPodCount(endpointB.GetMetadata().NamespacedName.String()))
	})
}

func TestActiveRequestScorer_ResponseComplete(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := NewActiveRequest(ctx, nil)

	endpointA := newTestEndpoint("pod-a", 2)
	request := newTestRequest("test-request-1")

	// Setup initial state: add request through PreRequest
	schedulingResult := newTestSchedulingResult(map[string]scheduling.Endpoint{
		"test-profile": endpointA,
	})
	scorer.PreRequest(ctx, request, schedulingResult)

	// Call ResponseComplete
	scorer.ResponseComplete(ctx, request, &requestcontrol.Response{}, endpointA.GetMetadata())

	assert.False(t, scorer.requestCache.Has(request.RequestId))
	assert.False(t, scorer.hasPodCount(endpointA.GetMetadata().NamespacedName.String()),
		"Pod count should be removed after decrement to zero")
}

func TestActiveRequestScorer_TTLExpiration(t *testing.T) {
	ctx := utils.NewTestContext(t)

	// Use very short timeout for test
	params := &ActiveRequestParameters{RequestTimeout: "1s"}
	scorer := NewActiveRequest(ctx, params)

	endpointA := newTestEndpoint("pod-a", 0)
	request := newTestRequest("test-request-ttl")
	schedulingResult := newTestSchedulingResult(map[string]scheduling.Endpoint{
		"test-profile": endpointA,
	})

	// Add request
	scorer.PreRequest(ctx, request, schedulingResult)

	// Verify request is added
	require.Equal(t, 1, scorer.getPodCount("default/pod-a"), "Expected initial count to be 1")

	// Wait for TTL expiration
	time.Sleep(2 * time.Second)

	// Trigger cleanup
	scorer.requestCache.DeleteExpired()

	// Check that endpoint count is decremented due to TTL expiration
	assert.False(t, scorer.hasPodCount("default/pod-a"),
		"Pod should be removed from endpointCounts after TTL expiration")
}

func TestNewActiveRequestScorer_InvalidTimeout(t *testing.T) {
	ctx := utils.NewTestContext(t)

	params := &ActiveRequestParameters{RequestTimeout: "invalid"}
	scorer := NewActiveRequest(ctx, params)

	// Should use default timeout when invalid value is provided
	assert.NotNil(t, scorer, "Expected scorer to be created even with invalid timeout")
}

func TestActiveRequestScorer_TypedName(t *testing.T) {
	ctx := utils.NewTestContext(t)

	scorer := NewActiveRequest(ctx, nil)

	assert.Equal(t, ActiveRequestType, scorer.TypedName().Type)
}

func TestActiveRequestScorer_WithName(t *testing.T) {
	ctx := utils.NewTestContext(t)

	scorer := NewActiveRequest(ctx, nil)
	testName := "test-scorer"

	scorer = scorer.WithName(testName)

	assert.Equal(t, testName, scorer.TypedName().Name)
}

func TestActiveRequest_IdleThresholdAndMaxBusyScore(t *testing.T) {
	ctx := utils.NewTestContext(t)

	t.Run("binary mode: idleThreshold=0, maxBusyScore=0", func(t *testing.T) {
		params := &ActiveRequestParameters{
			RequestTimeout: "1m",
			IdleThreshold:  0,
			MaxBusyScore:   0.0,
		}
		scorer := NewActiveRequest(ctx, params)

		podA := newTestEndpoint("pod-a", 0)
		podB := newTestEndpoint("pod-b", 0)

		// Both idle → both score 1.0
		scores := scorer.Score(ctx, nil, nil, []scheduling.Endpoint{podA, podB})
		assert.Equal(t, 1.0, scores[podA])
		assert.Equal(t, 1.0, scores[podB])

		// Send request to pod A
		req1 := newTestRequest("req-1")
		result := newTestSchedulingResult(map[string]scheduling.Endpoint{"primary": podA})
		scorer.PreRequest(ctx, req1, result)

		// Pod A busy → 0.0, Pod B idle → 1.0
		scores = scorer.Score(ctx, nil, nil, []scheduling.Endpoint{podA, podB})
		assert.Equal(t, 0.0, scores[podA], "Busy pod scores 0.0 in binary mode")
		assert.Equal(t, 1.0, scores[podB], "Idle pod scores 1.0")
	})

	t.Run("hybrid mode: idleThreshold=1, maxBusyScore=0.5", func(t *testing.T) {
		params := &ActiveRequestParameters{
			RequestTimeout: "1m",
			IdleThreshold:  1,
			MaxBusyScore:   0.5,
		}
		scorer := NewActiveRequest(ctx, params)

		podA := newTestEndpoint("pod-a", 0)
		podB := newTestEndpoint("pod-b", 0)
		podC := newTestEndpoint("pod-c", 0)

		// Send 1 request to pod A, 2 to pod B
		req1 := newTestRequest("req-1")
		resultA := newTestSchedulingResult(map[string]scheduling.Endpoint{"primary": podA})
		scorer.PreRequest(ctx, req1, resultA)

		req2 := newTestRequest("req-2")
		req3 := newTestRequest("req-3")
		resultB := newTestSchedulingResult(map[string]scheduling.Endpoint{"primary": podB})
		scorer.PreRequest(ctx, req2, resultB)
		scorer.PreRequest(ctx, req3, resultB)

		// Pod A: 1 request ≤ idleThreshold → idle → 1.0
		// Pod B: 2 requests > idleThreshold → busy, maxCount=2 → (2-2)/2*0.5 = 0.0
		// Pod C: 0 requests ≤ idleThreshold → idle → 1.0
		scores := scorer.Score(ctx, nil, nil, []scheduling.Endpoint{podA, podB, podC})
		assert.Equal(t, 1.0, scores[podA], "Pod with 1 request is idle (threshold=1)")
		assert.Equal(t, 0.0, scores[podB], "Pod with 2 requests (busiest) scores 0.0")
		assert.Equal(t, 1.0, scores[podC], "Pod with 0 requests is idle")
	})
}
