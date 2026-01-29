package scorer_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

var _ plugin.Handle = &fakeHandle{}

type fakeHandle struct {
	ctx     context.Context
	plugins map[string]plugin.Plugin
}

func newFakeHandle(ctx context.Context) *fakeHandle {
	return &fakeHandle{ctx: ctx, plugins: map[string]plugin.Plugin{}}
}

func (h *fakeHandle) Context() context.Context {
	return h.ctx
}

func (h *fakeHandle) Plugin(name string) plugin.Plugin {
	return h.plugins[name]
}

func (h *fakeHandle) AddPlugin(name string, plugin plugin.Plugin) {
	h.plugins[name] = plugin
}

func (h *fakeHandle) GetAllPlugins() []plugin.Plugin {
	result := make([]plugin.Plugin, 0, len(h.plugins))
	for _, plugin := range h.plugins {
		result = append(result, plugin)
	}
	return result
}

func (h *fakeHandle) GetAllPluginsWithNames() map[string]plugin.Plugin {
	return h.plugins
}

func (h *fakeHandle) PodList() []k8stypes.NamespacedName {
	return make([]k8stypes.NamespacedName, 0)
}

type stubPlugin struct {
	name plugin.TypedName
}

func (p *stubPlugin) TypedName() plugin.TypedName {
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
			handle:      newFakeHandle(utils.NewTestContext(t)),
			expectError: false,
		},
		{
			name: "prefix plugin present - should work",
			handle: func() *fakeHandle {
				h := newFakeHandle(utils.NewTestContext(t))
				h.AddPlugin(prefix.PrefixCachePluginType, &stubPlugin{name: plugin.TypedName{Type: prefix.PrefixCachePluginType, Name: prefix.PrefixCachePluginType}})
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
	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		&datalayer.Metrics{},
		nil,
	)
	endpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		&datalayer.Metrics{},
		nil,
	)
	endpointC := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-c"}},
		&datalayer.Metrics{},
		nil,
	)

	tests := []struct {
		name        string
		scorer      scheduling.Scorer
		req         *scheduling.LLMRequest
		input       []scheduling.Endpoint
		prefixState *prefix.SchedulingContextState
		wantScores  map[scheduling.Endpoint]float64
		description string
	}{
		{
			name:   "cold request - all endpoints never used",
			scorer: scorer.NewNoHitLRU(utils.NewTestContext(t), nil),
			req: &scheduling.LLMRequest{
				TargetModel: "test-model",
			},
			input: []scheduling.Endpoint{endpointA, endpointB, endpointC},
			prefixState: &prefix.SchedulingContextState{
				PrefixCacheServers: make(map[prefix.ServerID]int), // empty = cold request
			},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 1.0, // All never-used endpoints get high scores
				endpointB: 0.5,
				endpointC: 0.0,
			},
			description: "Never-used endpoints should get high scores for cold requests",
		},
		{
			name:   "cache hit - neutral scores",
			scorer: scorer.NewNoHitLRU(utils.NewTestContext(t), nil),
			req: &scheduling.LLMRequest{
				TargetModel: "test-model",
			},
			input: []scheduling.Endpoint{endpointA, endpointB, endpointC},
			prefixState: &prefix.SchedulingContextState{
				PrefixCacheServers: map[prefix.ServerID]int{
					{Name: "server1", Namespace: "default"}: 5, // non-empty = cache hit
				},
			},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.5, // All endpoints get neutral scores for cache hits
				endpointB: 0.5,
				endpointC: 0.5,
			},
			description: "Cache hits should return neutral scores",
		},
		{
			name:   "single endpoint - max score",
			scorer: scorer.NewNoHitLRU(utils.NewTestContext(t), nil),
			req: &scheduling.LLMRequest{
				TargetModel: "test-model",
			},
			input: []scheduling.Endpoint{endpointA},
			prefixState: &prefix.SchedulingContextState{
				PrefixCacheServers: make(map[prefix.ServerID]int), // empty = cold request
			},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 1.0, // Single endpoint gets max score
			},
			description: "Single endpoint should get maximum score",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create cycle state and set prefix state
			cycleState := &scheduling.CycleState{}
			if test.prefixState != nil {
				cycleState.Write(plugin.StateKey(plugin.TypedName{Type: prefix.PrefixCachePluginType,
					Name: prefix.PrefixCachePluginType}.String()), test.prefixState)
			}

			got := test.scorer.Score(utils.NewTestContext(t), cycleState, test.req, test.input)

			if diff := cmp.Diff(test.wantScores, got); diff != "" {
				t.Errorf("%s: Unexpected output (-want +got): %v", test.description, diff)
			}
		})
	}
}

func TestNoHitLRUBasicFunctionality(t *testing.T) {
	ctx := utils.NewTestContext(t)

	scorer := scorer.NewNoHitLRU(ctx, nil)

	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		&datalayer.Metrics{},
		nil,
	)
	endpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		&datalayer.Metrics{},
		nil,
	)

	endpoints := []scheduling.Endpoint{endpointA, endpointB}

	// Test basic scoring for cold request (no crashes, returns valid scores)
	coldPrefixState := &prefix.SchedulingContextState{
		PrefixCacheServers: make(map[prefix.ServerID]int), // empty = cold request
	}
	cycleState := &scheduling.CycleState{}
	cycleState.Write(plugin.StateKey(plugin.TypedName{Type: prefix.PrefixCachePluginType,
		Name: prefix.PrefixCachePluginType}.String()), coldPrefixState)

	scores := scorer.Score(ctx, cycleState, &scheduling.LLMRequest{}, endpoints)

	// Should return scores for all endpoints
	if len(scores) != 2 {
		t.Errorf("Expected 2 scores, got %d", len(scores))
	}

	// All scores should be valid (between 0 and 1)
	for endpoint, score := range scores {
		if score < 0 || score > 1 {
			t.Errorf("Invalid score %f for endpoint %s", score, endpoint.GetMetadata().NamespacedName.String())
		}
	}

	// For never-used endpoints, should have different scores (to provide ordering)
	if scores[endpointA] == scores[endpointB] {
		t.Errorf("Expected different scores for different endpoints, both got %f", scores[endpointA])
	}
}

func TestNoPrefixCacheStateFound(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := scorer.NewNoHitLRU(ctx, nil)

	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		&datalayer.Metrics{},
		nil,
	)
	endpoints := []scheduling.Endpoint{endpointA}
	cycleState := &scheduling.CycleState{}

	scores := scorer.Score(ctx, cycleState, &scheduling.LLMRequest{}, endpoints)

	if scores[endpointA] != 1.0 {
		t.Errorf("Failure to find a prefix cache should result in scoring as a cold request.")
	}
}

func TestNoHitLRUPreferLeastRecentlyUsedAfterColdRequests(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := scorer.NewNoHitLRU(ctx, nil)

	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
		&datalayer.Metrics{},
		nil,
	)
	endpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b", Namespace: "default"}},
		&datalayer.Metrics{},
		nil,
	)
	endpointC := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-c", Namespace: "default"}},
		&datalayer.Metrics{},
		nil,
	)
	endpoints := []scheduling.Endpoint{endpointA, endpointB, endpointC}

	primaryProfile := "primary-profile"
	toPrefixState := func(entries map[prefix.ServerID]int) *scheduling.CycleState {
		cycle := &scheduling.CycleState{}
		cycle.Write(plugin.StateKey(plugin.TypedName{Type: prefix.PrefixCachePluginType,
			Name: prefix.PrefixCachePluginType}.String()), &prefix.SchedulingContextState{PrefixCacheServers: entries})
		return cycle
	}

	requestToEndpoint := func(target scheduling.Endpoint) *scheduling.SchedulingResult {
		return &scheduling.SchedulingResult{
			PrimaryProfileName: primaryProfile,
			ProfileResults: map[string]*scheduling.ProfileRunResult{
				primaryProfile: {
					TargetEndpoints: []scheduling.Endpoint{target},
				},
			},
		}
	}

	// Test LRU behavior indirectly through scoring rather than internal state
	assertHighestScoredPod := func(expectedEndpoint scheduling.Endpoint, testName string) {
		t.Helper()
		coldReq := &scheduling.LLMRequest{RequestId: testName + "-scoring-check"}
		scores := scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReq, endpoints)

		highestScore := -1.0
		var highestEndpoint scheduling.Endpoint
		for endpoint, score := range scores {
			if score > highestScore {
				highestScore = score
				highestEndpoint = endpoint
			}
		}

		if highestEndpoint.GetMetadata().NamespacedName.String() != expectedEndpoint.GetMetadata().NamespacedName.String() {
			t.Fatalf("expected %s to have highest score for LRU behavior, but %s had highest score (%f). All scores: %+v",
				expectedEndpoint.GetMetadata().NamespacedName.String(),
				highestEndpoint.GetMetadata().NamespacedName.String(),
				highestScore,
				scores)
		}
	}

	t.Run("initial cold request seeds cache", func(_ *testing.T) {
		coldReqA := &scheduling.LLMRequest{RequestId: "cold-1"}
		scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReqA, endpoints)
		scorer.PreRequest(ctx, coldReqA, requestToEndpoint(endpointA))
		// After endpointA handles a cold request, other endpoints should score higher for new cold requests
		assertHighestScoredPod(endpointB, "after-endpointA-used")
	})

	t.Run("unused endpoints rank above existing ones", func(t *testing.T) {
		coldReqCheck := &scheduling.LLMRequest{RequestId: "cold-check"}
		coldScores := scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReqCheck, endpoints)
		if coldScores[endpointB] <= coldScores[endpointA] {
			t.Fatalf("expected endpoint-b to outrank endpoint-a after endpoint-a handled previous cold request, scores=%+v", coldScores)
		}
		if coldScores[endpointB] != 1.0 {
			t.Fatalf("expected endpoint-b to score 1.0, scores=%+v", coldScores)
		}
		if coldScores[endpointC] != 0.5 {
			t.Fatalf("expected endpoint-c to score 0.5, scores=%+v", coldScores)
		}
	})

	t.Run("warm request leaves LRU untouched", func(t *testing.T) {
		warmReq := &scheduling.LLMRequest{RequestId: "warm-1"}
		warmState := map[prefix.ServerID]int{
			{Name: "server1", Namespace: "default"}: 1,
		}
		warmScores := scorer.Score(ctx, toPrefixState(warmState), warmReq, endpoints)
		for _, score := range warmScores {
			if score != 0.5 {
				t.Fatalf("expected neutral score for warm request, got %f", score)
			}
		}
		scorer.PreRequest(ctx, warmReq, requestToEndpoint(endpointB))
		postWarmReq := &scheduling.LLMRequest{RequestId: "cold-after-warm"}
		postWarmScores := scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), postWarmReq, endpoints)
		if postWarmScores[endpointB] <= postWarmScores[endpointA] {
			t.Fatalf("expected warm request to leave ordering unchanged, scores=%+v", postWarmScores)
		}
	})

	t.Run("second cold request rotates to endpointB", func(_ *testing.T) {
		// Simulate endpointB handling a cold request
		coldReqB := &scheduling.LLMRequest{RequestId: "cold-2"}
		scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReqB, endpoints)
		scorer.PreRequest(ctx, coldReqB, requestToEndpoint(endpointB))
		// Now endpointC should score highest since both endpointA and endpointB have been used
		assertHighestScoredPod(endpointC, "after-endpointB-used")
	})

	t.Run("third cold request rotates back to endpointA", func(_ *testing.T) {
		// Simulate endpointC handling a cold request
		coldReqC := &scheduling.LLMRequest{RequestId: "cold-3"}
		scorer.Score(ctx, toPrefixState(make(map[prefix.ServerID]int)), coldReqC, endpoints)
		scorer.PreRequest(ctx, coldReqC, requestToEndpoint(endpointC))
		// Now endpointA should score highest again (LRU rotation)
		assertHighestScoredPod(endpointA, "after-endpointC-used")
	})
}

func TestNoHitLRUEdgeCases(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := scorer.NewNoHitLRU(ctx, nil)

	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		&datalayer.Metrics{},
		nil,
	)

	t.Run("empty endpoints list", func(t *testing.T) {
		emptyEndpoints := []scheduling.Endpoint{}
		cycleState := &scheduling.CycleState{}
		cycleState.Write(plugin.StateKey(plugin.TypedName{Type: prefix.PrefixCachePluginType,
			Name: prefix.PrefixCachePluginType}.String()), &prefix.SchedulingContextState{
			PrefixCacheServers: make(map[prefix.ServerID]int), // cold request
		})

		scores := scorer.Score(ctx, cycleState, &scheduling.LLMRequest{}, emptyEndpoints)

		if len(scores) != 0 {
			t.Errorf("Expected empty scores for empty endpoints list, got %d scores", len(scores))
		}
	})

	t.Run("nil endpoints list", func(t *testing.T) {
		cycleState := &scheduling.CycleState{}
		cycleState.Write(plugin.StateKey(plugin.TypedName{Type: prefix.PrefixCachePluginType,
			Name: prefix.PrefixCachePluginType}.String()), &prefix.SchedulingContextState{
			PrefixCacheServers: make(map[prefix.ServerID]int), // cold request
		})

		scores := scorer.Score(ctx, cycleState, &scheduling.LLMRequest{}, nil)

		if scores == nil {
			t.Errorf("Expected non-nil scores map for nil endpoints list")
		}
		if len(scores) != 0 {
			t.Errorf("Expected empty scores for nil endpoints list, got %d scores", len(scores))
		}
	})

	t.Run("single endpoint returns 1.0", func(t *testing.T) {
		endpoints := []scheduling.Endpoint{endpointA}
		cycleState := &scheduling.CycleState{}
		cycleState.Write(plugin.StateKey(plugin.TypedName{Type: prefix.PrefixCachePluginType,
			Name: prefix.PrefixCachePluginType}.String()), &prefix.SchedulingContextState{
			PrefixCacheServers: make(map[prefix.ServerID]int), // cold request
		})

		scores := scorer.Score(ctx, cycleState, &scheduling.LLMRequest{}, endpoints)

		if scores[endpointA] != 1.0 {
			t.Errorf("Expected single endpoint to get score 1.0, got %f", scores[endpointA])
		}
	})
}

func TestNoHitLRUPrefillDecodeTracking(t *testing.T) {
	// Prefill worker endpoints
	prefillEndpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "prefill-a", Namespace: "default"}},
		&datalayer.Metrics{},
		nil,
	)
	prefillEndpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "prefill-b", Namespace: "default"}},
		&datalayer.Metrics{},
		nil,
	)

	// Decode worker endpoints
	decodeEndpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "decode-a", Namespace: "default"}},
		&datalayer.Metrics{},
		nil,
	)
	decodeEndpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "decode-b", Namespace: "default"}},
		&datalayer.Metrics{},
		nil,
	)

	prefillEndpoints := []scheduling.Endpoint{prefillEndpointA, prefillEndpointB}
	decodeEndpoints := []scheduling.Endpoint{decodeEndpointA, decodeEndpointB}

	coldPrefixState := &scheduling.CycleState{}
	coldPrefixState.Write(plugin.StateKey(prefix.PrefixCachePluginType), &prefix.SchedulingContextState{
		PrefixCacheServers: make(map[prefix.ServerID]int), // empty = cold request
	})

	ctx := context.Background()

	t.Run("P/D scenario - both profiles tracked separately", func(t *testing.T) {
		scorer := scorer.NewNoHitLRU(ctx, nil)

		// First cold request with P/D
		req1 := &scheduling.LLMRequest{RequestId: "pd-request-1"}
		scorer.Score(ctx, coldPrefixState, req1, append(prefillEndpoints, decodeEndpoints...))

		// Simulate scheduling result with both prefill and decode profiles
		pdResult := &scheduling.SchedulingResult{
			PrimaryProfileName: "decode",
			ProfileResults: map[string]*scheduling.ProfileRunResult{
				"prefill": {
					TargetEndpoints: []scheduling.Endpoint{prefillEndpointA},
				},
				"decode": {
					TargetEndpoints: []scheduling.Endpoint{decodeEndpointA},
				},
			},
		}
		scorer.PreRequest(ctx, req1, pdResult)

		// Second cold request - both prefillPodB and decodePodB should score higher
		// since prefillPodA and decodePodA were just used
		req2 := &scheduling.LLMRequest{RequestId: "pd-request-2"}
		prefillScores := scorer.Score(ctx, coldPrefixState, req2, prefillEndpoints)
		decodeScores := scorer.Score(ctx, coldPrefixState, req2, decodeEndpoints)

		if prefillScores[prefillEndpointB] <= prefillScores[prefillEndpointA] {
			t.Errorf("Expected prefill-b to score higher than prefill-a after prefill-a was used: %+v", prefillScores)
		}

		if decodeScores[decodeEndpointB] <= decodeScores[decodeEndpointA] {
			t.Errorf("Expected decode-b to score higher than decode-a after decode-a was used: %+v", decodeScores)
		}
	})

	t.Run("non-P/D scenario - only primary profile exists", func(t *testing.T) {
		req := &scheduling.LLMRequest{RequestId: "non-pd-request"}
		scorer := scorer.NewNoHitLRU(ctx, nil)
		scorer.Score(ctx, coldPrefixState, req, decodeEndpoints)

		// Scheduling result with only decode profile (no prefill)
		result := &scheduling.SchedulingResult{
			PrimaryProfileName: "decode",
			ProfileResults: map[string]*scheduling.ProfileRunResult{
				"decode": {
					TargetEndpoints: []scheduling.Endpoint{decodeEndpointA},
				},
				// No "prefill" profile in results
			},
		}
		// Should not panic when prefill profile doesn't exist
		scorer.PreRequest(ctx, req, result)

		// Verify decodePodA was tracked
		req2 := &scheduling.LLMRequest{RequestId: "non-pd-request-2"}
		scores := scorer.Score(ctx, coldPrefixState, req2, decodeEndpoints)

		if scores[decodeEndpointB] <= scores[decodeEndpointA] {
			t.Errorf("Expected decode-b to score higher than decode-a: %+v", scores)
		}
	})

	t.Run("nil scheduling result - graceful handling", func(_ *testing.T) {
		req := &scheduling.LLMRequest{RequestId: "nil-result"}
		scorer := scorer.NewNoHitLRU(ctx, nil)
		scorer.Score(ctx, coldPrefixState, req, decodeEndpoints)

		// Should not panic with nil result
		scorer.PreRequest(ctx, req, nil)
	})

	t.Run("empty profile results - graceful handling", func(_ *testing.T) {
		req := &scheduling.LLMRequest{RequestId: "empty-results"}
		scorer := scorer.NewNoHitLRU(ctx, nil)
		scorer.Score(ctx, coldPrefixState, req, decodeEndpoints)

		result := &scheduling.SchedulingResult{
			PrimaryProfileName: "decode",
			ProfileResults:     map[string]*scheduling.ProfileRunResult{},
		}
		// Should not panic with empty profile results
		scorer.PreRequest(ctx, req, result)
	})
}
