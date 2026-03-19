//go:build embedded_tokenizers

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
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

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
		endpoints           []scheduling.Endpoint
		request             *scheduling.LLMRequest
		kvBlockData         func(req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry
		wantScoresByAddress map[string]float64
	}{
		{
			name: "nil request",
			endpoints: []scheduling.Endpoint{
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					nil,
					nil,
				),
			},
			wantScoresByAddress: map[string]float64{}, // empty map
		},
		{
			name: "empty request body",
			endpoints: []scheduling.Endpoint{
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					nil,
					nil,
				),
			},
			request: &scheduling.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body:        nil,
			},
			wantScoresByAddress: map[string]float64{}, // empty map
		},
		{
			name: "longest prefix scorer (default scorer)",
			endpoints: []scheduling.Endpoint{
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 0,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 1,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 2,
					},
					nil,
				),
			},
			request: &scheduling.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")
				prompt := req.Completions.Prompt

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				// use the actual tokenizer on the test prompt
				tokens, _, err := testTokenizer.Encode(prompt, model, true)
				require.NoError(t, err)

				// compute chunk hashes using the default block size
				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 3, "Need at least 3 chunks for test")

				// populate kvblock.Index to test longest prefix matching:
				// - chunk0 (first chunk): all endpoints have it (common prefix start)
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
			endpoints: []scheduling.Endpoint{
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 0,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 1,
					},
					nil,
				),
			},
			request: &scheduling.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &scheduling.LLMRequestBody{
					ChatCompletions: &scheduling.ChatCompletionsRequest{
						ChatTemplate: `{% for message in messages %}{{ message.role }}: {{ message.content }}
		{% endfor %}`,
						Messages: []scheduling.Message{
							{
								Role:    "user",
								Content: scheduling.Content{Raw: "Hello, how are you?"},
							},
							{
								Role:    "assistant",
								Content: scheduling.Content{Raw: "I'm doing well, thank you for asking!"},
							},
							{
								Role:    "user",
								Content: scheduling.Content{Raw: "Can you help me with a question about prefix caching in LLM inference?"},
							},
						},
					},
				},
			},
			kvBlockData: func(req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.ChatCompletions, "req expected to use ChatCompletions API")

				// convert to preprocessing format
				conversations := make([]preprocessing.Conversation, len(req.ChatCompletions.Messages))
				for idx, msg := range req.ChatCompletions.Messages {
					conversations[idx] = preprocessing.Conversation{
						Role:    msg.Role,
						Content: msg.Content.Raw,
					}
				}

				processor := preprocessing.NewChatTemplatingProcessor()
				tokenizerCacheKey, err := processor.GetOrCreateTokenizerKey(t.Context(), &preprocessing.GetOrCreateTokenizerKeyRequest{
					IsLocal: true,
					Model:   "testdata/" + model,
				})
				require.NoError(t, err)

				// render the chat template and tokenize
				renderReq := &preprocessing.RenderChatRequest{
					Key:          tokenizerCacheKey,
					Conversation: conversations,
					ChatTemplate: req.ChatCompletions.ChatTemplate,
				}
				tokens, _, err := processor.RenderChat(t.Context(), renderReq)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model)

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
			endpoints: []scheduling.Endpoint{
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 0,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 1,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 2,
					},
					nil,
				),
			},
			request: &scheduling.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(req.Completions.Prompt, model, true)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 3, "Need at least 3 chunks for test")

				// Test partial prefix cache scenario:
				// - chunk0: all endpoints (common prefix start)
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
			name: "single endpoint",
			endpoints: []scheduling.Endpoint{
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 0,
					},
					nil,
				),
			},
			request: &scheduling.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(req.Completions.Prompt, model, true)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 2, "Need at least 2 chunks for test")

				// Single endpoint has 2 chunks cached
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
				// with only one endpoint, minScore == maxScore, so normalization returns 1.0
				"10.0.0.1:8080": 1.0,
			},
		},
		{
			name: "no cache hits (empty index)",
			endpoints: []scheduling.Endpoint{
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					nil,
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
					nil,
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3:8080",
					},
					nil,
					nil,
				),
			},
			request: &scheduling.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{
						Prompt: "This prompt has never been cached before on any endpoint.",
					},
				},
			},
			kvBlockData: nil, // no cached data
			wantScoresByAddress: map[string]float64{
				// when no endpoints have any cache hits, all should get equal scores (0.0)
				"10.0.0.1:8080": 0.0,
				"10.0.0.2:8080": 0.0,
				"10.0.0.3:8080": 0.0,
			},
		},
		{
			name: "all endpoints have equal prefix length",
			endpoints: []scheduling.Endpoint{
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					nil,
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
					nil,
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3:8080",
					},
					nil,
					nil,
				),
			},
			request: &scheduling.LLMRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{
						Prompt: prompt,
					},
				},
			},
			kvBlockData: func(req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				testTokenizer, err := tokenization.NewCachedLocalTokenizer(t.Context(), model, localTokenizerConfig)
				require.NoError(t, err)

				tokens, _, err := testTokenizer.Encode(req.Completions.Prompt, model, true)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 2, "Need at least 2 chunks for test")

				// all endpoints have the same 2 chunks cached
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
				// when all endpoints have equal cache (minScore == maxScore), the implementation
				// returns 1.0 for all endpoints to avoid division by zero
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
				ModelName:            "test-model",
				WorkersCount:         1,
				LocalTokenizerConfig: &localTokenizerConfig,
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
					err := kvBlockIndex.Add(ctx, []kvblock.BlockHash{kvblock.EmptyBlockHash}, []kvblock.BlockHash{key}, entries)
					require.NoError(t, err)
				}
			}

			got := prefixCacheScorer.Score(ctx, scheduling.NewCycleState(), tt.request, tt.endpoints)

			gotByAddress := make(map[string]float64)
			for endpoint, score := range got {
				if endpoint.GetMetadata() != nil {
					gotByAddress[endpoint.GetMetadata().Address] = score
				}
			}

			if diff := cmp.Diff(tt.wantScoresByAddress, gotByAddress); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}
