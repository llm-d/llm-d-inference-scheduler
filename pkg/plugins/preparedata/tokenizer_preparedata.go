//go:build gaie_tokenized_prompt

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

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// compile-time type assertion.
var _ requestcontrol.PrepareDataPlugin = &TokenizerPlugin{}

// Produces returns the data keys this plugin produces.
func (p *TokenizerPlugin) Produces() map[string]any {
	return map[string]any{TokenizedPromptKey: scheduling.TokenizedPrompt{}}
}

// Consumes returns the data keys this plugin requires.
func (p *TokenizerPlugin) Consumes() map[string]any {
	return nil
}

// PrepareRequestData tokenizes the request prompt and stores the result
// on the LLMRequest so that scorers and filters can use it.
// If the request already contains tokenized data, tokenization is skipped.
// This method is fail-open: errors are logged and TokenizedPrompt is left nil.
func (p *TokenizerPlugin) PrepareRequestData(ctx context.Context, request *scheduling.LLMRequest, pods []scheduling.Endpoint) error {
	if request.TokenizedPrompt != nil {
		return nil
	}

	tokenIDs, _ := p.tokenize(ctx, request)
	if tokenIDs == nil {
		return nil
	}

	request.TokenizedPrompt = &scheduling.TokenizedPrompt{
		TokenIDs: tokenIDs,
	}

	return nil
}
