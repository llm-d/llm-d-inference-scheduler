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

package preparedata

import (
	"errors"
	"testing"

	tokenizerTypes "github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

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
		renderChatFunc: func(req *tokenizerTypes.RenderChatRequest) ([]uint32, []tokenizerTypes.Offset, error) {
			return fakeTokenIDs, nil, nil
		},
	}

	tests := []struct {
		name         string
		request      *scheduling.LLMRequest
		tokenizer    tokenizer
		wantTokenIDs []uint32
		wantNil      bool
	}{
		{
			name:    "skips nil body",
			request: &scheduling.LLMRequest{RequestId: "nil-body", Body: nil},
			wantNil: true,
		},
		{
			name: "skips unsupported request type",
			request: &scheduling.LLMRequest{
				RequestId: "unsupported",
				Body:      &scheduling.LLMRequestBody{},
			},
			wantNil: true,
		},
		{
			name: "tokenizes completions and writes to CycleState",
			request: &scheduling.LLMRequest{
				RequestId: "completions",
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
			name: "tokenizes chat completions and writes to CycleState",
			request: &scheduling.LLMRequest{
				RequestId: "chat",
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
				RequestId: "fail-open",
				Body: &scheduling.LLMRequestBody{
					Completions: &scheduling.CompletionsRequest{Prompt: "fail"},
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

	request := &scheduling.LLMRequest{
		RequestId: "already-tokenized",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: "hello"},
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

func TestTokenizerScorer_Category(t *testing.T) {
	p := newTestPlugin(nil)
	assert.Equal(t, scheduling.Affinity, p.Category())
}
