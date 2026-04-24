//go:build !gaie_tokenized_prompt

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
	"errors"
	"testing"

	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	tokenizerTypes "github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

var testEndpoints = []scheduling.Endpoint{
	scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
			Address:        "10.0.0.1",
			Port:           "8080",
		},
		nil, nil,
	),
}

func TestTokenizerScorer_Score(t *testing.T) {
	fakeTokenIDs := []uint32{10, 20, 30, 40}

	tok := &mockTokenizer{
		renderFunc: func(prompt string) ([]uint32, []tokenizerTypes.Offset, error) {
			return fakeTokenIDs, nil, nil
		},
		renderChatFunc: func(req *tokenizerTypes.RenderChatRequest) ([]uint32, *tokenization.MultiModalFeatures, error) {
			return fakeTokenIDs, nil, nil
		},
	}

	tests := []struct {
		name         string
		request      *scheduling.InferenceRequest
		tokenizer    tokenizer
		wantTokenIDs []uint32
		wantNil      bool
	}{
		{
			name:    "skips nil body",
			request: &scheduling.InferenceRequest{RequestId: "nil-body", Body: nil},
			wantNil: true,
		},
		{
			name: "skips unsupported request type",
			request: &scheduling.InferenceRequest{
				RequestId: "unsupported",
				Body:      &fwkrh.InferenceRequestBody{},
			},
			wantNil: true,
		},
		{
			name: "tokenizes completions and writes to CycleState",
			request: &scheduling.InferenceRequest{
				RequestId: "completions",
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: "The quick brown fox"},
					},
				},
			},
			tokenizer:    tok,
			wantTokenIDs: fakeTokenIDs,
		},
		{
			name: "tokenizes chat completions and writes to CycleState",
			request: &scheduling.InferenceRequest{
				RequestId: "chat",
				Body: &fwkrh.InferenceRequestBody{
					ChatCompletions: &fwkrh.ChatCompletionsRequest{
						Messages: []fwkrh.Message{
							{Role: "user", Content: fwkrh.Content{Raw: "Hello"}},
						},
					},
				},
			},
			tokenizer:    tok,
			wantTokenIDs: fakeTokenIDs,
		},
		{
			name: "fail-open on tokenization error",
			request: &scheduling.InferenceRequest{
				RequestId: "fail-open",
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{Prompt: fwkrh.Prompt{Raw: "fail"}},
				},
			},
			tokenizer: &mockTokenizer{
				renderFunc: func(string) ([]uint32, []tokenizerTypes.Offset, error) {
					return nil, nil, errors.New("tokenizer exploded")
				},
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := utils.NewTestContext(t)
			p := newTestPlugin(tt.tokenizer)
			cycleState := scheduling.NewCycleState()

			scores := p.Score(ctx, cycleState, tt.request, testEndpoints)

			// All scores should be zero (tokenizer scorer doesn't score).
			for _, score := range scores {
				assert.Equal(t, float64(0), score)
			}

			stored, err := scheduling.ReadCycleStateKey[*TokenizedPromptState](
				cycleState, TokenizedPromptStateKey)

			if tt.wantNil {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, stored)
				assert.Equal(t, tt.wantTokenIDs, stored.TokenIDs)
			}
		})
	}
}

func TestTokenizerScorer_SkipsWhenAlreadyInCycleState(t *testing.T) {
	ctx := utils.NewTestContext(t)
	cycleState := scheduling.NewCycleState()

	// Pre-populate CycleState.
	existing := &TokenizedPromptState{TokenIDs: []uint32{1, 2, 3}}
	cycleState.Write(TokenizedPromptStateKey, existing)

	// Use a recording mock to assert tokenizer is never called.
	tokenizerCalled := false
	tok := &mockTokenizer{
		renderFunc: func(string) ([]uint32, []tokenizerTypes.Offset, error) {
			tokenizerCalled = true
			return nil, nil, nil
		},
	}
	p := newTestPlugin(tok)

	request := &scheduling.InferenceRequest{
		RequestId: "already-tokenized",
		Body: &fwkrh.InferenceRequestBody{
			Completions: &fwkrh.CompletionsRequest{Prompt: fwkrh.Prompt{Raw: "hello"}},
		},
	}

	p.Score(ctx, cycleState, request, testEndpoints)

	assert.False(t, tokenizerCalled, "tokenizer should not be called when CycleState already has data")

	// Original data should remain unchanged.
	stored, err := scheduling.ReadCycleStateKey[*TokenizedPromptState](
		cycleState, TokenizedPromptStateKey)
	require.NoError(t, err)
	assert.Equal(t, []uint32{1, 2, 3}, stored.TokenIDs)
}

func TestTokenizerScorer_RenderChat_WritesMMFeaturesToCycleState(t *testing.T) {
	ctx := utils.NewTestContext(t)
	fakeTokenIDs := []uint32{10, 20, 30, 40}
	fakeMMFeatures := &tokenization.MultiModalFeatures{
		MMHashes: map[string][]string{
			"image": {"hash1", "hash2"},
		},
	}

	tok := &mockTokenizer{
		renderChatFunc: func(req *tokenizerTypes.RenderChatRequest) ([]uint32, *tokenization.MultiModalFeatures, error) {
			return fakeTokenIDs, fakeMMFeatures, nil
		},
	}
	p := newTestPlugin(tok)
	cycleState := scheduling.NewCycleState()

	request := &scheduling.InferenceRequest{
		RequestId: "mm-chat",
		Body: &fwkrh.InferenceRequestBody{
			ChatCompletions: &fwkrh.ChatCompletionsRequest{
				Messages: []fwkrh.Message{
					{Role: "user", Content: fwkrh.Content{Raw: "Describe this image"}},
				},
			},
		},
	}

	p.Score(ctx, cycleState, request, testEndpoints)

	stored, err := scheduling.ReadCycleStateKey[*TokenizedPromptState](
		cycleState, TokenizedPromptStateKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, fakeTokenIDs, stored.TokenIDs)
	require.NotNil(t, stored.MMFeatures, "MMFeatures should be stored in CycleState")
	assert.Equal(t, fakeMMFeatures.MMHashes, stored.MMFeatures.MMHashes)
}

func TestTokenizerScorer_RenderChat_ForwardsStructuredContent(t *testing.T) {
	ctx := utils.NewTestContext(t)
	fakeTokenIDs := []uint32{10, 20, 30, 40, 50}
	fakeMMFeatures := &tokenization.MultiModalFeatures{
		MMHashes: map[string][]string{"image": {"imghash1"}},
	}

	var capturedReq *tokenizerTypes.RenderChatRequest
	tok := &mockTokenizer{
		renderChatFunc: func(req *tokenizerTypes.RenderChatRequest) ([]uint32, *tokenization.MultiModalFeatures, error) {
			capturedReq = req
			return fakeTokenIDs, fakeMMFeatures, nil
		},
	}
	p := newTestPlugin(tok)
	cycleState := scheduling.NewCycleState()

	request := &scheduling.InferenceRequest{
		RequestId: "mm-structured",
		Body: &fwkrh.InferenceRequestBody{
			ChatCompletions: &fwkrh.ChatCompletionsRequest{
				Messages: []fwkrh.Message{
					{Role: "system", Content: fwkrh.Content{Raw: "You are a visual analyst."}},
					{Role: "user", Content: fwkrh.Content{
						Structured: []fwkrh.ContentBlock{
							{Type: "text", Text: "Describe this image"},
							{Type: "image_url", ImageURL: fwkrh.ImageBlock{Url: "data:image/png;base64,abc"}},
						},
					}},
				},
			},
		},
	}

	p.Score(ctx, cycleState, request, testEndpoints)

	// Verify the RenderChat request received structured content.
	require.NotNil(t, capturedReq, "RenderChat should have been called")
	require.Len(t, capturedReq.Conversation, 2)
	assert.Equal(t, "You are a visual analyst.", capturedReq.Conversation[0].Content.Raw)
	assert.Nil(t, capturedReq.Conversation[0].Content.Structured)
	require.Len(t, capturedReq.Conversation[1].Content.Structured, 2)
	assert.Equal(t, "text", capturedReq.Conversation[1].Content.Structured[0].Type)
	assert.Equal(t, "image_url", capturedReq.Conversation[1].Content.Structured[1].Type)
	assert.Equal(t, "data:image/png;base64,abc", capturedReq.Conversation[1].Content.Structured[1].ImageURL.URL)

	// Verify MM features propagated to CycleState.
	stored, err := scheduling.ReadCycleStateKey[*TokenizedPromptState](
		cycleState, TokenizedPromptStateKey)
	require.NoError(t, err)
	require.NotNil(t, stored.MMFeatures)
	assert.Equal(t, fakeMMFeatures.MMHashes, stored.MMFeatures.MMHashes)
}

func TestTokenizerScorer_Render_NilMMFeatures(t *testing.T) {
	ctx := utils.NewTestContext(t)
	fakeTokenIDs := []uint32{10, 20, 30}

	tok := &mockTokenizer{
		renderFunc: func(prompt string) ([]uint32, []tokenizerTypes.Offset, error) {
			return fakeTokenIDs, nil, nil
		},
	}
	p := newTestPlugin(tok)
	cycleState := scheduling.NewCycleState()

	request := &scheduling.InferenceRequest{
		RequestId: "text-completions",
		Body: &fwkrh.InferenceRequestBody{
			Completions: &fwkrh.CompletionsRequest{Prompt: fwkrh.Prompt{Raw: "hello"}},
		},
	}

	p.Score(ctx, cycleState, request, testEndpoints)

	stored, err := scheduling.ReadCycleStateKey[*TokenizedPromptState](
		cycleState, TokenizedPromptStateKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, fakeTokenIDs, stored.TokenIDs)
	assert.Nil(t, stored.MMFeatures, "MMFeatures should be nil for text-only completions")
}

func TestTokenizerScorer_Category(t *testing.T) {
	p := newTestPlugin(nil)
	assert.Equal(t, scheduling.Affinity, p.Category())
}
