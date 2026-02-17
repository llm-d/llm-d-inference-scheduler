package scorer

import (
	"context"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

const udsSocketPath = "/tmp/tokenizer/tokenizer-uds.socket"

// skipIfNoUDSTokenizer skips the test if UDS tokenizer socket is not available.
func skipIfNoUDSTokenizer(t *testing.T) {
	if _, err := os.Stat(udsSocketPath); os.IsNotExist(err) {
		t.Skipf("UDS tokenizer socket not available at %s, skipping test", udsSocketPath)
	}
}

// createUDSTokenizer creates a UDS tokenizer for testing.
func createUDSTokenizer(t *testing.T, model string) *tokenization.UdsTokenizer {
	udsTokenizer, err := tokenization.NewUdsTokenizer(context.Background(),
		&tokenization.UdsTokenizerConfig{SocketFile: udsSocketPath}, model)
	require.NoError(t, err)
	return udsTokenizer
}

// TestPrefixCacheTracking_Score_UDS tests the prefix cache scoring with UDS tokenizer.
// This test requires a running UDS tokenizer sidecar.
func TestPrefixCacheTracking_Score_UDS(t *testing.T) {
	skipIfNoUDSTokenizer(t)

	prompt := "One morning, when Gregor Samsa woke from troubled dreams, " +
		"he found himself transformed in his bed into a horrible vermin. " +
		"He lay on his armour-like back, and if he lifted his head a little he could see his brown belly, " +
		"slightly domed and divided by arches into stiff sections."

	testcases := []struct {
		name                string
		endpoints           []scheduling.Endpoint
		request             *scheduling.LLMRequest
		kvBlockData         func(t *testing.T, req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry
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
			kvBlockData: func(t *testing.T, req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				tokens, _, err := udsTokenizer.Render(req.Completions.Prompt)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 3, "Need at least 3 chunks for test")

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
			kvBlockData: func(t *testing.T, req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.ChatCompletions, "req expected to use ChatCompletions API")

				// convert to types format
				conversations := make([]types.Conversation, 0, len(req.ChatCompletions.Messages))
				for _, msg := range req.ChatCompletions.Messages {
					conversations = append(conversations, types.Conversation{
						Role:    msg.Role,
						Content: msg.Content.Raw,
					})
				}

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				// render the chat template using UDS tokenizer
				renderReq := &types.RenderChatRequest{
					Conversation: conversations,
					ChatTemplate: req.ChatCompletions.ChatTemplate,
				}
				tokens, _, err := udsTokenizer.RenderChat(renderReq)
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
			kvBlockData: func(t *testing.T, req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				tokens, _, err := udsTokenizer.Render(req.Completions.Prompt)
				require.NoError(t, err)

				tokenProcessor := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				chunkKeys := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model)

				require.GreaterOrEqual(t, len(chunkKeys), 3, "Need at least 3 chunks for test")

				// Test partial prefix cache scenario:
				// - chunk0: all endpoints (common prefix start)
				// - chunk1: only pod-a (creates a gap for pod-b and pod-c)
				// - chunk2: pod-a and pod-b (pod-b has this but missing chunk1)
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
			kvBlockData: func(t *testing.T, req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				tokens, _, err := udsTokenizer.Render(req.Completions.Prompt)
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
			kvBlockData: func(t *testing.T, req *scheduling.LLMRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				tokens, _, err := udsTokenizer.Render(req.Completions.Prompt)
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
			require.NoError(t, err)

			// Configure UDS tokenizer
			kvcacheConfig.TokenizersPoolConfig = &tokenization.Config{
				ModelName:    "test-model",
				WorkersCount: 1,
				UdsTokenizerConfig: &tokenization.UdsTokenizerConfig{
					SocketFile: udsSocketPath,
				},
			}

			prefixCacheScorer, err := New(ctx, PrecisePrefixCachePluginConfig{
				IndexerConfig:  kvcacheConfig,
				KVEventsConfig: kvevents.DefaultConfig(),
			})
			require.NoError(t, err)
			require.NotNil(t, prefixCacheScorer)

			// populate the kvblock.Index with test data
			if tt.kvBlockData != nil && tt.request != nil && tt.request.Body != nil {
				kvBlockIndex := prefixCacheScorer.kvCacheIndexer.KVBlockIndex()
				blockData := tt.kvBlockData(t, tt.request.Body, tt.request.TargetModel)
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
