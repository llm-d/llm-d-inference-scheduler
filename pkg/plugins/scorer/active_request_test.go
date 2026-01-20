package scorer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

// Test helper functions

func newTestPod(name string, queueSize int) *types.PodMetrics {
	return &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: name, Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: queueSize,
		},
	}
}

func newTestRequest(id string) *types.LLMRequest {
	return &types.LLMRequest{
		RequestId: id,
	}
}

func newTestSchedulingResult(profilePods map[string]types.Pod) *types.SchedulingResult {
	profileResults := make(map[string]*types.ProfileRunResult)
	for profile, pod := range profilePods {
		profileResults[profile] = &types.ProfileRunResult{
			TargetPods: []types.Pod{pod},
		}
	}
	return &types.SchedulingResult{
		ProfileResults: profileResults,
	}
}

func (s *ActiveRequest) getPodCount(podName string) int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.podCounts[podName]
}

func (s *ActiveRequest) hasPodCount(podName string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	_, exists := s.podCounts[podName]
	return exists
}

func TestActiveRequestScorer_Score(t *testing.T) {
	podA := newTestPod("pod-a", 2)
	podB := newTestPod("pod-b", 0)
	podC := newTestPod("pod-c", 15)

	tests := []struct {
		name       string
		setupCache func(*ActiveRequest)
		input      []types.Pod
		wantScores map[types.Pod]float64
	}{
		{
			name: "no pods in cache",
			setupCache: func(_ *ActiveRequest) {
				// Cache is empty
			},
			input: []types.Pod{podA, podB, podC},
			wantScores: map[types.Pod]float64{
				podA: 1,
				podB: 1,
				podC: 1,
			},
		},
		{
			name: "all pods in cache with different request counts",
			setupCache: func(s *ActiveRequest) {
				s.mutex.Lock()
				s.podCounts["default/pod-a"] = 3
				s.podCounts["default/pod-b"] = 0
				s.podCounts["default/pod-c"] = 6
				s.mutex.Unlock()
			},
			input: []types.Pod{podA, podB, podC},
			wantScores: map[types.Pod]float64{
				podA: 0.5,
				podB: 1.0,
				podC: 0.0,
			},
		},
		{
			name: "some pods in cache",
			setupCache: func(s *ActiveRequest) {
				s.mutex.Lock()
				s.podCounts["default/pod-a"] = 4
				s.podCounts["default/pod-c"] = 1
				// pod-b not in cache
				s.mutex.Unlock()
			},
			input: []types.Pod{podA, podB, podC},
			wantScores: map[types.Pod]float64{
				podA: 0.0,
				podB: 1.0,
				podC: 0.75,
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

	podA := newTestPod("pod-a", 2)
	podB := newTestPod("pod-b", 0)

	testProfile := "test-profile"

	t.Run("First request", func(t *testing.T) {
		request := newTestRequest("test-request-1")
		schedulingResult := newTestSchedulingResult(map[string]types.Pod{
			testProfile: podA,
		})

		scorer.PreRequest(ctx, request, schedulingResult)

		assert.True(t, scorer.requestCache.Has(request.RequestId), "Expected request to be in cache")
		assert.Equal(t, 1, scorer.getPodCount(podA.GetPod().NamespacedName.String()))
	})

	t.Run("Second request to multiple pods", func(t *testing.T) {
		request := newTestRequest("test-request-2")
		schedulingResult := newTestSchedulingResult(map[string]types.Pod{
			testProfile: podA,
			"prefill":   podB,
		})

		scorer.PreRequest(ctx, request, schedulingResult)

		assert.True(t, scorer.requestCache.Has(request.RequestId), "Expected request to be in cache")
		assert.Equal(t, 2, scorer.getPodCount(podA.GetPod().NamespacedName.String()))
		assert.Equal(t, 1, scorer.getPodCount(podB.GetPod().NamespacedName.String()))
	})
}

func TestActiveRequestScorer_ResponseComplete(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := NewActiveRequest(ctx, nil)

	podA := newTestPod("pod-a", 2)
	request := newTestRequest("test-request-1")

	// Setup initial state: add request through PreRequest
	schedulingResult := newTestSchedulingResult(map[string]types.Pod{
		"test-profile": podA,
	})
	scorer.PreRequest(ctx, request, schedulingResult)

	// Call ResponseComplete
	scorer.ResponseComplete(ctx, request, &requestcontrol.Response{}, podA.GetPod())

	assert.False(t, scorer.requestCache.Has(request.RequestId))
	assert.False(t, scorer.hasPodCount(podA.GetPod().NamespacedName.String()),
		"Pod count should be removed after decrement to zero")
}

func TestActiveRequestScorer_TTLExpiration(t *testing.T) {
	ctx := utils.NewTestContext(t)

	// Use very short timeout for test
	params := &ActiveRequestParameters{RequestTimeout: "1s"}
	scorer := NewActiveRequest(ctx, params)

	podA := newTestPod("pod-a", 0)
	request := newTestRequest("test-request-ttl")
	schedulingResult := newTestSchedulingResult(map[string]types.Pod{
		"test-profile": podA,
	})

	// Add request
	scorer.PreRequest(ctx, request, schedulingResult)

	// Verify request is added
	require.Equal(t, 1, scorer.getPodCount("default/pod-a"), "Expected initial count to be 1")

	// Wait for TTL expiration
	time.Sleep(2 * time.Second)

	// Trigger cleanup
	scorer.requestCache.DeleteExpired()

	// Check that pod count is decremented due to TTL expiration
	assert.False(t, scorer.hasPodCount("default/pod-a"),
		"Pod should be removed from podCounts after TTL expiration")
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
