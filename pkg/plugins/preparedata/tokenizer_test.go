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

package preparedata

import (
	"encoding/json"
	"errors"
	"testing"

	tokenizerTypes "github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

type mockTokenizer struct {
	renderFunc     func(prompt string) ([]uint32, []tokenizerTypes.Offset, error)
	renderChatFunc func(req *tokenizerTypes.RenderChatRequest) ([]uint32, []tokenizerTypes.Offset, error)
}

func (m *mockTokenizer) Render(prompt string) ([]uint32, []tokenizerTypes.Offset, error) {
	return m.renderFunc(prompt)
}

func (m *mockTokenizer) RenderChat(req *tokenizerTypes.RenderChatRequest) ([]uint32, []tokenizerTypes.Offset, error) {
	return m.renderChatFunc(req)
}

func newTestPlugin(tok tokenizer) *TokenizerPlugin {
	return &TokenizerPlugin{
		typedName: plugin.TypedName{Type: TokenizerPluginType, Name: "test"},
		tokenizer: tok,
	}
}

func TestTokenizerPluginFactory_Validation(t *testing.T) {
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

			p, err := TokenizerPluginFactory("test-tokenizer", rawParams, handle)
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

func TestTokenizerPlugin_ProducesAndConsumes(t *testing.T) {
	p := newTestPlugin(nil)

	produces := p.Produces()
	require.NotNil(t, produces)
	assert.Contains(t, produces, TokenizedPromptKey)
	assert.IsType(t, scheduling.TokenizedPrompt{}, produces[TokenizedPromptKey])

	assert.Nil(t, p.Consumes())
}

func TestTokenizerPlugin_PrepareRequestData(t *testing.T) {
	fakeTokenIDs := []uint32{10, 20, 30, 40}

	tok := &mockTokenizer{
		renderFunc: func(prompt string) ([]uint32, []tokenizerTypes.Offset, error) {
			return fakeTokenIDs, nil, nil
		},
		renderChatFunc: func(req *tokenizerTypes.RenderChatRequest) ([]uint32, []tokenizerTypes.Offset, error) {
			return fakeTokenIDs, nil, nil
		},
	}

	tests := []struct {
		name          string
		request       *scheduling.LLMRequest
		tokenizer     tokenizer
		wantTokenIDs  []uint32
		wantNilPrompt bool
	}{
		{
			name: "skips when already tokenized",
			request: &scheduling.LLMRequest{
				TokenizedPrompt: &scheduling.TokenizedPrompt{TokenIDs: []uint32{1, 2, 3}},
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{Prompt: "hello"},
				},
			},
			tokenizer:    nil, // would panic if called
			wantTokenIDs: []uint32{1, 2, 3},
		},
		{
			name:          "skips nil body",
			request:       &scheduling.LLMRequest{Body: nil},
			tokenizer:     nil,
			wantNilPrompt: true,
		},
		{
			name: "skips unsupported request type",
			request: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{},
			},
			tokenizer:     nil,
			wantNilPrompt: true,
		},
		{
			name: "tokenizes completions request",
			request: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{
						Prompt: "The quick brown fox",
					},
				},
			},
			tokenizer:    tok,
			wantTokenIDs: fakeTokenIDs,
		},
		{
			name: "tokenizes chat completions request",
			request: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{
					ChatCompletions: &scheduling.ChatCompletionsRequest{
						Messages: []scheduling.Message{
							{Role: "user", Content: scheduling.Content{Raw: "Hello"}},
						},
					},
				},
			},
			tokenizer:    tok,
			wantTokenIDs: fakeTokenIDs,
		},
		{
			name: "fail-open on tokenization error",
			request: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{Prompt: "fail"},
				},
			},
			tokenizer: &mockTokenizer{
				renderFunc: func(string) ([]uint32, []tokenizerTypes.Offset, error) {
					return nil, nil, errors.New("tokenizer exploded")
				},
			},
			wantNilPrompt: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := utils.NewTestContext(t)
			p := newTestPlugin(tt.tokenizer)

			err := p.PrepareRequestData(ctx, tt.request, nil)
			require.NoError(t, err)

			if tt.wantNilPrompt {
				assert.Nil(t, tt.request.TokenizedPrompt)
			} else {
				require.NotNil(t, tt.request.TokenizedPrompt)
				assert.Equal(t, tt.wantTokenIDs, tt.request.TokenizedPrompt.TokenIDs)
			}
		})
	}
}

func TestChatCompletionsToRenderChatRequest(t *testing.T) {
	chat := &scheduling.ChatCompletionsRequest{
		Messages: []scheduling.Message{
			{Role: "system", Content: scheduling.Content{Raw: "You are a helpful assistant."}},
			{Role: "user", Content: scheduling.Content{Raw: "Hello!"}},
		},
		ChatTemplate:              "template",
		AddGenerationPrompt:       true,
		ContinueFinalMessage:      false,
		ReturnAssistantTokensMask: true,
	}

	result := chatCompletionsToRenderChatRequest(chat)

	require.Len(t, result.Conversation, 2)
	assert.Equal(t, "system", result.Conversation[0].Role)
	assert.Equal(t, "You are a helpful assistant.", result.Conversation[0].Content)
	assert.Equal(t, "user", result.Conversation[1].Role)
	assert.Equal(t, "Hello!", result.Conversation[1].Content)
	assert.Equal(t, "template", result.ChatTemplate)
	assert.True(t, result.AddGenerationPrompt)
	assert.False(t, result.ContinueFinalMessage)
	assert.True(t, result.ReturnAssistantTokensMask)
}
