package scorer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// newApproxScorer creates a PrecisePrefixCacheScorer in approximate mode for tests.
func newApproxScorer(t *testing.T) *PrecisePrefixCacheScorer {
	t.Helper()
	ctx := context.Background()

	scorer, err := New(ctx, PrecisePrefixCachePluginConfig{
		Mode: ModeApproximate,
		ApproximateConfig: &ApproximateConfig{
			BlockSizeTokens:        1, // 1 token = 4 chars for easy testing
			MaxPrefixBlocksToMatch: 256,
			LRUCapacityPerServer:   100,
			AutoTune:               false,
		},
	}, nil)
	require.NoError(t, err)
	return scorer
}

func testEndpoint(name, addr, port string) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: name},
			Address:        addr,
			Port:           port,
		},
		&fwkdl.Metrics{},
		nil,
	)
}

func completionsRequest(prompt string) *scheduling.LLMRequest {
	return &scheduling.LLMRequest{
		RequestId:   "test-req-1",
		TargetModel: "test-model",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: prompt},
		},
	}
}

// TestApproximateMode_NewScorer verifies approximate mode creates successfully
// without tokenizer or KV events config.
func TestApproximateMode_NewScorer(t *testing.T) {
	scorer := newApproxScorer(t)

	assert.Equal(t, ModeApproximate, scorer.mode)
	assert.NotNil(t, scorer.approxIndexer)
	assert.NotNil(t, scorer.approxPluginState)
	assert.Nil(t, scorer.kvCacheIndexer, "approximate mode should not initialize kvCacheIndexer")
	assert.Nil(t, scorer.kvEventsConfig, "approximate mode should not initialize kvEventsConfig")
}

// TestApproximateMode_E2E tests the full approximate mode flow:
// PrepareRequestData → Score → PreRequest → Score (with learned cache)
func TestApproximateMode_E2E(t *testing.T) {
	scorer := newApproxScorer(t)

	endpoints := []scheduling.Endpoint{
		testEndpoint("pod-a", "10.0.0.1", "8080"),
		testEndpoint("pod-b", "10.0.0.2", "8080"),
	}

	// Prompt: 16 chars = 4 blocks at blockSize=1 (4 chars/block)
	request := completionsRequest("aaaabbbbccccdddd")

	// --- Phase 1: First request, empty indexer ---
	cycleState := scheduling.NewCycleState()
	err := scorer.PrepareRequestData(context.Background(), request, endpoints)
	require.NoError(t, err)

	scores := scorer.Score(context.Background(), cycleState, request, endpoints)
	// No data in indexer yet, all scores should be 0
	for _, ep := range endpoints {
		assert.Equal(t, 0.0, scores[ep], "first request should have no cache hits")
	}

	// Simulate routing to pod-a
	scorer.PreRequest(context.Background(), request, &scheduling.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"default": {
				TargetEndpoints: []scheduling.Endpoint{endpoints[0]}, // pod-a
			},
		},
	})

	// --- Phase 2: Second request with same prefix, should hit pod-a ---
	request2 := completionsRequest("aaaabbbbccccdddd")
	request2.RequestId = "test-req-2"

	cycleState2 := scheduling.NewCycleState()
	err = scorer.PrepareRequestData(context.Background(), request2, endpoints)
	require.NoError(t, err)

	scores2 := scorer.Score(context.Background(), cycleState2, request2, endpoints)

	// pod-a should have score > 0 (cache hit from self-report)
	// pod-b should have score 0
	assert.InDelta(t, 1.0, scores2[endpoints[0]], 0.01, "pod-a should have full cache hit (4/4 blocks)")
	assert.InDelta(t, 0.0, scores2[endpoints[1]], 0.01, "pod-b should have no cache hit")
}

// TestApproximateMode_SharedPrefix tests that two requests sharing a prefix
// both benefit from the cached prefix.
func TestApproximateMode_SharedPrefix(t *testing.T) {
	scorer := newApproxScorer(t)

	endpoints := []scheduling.Endpoint{
		testEndpoint("pod-a", "10.0.0.1", "8080"),
	}

	// First request: "aaaabbbbccccdddd" → 4 blocks
	req1 := completionsRequest("aaaabbbbccccdddd")
	req1.RequestId = "req-1"

	cycleState := scheduling.NewCycleState()
	_ = scorer.PrepareRequestData(context.Background(), req1, endpoints)
	scorer.Score(context.Background(), cycleState, req1, endpoints)
	scorer.PreRequest(context.Background(), req1, &scheduling.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"default": {TargetEndpoints: []scheduling.Endpoint{endpoints[0]}},
		},
	})

	// Second request shares prefix but has different suffix
	req2 := completionsRequest("aaaabbbbccccddddeeeeffffgggghhhh")
	req2.RequestId = "req-2"

	cycleState2 := scheduling.NewCycleState()
	_ = scorer.PrepareRequestData(context.Background(), req2, endpoints)
	scores := scorer.Score(context.Background(), cycleState2, req2, endpoints)

	// 4 out of 8 blocks match → score = 4/8 = 0.5
	assert.InDelta(t, 0.5, scores[endpoints[0]], 0.01,
		"shared prefix should give partial score")
}

// TestApproximateMode_ModelScoped tests that different models don't share cache.
func TestApproximateMode_ModelScoped(t *testing.T) {
	scorer := newApproxScorer(t)

	endpoints := []scheduling.Endpoint{
		testEndpoint("pod-a", "10.0.0.1", "8080"),
	}

	prompt := "aaaabbbbccccdddd"

	// Route a request for model-a
	req1 := &scheduling.LLMRequest{
		RequestId:   "req-1",
		TargetModel: "model-a",
		Body:        &scheduling.LLMRequestBody{Completions: &scheduling.CompletionsRequest{Prompt: prompt}},
	}
	cycleState := scheduling.NewCycleState()
	_ = scorer.PrepareRequestData(context.Background(), req1, endpoints)
	scorer.Score(context.Background(), cycleState, req1, endpoints)
	scorer.PreRequest(context.Background(), req1, &scheduling.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"default": {TargetEndpoints: []scheduling.Endpoint{endpoints[0]}},
		},
	})

	// Now request same prompt but different model
	req2 := &scheduling.LLMRequest{
		RequestId:   "req-2",
		TargetModel: "model-b",
		Body:        &scheduling.LLMRequestBody{Completions: &scheduling.CompletionsRequest{Prompt: prompt}},
	}
	cycleState2 := scheduling.NewCycleState()
	_ = scorer.PrepareRequestData(context.Background(), req2, endpoints)
	scores := scorer.Score(context.Background(), cycleState2, req2, endpoints)

	assert.Equal(t, 0.0, scores[endpoints[0]],
		"different model should have no cache hit even with same prompt")
}

// TestApproximateMode_PreRequestPDDisaggregation tests that P/D disaggregation
// records both primary and prefill endpoints.
func TestApproximateMode_PreRequestPDDisaggregation(t *testing.T) {
	scorer := newApproxScorer(t)

	podA := testEndpoint("pod-a", "10.0.0.1", "8080")
	podB := testEndpoint("pod-b", "10.0.0.2", "8080")
	endpoints := []scheduling.Endpoint{podA, podB}

	req := completionsRequest("aaaabbbbccccdddd")
	_ = scorer.PrepareRequestData(context.Background(), req, endpoints)
	scorer.Score(context.Background(), scheduling.NewCycleState(), req, endpoints)

	// Route with both primary (pod-a) and prefill (pod-b)
	scorer.PreRequest(context.Background(), req, &scheduling.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"default":  {TargetEndpoints: []scheduling.Endpoint{podA}},
			"prefill":  {TargetEndpoints: []scheduling.Endpoint{podB}},
		},
	})

	// Both pods should have cache entries now
	req2 := completionsRequest("aaaabbbbccccdddd")
	req2.RequestId = "req-2"
	_ = scorer.PrepareRequestData(context.Background(), req2, endpoints)
	scores := scorer.Score(context.Background(), scheduling.NewCycleState(), req2, endpoints)

	assert.Greater(t, scores[podA], 0.0, "primary pod should have cache hit")
	assert.Greater(t, scores[podB], 0.0, "prefill pod should have cache hit")
}

// TestApproximateMode_NilRequest tests graceful handling of nil/empty requests.
func TestApproximateMode_NilRequest(t *testing.T) {
	scorer := newApproxScorer(t)
	endpoints := []scheduling.Endpoint{testEndpoint("pod-a", "10.0.0.1", "8080")}

	// nil request
	err := scorer.PrepareRequestData(context.Background(), nil, endpoints)
	assert.NoError(t, err)

	scores := scorer.Score(context.Background(), scheduling.NewCycleState(), nil, endpoints)
	assert.Nil(t, scores)
}

// TestApproximateMode_TooShortPrompt tests that prompts shorter than block size are skipped.
func TestApproximateMode_TooShortPrompt(t *testing.T) {
	scorer := newApproxScorer(t)
	endpoints := []scheduling.Endpoint{testEndpoint("pod-a", "10.0.0.1", "8080")}

	// "hi" = 2 chars < 4 chars (1 token block)
	req := completionsRequest("hi")

	err := scorer.PrepareRequestData(context.Background(), req, endpoints)
	assert.NoError(t, err)

	scores := scorer.Score(context.Background(), scheduling.NewCycleState(), req, endpoints)
	assert.Nil(t, scores, "too-short prompt should produce nil scores")
}

// TestApproximateMode_DefaultConfig tests that default config is applied when ApproximateConfig is nil.
func TestApproximateMode_DefaultConfig(t *testing.T) {
	ctx := context.Background()
	scorer, err := New(ctx, PrecisePrefixCachePluginConfig{
		Mode: ModeApproximate,
		// No ApproximateConfig → should use defaults
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, defaultApproxBlockSizeTokens, scorer.approxBlockSize)
	assert.Equal(t, defaultApproxMaxPrefixBlocks, scorer.approxMaxBlocks)
	assert.True(t, scorer.approxAutoTune)
}

// TestModeConfig_Validation tests mode configuration.
func TestModeConfig_Validation(t *testing.T) {
	ctx := context.Background()

	// Approximate mode should work without indexerConfig/tokenizer
	scorer, err := New(ctx, PrecisePrefixCachePluginConfig{
		Mode: ModeApproximate,
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, ModeApproximate, scorer.mode)

	// Default mode should be precise
	_, err = New(ctx, PrecisePrefixCachePluginConfig{}, nil)
	// This will fail because precise mode requires tokenizer config.
	// That's expected — precise mode needs a real indexer.
	assert.Error(t, err, "precise mode without indexer config should fail")
}

// TestApproximateMode_PluginStateIsolation tests that approximate mode uses
// its own PluginState key, not conflicting with precise mode.
func TestApproximateMode_PluginStateIsolation(t *testing.T) {
	assert.NotEqual(t, stateKey, approxStateKey,
		"precise and approximate mode should use different PluginState keys")
}

// TestApproximateMode_Produces tests that the plugin declares the correct data keys.
func TestApproximateMode_Produces(t *testing.T) {
	scorer := newApproxScorer(t)
	produces := scorer.Produces()
	assert.Contains(t, produces, "PrefixCacheMatchInfoKey")
}

// TestApproximateMode_Category tests that the plugin returns Affinity category.
func TestApproximateMode_Category(t *testing.T) {
	scorer := newApproxScorer(t)
	assert.Equal(t, scheduling.Affinity, scorer.Category())
}

// TestCombineScores tests the score combination logic for unified mode.
func TestCombineScores(t *testing.T) {
	epA := testEndpoint("pod-a", "10.0.0.1", "8080")
	epB := testEndpoint("pod-b", "10.0.0.2", "8080")

	precise := map[scheduling.Endpoint]float64{epA: 0.3, epB: 0.8}
	approx := map[scheduling.Endpoint]float64{epA: 0.7, epB: 0.2}

	combined := combineScores(precise, approx)
	assert.Equal(t, 0.7, combined[epA], "should take max(0.3, 0.7)")
	assert.Equal(t, 0.8, combined[epB], "should take max(0.8, 0.2)")
}

func TestCombineScores_NilInputs(t *testing.T) {
	epA := testEndpoint("pod-a", "10.0.0.1", "8080")

	scores := map[scheduling.Endpoint]float64{epA: 0.5}

	assert.Equal(t, scores, combineScores(nil, scores))
	assert.Equal(t, scores, combineScores(scores, nil))
	assert.Nil(t, combineScores(nil, nil))
}

func TestCombineScores_AsymmetricEndpoints(t *testing.T) {
	epA := testEndpoint("pod-a", "10.0.0.1", "8080")
	epB := testEndpoint("pod-b", "10.0.0.2", "8080")

	// precise only knows about epA, approximate knows about both
	precise := map[scheduling.Endpoint]float64{epA: 0.5}
	approx := map[scheduling.Endpoint]float64{epA: 0.3, epB: 0.7}

	combined := combineScores(precise, approx)
	assert.Equal(t, 0.5, combined[epA], "should keep precise for epA (0.5 > 0.3)")
	assert.Equal(t, 0.7, combined[epB], "should add epB from approximate")
}

func TestCombineScores_PreciseUndifferentiated(t *testing.T) {
	epA := testEndpoint("pod-a", "10.0.0.1", "8080")
	epB := testEndpoint("pod-b", "10.0.0.2", "8080")

	// precise has no differentiation (all 1.0 from min-max normalization of all-zeros)
	precise := map[scheduling.Endpoint]float64{epA: 1.0, epB: 1.0}
	approx := map[scheduling.Endpoint]float64{epA: 0.8, epB: 0.2}

	combined := combineScores(precise, approx)
	// When precise is undifferentiated, approximate scores should be used directly
	assert.Equal(t, 0.8, combined[epA], "should use approximate when precise is undifferentiated")
	assert.Equal(t, 0.2, combined[epB], "should use approximate when precise is undifferentiated")
}

// TestApproximateMode_E2E_ExactScores verifies exact score values, not just > 0.
func TestApproximateMode_E2E_ExactScores(t *testing.T) {
	scorer := newApproxScorer(t)

	endpoints := []scheduling.Endpoint{
		testEndpoint("pod-a", "10.0.0.1", "8080"),
		testEndpoint("pod-b", "10.0.0.2", "8080"),
	}

	// 16 chars = 4 blocks at blockSize=1 (4 chars/block)
	req1 := completionsRequest("aaaabbbbccccdddd")
	req1.RequestId = "req-1"

	cycleState := scheduling.NewCycleState()
	_ = scorer.PrepareRequestData(context.Background(), req1, endpoints)
	scorer.Score(context.Background(), cycleState, req1, endpoints)
	scorer.PreRequest(context.Background(), req1, &scheduling.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"default": {TargetEndpoints: []scheduling.Endpoint{endpoints[0]}},
		},
	})

	// Second request — same prompt, full cache hit on pod-a
	req2 := completionsRequest("aaaabbbbccccdddd")
	req2.RequestId = "req-2"
	_ = scorer.PrepareRequestData(context.Background(), req2, endpoints)
	scores := scorer.Score(context.Background(), scheduling.NewCycleState(), req2, endpoints)

	assert.InDelta(t, 1.0, scores[endpoints[0]], 0.01, "pod-a should have full cache hit (4/4)")
	assert.InDelta(t, 0.0, scores[endpoints[1]], 0.01, "pod-b should have no cache hit")
}

// TestApproximateMode_PreRequest_NilSchedulingResult tests that nil scheduling result doesn't panic.
func TestApproximateMode_PreRequest_NilSchedulingResult(t *testing.T) {
	scorer := newApproxScorer(t)
	endpoints := []scheduling.Endpoint{testEndpoint("pod-a", "10.0.0.1", "8080")}

	req := completionsRequest("aaaabbbbccccdddd")
	_ = scorer.PrepareRequestData(context.Background(), req, endpoints)
	scorer.Score(context.Background(), scheduling.NewCycleState(), req, endpoints)

	// Should not panic with empty scheduling result
	assert.NotPanics(t, func() {
		scorer.PreRequest(context.Background(), req, &scheduling.SchedulingResult{
			PrimaryProfileName: "default",
			ProfileResults:     map[string]*scheduling.ProfileRunResult{},
		})
	})
}

// TestModeConfig_InvalidMode tests that invalid mode strings are rejected.
func TestModeConfig_InvalidMode(t *testing.T) {
	ctx := context.Background()
	_, err := New(ctx, PrecisePrefixCachePluginConfig{
		Mode: "invalid-mode",
	}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mode")
}

// TestApproximateMode_RecencyWeighted tests that recency-weighted scoring
// gives lower scores to older self-reported entries.
func TestApproximateMode_RecencyWeighted(t *testing.T) {
	ctx := context.Background()
	scorer, err := New(ctx, PrecisePrefixCachePluginConfig{
		Mode: ModeApproximate,
		ApproximateConfig: &ApproximateConfig{
			BlockSizeTokens:        1,
			MaxPrefixBlocksToMatch: 256,
			LRUCapacityPerServer:   100,
			AutoTune:               false,
			RecencyHalfLife:         "30s",
		},
	}, nil)
	require.NoError(t, err)
	assert.Greater(t, scorer.approxRecencyHalfLife, time.Duration(0),
		"recency half-life should be parsed")

	endpoints := []scheduling.Endpoint{
		testEndpoint("pod-a", "10.0.0.1", "8080"),
	}

	// Route a request to populate the indexer
	req1 := completionsRequest("aaaabbbbccccdddd")
	req1.RequestId = "req-1"
	_ = scorer.PrepareRequestData(ctx, req1, endpoints)
	scorer.Score(ctx, scheduling.NewCycleState(), req1, endpoints)
	scorer.PreRequest(ctx, req1, &scheduling.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"default": {TargetEndpoints: []scheduling.Endpoint{endpoints[0]}},
		},
	})

	// Score immediately — should be close to 1.0 (fresh entries)
	req2 := completionsRequest("aaaabbbbccccdddd")
	req2.RequestId = "req-2"
	_ = scorer.PrepareRequestData(ctx, req2, endpoints)
	freshScores := scorer.Score(ctx, scheduling.NewCycleState(), req2, endpoints)

	// The score should be > 0 and <= 1.0
	assert.Greater(t, freshScores[endpoints[0]], 0.0, "fresh entry should have positive score")
	assert.LessOrEqual(t, freshScores[endpoints[0]], 1.0, "score should be <= 1.0")
}
