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
	"errors"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/dataproducer/tokenizer"
)

// kvCacheIndexer is the subset of kvcache.Indexer the producer needs.
// Narrowed for testability.
type kvCacheIndexer interface {
	ComputeBlockKeys(ctx context.Context, renderReq *types.RenderChatRequest, prompt, modelName string) ([]kvblock.BlockHash, error)
	ComputeBlockKeysFromTokens(ctx context.Context, tokens []uint32, modelName string, extraFeatures []*kvblock.BlockExtraFeatures) ([]kvblock.BlockHash, error)
	KVBlockIndex() kvblock.Index
}

// computeBlockKeys converts the request to KV-block keys. Prefers
// TokenizedPrompt (no tokenization); falls back to the indexer's
// prompt-based path (legacy tokenizersPoolConfig configs) when tokens are
// absent. Returns (nil, nil) when no input is resolvable.
func computeBlockKeys(ctx context.Context, idx kvCacheIndexer,
	request *scheduling.InferenceRequest, blockSizeTokens int,
) ([]kvblock.BlockHash, error) {
	if request == nil || request.Body == nil {
		return nil, nil
	}

	if tp := request.Body.TokenizedPrompt; tp != nil && len(tp.TokenIDs) > 0 {
		var extraFeatures []*kvblock.BlockExtraFeatures
		if len(tp.MultiModalFeatures) > 0 {
			mmHashes, mmPlaceholders := tokenizer.ConvertMMFeaturesFromUpstream(tp.MultiModalFeatures)
			extraFeatures = kvblock.ComputeBlockExtraFeatures(
				mmHashes, mmPlaceholders, blockSizeTokens, len(tp.TokenIDs))
		}
		return idx.ComputeBlockKeysFromTokens(ctx, tp.TokenIDs, request.TargetModel, extraFeatures)
	}

	var (
		keys []kvblock.BlockHash
		err  error
	)
	switch {
	case request.Body.ChatCompletions != nil:
		renderReq := tokenizer.ChatCompletionsToRenderChatRequest(request.Body.ChatCompletions)
		//nolint:staticcheck // SA1019: legacy path retained for tokenizersPoolConfig configs.
		keys, err = idx.ComputeBlockKeys(ctx, renderReq, "", request.TargetModel)
	case request.Body.Completions != nil:
		//nolint:staticcheck // SA1019: legacy path retained for tokenizersPoolConfig configs.
		keys, err = idx.ComputeBlockKeys(ctx, nil, request.Body.Completions.Prompt.Raw, request.TargetModel)
	default:
		return nil, nil
	}
	if errors.Is(err, kvcache.ErrInternalTokenizationDisabled) {
		return nil, nil
	}
	return keys, err
}
