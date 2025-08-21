package scorer

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

func TestActiveRequestScorer_Score(t *testing.T) {
	podA := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: 2,
		},
	}
	podB := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-b", Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: 0,
		},
	}
	podC := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-c", Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: 15,
		},
	}

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
			scorer := NewActiveRequest(context.Background(), nil)
			test.setupCache(scorer)

			got := scorer.Score(context.Background(), nil, nil, test.input)

			if diff := cmp.Diff(test.wantScores, got); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}

func TestActiveRequestScorer_PreRequest(t *testing.T) {
	ctx := context.Background()

	scorer := NewActiveRequest(ctx, nil)

	podA := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: 2,
		},
	}

	request := &types.LLMRequest{
		RequestId: "test-request-1",
	}

	schedulingResult := &types.SchedulingResult{
		ProfileResults: map[string]*types.ProfileRunResult{
			"test-profile": {
				TargetPods: []types.Pod{podA},
			},
		},
	}

	// First request
	scorer.PreRequest(ctx, request, schedulingResult, 0)

	// Check cache and pod counts
	compositeKey := "default/pod-a.test-request-1"
	if !scorer.requestCache.Has(compositeKey) {
		t.Errorf("Expected request to be in cache with key %s", compositeKey)
	}

	scorer.mutex.RLock()
	count := scorer.podCounts["default/pod-a"]
	scorer.mutex.RUnlock()
	if count != 1 {
		t.Errorf("Expected pod-a count to be 1, got %d", count)
	}

	// Second request with different ID to same pod
	request2 := &types.LLMRequest{
		RequestId: "test-request-2",
	}
	schedulingResult2 := &types.SchedulingResult{
		ProfileResults: map[string]*types.ProfileRunResult{
			"test-profile": {
				TargetPods: []types.Pod{podA},
			},
		},
	}

	scorer.PreRequest(ctx, request2, schedulingResult2, 0)

	// Check incremented count
	scorer.mutex.RLock()
	count = scorer.podCounts["default/pod-a"]
	scorer.mutex.RUnlock()
	if count != 2 {
		t.Errorf("Expected pod-a count to be 2, got %d", count)
	}

	// Check both requests are in cache
	compositeKey2 := "default/pod-a.test-request-2"
	if !scorer.requestCache.Has(compositeKey2) {
		t.Errorf("Expected second request to be in cache with key %s", compositeKey2)
	}
}

func TestActiveRequestScorer_PostResponse(t *testing.T) {
	ctx := context.Background()

	scorer := NewActiveRequest(ctx, nil)

	request := &types.LLMRequest{
		RequestId: "test-request-1",
	}

	podA := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: 2,
		},
	}
	// Setup initial state: add request through PreRequest
	schedulingResult := &types.SchedulingResult{
		ProfileResults: map[string]*types.ProfileRunResult{
			"test-profile": {
				TargetPods: []types.Pod{podA},
			},
		},
	}

	scorer.PreRequest(ctx, request, schedulingResult, 0)

	// Verify initial state
	compositeKey := "default/pod-a.test-request-1"
	if !scorer.requestCache.Has(compositeKey) {
		t.Fatal("Request should be in cache before PostResponse")
	}

	scorer.mutex.RLock()
	initialCount := scorer.podCounts["default/pod-a"]
	scorer.mutex.RUnlock()
	if initialCount != 1 {
		t.Fatalf("Expected initial count to be 1, got %d", initialCount)
	}

	// Call PostResponse
	scorer.PostResponse(ctx, request, &requestcontrol.Response{}, podA.GetPod())

	// Check request is removed from cache
	if scorer.requestCache.Has(compositeKey) {
		t.Errorf("Request should be removed from cache after PostResponse")
	}

	// Check pod count is decremented and removed (since it was 1)
	scorer.mutex.RLock()
	_, exists := scorer.podCounts["default/pod-a"]
	scorer.mutex.RUnlock()
	if exists {
		t.Errorf("Pod should be removed from podCounts when count reaches 0")
	}
}

func TestActiveRequestScorer_TTLExpiration(t *testing.T) {
	ctx := context.Background()

	// Use very short timeout for test
	params := &ActiveRequestParameters{RequestTimeout: "1s"}
	scorer := NewActiveRequest(ctx, params) // 1 second timeout

	request := &types.LLMRequest{
		RequestId: "test-request-ttl",
	}

	podA := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
	}

	schedulingResult := &types.SchedulingResult{
		ProfileResults: map[string]*types.ProfileRunResult{
			"test-profile": {
				TargetPods: []types.Pod{podA},
			},
		},
	}

	// Add request
	scorer.PreRequest(ctx, request, schedulingResult, 0)

	// Verify request is added
	scorer.mutex.RLock()
	initialCount := scorer.podCounts["default/pod-a"]
	scorer.mutex.RUnlock()
	if initialCount != 1 {
		t.Fatalf("Expected initial count to be 1, got %d", initialCount)
	}

	// Wait for TTL expiration
	time.Sleep(2 * time.Second)

	// Trigger cleanup
	scorer.requestCache.DeleteExpired()

	// Check that pod count is decremented due to TTL expiration
	scorer.mutex.RLock()
	_, exists := scorer.podCounts["default/pod-a"]
	scorer.mutex.RUnlock()
	if exists {
		t.Errorf("Pod should be removed from podCounts after TTL expiration")
	}
}

func TestNewActiveRequestScorer_InvalidTimeout(t *testing.T) {
	params := &ActiveRequestParameters{RequestTimeout: "invalid"}
	scorer := NewActiveRequest(context.Background(), params)

	// Should use default timeout when invalid value is provided
	if scorer == nil {
		t.Error("Expected scorer to be created even with invalid timeout")
	}
}

func TestActiveRequestScorer_TypedName(t *testing.T) {
	scorer := NewActiveRequest(context.Background(), nil)

	typedName := scorer.TypedName()
	if typedName.Type != ActiveRequestType {
		t.Errorf("Expected type %s, got %s", ActiveRequestType, typedName.Type)
	}
}

func TestActiveRequestScorer_WithName(t *testing.T) {
	scorer := NewActiveRequest(context.Background(), nil)
	testName := "test-scorer"

	scorer = scorer.WithName(testName)

	if scorer.TypedName().Name != testName {
		t.Errorf("Expected name %s, got %s", testName, scorer.TypedName().Name)
	}
}
