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
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// compile-time type assertion.
var _ scheduling.Scorer = &TokenizerPlugin{}

// Category implements scheduling.Scorer.
// The tokenizer scorer is an Affinity scorer since it produces data consumed
// by cache-locality scorers.
func (p *TokenizerPlugin) Category() scheduling.ScorerCategory {
	return scheduling.Affinity
}

// Score implements scheduling.Scorer. It tokenizes the request prompt, writes
// the result to CycleState under the TokenizedPromptStateKey constant, and
// returns zero scores for all endpoints since this scorer only produces data.
func (p *TokenizerPlugin) Score(ctx context.Context, cycleState *scheduling.CycleState, request *scheduling.LLMRequest, pods []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	logger := log.FromContext(ctx).WithName(p.typedName.String())
	traceLogger := logger.V(logutil.TRACE)

	// Check if tokenization data already exists in CycleState.
	if _, err := scheduling.ReadCycleStateKey[*TokenizedPromptState](
		cycleState, TokenizedPromptStateKey); err == nil {
		traceLogger.Info("TokenizedPrompt already in CycleState, skipping")
	} else {
		tokenIDs, mmFeatures := p.tokenize(ctx, request)
		if tokenIDs != nil {
			cycleState.Write(TokenizedPromptStateKey, &TokenizedPromptState{
				TokenIDs:   tokenIDs,
				MMFeatures: mmFeatures,
			})
		}
	}

	// Return zero scores — this scorer only produces data for downstream consumers.
	scores := make(map[scheduling.Endpoint]float64, len(pods))
	for _, pod := range pods {
		scores[pod] = 0
	}
	return scores
}
