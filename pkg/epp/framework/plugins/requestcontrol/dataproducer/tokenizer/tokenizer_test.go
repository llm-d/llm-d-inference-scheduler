/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tokenizer

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	tokenizerTypes "github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

type mockTokenizer struct {
	renderFunc     func(prompt string) ([]uint32, []tokenizerTypes.Offset, error)
	renderChatFunc func(req *tokenizerTypes.RenderChatRequest) ([]uint32, *tokenization.MultiModalFeatures, error)
}

func (m *mockTokenizer) Render(prompt string) ([]uint32, []tokenizerTypes.Offset, error) {
	return m.renderFunc(prompt)
}

func (m *mockTokenizer) RenderChat(req *tokenizerTypes.RenderChatRequest) ([]uint32, *tokenization.MultiModalFeatures, error) {
	return m.renderChatFunc(req)
}

func newTestPlugin(tok tokenizer) *Plugin {
	return &Plugin{
		typedName: plugin.TypedName{Type: PluginType, Name: "test"},
		tokenizer: tok,
	}
}

func TestPrepareRequestData(t *testing.T) {
	fakeTokenIDs := []uint32{10, 20, 30}
	fakeMMFeatures := &tokenization.MultiModalFeatures{
		MMHashes:       map[string][]string{"image": {"hash1"}},
		MMPlaceholders: map[string][]kvblock.PlaceholderRange{"image": {{Offset: 2, Length: 4}}},
	}

	tok := &mockTokenizer{
		renderFunc: func(prompt string) ([]uint32, []tokenizerTypes.Offset, error) {
			return fakeTokenIDs, nil, nil
		},
		renderChatFunc: func(req *tokenizerTypes.RenderChatRequest) ([]uint32, *tokenization.MultiModalFeatures, error) {
			return fakeTokenIDs, nil, nil
		},
	}
	tokWithMM := &mockTokenizer{
		renderChatFunc: func(req *tokenizerTypes.RenderChatRequest) ([]uint32, *tokenization.MultiModalFeatures, error) {
			return fakeTokenIDs, fakeMMFeatures, nil
		},
	}

	tests := []struct {
		name          string
		request       *scheduling.InferenceRequest
		tokenizer     tokenizer
		wantErr       string
		wantTokenized *scheduling.TokenizedPrompt
	}{
		{
			name:    "nil body returns error",
			request: &scheduling.InferenceRequest{Body: nil},
			wantErr: "request body is nil",
		},
		{
			name: "already tokenized is left unchanged",
			request: &scheduling.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					TokenizedPrompt: &scheduling.TokenizedPrompt{TokenIDs: []uint32{1, 2, 3}},
				},
			},
			tokenizer:     tok,
			wantTokenized: &scheduling.TokenizedPrompt{TokenIDs: []uint32{1, 2, 3}},
		},
		{
			name: "unsupported body type leaves TokenizedPrompt nil",
			request: &scheduling.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{},
			},
			tokenizer: tok,
		},
		{
			name: "tokenizer error is returned",
			request: &scheduling.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{Prompt: fwkrh.Prompt{Raw: "fail"}},
				},
			},
			tokenizer: &mockTokenizer{
				renderFunc: func(string) ([]uint32, []tokenizerTypes.Offset, error) {
					return nil, nil, errors.New("tokenizer exploded")
				},
			},
			wantErr: "tokenization failed",
		},
		{
			name: "completions tokenized successfully with nil MM features",
			request: &scheduling.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{Prompt: fwkrh.Prompt{Raw: "hello"}},
				},
			},
			tokenizer:     tok,
			wantTokenized: &scheduling.TokenizedPrompt{TokenIDs: fakeTokenIDs, MultiModalFeatures: nil},
		},
		{
			name: "chat completions tokenized successfully",
			request: &scheduling.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					ChatCompletions: &fwkrh.ChatCompletionsRequest{
						Messages: []fwkrh.Message{{Role: "user", Content: fwkrh.Content{Raw: "hello"}}},
					},
				},
			},
			tokenizer:     tok,
			wantTokenized: &scheduling.TokenizedPrompt{TokenIDs: fakeTokenIDs},
		},
		{
			name: "multimodal chat completions stores MM features",
			request: &scheduling.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					ChatCompletions: &fwkrh.ChatCompletionsRequest{
						Messages: []fwkrh.Message{
							{Role: "user", Content: fwkrh.Content{
								Structured: []fwkrh.ContentBlock{
									{Type: "text", Text: "describe this"},
									{Type: "image_url", ImageURL: fwkrh.ImageBlock{URL: "data:image/png;base64,abc"}},
								},
							}},
						},
					},
				},
			},
			tokenizer: tokWithMM,
			wantTokenized: &scheduling.TokenizedPrompt{
				TokenIDs: fakeTokenIDs,
				MultiModalFeatures: []scheduling.MultiModalFeature{
					{Modality: "image", Hash: "hash1", Offset: 2, Length: 4},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := utils.NewTestContext(t)
			p := newTestPlugin(tt.tokenizer)

			err := p.PrepareRequestData(ctx, tt.request, nil)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantTokenized, tt.request.Body.TokenizedPrompt)
		})
	}
}

func TestPluginFactory_Validation(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handle := plugin.NewEppHandle(ctx, nil)

	tests := []struct {
		name       string
		params     string
		expectErr  bool
		errContain string
	}{
		{
			name:       "missing modelName",
			params:     `{}`,
			expectErr:  true,
			errContain: "'modelName' must be specified",
		},
		{
			name:       "nil parameters",
			params:     "",
			expectErr:  true,
			errContain: "'modelName' must be specified",
		},
		{
			name:       "invalid JSON",
			params:     `{invalid}`,
			expectErr:  true,
			errContain: "failed to parse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rawParams json.RawMessage
			if tt.params != "" {
				rawParams = json.RawMessage(tt.params)
			}

			p, err := PluginFactory("test-tokenizer", rawParams, handle)
			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, p)
				assert.Contains(t, err.Error(), tt.errContain)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, p)
			}
		})
	}
}

func TestChatCompletionsToRenderChatRequest(t *testing.T) {
	chat := &fwkrh.ChatCompletionsRequest{
		Messages: []fwkrh.Message{
			{Role: "system", Content: fwkrh.Content{Raw: "You are a helpful assistant."}},
			{Role: "user", Content: fwkrh.Content{Raw: "Hello!"}},
		},
		ChatTemplate:              "template",
		AddGenerationPrompt:       true,
		ContinueFinalMessage:      false,
		ReturnAssistantTokensMask: true,
	}

	result := ChatCompletionsToRenderChatRequest(chat)

	require.Len(t, result.Conversation, 2)
	assert.Equal(t, "system", result.Conversation[0].Role)
	assert.Equal(t, tokenizerTypes.Content{Raw: "You are a helpful assistant."}, result.Conversation[0].Content)
	assert.Equal(t, "user", result.Conversation[1].Role)
	assert.Equal(t, tokenizerTypes.Content{Raw: "Hello!"}, result.Conversation[1].Content)
	assert.Equal(t, "template", result.ChatTemplate)
	assert.True(t, result.AddGenerationPrompt)
	assert.False(t, result.ContinueFinalMessage)
	assert.True(t, result.ReturnAssistantTokensMask)
}

func TestChatCompletionsToRenderChatRequest_MultimodalContent(t *testing.T) {
	tests := []struct {
		name     string
		messages []fwkrh.Message
		wantConv []tokenizerTypes.Conversation
	}{
		{
			name: "single image with text",
			messages: []fwkrh.Message{
				{Role: "user", Content: fwkrh.Content{
					Structured: []fwkrh.ContentBlock{
						{Type: "text", Text: "Describe this image"},
						{Type: "image_url", ImageURL: fwkrh.ImageBlock{URL: "data:image/png;base64,abc123"}},
					},
				}},
			},
			wantConv: []tokenizerTypes.Conversation{
				{Role: "user", Content: tokenizerTypes.Content{
					Structured: []tokenizerTypes.ContentBlock{
						{Type: "text", Text: "Describe this image"},
						{Type: "image_url", ImageURL: tokenizerTypes.ImageBlock{URL: "data:image/png;base64,abc123"}},
					},
				}},
			},
		},
		{
			name: "system text message plus multimodal user message",
			messages: []fwkrh.Message{
				{Role: "system", Content: fwkrh.Content{Raw: "You are a visual analyst."}},
				{Role: "user", Content: fwkrh.Content{
					Structured: []fwkrh.ContentBlock{
						{Type: "text", Text: "Compare these two images"},
						{Type: "image_url", ImageURL: fwkrh.ImageBlock{URL: "data:image/png;base64,img1"}},
						{Type: "image_url", ImageURL: fwkrh.ImageBlock{URL: "data:image/png;base64,img2"}},
					},
				}},
			},
			wantConv: []tokenizerTypes.Conversation{
				{Role: "system", Content: tokenizerTypes.Content{Raw: "You are a visual analyst."}},
				{Role: "user", Content: tokenizerTypes.Content{
					Structured: []tokenizerTypes.ContentBlock{
						{Type: "text", Text: "Compare these two images"},
						{Type: "image_url", ImageURL: tokenizerTypes.ImageBlock{URL: "data:image/png;base64,img1"}},
						{Type: "image_url", ImageURL: tokenizerTypes.ImageBlock{URL: "data:image/png;base64,img2"}},
					},
				}},
			},
		},
		{
			name: "multi-turn with image in history",
			messages: []fwkrh.Message{
				{Role: "user", Content: fwkrh.Content{
					Structured: []fwkrh.ContentBlock{
						{Type: "text", Text: "What is in this image?"},
						{Type: "image_url", ImageURL: fwkrh.ImageBlock{URL: "https://example.com/img.jpg"}},
					},
				}},
				{Role: "assistant", Content: fwkrh.Content{Raw: "I see a dog."}},
				{Role: "user", Content: fwkrh.Content{Raw: "What breed is it?"}},
			},
			wantConv: []tokenizerTypes.Conversation{
				{Role: "user", Content: tokenizerTypes.Content{
					Structured: []tokenizerTypes.ContentBlock{
						{Type: "text", Text: "What is in this image?"},
						{Type: "image_url", ImageURL: tokenizerTypes.ImageBlock{URL: "https://example.com/img.jpg"}},
					},
				}},
				{Role: "assistant", Content: tokenizerTypes.Content{Raw: "I see a dog."}},
				{Role: "user", Content: tokenizerTypes.Content{Raw: "What breed is it?"}},
			},
		},
		{
			name: "text-only messages produce no Structured field",
			messages: []fwkrh.Message{
				{Role: "user", Content: fwkrh.Content{Raw: "Hello!"}},
			},
			wantConv: []tokenizerTypes.Conversation{
				{Role: "user", Content: tokenizerTypes.Content{Raw: "Hello!"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat := &fwkrh.ChatCompletionsRequest{Messages: tt.messages}
			result := ChatCompletionsToRenderChatRequest(chat)
			require.Len(t, result.Conversation, len(tt.wantConv))
			for i, want := range tt.wantConv {
				got := result.Conversation[i]
				assert.Equal(t, want.Role, got.Role)
				assert.Equal(t, want.Content.Raw, got.Content.Raw)
				assert.Equal(t, want.Content.Structured, got.Content.Structured,
					"message %d: Structured content mismatch", i)
			}
		})
	}
}
