package scorer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

func TestHashPrompt_Deterministic(t *testing.T) {
	ctx := context.Background()
	req := &scheduling.LLMRequest{
		TargetModel: "llama3-8b",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: "Hello, how are you doing today? This is a test prompt for hashing.",
			},
		},
	}

	hashes1 := hashPrompt(ctx, req, 1, 256) // blockSize=1 token = 4 chars
	hashes2 := hashPrompt(ctx, req, 1, 256)

	require.NotEmpty(t, hashes1)
	assert.Equal(t, hashes1, hashes2, "same input should produce same hashes")
}

func TestHashPrompt_ModelScoped(t *testing.T) {
	ctx := context.Background()
	prompt := "Hello, how are you doing today? This is a test prompt for hashing."

	reqA := &scheduling.LLMRequest{
		TargetModel: "model-a",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: prompt},
		},
	}
	reqB := &scheduling.LLMRequest{
		TargetModel: "model-b",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: prompt},
		},
	}

	hashesA := hashPrompt(ctx, reqA, 1, 256)
	hashesB := hashPrompt(ctx, reqB, 1, 256)

	require.NotEmpty(t, hashesA)
	require.Equal(t, len(hashesA), len(hashesB), "same prompt length should produce same number of blocks")
	assert.NotEqual(t, hashesA[0], hashesB[0], "different models must produce different hashes")
}

func TestHashPrompt_CacheSalt(t *testing.T) {
	ctx := context.Background()
	prompt := "Hello, how are you doing today? This is a test prompt for hashing."

	reqNoSalt := &scheduling.LLMRequest{
		TargetModel: "llama3",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: prompt},
		},
	}
	reqWithSalt := &scheduling.LLMRequest{
		TargetModel: "llama3",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt:    prompt,
				CacheSalt: "tenant-123",
			},
		},
	}

	hashesNoSalt := hashPrompt(ctx, reqNoSalt, 1, 256)
	hashesWithSalt := hashPrompt(ctx, reqWithSalt, 1, 256)

	require.NotEmpty(t, hashesNoSalt)
	require.NotEmpty(t, hashesWithSalt)
	assert.NotEqual(t, hashesNoSalt[0], hashesWithSalt[0], "CacheSalt must affect hashes")
}

func TestHashPrompt_TooSmall(t *testing.T) {
	ctx := context.Background()
	req := &scheduling.LLMRequest{
		TargetModel: "llama3",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: "hi", // 2 chars < 4 chars (1 token block)
			},
		},
	}

	hashes := hashPrompt(ctx, req, 1, 256)
	assert.Empty(t, hashes, "prompt shorter than block size should return nil")
}

func TestHashPrompt_MaxPrefixBlocksTruncation(t *testing.T) {
	ctx := context.Background()
	// Create a prompt that would produce many blocks
	longPrompt := ""
	for i := 0; i < 100; i++ {
		longPrompt += "abcd" // 4 chars each = 1 block per "abcd" at blockSize=1
	}

	req := &scheduling.LLMRequest{
		TargetModel: "llama3",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: longPrompt},
		},
	}

	hashesUnlimited := hashPrompt(ctx, req, 1, 100)
	hashesLimited := hashPrompt(ctx, req, 1, 10)

	assert.Equal(t, 100, len(hashesUnlimited))
	assert.Equal(t, 10, len(hashesLimited), "should truncate to maxPrefixBlocks")

	// First 10 blocks should be identical (rolling chain is prefix-stable)
	assert.Equal(t, hashesUnlimited[:10], hashesLimited,
		"truncated hashes should match prefix of full hashes")
}

func TestHashPrompt_BlockSizeTokens(t *testing.T) {
	ctx := context.Background()
	// 128 chars = 32 tokens (at 4 chars/token)
	prompt := ""
	for i := 0; i < 128; i++ {
		prompt += "a"
	}

	req := &scheduling.LLMRequest{
		TargetModel: "llama3",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: prompt},
		},
	}

	// blockSize=16 tokens = 64 chars → 128/64 = 2 blocks
	hashes16 := hashPrompt(ctx, req, 16, 256)
	assert.Equal(t, 2, len(hashes16))

	// blockSize=8 tokens = 32 chars → 128/32 = 4 blocks
	hashes8 := hashPrompt(ctx, req, 8, 256)
	assert.Equal(t, 4, len(hashes8))
}

func TestHashPrompt_ChatCompletions(t *testing.T) {
	ctx := context.Background()
	// JSON marshal of messages will be > 4 chars
	req := &scheduling.LLMRequest{
		TargetModel: "llama3",
		Body: &scheduling.LLMRequestBody{
			ChatCompletions: &scheduling.ChatCompletionsRequest{
				Messages: []scheduling.Message{
					{Role: "user", Content: scheduling.Content{Raw: "Hello, this is a fairly long message for testing block hashing behavior."}},
				},
			},
		},
	}

	hashes := hashPrompt(ctx, req, 1, 256)
	assert.NotEmpty(t, hashes, "ChatCompletions should produce hashes from JSON-marshaled messages")
}

func TestHashPrompt_NilRequest(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, hashPrompt(ctx, nil, 1, 256))
	assert.Nil(t, hashPrompt(ctx, &scheduling.LLMRequest{}, 1, 256))
	assert.Nil(t, hashPrompt(ctx, &scheduling.LLMRequest{Body: &scheduling.LLMRequestBody{}}, 1, 256))
}

func TestHashPrompt_RollingChainPrefix(t *testing.T) {
	ctx := context.Background()

	// Two prompts sharing the same prefix
	shortPrompt := "aaaabbbbccccdddd" // 16 chars = 4 blocks at blockSize=1
	longPrompt := "aaaabbbbccccddddeeeeffffgggghhhh"

	reqShort := &scheduling.LLMRequest{
		TargetModel: "llama3",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: shortPrompt},
		},
	}
	reqLong := &scheduling.LLMRequest{
		TargetModel: "llama3",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: longPrompt},
		},
	}

	hashesShort := hashPrompt(ctx, reqShort, 1, 256)
	hashesLong := hashPrompt(ctx, reqLong, 1, 256)

	require.Equal(t, 4, len(hashesShort))
	require.Equal(t, 8, len(hashesLong))

	// Shared prefix should produce identical hashes
	assert.Equal(t, hashesShort, hashesLong[:4],
		"shared prefix should produce identical hash sequence")
}

func TestGetUserInputBytes_AllAPITypes(t *testing.T) {
	tests := []struct {
		name string
		req  *scheduling.LLMRequest
	}{
		{
			name: "Completions",
			req: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{Prompt: "test prompt"},
				},
			},
		},
		{
			name: "ChatCompletions",
			req: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{
					ChatCompletions: &scheduling.ChatCompletionsRequest{
						Messages: []scheduling.Message{{Role: "user", Content: scheduling.Content{Raw: "hello"}}},
					},
				},
			},
		},
		{
			name: "Responses",
			req: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{
					Responses: &scheduling.ResponsesRequest{
						Input: "test input",
					},
				},
			},
		},
		{
			name: "Conversations",
			req: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{
					Conversations: &scheduling.ConversationsRequest{
						Items: []scheduling.ConversationItem{{Type: "message"}},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := getUserInputBytes(tt.req)
			assert.NoError(t, err)
			assert.NotEmpty(t, b)
		})
	}
}

func TestGetUserInputBytes_EmptyBody(t *testing.T) {
	_, err := getUserInputBytes(&scheduling.LLMRequest{Body: &scheduling.LLMRequestBody{}})
	assert.Error(t, err)
}
