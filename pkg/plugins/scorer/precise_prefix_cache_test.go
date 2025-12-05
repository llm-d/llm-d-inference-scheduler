package scorer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache/kvevents"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/tokenization"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

func TestPrefixCacheTracking_Score(t *testing.T) {
	d, err := os.Getwd()
	require.NoError(t, err)
	modelDir := filepath.Join(d, "testdata")
	localTokenizerConfig := tokenization.LocalTokenizerConfig{
		ModelTokenizerMap: map[string]string{
			"test-model": filepath.Join(modelDir, "test-model/tokenizer.json"),
		},
	}

	testcases := []struct {
		name                string
		pods                []types.Pod
		request             *types.LLMRequest
		kvBlockData         func(prompt string, model string) map[kvblock.Key][]kvblock.PodEntry
		wantScoresByAddress map[string]float64
	}{
		{
			name: "nil request",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
				},
			},
			wantScoresByAddress: map[string]float64{}, // empty map
		},
		{
			name: "empty request body",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
				},
			},
			request: &types.LLMRequest{
				RequestId:   "test-request-1",
				TargetModel: "test-model",
				Body:        nil,
			},
			wantScoresByAddress: map[string]float64{}, // empty map
		},
		{
			name: "longest prefix scorer (default scorer)",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize: 0,
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize: 1,
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3:8080",
					},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize: 2,
					},
				},
			},
			request: &types.LLMRequest{
				RequestId:   "test-request-1",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: "Testing prefix cache with multiple blocks of tokens. " +
							"First block should be cached across all pods in the test. " +
							"Second block will be cached on a subset of pods only. " +
							"Third block exists only on the pod with the longest prefix match.",
					},
				},
			},
			kvBlockData: func(prompt, model string) map[kvblock.Key][]kvblock.PodEntry {
				testTokenizer, err := tokenization.NewCachedLocalTokenizer(tokenization.LocalTokenizerConfig{
					ModelTokenizerMap: map[string]string{
						"test-model": filepath.Join(modelDir, "test-model/tokenizer.json"),
					},
				})
				require.NoError(t, err)

				// use the actual tokenizer on the test prompt
				tokens, _, err := testTokenizer.Encode(prompt, model)
				require.NoError(t, err)

				// compute chunk hashes using the default block size
				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 3, "Need at least 3 chunks for test")

				// populate kvblock.Index to test longest prefix matching:
				// - chunk0 (first chunk): all pods have it (common prefix start)
				// - chunk1: pod-a and pod-b have it (pod-c drops off after chunk0)
				// - chunk2: only pod-a has it (pod-b drops off after chunk1)
				// LongestPrefixScorer uses intersection, so:
				//   pod-a: 3 chunks (0,1,2) -> score 3
				//   pod-b: 2 chunks (0,1) -> score 2
				//   pod-c: 1 chunk (0) -> score 1
				// Normalized: (3-1)/(3-1) = 1.0, (2-1)/(3-1) = 0.5, (1-1)/(3-1) = 0.0

				return map[kvblock.Key][]kvblock.PodEntry{
					{ModelName: model, ChunkHash: chunkKeys[0].ChunkHash}: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
						{PodIdentifier: "10.0.0.3:8080"},
					},
					{ModelName: model, ChunkHash: chunkKeys[1].ChunkHash}: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
					},
					{ModelName: model, ChunkHash: chunkKeys[2].ChunkHash}: {
						{PodIdentifier: "10.0.0.1:8080"},
					},
				}
			},
			wantScoresByAddress: map[string]float64{
				"10.0.0.1:8080": 1.0, // 3 chunks -> (3-1)/(3-1) = 1.0
				"10.0.0.2:8080": 0.5, // 2 chunks -> (2-1)/(3-1) = 0.5
				"10.0.0.3:8080": 0.0, // 1 chunk -> (1-1)/(3-1) = 0.0
			},
		},
		{
			name: "no cache hits (empty index)",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3:8080",
					},
				},
			},
			request: &types.LLMRequest{
				RequestId:   "test-request-3",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: "This prompt has never been cached before on any pod.",
					},
				},
			},
			kvBlockData: nil, // no cached data
			wantScoresByAddress: map[string]float64{
				// when no pods have any cache hits, all should get equal scores (0.0)
				"10.0.0.1:8080": 0.0,
				"10.0.0.2:8080": 0.0,
				"10.0.0.3:8080": 0.0,
			},
		},
		{
			name: "all pods have equal prefix length",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3:8080",
					},
				},
			},
			request: &types.LLMRequest{
				RequestId:   "test-request-4",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: "All pods have the same cached prefix for this particular prompt text. " +
							"We need to ensure this prompt is long enough to generate at least two token chunks. " +
							"This additional text should provide sufficient tokens to meet the minimum requirement.",
					},
				},
			},
			kvBlockData: func(prompt, model string) map[kvblock.Key][]kvblock.PodEntry {
				testTokenizer, err := tokenization.NewCachedLocalTokenizer(tokenization.LocalTokenizerConfig{
					ModelTokenizerMap: map[string]string{
						"test-model": filepath.Join(modelDir, "test-model/tokenizer.json"),
					},
				})
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(prompt, model)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 2, "Need at least 2 chunks for test")

				// all pods have the same 2 chunks cached
				return map[kvblock.Key][]kvblock.PodEntry{
					{ModelName: model, ChunkHash: chunkKeys[0].ChunkHash}: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
						{PodIdentifier: "10.0.0.3:8080"},
					},
					{ModelName: model, ChunkHash: chunkKeys[1].ChunkHash}: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
						{PodIdentifier: "10.0.0.3:8080"},
					},
				}
			},
			wantScoresByAddress: map[string]float64{
				// when all pods have equal cache (minScore == maxScore), the implementation
				// returns 1.0 for all pods to avoid division by zero
				"10.0.0.1:8080": 1.0,
				"10.0.0.2:8080": 1.0,
				"10.0.0.3:8080": 1.0,
			},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()

			kvcacheConfig, err := kvcache.NewDefaultConfig()
			kvcacheConfig.TokenizersPoolConfig = &tokenization.Config{
				WorkersCount:          1,
				MinPrefixOverlapRatio: 0.8,
				LocalTokenizerConfig:  &localTokenizerConfig,
			}
			require.NoError(t, err)

			prefixCacheScorer, err := scorer.New(ctx, scorer.PrecisePrefixCachePluginConfig{
				IndexerConfig:  kvcacheConfig,
				KVEventsConfig: kvevents.DefaultConfig(),
			})
			require.NoError(t, err)
			require.NotNil(t, prefixCacheScorer)

			// populate the kvblock.Index with test data
			if tt.kvBlockData != nil && tt.request != nil && tt.request.Body != nil {
				kvBlockIndex := prefixCacheScorer.KVBlockIndex()
				var prompt string
				if tt.request.Body.Completions != nil {
					prompt = tt.request.Body.Completions.Prompt
				} else if tt.request.Body.ChatCompletions != nil {
					// ChatCompletions seem to be hanging right now
					t.Fatalf("Not yet implemented")
				}
				blockData := tt.kvBlockData(prompt, tt.request.TargetModel)
				for key, entries := range blockData {
					err := kvBlockIndex.Add(ctx, []kvblock.Key{key}, entries)
					require.NoError(t, err)
				}
			}

			got := prefixCacheScorer.Score(ctx, nil, tt.request, tt.pods)

			gotByAddress := make(map[string]float64)
			for pod, score := range got {
				if podMetrics, ok := pod.(*types.PodMetrics); ok && podMetrics.GetPod() != nil {
					gotByAddress[podMetrics.GetPod().Address] = score
				}
			}

			if diff := cmp.Diff(tt.wantScoresByAddress, gotByAddress); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}
