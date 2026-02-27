//go:build embedded_tokenizers

package scorer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	preprocessing "github.com/llm-d/llm-d-kv-cache/pkg/preprocessing/chat_completions"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
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
						Address:        "10.0.0.1",
						Port:           "8080",
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
						Address:        "10.0.0.1",
						Port:           "8080",
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
						Address:        "10.0.0.1",
						Port:           "8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 0,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2",
						Port:           "8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 1,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3",
						Port:           "8080",
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
				tokens, _, err := testTokenizer.Render(prompt)
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
						Address:        "10.0.0.1",
						Port:           "8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 0,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2",
						Port:           "8080",
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

				// render the chat template and get tokens directly
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
						Address:        "10.0.0.1",
						Port:           "8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 0,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2",
						Port:           "8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 1,
					},
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3",
						Port:           "8080",
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

				tokens, _, err := testTokenizer.Render(req.Completions.Prompt)
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
						Address:        "10.0.0.1",
						Port:           "8080",
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

				tokens, _, err := testTokenizer.Render(req.Completions.Prompt)
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
						Address:        "10.0.0.1",
						Port:           "8080",
					},
					nil,
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2",
						Port:           "8080",
					},
					nil,
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3",
						Port:           "8080",
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
						Address:        "10.0.0.1",
						Port:           "8080",
					},
					nil,
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2",
						Port:           "8080",
					},
					nil,
					nil,
				),
				scheduling.NewEndpoint(
					&fwkdl.EndpointMetadata{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3",
						Port:           "8080",
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

				tokens, _, err := testTokenizer.Render(req.Completions.Prompt)
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
				ModelName:             "test-model",
				WorkersCount:          1,
	
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
					err := kvBlockIndex.Add(ctx, []kvblock.BlockHash{kvblock.EmptyBlockHash}, []kvblock.BlockHash{key}, entries)
					require.NoError(t, err)
				}
			}

			got := prefixCacheScorer.Score(ctx, scheduling.NewCycleState(), tt.request, tt.endpoints)

			gotByAddress := make(map[string]float64)
			for endpoint, score := range got {
				if m := endpoint.GetMetadata(); m != nil {
					gotByAddress[fmt.Sprintf("%s:%s", m.Address, m.Port)] = score
				}
			}

			if diff := cmp.Diff(tt.wantScoresByAddress, gotByAddress); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}

// newTestScorer creates a PrecisePrefixCacheScorer for testing.
func newTestScorer(t *testing.T) *PrecisePrefixCacheScorer {
	t.Helper()
	ctx := utils.NewTestContext(t)

	d, err := os.Getwd()
	require.NoError(t, err)
	modelDir := filepath.Join(d, "testdata")
	localTokenizerConfig := tokenization.LocalTokenizerConfig{
		ModelTokenizerMap: map[string]string{
			"test-model": filepath.Join(modelDir, "test-model/tokenizer.json"),
		},
	}

	kvcacheConfig, err := kvcache.NewDefaultConfig()
	require.NoError(t, err)
	kvcacheConfig.TokenizersPoolConfig = &tokenization.Config{
		ModelName:            "test-model",
		WorkersCount:         1,
		LocalTokenizerConfig: &localTokenizerConfig,
	}

	scorer, err := New(ctx, PrecisePrefixCachePluginConfig{
		IndexerConfig:  kvcacheConfig,
		KVEventsConfig: kvevents.DefaultConfig(),
		SpeculativeTTL: 5 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, scorer)
	return scorer
}

// TestPrepareRequestData_PopulatesPluginState verifies that PrepareRequestData
// stores block keys and scores in PluginState for later reuse by Score().
func TestPrepareRequestData_PopulatesPluginState(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := newTestScorer(t)

	prompt := "One morning, when Gregor Samsa woke from troubled dreams, " +
		"he found himself transformed in his bed into a horrible vermin. " +
		"He lay on his armour-like back, and if he lifted his head a little he could see his brown belly, " +
		"slightly domed and divided by arches into stiff sections."

	request := &scheduling.LLMRequest{
		RequestId:   "test-prepare-request",
		TargetModel: "test-model",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: prompt,
			},
		},
	}

	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(
			&fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
				Address:        "10.0.0.1",
				Port:           "8080",
			}, nil, nil),
	}

	err := scorer.PrepareRequestData(ctx, request, endpoints)
	require.NoError(t, err)

	// Verify that PluginState was populated
	state, err := readPrecisePluginState(scorer.pluginState, request.RequestId)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.NotEmpty(t, state.blockKeys, "block keys should be populated after PrepareRequestData")
}

// TestScoreReusesPluginState verifies that Score() picks up the pre-computed
// scores from PrepareRequestData via PluginState.
func TestScoreReusesPluginState(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := newTestScorer(t)

	prompt := "One morning, when Gregor Samsa woke from troubled dreams, " +
		"he found himself transformed in his bed into a horrible vermin. " +
		"He lay on his armour-like back, and if he lifted his head a little he could see his brown belly, " +
		"slightly domed and divided by arches into stiff sections."

	d, err := os.Getwd()
	require.NoError(t, err)
	modelDir := filepath.Join(d, "testdata")
	localTokenizerConfig := tokenization.LocalTokenizerConfig{
		ModelTokenizerMap: map[string]string{
			"test-model": filepath.Join(modelDir, "test-model/tokenizer.json"),
		},
	}
	testTokenizer, err := tokenization.NewCachedLocalTokenizer(ctx, "test-model", localTokenizerConfig)
	require.NoError(t, err)

	tokens, _, err := testTokenizer.Render(prompt)
	require.NoError(t, err)
	tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
	chunkKeys := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, "test-model")
	require.GreaterOrEqual(t, len(chunkKeys), 2, "Need at least 2 chunks")

	// Populate index: pod-a has 2 chunks, pod-b has 1
	kvBlockIndex := scorer.kvCacheIndexer.KVBlockIndex()
	err = kvBlockIndex.Add(ctx, []kvblock.BlockHash{kvblock.EmptyBlockHash}, []kvblock.BlockHash{chunkKeys[0]}, []kvblock.PodEntry{
		{PodIdentifier: "10.0.0.1:8080"},
		{PodIdentifier: "10.0.0.2:8080"},
	})
	require.NoError(t, err)
	err = kvBlockIndex.Add(ctx, []kvblock.BlockHash{kvblock.EmptyBlockHash}, []kvblock.BlockHash{chunkKeys[1]}, []kvblock.PodEntry{
		{PodIdentifier: "10.0.0.1:8080"},
	})
	require.NoError(t, err)

	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
			Address:        "10.0.0.1",
			Port:           "8080",
		}, nil, nil),
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
			Address:        "10.0.0.2",
			Port:           "8080",
		}, nil, nil),
	}

	request := &scheduling.LLMRequest{
		RequestId:   "test-reuse",
		TargetModel: "test-model",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: prompt,
			},
		},
	}

	// Call PrepareRequestData first (as would happen in a real flow)
	err = scorer.PrepareRequestData(ctx, request, endpoints)
	require.NoError(t, err)

	// Now call Score - it should reuse the state
	scores := scorer.Score(ctx, scheduling.NewCycleState(), request, endpoints)
	require.NotEmpty(t, scores)

	gotByAddress := make(map[string]float64)
	for ep, score := range scores {
		m := ep.GetMetadata()
		gotByAddress[fmt.Sprintf("%s:%s", m.Address, m.Port)] = score
	}

	// pod-a has 2 chunks, pod-b has 1
	assert.Equal(t, 1.0, gotByAddress["10.0.0.1:8080"])
	assert.Equal(t, 0.0, gotByAddress["10.0.0.2:8080"])
}

// TestPreRequest_AddsSpeculativeEntries verifies that PreRequest adds speculative
// entries to the index and that they are visible to subsequent lookups.
func TestPreRequest_AddsSpeculativeEntries(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := newTestScorer(t)

	prompt := "One morning, when Gregor Samsa woke from troubled dreams, " +
		"he found himself transformed in his bed into a horrible vermin. " +
		"He lay on his armour-like back, and if he lifted his head a little he could see his brown belly, " +
		"slightly domed and divided by arches into stiff sections."

	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
			Address:        "10.0.0.1",
			Port:           "8080",
		}, nil, nil),
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
			Address:        "10.0.0.2",
			Port:           "8080",
		}, nil, nil),
	}

	request := &scheduling.LLMRequest{
		RequestId:   "test-speculative",
		TargetModel: "test-model",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: prompt,
			},
		},
	}

	// 1. Call PrepareRequestData to populate PluginState
	err := scorer.PrepareRequestData(ctx, request, endpoints)
	require.NoError(t, err)

	// 2. Simulate scheduling result: selected pod-a as primary target
	schedulingResult := &scheduling.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"default": {
				TargetEndpoints: []scheduling.Endpoint{endpoints[0]},
			},
		},
	}

	// 3. Call PreRequest - should add speculative entries for pod-a
	scorer.PreRequest(ctx, request, schedulingResult)

	// 4. Verify speculative entries are in the index
	// Compute block keys to look them up
	blockKeys, err := scorer.computeBlockKeys(ctx, request)
	require.NoError(t, err)
	require.NotEmpty(t, blockKeys)

	// Look up speculative entries
	index := scorer.kvCacheIndexer.KVBlockIndex()
	keyToPods, err := index.Lookup(ctx, blockKeys, sets.New[string]())
	require.NoError(t, err)

	// The first block key should have a speculative entry for pod-a
	firstKeyPods, exists := keyToPods[blockKeys[0]]
	require.True(t, exists, "First block key should have entries")
	found := false
	for _, pod := range firstKeyPods {
		if pod.PodIdentifier == "10.0.0.1:8080" && pod.Annotation == "speculative" {
			found = true
			break
		}
	}
	assert.True(t, found, "Expected to find speculative entry for 10.0.0.1:8080")

	// 5. Verify speculative entries appear in the TTL cache
	item := scorer.speculativeCache.Get(request.RequestId)
	require.NotNil(t, item, "Speculative entry should be in TTL cache")
	assert.Equal(t, len(blockKeys), len(item.Value().blockKeys))
}

// TestSpeculativeEntriesEvictOnTTL verifies that speculative entries are
// evicted from the index when their TTL expires.
func TestSpeculativeEntriesEvictOnTTL(t *testing.T) {
	ctx := utils.NewTestContext(t)

	d, err := os.Getwd()
	require.NoError(t, err)
	modelDir := filepath.Join(d, "testdata")
	localTokenizerConfig := tokenization.LocalTokenizerConfig{
		ModelTokenizerMap: map[string]string{
			"test-model": filepath.Join(modelDir, "test-model/tokenizer.json"),
		},
	}

	kvcacheConfig, err := kvcache.NewDefaultConfig()
	require.NoError(t, err)
	kvcacheConfig.TokenizersPoolConfig = &tokenization.Config{
		ModelName:            "test-model",
		WorkersCount:         1,
		LocalTokenizerConfig: &localTokenizerConfig,
	}

	// Create scorer with very short TTL for testing eviction
	scorer, err := New(ctx, PrecisePrefixCachePluginConfig{
		IndexerConfig:  kvcacheConfig,
		KVEventsConfig: kvevents.DefaultConfig(),
		SpeculativeTTL: 200 * time.Millisecond,
	})
	require.NoError(t, err)

	prompt := "One morning, when Gregor Samsa woke from troubled dreams, " +
		"he found himself transformed in his bed into a horrible vermin. " +
		"He lay on his armour-like back, and if he lifted his head a little he could see his brown belly, " +
		"slightly domed and divided by arches into stiff sections."

	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
			Address:        "10.0.0.1",
			Port:           "8080",
		}, nil, nil),
	}

	request := &scheduling.LLMRequest{
		RequestId:   "test-ttl-evict",
		TargetModel: "test-model",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: prompt,
			},
		},
	}

	// 1. PrepareRequestData + PreRequest
	err = scorer.PrepareRequestData(ctx, request, endpoints)
	require.NoError(t, err)

	schedulingResult := &scheduling.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"default": {
				TargetEndpoints: endpoints,
			},
		},
	}
	scorer.PreRequest(ctx, request, schedulingResult)

	// 2. Verify speculative entries exist initially
	blockKeys, err := scorer.computeBlockKeys(ctx, request)
	require.NoError(t, err)
	require.NotEmpty(t, blockKeys)

	index := scorer.kvCacheIndexer.KVBlockIndex()
	keyToPods, err := index.Lookup(ctx, blockKeys[:1], sets.New[string]())
	require.NoError(t, err)
	initialCount := len(keyToPods[blockKeys[0]])
	assert.Greater(t, initialCount, 0, "Should have speculative entries initially")

	// 3. Wait for TTL to expire and trigger eviction
	time.Sleep(500 * time.Millisecond)
	scorer.speculativeCache.DeleteExpired()

	// 4. Verify speculative entries were evicted
	keyToPods, err = index.Lookup(ctx, blockKeys[:1], sets.New[string]())
	require.NoError(t, err)

	// After eviction, no speculative entries should remain for this pod
	for _, pod := range keyToPods[blockKeys[0]] {
		assert.NotEqual(t, "speculative", pod.Annotation,
			"Speculative entries should have been evicted after TTL")
	}
}

// TestComputeScoresFromKeyToPods verifies the helper function that computes
// longest prefix match scores from index lookup results.
func TestComputeScoresFromKeyToPods(t *testing.T) {
	blockKeys := []kvblock.BlockHash{
		kvblock.BlockHash(111),
		kvblock.BlockHash(222),
		kvblock.BlockHash(333),
	}

	t.Run("contiguous prefix", func(t *testing.T) {
		keyToPods := map[kvblock.BlockHash][]kvblock.PodEntry{
			blockKeys[0]: {{PodIdentifier: "pod-a"}, {PodIdentifier: "pod-b"}},
			blockKeys[1]: {{PodIdentifier: "pod-a"}},
			blockKeys[2]: {{PodIdentifier: "pod-a"}},
		}

		scores := computeScoresFromKeyToPods(blockKeys, keyToPods)
		assert.Equal(t, float64(3), scores["pod-a"])
		assert.Equal(t, float64(1), scores["pod-b"]) // only first block
	})

	t.Run("prefix breaks at gap", func(t *testing.T) {
		keyToPods := map[kvblock.BlockHash][]kvblock.PodEntry{
			blockKeys[0]: {{PodIdentifier: "pod-a"}},
			// blockKeys[1] missing -> prefix chain breaks
			blockKeys[2]: {{PodIdentifier: "pod-a"}},
		}

		scores := computeScoresFromKeyToPods(blockKeys, keyToPods)
		assert.Equal(t, float64(1), scores["pod-a"]) // only first block counted
	})

	t.Run("empty index", func(t *testing.T) {
		keyToPods := map[kvblock.BlockHash][]kvblock.PodEntry{}
		scores := computeScoresFromKeyToPods(blockKeys, keyToPods)
		assert.Empty(t, scores)
	})
}

// readPrecisePluginState is a test helper to read precisePluginState from PluginState.
func readPrecisePluginState(ps *plugin.PluginState, requestID string) (*precisePluginState, error) {
	raw, err := ps.Read(requestID, stateKey)
	if err != nil {
		return nil, err
	}
	state, ok := raw.(*precisePluginState)
	if !ok {
		return nil, fmt.Errorf("unexpected type: %T", raw)
	}
	return state, nil
}
