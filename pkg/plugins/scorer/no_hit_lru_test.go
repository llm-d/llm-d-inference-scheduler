package scorer_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

type fakeHandle struct {
	ctx     context.Context
	plugins map[string]plugins.Plugin
}

func newFakeHandle(ctx context.Context) *fakeHandle {
	return &fakeHandle{ctx: ctx, plugins: map[string]plugins.Plugin{}}
}

func (h *fakeHandle) Context() context.Context {
	return h.ctx
}

func (h *fakeHandle) Plugin(name string) plugins.Plugin {
	return h.plugins[name]
}

func (h *fakeHandle) AddPlugin(name string, plugin plugins.Plugin) {
	h.plugins[name] = plugin
}

func (h *fakeHandle) GetAllPlugins() []plugins.Plugin {
	result := make([]plugins.Plugin, 0, len(h.plugins))
	for _, plugin := range h.plugins {
		result = append(result, plugin)
	}
	return result
}

func (h *fakeHandle) GetAllPluginsWithNames() map[string]plugins.Plugin {
	return h.plugins
}

type stubPlugin struct {
	name plugins.TypedName
}

func (p *stubPlugin) TypedName() plugins.TypedName {
	return p.name
}

func TestNoHitLRUFactoryDependencyValidation(t *testing.T) {
	tests := []struct {
		name         string
		handle       *fakeHandle
		params       map[string]any
		expectError  bool
		errorMessage string
	}{
		{
			name:        "missing prefix cache plugin - should work as optimization",
			handle:      newFakeHandle(context.Background()),
			expectError: false,
		},
		{
			name: "prefix plugin present - should work",
			handle: func() *fakeHandle {
				h := newFakeHandle(context.Background())
				h.AddPlugin(prefix.PrefixCachePluginType, &stubPlugin{name: plugins.TypedName{Type: prefix.PrefixCachePluginType, Name: prefix.PrefixCachePluginType}})
				return h
			}(),
			expectError: false,
		},
	}

	for _, tt := range tests {
		// Marshal params if provided
		var raw json.RawMessage
		if tt.params != nil {
			bytes, err := json.Marshal(tt.params)
			if err != nil {
				t.Fatalf("failed to marshal parameters: %v", err)
			}
			raw = bytes
		}

		plugin, err := scorer.NoHitLRUFactory("test", raw, tt.handle)
		if tt.expectError {
			if err == nil {
				t.Fatalf("expected error for case %q, got none", tt.name)
			}
			if tt.errorMessage != "" && !strings.Contains(err.Error(), tt.errorMessage) {
				t.Fatalf("error message mismatch for case %q: %v", tt.name, err)
			}
			continue
		}

		if err != nil {
			t.Fatalf("unexpected error for case %q: %v", tt.name, err)
		}
		if plugin == nil {
			t.Fatalf("expected plugin instance for case %q", tt.name)
		}
	}
}

func TestNoHitLRUScorer(t *testing.T) {
	podA := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		MetricsState: &backendmetrics.MetricsState{},
	}
	podB := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		MetricsState: &backendmetrics.MetricsState{},
	}
	podC := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-c"}},
		MetricsState: &backendmetrics.MetricsState{},
	}

	tests := []struct {
		name        string
		scorer      framework.Scorer
		req         *types.LLMRequest
		input       []types.Pod
		prefixState *prefix.SchedulingContextState
		wantScores  map[types.Pod]float64
		description string
	}{
		{
			name:   "cold request - all pods never used",
			scorer: scorer.NewNoHitLRU(context.Background(), nil),
			req: &types.LLMRequest{
				TargetModel: "test-model",
			},
			input: []types.Pod{podA, podB, podC},
			prefixState: &prefix.SchedulingContextState{
				PrefixCacheServers: make(map[prefix.ServerID]int), // empty = cold request
			},
			wantScores: map[types.Pod]float64{
				podA: 1.0, // All never-used pods get high scores
				podB: 0.5,
				podC: 0.0,
			},
			description: "Never-used pods should get high scores for cold requests",
		},
		{
			name:   "cache hit - neutral scores",
			scorer: scorer.NewNoHitLRU(context.Background(), nil),
			req: &types.LLMRequest{
				TargetModel: "test-model",
			},
			input: []types.Pod{podA, podB, podC},
			prefixState: &prefix.SchedulingContextState{
				PrefixCacheServers: map[prefix.ServerID]int{
					{Name: "server1", Namespace: "default"}: 5, // non-empty = cache hit
				},
			},
			wantScores: map[types.Pod]float64{
				podA: 0.5, // All pods get neutral scores for cache hits
				podB: 0.5,
				podC: 0.5,
			},
			description: "Cache hits should return neutral scores",
		},
		{
			name:   "single pod - max score",
			scorer: scorer.NewNoHitLRU(context.Background(), nil),
			req: &types.LLMRequest{
				TargetModel: "test-model",
			},
			input: []types.Pod{podA},
			prefixState: &prefix.SchedulingContextState{
				PrefixCacheServers: make(map[prefix.ServerID]int), // empty = cold request
			},
			wantScores: map[types.Pod]float64{
				podA: 1.0, // Single pod gets max score
			},
			description: "Single pod should get maximum score",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create cycle state and set prefix state
			cycleState := &types.CycleState{}
			if test.prefixState != nil {
				cycleState.Write(plugins.StateKey(prefix.PrefixCachePluginType), test.prefixState)
			}

			got := test.scorer.Score(context.Background(), cycleState, test.req, test.input)

			if diff := cmp.Diff(test.wantScores, got); diff != "" {
				t.Errorf("%s: Unexpected output (-want +got): %v", test.description, diff)
			}
		})
	}
}

func TestNoHitLRUBasicFunctionality(t *testing.T) {
	ctx := context.Background()
	scorer := scorer.NewNoHitLRU(ctx, nil)

	podA := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		MetricsState: &backendmetrics.MetricsState{},
	}
	podB := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		MetricsState: &backendmetrics.MetricsState{},
	}

	pods := []types.Pod{podA, podB}

	// Test basic scoring for cold request (no crashes, returns valid scores)
	coldPrefixState := &prefix.SchedulingContextState{
		PrefixCacheServers: make(map[prefix.ServerID]int), // empty = cold request
	}
	cycleState := &types.CycleState{}
	cycleState.Write(plugins.StateKey(prefix.PrefixCachePluginType), coldPrefixState)

	scores := scorer.Score(ctx, cycleState, &types.LLMRequest{}, pods)

	// Should return scores for all pods
	if len(scores) != 2 {
		t.Errorf("Expected 2 scores, got %d", len(scores))
	}

	// All scores should be valid (between 0 and 1)
	for pod, score := range scores {
		if score < 0 || score > 1 {
			t.Errorf("Invalid score %f for pod %s", score, pod.GetPod().NamespacedName.String())
		}
	}

	// For never-used pods, should have different scores (to provide ordering)
	if scores[podA] == scores[podB] {
		t.Errorf("Expected different scores for different pods, both got %f", scores[podA])
	}
}

func TestNoPrefixCacheStateFound(t *testing.T) {
	ctx := context.Background()
	scorer := scorer.NewNoHitLRU(ctx, nil)

	podA := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		MetricsState: &backendmetrics.MetricsState{},
	}
	pods := []types.Pod{podA}
	cycleState := &types.CycleState{}

	scores := scorer.Score(ctx, cycleState, &types.LLMRequest{}, pods)

	if scores[podA] != 1.0 {
		t.Errorf("Failure to find a prefix cache should result in scoring as a cold request.")
	}
}

func TestNoHitLRUPreferLeastRecentlyUsedAfterColdRequests(t *testing.T) {
	ctx := context.Background()
	scorer := scorer.NewNoHitLRU(ctx, nil)

	podA := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{},
	}
	podB := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-b", Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{},
	}
	podC := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-c", Namespace: "default"}},
		MetricsState: &backendmetrics.MetricsState{},
	}
	pods := []types.Pod{podA, podB, podC}

	primaryProfile := "primary-profile"
	toPrefixState := func(entries map[prefix.ServerID]int) *types.CycleState {
		cycle := &types.CycleState{}
		cycle.Write(plugins.StateKey(prefix.PrefixCachePluginType), &prefix.SchedulingContextState{PrefixCacheServers: entries})
		return cycle
	}

	requestToPod := func(target types.Pod) *types.SchedulingResult {
		return &types.SchedulingResult{
			PrimaryProfileName: primaryProfile,
			ProfileResults: map[string]*types.ProfileRunResult{
				primaryProfile: {
					TargetPods: []types.Pod{target},
				},
			},
		}
	}

	// Test LRU behavior indirectly through scoring rather than internal state
	assertHighestScoredPod := func(expectedPod types.Pod, testName string) {
		t.Helper()
		coldReq := &types.LLMRequest{RequestId: testName + "-scoring-check"}
		scores := scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReq, pods)

		highestScore := -1.0
		var highestPod types.Pod
		for pod, score := range scores {
			if score > highestScore {
				highestScore = score
				highestPod = pod
			}
		}

		if highestPod.GetPod().NamespacedName.String() != expectedPod.GetPod().NamespacedName.String() {
			t.Fatalf("expected %s to have highest score for LRU behavior, but %s had highest score (%f). All scores: %+v",
				expectedPod.GetPod().NamespacedName.String(),
				highestPod.GetPod().NamespacedName.String(),
				highestScore,
				scores)
		}
	}

	t.Run("initial cold request seeds cache", func(_ *testing.T) {
		coldReqA := &types.LLMRequest{RequestId: "cold-1"}
		scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReqA, pods)
		scorer.PreRequest(ctx, coldReqA, requestToPod(podA), 0)
		// After podA handles a cold request, other pods should score higher for new cold requests
		assertHighestScoredPod(podB, "after-podA-used")
	})

	t.Run("unused pods rank above existing ones", func(t *testing.T) {
		coldReqCheck := &types.LLMRequest{RequestId: "cold-check"}
		coldScores := scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReqCheck, pods)
		if coldScores[podB] <= coldScores[podA] {
			t.Fatalf("expected pod-b to outrank pod-a after pod-a handled previous cold request, scores=%+v", coldScores)
		}
		if coldScores[podB] != 1.0 {
			t.Fatalf("expected pod-b to score 1.0, scores=%+v", coldScores)
		}
		if coldScores[podC] != 0.5 {
			t.Fatalf("expected pod-c to score 0.5, scores=%+v", coldScores)
		}
	})

	t.Run("warm request leaves LRU untouched", func(t *testing.T) {
		warmReq := &types.LLMRequest{RequestId: "warm-1"}
		warmState := map[prefix.ServerID]int{
			{Name: "server1", Namespace: "default"}: 1,
		}
		warmScores := scorer.Score(ctx, toPrefixState(warmState), warmReq, pods)
		for _, score := range warmScores {
			if score != 0.5 {
				t.Fatalf("expected neutral score for warm request, got %f", score)
			}
		}
		scorer.PreRequest(ctx, warmReq, requestToPod(podB), 0)
		postWarmReq := &types.LLMRequest{RequestId: "cold-after-warm"}
		postWarmScores := scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), postWarmReq, pods)
		if postWarmScores[podB] <= postWarmScores[podA] {
			t.Fatalf("expected warm request to leave ordering unchanged, scores=%+v", postWarmScores)
		}
	})

	t.Run("second cold request rotates to podB", func(_ *testing.T) {
		// Simulate podB handling a cold request
		coldReqB := &types.LLMRequest{RequestId: "cold-2"}
		scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReqB, pods)
		scorer.PreRequest(ctx, coldReqB, requestToPod(podB), 0)
		// Now podC should score highest since both podA and podB have been used
		assertHighestScoredPod(podC, "after-podB-used")
	})

	t.Run("third cold request rotates back to podA", func(_ *testing.T) {
		// Simulate podC handling a cold request
		coldReqC := &types.LLMRequest{RequestId: "cold-3"}
		scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReqC, pods)
		scorer.PreRequest(ctx, coldReqC, requestToPod(podC), 0)
		// Now podA should score highest again (LRU rotation)
		assertHighestScoredPod(podA, "after-podC-used")
	})
}

func TestNoHitLRUEdgeCases(t *testing.T) {
	ctx := context.Background()
	scorer := scorer.NewNoHitLRU(ctx, nil)

	podA := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		MetricsState: &backendmetrics.MetricsState{},
	}

	t.Run("empty pods list", func(t *testing.T) {
		emptyPods := []types.Pod{}
		cycleState := &types.CycleState{}
		cycleState.Write(plugins.StateKey(prefix.PrefixCachePluginType), &prefix.SchedulingContextState{
			PrefixCacheServers: make(map[prefix.ServerID]int), // cold request
		})

		scores := scorer.Score(ctx, cycleState, &types.LLMRequest{}, emptyPods)

		if len(scores) != 0 {
			t.Errorf("Expected empty scores for empty pods list, got %d scores", len(scores))
		}
	})

	t.Run("nil pods list", func(t *testing.T) {
		cycleState := &types.CycleState{}
		cycleState.Write(plugins.StateKey(prefix.PrefixCachePluginType), &prefix.SchedulingContextState{
			PrefixCacheServers: make(map[prefix.ServerID]int), // cold request
		})

		scores := scorer.Score(ctx, cycleState, &types.LLMRequest{}, nil)

		if scores == nil {
			t.Errorf("Expected non-nil scores map for nil pods list")
		}
		if len(scores) != 0 {
			t.Errorf("Expected empty scores for nil pods list, got %d scores", len(scores))
		}
	})

	t.Run("single pod returns 1.0", func(t *testing.T) {
		pods := []types.Pod{podA}
		cycleState := &types.CycleState{}
		cycleState.Write(plugins.StateKey(prefix.PrefixCachePluginType), &prefix.SchedulingContextState{
			PrefixCacheServers: make(map[prefix.ServerID]int), // cold request
		})

		scores := scorer.Score(ctx, cycleState, &types.LLMRequest{}, pods)

		if scores[podA] != 1.0 {
			t.Errorf("Expected single pod to get score 1.0, got %f", scores[podA])
		}
	})
}
