package scorer

import (
	"testing"

	types "github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/preparedata"
)

// TestChatCompletionsToRenderRequest_MultimodalContent verifies that the shared
// conversion used by both the tokenizer plugin and the prefix cache scorer
// correctly forwards multimodal structured content blocks.
func TestChatCompletionsToRenderRequest_MultimodalContent(t *testing.T) {
	tests := []struct {
		name     string
		chat     *scheduling.ChatCompletionsRequest
		wantConv []types.Conversation
	}{
		{
			name: "single image with text",
			chat: &scheduling.ChatCompletionsRequest{
				Messages: []scheduling.Message{
					{Role: "user", Content: scheduling.Content{
						Structured: []scheduling.ContentBlock{
							{Type: "text", Text: "Describe this image"},
							{Type: "image_url", ImageURL: scheduling.ImageBlock{Url: "data:image/png;base64,abc123"}},
						},
					}},
				},
			},
			wantConv: []types.Conversation{
				{Role: "user", Content: types.Content{
					Structured: []types.ContentBlock{
						{Type: "text", Text: "Describe this image"},
						{Type: "image_url", ImageURL: types.ImageBlock{URL: "data:image/png;base64,abc123"}},
					},
				}},
			},
		},
		{
			name: "system text plus multimodal user with multiple images",
			chat: &scheduling.ChatCompletionsRequest{
				Messages: []scheduling.Message{
					{Role: "system", Content: scheduling.Content{Raw: "You are a visual analyst."}},
					{Role: "user", Content: scheduling.Content{
						Structured: []scheduling.ContentBlock{
							{Type: "text", Text: "Compare these"},
							{Type: "image_url", ImageURL: scheduling.ImageBlock{Url: "data:image/png;base64,img1"}},
							{Type: "image_url", ImageURL: scheduling.ImageBlock{Url: "data:image/png;base64,img2"}},
						},
					}},
				},
			},
			wantConv: []types.Conversation{
				{Role: "system", Content: types.Content{Raw: "You are a visual analyst."}},
				{Role: "user", Content: types.Content{
					Structured: []types.ContentBlock{
						{Type: "text", Text: "Compare these"},
						{Type: "image_url", ImageURL: types.ImageBlock{URL: "data:image/png;base64,img1"}},
						{Type: "image_url", ImageURL: types.ImageBlock{URL: "data:image/png;base64,img2"}},
					},
				}},
			},
		},
		{
			name: "text-only messages have nil Structured",
			chat: &scheduling.ChatCompletionsRequest{
				Messages: []scheduling.Message{
					{Role: "system", Content: scheduling.Content{Raw: "system prompt"}},
					{Role: "user", Content: scheduling.Content{Raw: "hello"}},
				},
			},
			wantConv: []types.Conversation{
				{Role: "system", Content: types.Content{Raw: "system prompt"}},
				{Role: "user", Content: types.Content{Raw: "hello"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := preparedata.ChatCompletionsToRenderChatRequest(tt.chat)
			require.Len(t, result.Conversation, len(tt.wantConv))
			for i, want := range tt.wantConv {
				got := result.Conversation[i]
				assert.Equal(t, want.Role, got.Role, "message %d role", i)
				assert.Equal(t, want.Content.Raw, got.Content.Raw, "message %d Raw", i)
				assert.Equal(t, want.Content.Structured, got.Content.Structured, "message %d Structured", i)
			}
		})
	}
}
