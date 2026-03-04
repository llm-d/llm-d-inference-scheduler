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

package scorer

import (
	"context"
	"testing"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

type mockKVCacheIndexer struct {
	getPodScoresFunc func(ctx context.Context, renderReq *types.RenderChatRequest, prompt, modelName string, podIdentifiers []string) (map[string]float64, error)
	scoreTokensFunc  func(ctx context.Context, tokens []uint32, modelName string, podIdentifiers []string) (map[string]float64, error)
}

func (m *mockKVCacheIndexer) GetPodScores(ctx context.Context, renderReq *types.RenderChatRequest, prompt, modelName string, podIdentifiers []string) (map[string]float64, error) {
	if m.getPodScoresFunc != nil {
		return m.getPodScoresFunc(ctx, renderReq, prompt, modelName, podIdentifiers)
	}
	return map[string]float64{}, nil
}

func (m *mockKVCacheIndexer) ScoreTokens(ctx context.Context, tokens []uint32, modelName string, podIdentifiers []string) (map[string]float64, error) {
	if m.scoreTokensFunc != nil {
		return m.scoreTokensFunc(ctx, tokens, modelName, podIdentifiers)
	}
	return map[string]float64{}, nil
}

func (m *mockKVCacheIndexer) KVBlockIndex() kvblock.Index {
	return nil
}

var testEndpoints = []scheduling.Endpoint{
	scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
			Address:        "10.0.0.1:8080",
		},
		nil, nil,
	),
	scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
			Address:        "10.0.0.2:8080",
		},
		nil, nil,
	),
}

func TestPrecisePrefixCacheScorer_UsesTokenizedPrompt(t *testing.T) {
	ctx := utils.NewTestContext(t)
	tokenIDs := []uint32{10, 20, 30, 40, 50}
	var capturedTokens []uint32
	var capturedModel string

	scorer := &PrecisePrefixCacheScorer{
		typedName:      plugin.TypedName{Type: PrecisePrefixCachePluginType, Name: "test"},
		kvEventsConfig: &kvevents.Config{},
		kvCacheIndexer: &mockKVCacheIndexer{
			scoreTokensFunc: func(_ context.Context, tokens []uint32, modelName string, _ []string) (map[string]float64, error) {
				capturedTokens = tokens
				capturedModel = modelName
				return map[string]float64{"10.0.0.1:8080": 1.0}, nil
			},
		},
	}

	request := &scheduling.LLMRequest{
		TargetModel: "test-model",
		TokenizedPrompt: &scheduling.TokenizedPrompt{
			TokenIDs: tokenIDs,
		},
	}

	scorer.Score(ctx, scheduling.NewCycleState(), request, testEndpoints)

	require.Equal(t, tokenIDs, capturedTokens)
	require.Equal(t, "test-model", capturedModel)
}

func TestPrecisePrefixCacheScorer_SkipsTokenizedPromptWhenEmpty(t *testing.T) {
	ctx := utils.NewTestContext(t)
	fromTokensCalled := false

	scorer := &PrecisePrefixCacheScorer{
		typedName:      plugin.TypedName{Type: PrecisePrefixCachePluginType, Name: "test"},
		kvEventsConfig: &kvevents.Config{},
		kvCacheIndexer: &mockKVCacheIndexer{
			scoreTokensFunc: func(_ context.Context, _ []uint32, _ string, _ []string) (map[string]float64, error) {
				fromTokensCalled = true
				return map[string]float64{}, nil
			},
			getPodScoresFunc: func(_ context.Context, _ *types.RenderChatRequest, _ string, _ string, _ []string) (map[string]float64, error) {
				return map[string]float64{}, nil
			},
		},
	}

	request := &scheduling.LLMRequest{
		TargetModel: "test-model",
		TokenizedPrompt: &scheduling.TokenizedPrompt{
			TokenIDs: []uint32{},
		},
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: "hello"},
		},
	}

	scorer.Score(ctx, scheduling.NewCycleState(), request, testEndpoints)
	assert.False(t, fromTokensCalled, "ScoreTokens should not be called with empty TokenIDs")
}
