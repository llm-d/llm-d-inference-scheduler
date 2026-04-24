package preciseprefixcache

import (
	"context"
	"fmt"
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

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/dataproducer/tokenizer"
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
		request             *scheduling.InferenceRequest
		kvBlockData         func(t *testing.T, req *fwkrh.InferenceRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry
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
			request: &scheduling.InferenceRequest{
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
			request: &scheduling.InferenceRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: prompt},
					},
				},
			},
			kvBlockData: func(t *testing.T, req *fwkrh.InferenceRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				tokens, _, err := udsTokenizer.Render(req.Completions.Prompt.Raw)
				require.NoError(t, err)

				tokenProcessor, err := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				require.NoError(t, err)
				chunkKeys, err := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model, nil)
				require.NoError(t, err)

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
			request: &scheduling.InferenceRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &fwkrh.InferenceRequestBody{
					ChatCompletions: &fwkrh.ChatCompletionsRequest{
						ChatTemplate: `{% for message in messages %}{{ message.role }}: {{ message.content }}
		{% endfor %}`,
						Messages: []fwkrh.Message{
							{
								Role:    "user",
								Content: fwkrh.Content{Raw: "Hello, how are you?"},
							},
							{
								Role:    "assistant",
								Content: fwkrh.Content{Raw: "I'm doing well, thank you for asking!"},
							},
							{
								Role:    "user",
								Content: fwkrh.Content{Raw: "Can you help me with a question about prefix caching in LLM inference?"},
							},
						},
					},
				},
			},
			kvBlockData: func(t *testing.T, req *fwkrh.InferenceRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.ChatCompletions, "req expected to use ChatCompletions API")

				// convert to types format
				conversations := make([]types.Conversation, 0, len(req.ChatCompletions.Messages))
				for _, msg := range req.ChatCompletions.Messages {
					conversations = append(conversations, types.Conversation{
						Role:    msg.Role,
						Content: types.Content{Raw: msg.Content.Raw},
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

				tokenProcessor, err := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				require.NoError(t, err)
				chunkKeys, err := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model, nil)
				require.NoError(t, err)

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
			request: &scheduling.InferenceRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: prompt},
					},
				},
			},
			kvBlockData: func(t *testing.T, req *fwkrh.InferenceRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				tokens, _, err := udsTokenizer.Render(req.Completions.Prompt.Raw)
				require.NoError(t, err)

				tokenProcessor, err := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				require.NoError(t, err)
				chunkKeys, err := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model, nil)
				require.NoError(t, err)

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
						Address:        "10.0.0.1",
						Port:           "8080",
					},
					&fwkdl.Metrics{
						WaitingQueueSize: 0,
					},
					nil,
				),
			},
			request: &scheduling.InferenceRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: prompt},
					},
				},
			},
			kvBlockData: func(t *testing.T, req *fwkrh.InferenceRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				tokens, _, err := udsTokenizer.Render(req.Completions.Prompt.Raw)
				require.NoError(t, err)

				tokenProcessor, err := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				require.NoError(t, err)
				chunkKeys, err := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model, nil)
				require.NoError(t, err)

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
			request: &scheduling.InferenceRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: "This prompt has never been cached before on any endpoint."},
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
			request: &scheduling.InferenceRequest{
				RequestId:   "test-request",
				TargetModel: "test-model",
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: prompt},
					},
				},
			},
			kvBlockData: func(t *testing.T, req *fwkrh.InferenceRequestBody, model string) map[kvblock.BlockHash][]kvblock.PodEntry {
				require.NotNil(t, req.Completions, "req expected to use Completions API")

				udsTokenizer := createUDSTokenizer(t, model)
				defer func() {
					err := udsTokenizer.Close()
					require.NoError(t, err)
				}()

				tokens, _, err := udsTokenizer.Render(req.Completions.Prompt.Raw)
				require.NoError(t, err)

				tokenProcessor, err := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
				require.NoError(t, err)
				chunkKeys, err := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, model, nil)
				require.NoError(t, err)

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

			prefixCacheScorer, err := New(ctx, PluginConfig{
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
					m := endpoint.GetMetadata()
					gotByAddress[fmt.Sprintf("%s:%s", m.Address, m.Port)] = score
				}
			}

			if diff := cmp.Diff(tt.wantScoresByAddress, gotByAddress); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}

const mmModelName = "Qwen/Qwen2-VL-2B-Instruct"

// TestRenderChat_MultimodalContent_UDS exercises the full MM pipeline through
// the real UDS tokenizer with a multimodal model:
//   - RenderChat with structured content (text + image_url)
//   - Verify MultiModalFeatures are returned (MMHashes, MMPlaceholders)
//   - Compute BlockExtraFeatures from MM features
//   - Compute block keys with extraFeatures taint
//   - Verify tainted block keys differ from text-only block keys
func TestRenderChat_MultimodalContent_UDS(t *testing.T) {
	skipIfNoUDSTokenizer(t)

	udsTokenizer := createUDSTokenizer(t, mmModelName)
	defer func() {
		err := udsTokenizer.Close()
		require.NoError(t, err)
	}()

	mmRenderReq := &types.RenderChatRequest{
		Conversation: []types.Conversation{
			{
				Role: "user",
				Content: types.Content{
					Structured: []types.ContentBlock{
						{Type: "image_url", ImageURL: types.ImageBlock{URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGP4z8AARAwQCgAf7gP9i18U1AAAAABJRU5ErkJggg=="}},
						{Type: "text", Text: "What do you see in this image? Please describe it in detail."},
					},
				},
			},
		},
		AddGenerationPrompt: true,
	}

	tokens, mmFeatures, err := udsTokenizer.RenderChat(mmRenderReq)
	require.NoError(t, err, "RenderChat with MM content should not error")
	require.NotEmpty(t, tokens, "Should produce tokens from MM content")
	require.NotNil(t, mmFeatures, "MultiModalFeatures should be non-nil for multimodal input")
	require.NotEmpty(t, mmFeatures.MMHashes, "MMHashes should contain at least one modality")
	require.NotEmpty(t, mmFeatures.MMPlaceholders, "MMPlaceholders should contain at least one modality")

	t.Logf("MM RenderChat: %d tokens, modalities=%v", len(tokens), func() []string {
		keys := make([]string, 0, len(mmFeatures.MMHashes))
		for k := range mmFeatures.MMHashes {
			keys = append(keys, k)
		}
		return keys
	}())

	// Compute BlockExtraFeatures from MM features.
	blockSize := kvblock.DefaultTokenProcessorConfig().BlockSize
	extraFeatures := kvblock.ComputeBlockExtraFeatures(
		mmFeatures.MMHashes, mmFeatures.MMPlaceholders,
		blockSize, len(tokens))
	require.NotNil(t, extraFeatures, "ComputeBlockExtraFeatures should produce non-nil result for MM input")

	// Verify at least one block has MM taint.
	hasTaint := false
	for _, ef := range extraFeatures {
		if ef != nil && len(ef.MMHashes) > 0 {
			hasTaint = true
			break
		}
	}
	require.True(t, hasTaint, "At least one block should have MM hash taint")

	// Compute block keys WITH extra features (MM-tainted).
	tokenProcessor, err := kvblock.NewChunkedTokenDatabase(kvblock.DefaultTokenProcessorConfig())
	require.NoError(t, err)
	mmBlockKeys, err := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, mmModelName, extraFeatures)
	require.NoError(t, err)
	require.NotEmpty(t, mmBlockKeys)

	// Compute block keys WITHOUT extra features (text-only view of same tokens).
	textBlockKeys, err := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, mmModelName, nil)
	require.NoError(t, err)
	require.Equal(t, len(mmBlockKeys), len(textBlockKeys), "Same token count should produce same number of blocks")

	// At least one tainted block should produce a different hash than text-only.
	differ := false
	for i := range mmBlockKeys {
		if mmBlockKeys[i] != textBlockKeys[i] {
			differ = true
			t.Logf("Block %d hashes differ: mm=%x text=%x", i, mmBlockKeys[i], textBlockKeys[i])
			break
		}
	}
	require.True(t, differ, "MM-tainted block keys must differ from text-only block keys")
}

// TestRenderChat_TextOnly_NoMMFeatures_UDS verifies that RenderChat with plain
// text content returns nil MMFeatures (no false positives).
func TestRenderChat_TextOnly_NoMMFeatures_UDS(t *testing.T) {
	skipIfNoUDSTokenizer(t)

	udsTokenizer := createUDSTokenizer(t, mmModelName)
	defer func() {
		err := udsTokenizer.Close()
		require.NoError(t, err)
	}()

	textRenderReq := &types.RenderChatRequest{
		Conversation: []types.Conversation{
			{
				Role:    "user",
				Content: types.Content{Raw: "Hello, how are you doing today?"},
			},
		},
		AddGenerationPrompt: true,
	}

	tokens, mmFeatures, err := udsTokenizer.RenderChat(textRenderReq)
	require.NoError(t, err)
	require.NotEmpty(t, tokens)
	require.Nil(t, mmFeatures, "Text-only RenderChat should return nil MultiModalFeatures")
}

// TestMMPipeline_ScoreTokensWithExtraFeatures_UDS is an end-to-end test that
// exercises the full MM-aware scoring pipeline through the real indexer:
//   - Tokenize MM content via UDS
//   - Compute block keys with MM taint
//   - Populate the index with tainted keys
//   - Score via ScoreTokens with extraFeatures
//   - Verify pods with tainted entries score higher
func TestMMPipeline_ScoreTokensWithExtraFeatures_UDS(t *testing.T) {
	skipIfNoUDSTokenizer(t)

	ctx := utils.NewTestContext(t)

	// 1. Tokenize MM content.
	udsTokenizer := createUDSTokenizer(t, mmModelName)
	defer func() {
		err := udsTokenizer.Close()
		require.NoError(t, err)
	}()

	renderReq := &types.RenderChatRequest{
		Conversation: []types.Conversation{
			{
				Role: "user",
				Content: types.Content{
					Structured: []types.ContentBlock{
						{Type: "image_url", ImageURL: types.ImageBlock{URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGP4z8AARAwQCgAf7gP9i18U1AAAAABJRU5ErkJggg=="}},
						{Type: "text", Text: "Describe the contents of this photograph."},
					},
				},
			},
		},
		AddGenerationPrompt: true,
	}

	tokens, mmFeatures, err := udsTokenizer.RenderChat(renderReq)
	require.NoError(t, err)
	require.NotEmpty(t, tokens)
	require.NotNil(t, mmFeatures)

	// 2. Compute extra features and block keys.
	tpConfig := kvblock.DefaultTokenProcessorConfig()
	extraFeatures := kvblock.ComputeBlockExtraFeatures(
		mmFeatures.MMHashes, mmFeatures.MMPlaceholders,
		tpConfig.BlockSize, len(tokens))
	require.NotNil(t, extraFeatures)

	tokenProcessor, err := kvblock.NewChunkedTokenDatabase(tpConfig)
	require.NoError(t, err)

	mmBlockKeys, err := tokenProcessor.TokensToKVBlockKeys(kvblock.EmptyBlockHash, tokens, mmModelName, extraFeatures)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(mmBlockKeys), 2, "Need at least 2 blocks for scoring test")

	// 3. Set up indexer.
	kvcacheConfig, err := kvcache.NewDefaultConfig()
	require.NoError(t, err)
	kvcacheConfig.TokenizersPoolConfig = &tokenization.Config{
		ModelName:    mmModelName,
		WorkersCount: 1,
		UdsTokenizerConfig: &tokenization.UdsTokenizerConfig{
			SocketFile: udsSocketPath,
		},
	}

	prefixCacheScorer, err := New(ctx, PluginConfig{
		IndexerConfig:  kvcacheConfig,
		KVEventsConfig: kvevents.DefaultConfig(),
	})
	require.NoError(t, err)

	// 4. Populate index with MM-tainted block keys.
	//    pod-a has all blocks, pod-b has only the first.
	kvBlockIndex := prefixCacheScorer.kvCacheIndexer.KVBlockIndex()
	for i, key := range mmBlockKeys {
		pods := []kvblock.PodEntry{{PodIdentifier: "10.0.0.1:8080"}}
		if i == 0 {
			pods = append(pods, kvblock.PodEntry{PodIdentifier: "10.0.0.2:8080"})
		}
		err := kvBlockIndex.Add(ctx, []kvblock.BlockHash{kvblock.EmptyBlockHash}, []kvblock.BlockHash{key}, pods)
		require.NoError(t, err)
	}

	// 5. Score with extraFeatures — pod-a should score higher.
	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(
			&fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
				Address:        "10.0.0.1",
				Port:           "8080",
			},
			nil, nil,
		),
		scheduling.NewEndpoint(
			&fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
				Address:        "10.0.0.2",
				Port:           "8080",
			},
			nil, nil,
		),
	}

	// Write tokenized state with MM features to CycleState (simulating tokenizer plugin).
	cycleState := scheduling.NewCycleState()
	cycleState.Write(tokenizer.TokenizedPromptStateKey, &tokenizer.TokenizedPromptState{
		TokenIDs:   tokens,
		MMFeatures: mmFeatures,
	})

	request := &scheduling.InferenceRequest{
		RequestId:   "test-mm-e2e",
		TargetModel: mmModelName,
	}

	scores := prefixCacheScorer.Score(ctx, cycleState, request, endpoints)

	gotByAddress := make(map[string]float64)
	for endpoint, score := range scores {
		if m := endpoint.GetMetadata(); m != nil {
			gotByAddress[fmt.Sprintf("%s:%s", m.Address, m.Port)] = score
		}
	}

	t.Logf("MM E2E scores: %v", gotByAddress)
	require.Contains(t, gotByAddress, "10.0.0.1:8080")
	require.Contains(t, gotByAddress, "10.0.0.2:8080")
	require.Greater(t, gotByAddress["10.0.0.1:8080"], gotByAddress["10.0.0.2:8080"],
		"pod-a (all MM-tainted blocks) should score higher than pod-b (only first block)")
}
