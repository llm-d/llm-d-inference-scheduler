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

package preciseprefixcache

import (
	"context"
	"testing"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	attrprefix "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/prefix"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

type fakeKVCacheIndexer struct {
	computeFromTokens func(ctx context.Context, tokens []uint32, model string, extra []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error)
	computePrompt     func(ctx context.Context, renderReq *types.RenderChatRequest, prompt, model string) ([]kvblock.BlockHash, error)
	index             *fakeKVBlockIndex
}

func (f *fakeKVCacheIndexer) ComputeBlockKeys(ctx context.Context, renderReq *types.RenderChatRequest, prompt, model string) ([]kvblock.BlockHash, error) {
	if f.computePrompt != nil {
		return f.computePrompt(ctx, renderReq, prompt, model)
	}
	return []kvblock.BlockHash{}, nil
}

func (f *fakeKVCacheIndexer) ComputeBlockKeysFromTokens(ctx context.Context, tokens []uint32, model string, extra []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error) {
	if f.computeFromTokens != nil {
		return f.computeFromTokens(ctx, tokens, model, extra)
	}
	return []kvblock.BlockHash{}, nil
}

func (f *fakeKVCacheIndexer) KVBlockIndex() kvblock.Index { return f.index }

type fakeKVBlockIndex struct {
	lookup func(ctx context.Context, keys []kvblock.BlockHash, podSet sets.Set[string]) (map[kvblock.BlockHash][]kvblock.PodEntry, error)
	addFn  func(ctx context.Context, prevKeys, keys []kvblock.BlockHash, entries []kvblock.PodEntry) error
}

func (f *fakeKVBlockIndex) Lookup(ctx context.Context, keys []kvblock.BlockHash, podSet sets.Set[string]) (map[kvblock.BlockHash][]kvblock.PodEntry, error) {
	if f.lookup != nil {
		return f.lookup(ctx, keys, podSet)
	}
	return map[kvblock.BlockHash][]kvblock.PodEntry{}, nil
}

func (f *fakeKVBlockIndex) Add(ctx context.Context, prevKeys, keys []kvblock.BlockHash, entries []kvblock.PodEntry) error {
	if f.addFn != nil {
		return f.addFn(ctx, prevKeys, keys, entries)
	}
	return nil
}

func (f *fakeKVBlockIndex) Evict(_ context.Context, _ kvblock.BlockHash, _ kvblock.KeyType, _ []kvblock.PodEntry) error {
	return nil
}

func (f *fakeKVBlockIndex) GetRequestKey(_ context.Context, _ kvblock.BlockHash) (kvblock.BlockHash, error) {
	return kvblock.EmptyBlockHash, nil
}

type fakeKVBlockScorer struct {
	score func(ctx context.Context, keys []kvblock.BlockHash, keyToPods map[kvblock.BlockHash][]kvblock.PodEntry) (map[string]float64, error)
}

func (f *fakeKVBlockScorer) Strategy() kvcache.KVScoringStrategy {
	return kvcache.LongestPrefixMatch
}

func (f *fakeKVBlockScorer) Score(ctx context.Context, keys []kvblock.BlockHash, keyToPods map[kvblock.BlockHash][]kvblock.PodEntry) (map[string]float64, error) {
	if f.score != nil {
		return f.score(ctx, keys, keyToPods)
	}
	return map[string]float64{}, nil
}

var testEndpoints = []scheduling.Endpoint{
	scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
			Address:        "10.0.0.1",
			Port:           "8080",
		}, nil, nil),
	scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
			Address:        "10.0.0.2",
			Port:           "8080",
		}, nil, nil),
}

const testBlockSize = 16

func newProducerWithIndexer(ctx context.Context, idx kvCacheIndexer, scorer kvcache.KVBlockScorer) *Producer {
	return &Producer{
		typedName:       plugin.TypedName{Type: PluginType, Name: "test"},
		kvCacheIndexer:  idx,
		kvBlockScorer:   scorer,
		kvEventsConfig:  &kvevents.Config{},
		pluginState:     plugin.NewPluginState(ctx),
		blockSizeTokens: testBlockSize,
	}
}

// When TokenizedPrompt is populated, Produce hashes the tokens directly and
// must not invoke the prompt-based ComputeBlockKeys.
func TestProduce_UsesTokenizedPromptOverFallback(t *testing.T) {
	ctx := utils.NewTestContext(t)

	tokens := []uint32{10, 20, 30, 40, 50}
	wantKey := kvblock.BlockHash(0xCAFE)

	var capturedTokens []uint32
	promptCalled := false

	idx := &fakeKVCacheIndexer{
		computeFromTokens: func(_ context.Context, ts []uint32, _ string, _ []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error) {
			capturedTokens = ts
			return []kvblock.BlockHash{wantKey}, nil
		},
		computePrompt: func(_ context.Context, _ *types.RenderChatRequest, _, _ string) ([]kvblock.BlockHash, error) {
			promptCalled = true
			return nil, nil
		},
		index: &fakeKVBlockIndex{
			lookup: func(_ context.Context, _ []kvblock.BlockHash, _ sets.Set[string]) (map[kvblock.BlockHash][]kvblock.PodEntry, error) {
				return map[kvblock.BlockHash][]kvblock.PodEntry{
					wantKey: {{PodIdentifier: "10.0.0.1:8080"}},
				}, nil
			},
		},
	}
	scorer := &fakeKVBlockScorer{
		score: func(_ context.Context, _ []kvblock.BlockHash, _ map[kvblock.BlockHash][]kvblock.PodEntry) (map[string]float64, error) {
			return map[string]float64{"10.0.0.1:8080": 1.0}, nil
		},
	}

	p := newProducerWithIndexer(ctx, idx, scorer)

	req := &scheduling.InferenceRequest{
		RequestID:   "req-1",
		TargetModel: "test-model",
		Body: &fwkrh.InferenceRequestBody{
			TokenizedPrompt: &fwkrh.TokenizedPrompt{TokenIDs: tokens},
			Completions:     &fwkrh.CompletionsRequest{Prompt: fwkrh.Prompt{Raw: "ignored"}},
		},
	}

	require.NoError(t, p.Produce(ctx, req, testEndpoints))
	require.Equal(t, tokens, capturedTokens)
	require.False(t, promptCalled)

	raw, ok := testEndpoints[0].Get(attrprefix.PrefixCacheMatchInfoKey)
	require.True(t, ok)
	info, ok := raw.(*attrprefix.PrefixCacheMatchInfo)
	require.True(t, ok)
	assert.Equal(t, 1, info.MatchBlocks())
	assert.Equal(t, 1, info.TotalBlocks())
	assert.Equal(t, 16, info.BlockSizeTokens())

	raw2, ok := testEndpoints[1].Get(attrprefix.PrefixCacheMatchInfoKey)
	require.True(t, ok)
	info2 := raw2.(*attrprefix.PrefixCacheMatchInfo)
	assert.Equal(t, 0, info2.MatchBlocks())
	assert.Equal(t, 1, info2.TotalBlocks())
}

// No TokenizedPrompt → fall back to the indexer's prompt-based path
// (legacy all-in-one mode).
func TestProduce_NoTokens_FallsBackToPrompt(t *testing.T) {
	ctx := utils.NewTestContext(t)

	wantKey := kvblock.BlockHash(0xDEAD)
	var capturedPrompt string
	fromTokensCalled := false

	idx := &fakeKVCacheIndexer{
		computeFromTokens: func(_ context.Context, _ []uint32, _ string, _ []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error) {
			fromTokensCalled = true
			return nil, nil
		},
		computePrompt: func(_ context.Context, _ *types.RenderChatRequest, prompt, _ string) ([]kvblock.BlockHash, error) {
			capturedPrompt = prompt
			return []kvblock.BlockHash{wantKey}, nil
		},
		index: &fakeKVBlockIndex{
			lookup: func(_ context.Context, _ []kvblock.BlockHash, _ sets.Set[string]) (map[kvblock.BlockHash][]kvblock.PodEntry, error) {
				return map[kvblock.BlockHash][]kvblock.PodEntry{
					wantKey: {{PodIdentifier: "10.0.0.1:8080"}},
				}, nil
			},
		},
	}
	scorer := &fakeKVBlockScorer{
		score: func(_ context.Context, _ []kvblock.BlockHash, _ map[kvblock.BlockHash][]kvblock.PodEntry) (map[string]float64, error) {
			return map[string]float64{"10.0.0.1:8080": 1.0}, nil
		},
	}

	p := newProducerWithIndexer(ctx, idx, scorer)

	req := &scheduling.InferenceRequest{
		RequestID:   "req-2",
		TargetModel: "test-model",
		Body: &fwkrh.InferenceRequestBody{
			Completions: &fwkrh.CompletionsRequest{Prompt: fwkrh.Prompt{Raw: "fallback prompt"}},
		},
	}

	require.NoError(t, p.Produce(ctx, req, testEndpoints))
	require.False(t, fromTokensCalled)
	require.Equal(t, "fallback prompt", capturedPrompt)
}

// An empty TokenIDs slice must NOT take the tokens path — the fallback
// applies when a prompt is present.
func TestProduce_EmptyTokenizedPromptDoesNotTrigger(t *testing.T) {
	ctx := utils.NewTestContext(t)

	wantKey := kvblock.BlockHash(0xBEEF)
	fromTokensCalled := false
	promptCalled := false

	idx := &fakeKVCacheIndexer{
		computeFromTokens: func(_ context.Context, _ []uint32, _ string, _ []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error) {
			fromTokensCalled = true
			return nil, nil
		},
		computePrompt: func(_ context.Context, _ *types.RenderChatRequest, _, _ string) ([]kvblock.BlockHash, error) {
			promptCalled = true
			return []kvblock.BlockHash{wantKey}, nil
		},
		index: &fakeKVBlockIndex{
			lookup: func(_ context.Context, _ []kvblock.BlockHash, _ sets.Set[string]) (map[kvblock.BlockHash][]kvblock.PodEntry, error) {
				return map[kvblock.BlockHash][]kvblock.PodEntry{}, nil
			},
		},
	}
	scorer := &fakeKVBlockScorer{}

	p := newProducerWithIndexer(ctx, idx, scorer)

	req := &scheduling.InferenceRequest{
		RequestID:   "req-3",
		TargetModel: "test-model",
		Body: &fwkrh.InferenceRequestBody{
			Completions:     &fwkrh.CompletionsRequest{Prompt: fwkrh.Prompt{Raw: "p"}},
			TokenizedPrompt: &fwkrh.TokenizedPrompt{TokenIDs: []uint32{}},
		},
	}

	require.NoError(t, p.Produce(ctx, req, testEndpoints))
	assert.False(t, fromTokensCalled)
	assert.True(t, promptCalled)
}

// Multimodal taint flows from TokenizedPrompt into
// ComputeBlockKeysFromTokens.
func TestProduce_PassesMMExtraFeatures(t *testing.T) {
	ctx := utils.NewTestContext(t)

	tokens := make([]uint32, 16)
	for i := range tokens {
		tokens[i] = uint32(i)
	}
	var captured []*kvblock.BlockExtraFeatures

	idx := &fakeKVCacheIndexer{
		computeFromTokens: func(_ context.Context, _ []uint32, _ string, extra []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error) {
			captured = extra
			return []kvblock.BlockHash{0xAA}, nil
		},
		index: &fakeKVBlockIndex{
			lookup: func(_ context.Context, _ []kvblock.BlockHash, _ sets.Set[string]) (map[kvblock.BlockHash][]kvblock.PodEntry, error) {
				return map[kvblock.BlockHash][]kvblock.PodEntry{}, nil
			},
		},
	}
	scorer := &fakeKVBlockScorer{}

	p := newProducerWithIndexer(ctx, idx, scorer)

	req := &scheduling.InferenceRequest{
		RequestID:   "req-mm",
		TargetModel: "test-model",
		Body: &fwkrh.InferenceRequestBody{
			TokenizedPrompt: &fwkrh.TokenizedPrompt{
				TokenIDs: tokens,
				MultiModalFeatures: []fwkrh.MultiModalFeature{
					{Modality: fwkrh.ModalityImage, Hash: "abc", Offset: 2, Length: 4},
				},
			},
		},
	}

	require.NoError(t, p.Produce(ctx, req, testEndpoints))
	require.NotNil(t, captured)
}

// nil request / empty body must not touch the indexer.
func TestProduce_NoOpPaths(t *testing.T) {
	ctx := utils.NewTestContext(t)
	idx := &fakeKVCacheIndexer{
		computeFromTokens: func(_ context.Context, _ []uint32, _ string, _ []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error) {
			t.Fatalf("ComputeBlockKeysFromTokens must not be called")
			return nil, nil
		},
		computePrompt: func(_ context.Context, _ *types.RenderChatRequest, _, _ string) ([]kvblock.BlockHash, error) {
			t.Fatalf("ComputeBlockKeys must not be called")
			return nil, nil
		},
		index: &fakeKVBlockIndex{},
	}
	p := newProducerWithIndexer(ctx, idx, &fakeKVBlockScorer{})

	require.NoError(t, p.Produce(ctx, &scheduling.InferenceRequest{RequestID: "x"}, testEndpoints))
	require.NoError(t, p.Produce(ctx, &scheduling.InferenceRequest{RequestID: "x", Body: &fwkrh.InferenceRequestBody{}}, testEndpoints))
}

func TestProduces_DeclaresPrefixCacheMatchInfo(t *testing.T) {
	p := &Producer{typedName: plugin.TypedName{Type: PluginType, Name: "x"}}
	_, ok := p.Produces()[attrprefix.PrefixCacheMatchInfoKey]
	require.True(t, ok)
}

func TestConsumes_DeclaresTokenizedPrompt(t *testing.T) {
	p := &Producer{typedName: plugin.TypedName{Type: PluginType, Name: "x"}}
	_, ok := p.Consumes()["TokenizedPrompt"]
	require.True(t, ok)
}
