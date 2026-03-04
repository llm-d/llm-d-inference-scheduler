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

// Package preparedata provides PrepareData plugins for the scheduler.
package preparedata

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	tokenizerTypes "github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

type tokenizer interface {
	Render(prompt string) ([]uint32, []tokenizerTypes.Offset, error)
	RenderChat(req *tokenizerTypes.RenderChatRequest) ([]uint32, []tokenizerTypes.Offset, error)
}

const (
	// TokenizerPluginType is the type name used to register the tokenizer plugin.
	TokenizerPluginType = "tokenizer"

	// TokenizedPromptKey is the data key advertised by this plugin to indicate
	// that it produces a TokenizedPrompt on the LLMRequest.
	TokenizedPromptKey = "TokenizedPrompt"
)

// compile-time type assertion.
var _ requestcontrol.PrepareDataPlugin = &TokenizerPlugin{}

// tokenizerPluginConfig holds the configuration for the tokenizer plugin.
type tokenizerPluginConfig struct {
	// SocketFile is the path to the Unix domain socket used to communicate
	// with the tokenizer service. Optional, defaults to /tmp/tokenizer/tokenizer-uds.socket.
	TokenizerConfig tokenization.UdsTokenizerConfig `json:"udsTokenizerConfig,omitempty"`
	// ModelName is the name of the model whose tokenizer should be loaded.
	ModelName string `json:"modelName"`
}

// TokenizerPluginFactory is the factory function for the tokenizer plugin.
func TokenizerPluginFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	config := tokenizerPluginConfig{}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &config); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' plugin - %w", TokenizerPluginType, err)
		}
	}

	if config.ModelName == "" {
		return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'modelName' must be specified", TokenizerPluginType)
	}

	p, err := NewTokenizerPlugin(handle.Context(), &config)
	if err != nil {
		return nil, err
	}

	return p.WithName(name), nil
}

// NewTokenizerPlugin creates a new tokenizer plugin instance and initializes the UDS tokenizer.
func NewTokenizerPlugin(ctx context.Context, config *tokenizerPluginConfig) (*TokenizerPlugin, error) {
	tokenizer, err := tokenization.NewUdsTokenizer(ctx, &config.TokenizerConfig, config.ModelName)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize UDS tokenizer for '%s' plugin - %w", TokenizerPluginType, err)
	}

	return &TokenizerPlugin{
		typedName: plugin.TypedName{Type: TokenizerPluginType},
		tokenizer: tokenizer,
	}, nil
}

// TokenizerPlugin tokenizes the prompt in the incoming request and attaches
// the result to the LLMRequest for downstream consumers.
type TokenizerPlugin struct {
	typedName plugin.TypedName
	tokenizer tokenizer
}

// TypedName returns the typed name of the plugin.
func (p *TokenizerPlugin) TypedName() plugin.TypedName {
	return p.typedName
}

// WithName sets the name of the plugin.
func (p *TokenizerPlugin) WithName(name string) *TokenizerPlugin {
	p.typedName.Name = name
	return p
}

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
	logger := log.FromContext(ctx).WithName(p.typedName.String())
	traceLogger := logger.V(logutil.TRACE)

	if request.TokenizedPrompt != nil {
		traceLogger.Info("TokenizedPrompt already set, skipping")
		return nil
	}

	if request.Body == nil {
		traceLogger.Info("Request body is nil, skipping tokenization")
		return nil
	}

	traceLogger.Info("Request body present",
		"hasCompletions", request.Body.Completions != nil,
		"hasChatCompletions", request.Body.ChatCompletions != nil)

	var tokenIDs []uint32
	var err error

	switch {
	case request.Body.Completions != nil:
		traceLogger.Info("Calling Render for completions", "prompt", request.Body.Completions.Prompt)
		tokenIDs, _, err = p.tokenizer.Render(request.Body.Completions.Prompt)
	case request.Body.ChatCompletions != nil:
		renderReq := chatCompletionsToRenderChatRequest(request.Body.ChatCompletions)
		traceLogger.Info("Calling RenderChat for chat completions", "messageCount", len(request.Body.ChatCompletions.Messages))
		tokenIDs, _, err = p.tokenizer.RenderChat(renderReq)
	default:
		traceLogger.Info("Unsupported request type, skipping tokenization")
		return nil
	}

	if err != nil {
		logger.Error(err, "Tokenization failed, skipping")
		return nil
	}

	traceLogger.Info("Tokenization succeeded", "tokenCount", len(tokenIDs))
	request.TokenizedPrompt = &scheduling.TokenizedPrompt{
		TokenIDs: tokenIDs,
	}

	return nil
}

// chatCompletionsToRenderChatRequest converts a ChatCompletionsRequest to a
// tokenization RenderChatRequest.
func chatCompletionsToRenderChatRequest(chat *scheduling.ChatCompletionsRequest) *tokenizerTypes.RenderChatRequest {
	conversation := make([]tokenizerTypes.Conversation, 0, len(chat.Messages))
	for _, msg := range chat.Messages {
		conversation = append(conversation, tokenizerTypes.Conversation{
			Role:    msg.Role,
			Content: msg.Content.Raw,
		})
	}

	return &tokenizerTypes.RenderChatRequest{
		Conversation:              conversation,
		Tools:                     chat.Tools,
		Documents:                 chat.Documents,
		ChatTemplate:              chat.ChatTemplate,
		ReturnAssistantTokensMask: chat.ReturnAssistantTokensMask,
		ContinueFinalMessage:      chat.ContinueFinalMessage,
		AddGenerationPrompt:       chat.AddGenerationPrompt,
		ChatTemplateKWArgs:        chat.ChatTemplateKWArgs,
	}
}
