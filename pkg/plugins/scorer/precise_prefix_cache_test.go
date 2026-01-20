package scorer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	preprocessing "github.com/llm-d/llm-d-kv-cache/pkg/preprocessing/chat_completions"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
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

	prompt := "One morning, when Gregor Samsa woke from troubled dreams, " +
		"he found himself transformed in his bed into a horrible vermin. " +
		"He lay on his armour-like back, and if he lifted his head a little he could see his brown belly, " +
		"slightly domed and divided by arches into stiff sections."

	testcases := []struct {
		name                string
		pods                []types.Pod
		request             *types.LLMRequest
		kvBlockData         func(req *types.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry
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
				RequestId:   "test-request",
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
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *types.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")
				prompt := req.Completions.Prompt

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
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

				return map[kvblock.BlockHash][]kvblock.PodEntry{
					chunkKeys[0]: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
						{PodIdentifier: "10.0.0.3:8080"},
					},
					chunkKeys[1]: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
					},
					chunkKeys[2]: {
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
			name: "chat completions request",
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
			},
			request: &types.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					ChatCompletions: &types.ChatCompletionsRequest{
						ChatTemplate: `{% for message in messages %}{{ message.role }}: {{ message.content }}
{% endfor %}`,
						Messages: []types.Message{
							{
								Role:    "user",
								Content: types.Content{Raw: "Hello, how are you?"},
							},
							{
								Role:    "assistant",
								Content: types.Content{Raw: "I'm doing well, thank you for asking!"},
							},
							{
								Role:    "user",
								Content: types.Content{Raw: "Can you help me with a question about prefix caching in LLM inference?"},
							},
						},
					},
				},
			},
			kvBlockData: func(req *types.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.ChatCompletions, "req expected to use ChatCompletions API")

				// convert to preprocessing format
				var conversations []preprocessing.Conversation
				for _, msg := range req.ChatCompletions.Messages {
					conversations = append(conversations, preprocessing.Conversation{
						Role:    msg.Role,
						Content: msg.Content.Raw,
					})
				}

				// render the chat template
				renderReq := &preprocessing.ApplyChatTemplateRequest{
					Conversation: [][]preprocessing.Conversation{conversations},
					ChatTemplate: req.ChatCompletions.ChatTemplate,
				}
				processor := preprocessing.NewChatTemplatingProcessor()
				rendered, err := processor.ApplyChatTemplate(t.Context(), renderReq)
				require.NoError(t, err)

				// tokenize rendered prompt
				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(rendered.RenderedChats[0], model)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 2, "Need at least 2 chunks for test")

				// pod-a has both chunks, pod-b has only the first
				return map[kvblock.BlockHash][]kvblock.PodEntry{
					chunkKeys[0]: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
					},
					chunkKeys[1]: {
						{PodIdentifier: "10.0.0.1:8080"},
					},
				}
			},
			wantScoresByAddress: map[string]float64{
				"10.0.0.1:8080": 1.0, // 2 chunks -> (2-1)/(2-1) = 1.0
				"10.0.0.2:8080": 0.0, // 1 chunk -> (1-1)/(2-1) = 0.0
			},
		},
		{
			name: "partial prefix",
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
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *types.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(req.Completions.Prompt, model)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 3, "Need at least 3 chunks for test")

				// Test partial prefix cache scenario:
				// - chunk0: all pods (common prefix start)
				// - chunk1: only pod-a (creates a gap for pod-b and pod-c)
				// - chunk2: pod-a and pod-b (pod-b has this but missing chunk1)
				//
				// Expected behavior (prefix matching stops at gaps):
				//   pod-a: has chunks 0,1,2 contiguously -> score 3
				//   pod-b: has chunks 0,2 (missing 1) -> prefix stops at chunk0 -> score 1
				//   pod-c: has only chunk 0 -> score 1
				return map[kvblock.BlockHash][]kvblock.PodEntry{
					chunkKeys[0]: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
						{PodIdentifier: "10.0.0.3:8080"},
					},
					chunkKeys[1]: {
						{PodIdentifier: "10.0.0.1:8080"}, // only pod-a has chunk1
					},
					chunkKeys[2]: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"}, // pod-b has chunk2 but missing chunk1
					},
				}
			},
			wantScoresByAddress: map[string]float64{
				// pod-a: 3 chunks contiguously -> (3-1)/(3-1) = 1.0
				// pod-b: prefix breaks at chunk1 (has 0,2 but not 1) -> only 1 chunk counted -> (1-1)/(3-1) = 0.0
				// pod-c: only chunk 0 -> (1-1)/(3-1) = 0.0
				"10.0.0.1:8080": 1.0,
				"10.0.0.2:8080": 0.0,
				"10.0.0.3:8080": 0.0,
			},
		},
		{
			name: "different model names",
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
			},
			request: &types.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *types.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(req.Completions.Prompt, model)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 1, "Need at least 1 chunk for test")

				// Populate the index with blocks for model `different-model`
				// The request will ask for "test-model" but the cache only has "different-model"
				// This should result in no cache hits since models don't share cache
				return map[kvblock.BlockHash][]kvblock.PodEntry{
					chunkKeys[0]: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
					},
				}
			},
			wantScoresByAddress: map[string]float64{
				// Even though both pods have the chunk cached, it's for a different model
				// so there should be no cache hits for the requested model
				"10.0.0.1:8080": 0.0,
				"10.0.0.2:8080": 0.0,
			},
		},
		{
			name: "single pod",
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
			},
			request: &types.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *types.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(req.Completions.Prompt, model)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 2, "Need at least 2 chunks for test")

				// Single pod has 2 chunks cached
				return map[kvblock.BlockHash][]kvblock.PodEntry{
					chunkKeys[0]: {
						{PodIdentifier: "10.0.0.1:8080"},
					},
					chunkKeys[1]: {
						{PodIdentifier: "10.0.0.1:8080"},
					},
				}
			},
			wantScoresByAddress: map[string]float64{
				// with only one pod, minScore == maxScore, so normalization returns 1.0
				"10.0.0.1:8080": 1.0,
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
				RequestId:   "test-request",
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
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *types.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(req.Completions.Prompt, model)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 2, "Need at least 2 chunks for test")

				// all pods have the same 2 chunks cached
				return map[kvblock.BlockHash][]kvblock.PodEntry{
					chunkKeys[0]: {
						{PodIdentifier: "10.0.0.1:8080"},
						{PodIdentifier: "10.0.0.2:8080"},
						{PodIdentifier: "10.0.0.3:8080"},
					},
					chunkKeys[1]: {
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
			ctx := utils.NewTestContext(t)

			kvcacheConfig, err := kvcache.NewDefaultConfig()
			kvcacheConfig.TokenizersPoolConfig = &tokenization.Config{
				WorkersCount:          1,
				MinPrefixOverlapRatio: 0.8,
				LocalTokenizerConfig:  &localTokenizerConfig,
			}
			require.NoError(t, err)

			prefixCacheScorer, err := New(ctx, PrecisePrefixCachePluginConfig{
				IndexerConfig:  kvcacheConfig,
				KVEventsConfig: kvevents.DefaultConfig(),
			})
			require.NoError(t, err)
			require.NotNil(t, prefixCacheScorer)

			// populate the kvblock.Index with test data
			if tt.kvBlockData != nil && tt.request != nil && tt.request.Body != nil {
				kvBlockIndex := prefixCacheScorer.kvCacheIndexer.KVBlockIndex()
				blockData := tt.kvBlockData(tt.request.Body, tt.request.TargetModel)
				for key, entries := range blockData {
					err := kvBlockIndex.Add(ctx, []kvblock.Key{key}, entries)
					require.NoError(t, err)
				}
			}

			got := prefixCacheScorer.Score(ctx, types.NewCycleState(), tt.request, tt.pods)

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
