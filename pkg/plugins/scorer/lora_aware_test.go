package scorer_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

const (
	// testEndpointCount is the default number of endpoints used in tests
	testEndpointCount = 10
)

// makeTestEndpoints creates n test endpoints with sequential naming.
// Endpoints are named "pod-0", "pod-1", etc. in the "default" namespace.
func makeTestEndpoints(count int) []scheduling.Endpoint {
	endpoints := make([]scheduling.Endpoint, count)
	for i := 0; i < count; i++ {
		endpoints[i] = scheduling.NewEndpoint(
			&fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{
					Name:      fmt.Sprintf("pod-%d", i),
					Namespace: "default",
				},
			},
			&fwkdl.Metrics{},
			nil,
		)
	}
	return endpoints
}

// countEndpointsWithScore counts how many endpoints have the given score.
func countEndpointsWithScore(scores map[scheduling.Endpoint]float64, targetScore float64) int {
	count := 0
	for _, score := range scores {
		if score == targetScore {
			count++
		}
	}
	return count
}

func TestLoRAAware_Score(t *testing.T) {
	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
		&fwkdl.Metrics{},
		nil,
	)
	endpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b", Namespace: "default"}},
		&fwkdl.Metrics{},
		nil,
	)
	endpointC := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-c", Namespace: "default"}},
		&fwkdl.Metrics{},
		nil,
	)
	endpointD := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-d", Namespace: "default"}},
		&fwkdl.Metrics{},
		nil,
	)

	tests := []struct {
		name        string
		shardSize   int
		baseModel   string
		endpoints   []scheduling.Endpoint
		request     *scheduling.LLMRequest
		wantScores  map[scheduling.Endpoint]float64
		description string
	}{
		{
			name:      "no lora adapter specified - neutral scores",
			shardSize: 2,
			baseModel: "",
			endpoints: []scheduling.Endpoint{endpointA, endpointB, endpointC},
			request: &scheduling.LLMRequest{
				RequestId:   "test-1",
				TargetModel: "",
			},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.5,
				endpointB: 0.5,
				endpointC: 0.5,
			},
			description: "Without LoRA adapter, all endpoints get neutral score",
		},
		{
			name:      "base model specified - neutral scores",
			shardSize: 2,
			baseModel: "llama-2-7b",
			endpoints: []scheduling.Endpoint{endpointA, endpointB, endpointC},
			request: &scheduling.LLMRequest{
				RequestId:   "test-base-model",
				TargetModel: "llama-2-7b",
			},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.5,
				endpointB: 0.5,
				endpointC: 0.5,
			},
			description: "Base model should get neutral scores for all endpoints",
		},
		{
			name:      "lora adapter specified - shard assignment",
			shardSize: 2,
			baseModel: "llama-2-7b",
			endpoints: []scheduling.Endpoint{endpointA, endpointB, endpointC, endpointD},
			request: &scheduling.LLMRequest{
				RequestId:   "test-2",
				TargetModel: "adapter-1",
			},
			// The actual assignment depends on hash function, but we expect:
			// - Exactly 2 endpoints with score 1.0
			// - Exactly 2 endpoints with score 0.0
			wantScores:  nil, // Will be validated differently
			description: "With LoRA adapter, exactly shardSize endpoints get score 1.0",
		},
		{
			name:      "single endpoint - always selected",
			shardSize: 2,
			baseModel: "",
			endpoints: []scheduling.Endpoint{endpointA},
			request: &scheduling.LLMRequest{
				RequestId:   "test-3",
				TargetModel: "adapter-1",
			},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 1.0,
			},
			description: "Single endpoint always gets selected",
		},
		{
			name:      "shard size larger than endpoints",
			shardSize: 10,
			baseModel: "",
			endpoints: []scheduling.Endpoint{endpointA, endpointB},
			request: &scheduling.LLMRequest{
				RequestId:   "test-4",
				TargetModel: "adapter-1",
			},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 1.0,
				endpointB: 1.0,
			},
			description: "When shard size > endpoints, all endpoints selected",
		},
		{
			name:      "empty endpoints list",
			shardSize: 2,
			baseModel: "",
			endpoints: []scheduling.Endpoint{},
			request: &scheduling.LLMRequest{
				RequestId:   "test-5",
				TargetModel: "adapter-1",
			},
			wantScores:  map[scheduling.Endpoint]float64{},
			description: "Empty endpoints list returns empty scores",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := utils.NewTestContext(t)
			params := &scorer.LoRAAwareParameters{
				ShardSize: tt.shardSize,
				BaseModel: tt.baseModel,
			}
			loraScorer := scorer.NewLoRAAware(ctx, params)

			got := loraScorer.Score(ctx, nil, tt.request, tt.endpoints)

			if tt.wantScores != nil {
				if diff := cmp.Diff(tt.wantScores, got); diff != "" {
					t.Errorf("%s: Unexpected output (-want +got): %v", tt.description, diff)
				}
			} else {
				// For cases where we can't predict exact assignment, validate properties
				if tt.name == "lora adapter specified - shard assignment" {
					// Count endpoints with score 1.0 and 0.0
					countOnes := countEndpointsWithScore(got, 1.0)
					countZeros := countEndpointsWithScore(got, 0.0)
					assert.Equal(t, tt.shardSize, countOnes, "Expected exactly shardSize endpoints with score 1.0")
					assert.Equal(t, len(tt.endpoints)-tt.shardSize, countZeros, "Expected remaining endpoints with score 0.0")
				}
			}
		})
	}
}

func TestLoRAAware_ConsistentSharding(t *testing.T) {
	ctx := utils.NewTestContext(t)
	params := &scorer.LoRAAwareParameters{ShardSize: 2}
	loraScorer := scorer.NewLoRAAware(ctx, params)

	endpoints := makeTestEndpoints(4)

	adapter := "test-adapter"
	request := &scheduling.LLMRequest{
		RequestId:   "consistency-test",
		TargetModel: adapter,
	}

	// Score multiple times and verify consistency
	firstScores := loraScorer.Score(ctx, nil, request, endpoints)
	for i := 0; i < 10; i++ {
		scores := loraScorer.Score(ctx, nil, request, endpoints)
		if diff := cmp.Diff(firstScores, scores); diff != "" {
			t.Errorf("Inconsistent scoring on iteration %d (-first +current): %v", i, diff)
		}
	}
}

func TestLoRAAware_DifferentAdaptersDifferentShards(t *testing.T) {
	ctx := utils.NewTestContext(t)
	params := &scorer.LoRAAwareParameters{ShardSize: 2}
	loraScorer := scorer.NewLoRAAware(ctx, params)

	endpoints := makeTestEndpoints(4)

	adapter1Request := &scheduling.LLMRequest{
		RequestId:   "adapter1-test",
		TargetModel: "adapter-1",
	}

	adapter2Request := &scheduling.LLMRequest{
		RequestId:   "adapter2-test",
		TargetModel: "adapter-2",
	}

	scores1 := loraScorer.Score(ctx, nil, adapter1Request, endpoints)
	scores2 := loraScorer.Score(ctx, nil, adapter2Request, endpoints)

	// Verify both have exactly 2 endpoints with score 1.0
	count1 := countEndpointsWithScore(scores1, 1.0)
	count2 := countEndpointsWithScore(scores2, 1.0)

	assert.Equal(t, 2, count1, "Adapter 1 should have 2 endpoints")
	assert.Equal(t, 2, count2, "Adapter 2 should have 2 endpoints")

	// Verify they're not identical (different adapters should get different shards)
	// Note: There's a small chance they could be the same, but very unlikely with 4 endpoints
	identical := true
	for endpoint := range scores1 {
		if scores1[endpoint] != scores2[endpoint] {
			identical = false
			break
		}
	}
	// With 4 endpoints and shard size 2, probability of identical assignment is low
	// but not zero, so we just log if they're identical rather than failing
	if identical {
		t.Log("Warning: Different adapters got identical shards (unlikely but possible)")
	}
}
func TestLoRAAwareDynamicShardSize(t *testing.T) {
	ctx := utils.NewTestContext(t)

	tests := []struct {
		name              string
		numEndpoints      int
		configuredSize    int
		expectedShardSize int
	}{
		{
			name:              "8 endpoints, auto-calculate",
			numEndpoints:      8,
			configuredSize:    0,
			expectedShardSize: 4, // ceil(8/2) = 4
		},
		{
			name:              "12 endpoints, auto-calculate",
			numEndpoints:      12,
			configuredSize:    0,
			expectedShardSize: 6, // ceil(12/2) = 6
		},
		{
			name:              "3 endpoints, auto-calculate",
			numEndpoints:      3,
			configuredSize:    0,
			expectedShardSize: 2, // ceil(3/2) = 2 (minimum)
		},
		{
			name:              "1 endpoint, auto-calculate",
			numEndpoints:      1,
			configuredSize:    0,
			expectedShardSize: 1, // Can't be less than total
		},
		{
			name:              "8 endpoints, configured size 5",
			numEndpoints:      8,
			configuredSize:    5,
			expectedShardSize: 5, // Use configured value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create endpoints
			endpoints := make([]scheduling.Endpoint, tt.numEndpoints)
			for i := 0; i < tt.numEndpoints; i++ {
				endpoints[i] = scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{
							Name:      fmt.Sprintf("endpoint-%c", rune('a'+i)),
							Namespace: "default",
						},
					},
					&fwkdl.Metrics{},
					nil,
				)
			}

			params := &scorer.LoRAAwareParameters{ShardSize: tt.configuredSize}
			loraScorer := scorer.NewLoRAAware(ctx, params)

			request := &scheduling.LLMRequest{
				RequestId:   "test-dynamic",
				TargetModel: "test-adapter",
			}

			scores := loraScorer.Score(ctx, nil, request, endpoints)

			selectedCount := countEndpointsWithScore(scores, 1.0)

			assert.Equalf(t, tt.expectedShardSize, selectedCount,
				"Expected %d endpoints to be selected, got %d", tt.expectedShardSize, selectedCount)
		})
	}
}

func TestLoRAAwareConservativeFormula(t *testing.T) {
	ctx := utils.NewTestContext(t)

	// Test that the conservative formula ceil(N/2) is applied correctly
	testCases := []struct {
		numEndpoints      int
		expectedShardSize int
	}{
		{2, 2},   // ceil(2/2) = 1, but min is 2, so capped at total = 2
		{4, 2},   // ceil(4/2) = 2
		{8, 4},   // ceil(8/2) = 4
		{10, 5},  // ceil(10/2) = 5
		{15, 8},  // ceil(15/2) = 8 (rounds up)
		{20, 10}, // ceil(20/2) = 10
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%d_endpoints", tc.numEndpoints), func(t *testing.T) {
			endpoints := make([]scheduling.Endpoint, tc.numEndpoints)
			for i := 0; i < tc.numEndpoints; i++ {
				endpoints[i] = scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{
							Name:      fmt.Sprintf("endpoint-%d", i),
							Namespace: "default",
						},
					},
					&fwkdl.Metrics{},
					nil,
				)
			}

			// Use auto-calculate (ShardSize = 0)
			params := &scorer.LoRAAwareParameters{ShardSize: 0}
			loraScorer := scorer.NewLoRAAware(ctx, params)

			request := &scheduling.LLMRequest{
				RequestId:   "test-formula",
				TargetModel: "adapter-1",
			}

			scores := loraScorer.Score(ctx, nil, request, endpoints)

			selectedCount := countEndpointsWithScore(scores, 1.0)

			// Calculate expected using ceil(N/2) with minimum of 2
			expected := int(math.Ceil(float64(tc.numEndpoints) / 2.0))
			if expected < 2 {
				expected = 2
			}
			if expected > tc.numEndpoints {
				expected = tc.numEndpoints
			}

			assert.Equal(t, expected, selectedCount,
				"For %d endpoints, expected shard size %d, got %d", tc.numEndpoints, expected, selectedCount)
		})
	}
}

func TestLoRAAware_TypedName(t *testing.T) {
	ctx := utils.NewTestContext(t)
	loraScorer := scorer.NewLoRAAware(ctx, nil)

	assert.Equal(t, scorer.LoRAAwareType, loraScorer.TypedName().Type)
}

func TestLoRAAware_WithName(t *testing.T) {
	ctx := utils.NewTestContext(t)
	loraScorer := scorer.NewLoRAAware(ctx, nil)
	testName := "test-lora-scorer"

	loraScorer = loraScorer.WithName(testName)

	assert.Equal(t, testName, loraScorer.TypedName().Name)
}

func TestLoRAAware_Category(t *testing.T) {
	ctx := utils.NewTestContext(t)
	loraScorer := scorer.NewLoRAAware(ctx, nil)

	assert.Equal(t, scheduling.Affinity, loraScorer.Category())
}

func TestLoRAAware_InvalidShardSize(t *testing.T) {
	ctx := utils.NewTestContext(t)

	tests := []struct {
		name              string
		params            *scorer.LoRAAwareParameters
		expectedShardSize int
	}{
		{
			name:              "nil parameters - use dynamic calculation",
			params:            nil,
			expectedShardSize: 5, // ceil(10/2) = 5
		},
		{
			name:              "zero shard size - use dynamic calculation",
			params:            &scorer.LoRAAwareParameters{ShardSize: 0},
			expectedShardSize: 5, // ceil(10/2) = 5
		},
		{
			name:              "negative shard size - use dynamic calculation",
			params:            &scorer.LoRAAwareParameters{ShardSize: -5},
			expectedShardSize: 5, // ceil(10/2) = 5
		},
		{
			name:              "valid custom shard size",
			params:            &scorer.LoRAAwareParameters{ShardSize: 5},
			expectedShardSize: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loraScorer := scorer.NewLoRAAware(ctx, tt.params)

			endpoints := makeTestEndpoints(testEndpointCount)
			request := &scheduling.LLMRequest{
				RequestId:   "test",
				TargetModel: "test-adapter",
			}

			scores := loraScorer.Score(ctx, nil, request, endpoints)
			selectedCount := countEndpointsWithScore(scores, 1.0)

			assert.Equalf(t, tt.expectedShardSize, selectedCount,
				"Expected %d endpoints with score 1.0, but got %d",
				tt.expectedShardSize, selectedCount)
		})
	}
}

func TestLoRAAware_EndpointOrderIndependence(t *testing.T) {
	ctx := utils.NewTestContext(t)
	params := &scorer.LoRAAwareParameters{ShardSize: 2}
	loraScorer := scorer.NewLoRAAware(ctx, params)

	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
		&fwkdl.Metrics{},
		nil,
	)
	endpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b", Namespace: "default"}},
		&fwkdl.Metrics{},
		nil,
	)
	endpointC := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-c", Namespace: "default"}},
		&fwkdl.Metrics{},
		nil,
	)

	request := &scheduling.LLMRequest{
		RequestId:   "order-test",
		TargetModel: "test-adapter",
	}

	// Test with different orderings
	order1 := []scheduling.Endpoint{endpointA, endpointB, endpointC}
	order2 := []scheduling.Endpoint{endpointC, endpointB, endpointA}
	order3 := []scheduling.Endpoint{endpointB, endpointA, endpointC}

	scores1 := loraScorer.Score(ctx, nil, request, order1)
	scores2 := loraScorer.Score(ctx, nil, request, order2)
	scores3 := loraScorer.Score(ctx, nil, request, order3)

	// Verify all orderings produce the same scores for each endpoint
	if diff := cmp.Diff(scores1, scores2); diff != "" {
		t.Errorf("Different scores for different orderings (order1 vs order2): %v", diff)
	}
	if diff := cmp.Diff(scores1, scores3); diff != "" {
		t.Errorf("Different scores for different orderings (order1 vs order3): %v", diff)
	}
}

func TestLoRAAware_ShardCaching(t *testing.T) {
	ctx := utils.NewTestContext(t)
	params := &scorer.LoRAAwareParameters{ShardSize: 2}
	loraScorer := scorer.NewLoRAAware(ctx, params)

	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(
			&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a", Namespace: "default"}},
			&fwkdl.Metrics{},
			nil,
		),
		scheduling.NewEndpoint(
			&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b", Namespace: "default"}},
			&fwkdl.Metrics{},
			nil,
		),
		scheduling.NewEndpoint(
			&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-c", Namespace: "default"}},
			&fwkdl.Metrics{},
			nil,
		),
		scheduling.NewEndpoint(
			&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-d", Namespace: "default"}},
			&fwkdl.Metrics{},
			nil,
		),
	}

	adapter1Request := &scheduling.LLMRequest{
		RequestId:   "cache-test-1",
		TargetModel: "adapter-1",
	}

	adapter2Request := &scheduling.LLMRequest{
		RequestId:   "cache-test-2",
		TargetModel: "adapter-2",
	}

	// First call for adapter-1 (should compute and cache)
	scores1a := loraScorer.Score(ctx, nil, adapter1Request, endpoints)

	// Second call for adapter-1 (should use cache)
	scores1b := loraScorer.Score(ctx, nil, adapter1Request, endpoints)

	// Verify both calls return identical results
	if diff := cmp.Diff(scores1a, scores1b); diff != "" {
		t.Errorf("Cached scores differ from computed scores for adapter-1: %v", diff)
	}

	// Call for adapter-2 (should compute and cache separately)
	scores2 := loraScorer.Score(ctx, nil, adapter2Request, endpoints)

	// Third call for adapter-1 (should still use cache)
	scores1c := loraScorer.Score(ctx, nil, adapter1Request, endpoints)

	// Verify adapter-1 cache is still valid
	if diff := cmp.Diff(scores1a, scores1c); diff != "" {
		t.Errorf("Cached scores changed after scoring different adapter: %v", diff)
	}

	// Verify adapter-2 has different scores than adapter-1
	identical := true
	for endpoint := range scores1a {
		if scores1a[endpoint] != scores2[endpoint] {
			identical = false
			break
		}
	}
	if identical {
		t.Log("Warning: Different adapters got identical shards (unlikely but possible)")
	}
}

func TestLoRAAware_ShardSizeCaching(t *testing.T) {
	ctx := utils.NewTestContext(t)
	params := &scorer.LoRAAwareParameters{ShardSize: 0} // Auto-calculate
	loraScorer := scorer.NewLoRAAware(ctx, params)

	endpoints8 := makeTestEndpoints(8)
	endpoints10 := makeTestEndpoints(10)

	request := &scheduling.LLMRequest{
		RequestId:   "shard-size-cache-test",
		TargetModel: "test-adapter",
	}

	// Score with 8 endpoints (should calculate and cache shardSize=4)
	scores8a := loraScorer.Score(ctx, nil, request, endpoints8)
	count8a := countEndpointsWithScore(scores8a, 1.0)
	assert.Equal(t, 4, count8a, "Expected 4 endpoints selected for 8 endpoints")

	// Score again with 8 endpoints (should use cached shardSize=4)
	scores8b := loraScorer.Score(ctx, nil, request, endpoints8)
	count8b := countEndpointsWithScore(scores8b, 1.0)
	assert.Equal(t, 4, count8b, "Expected 4 endpoints selected for 8 endpoints (cached)")

	// Score with 10 endpoints
	// Note: The shard cache persists the 4 endpoint names from the 8-endpoint scoring.
	// Only those 4 endpoints that exist in both sets will be selected.
	scores10 := loraScorer.Score(ctx, nil, request, endpoints10)
	count10 := countEndpointsWithScore(scores10, 1.0)
	assert.Equal(t, 4, count10, "Expected 4 endpoints selected (cached shard from 8 endpoints)")

	// Score again with 8 endpoints (should use cached shardSize=4 again)
	scores8c := loraScorer.Score(ctx, nil, request, endpoints8)
	count8c := countEndpointsWithScore(scores8c, 1.0)
	assert.Equal(t, 4, count8c, "Expected 4 endpoints selected for 8 endpoints (re-cached)")
}
