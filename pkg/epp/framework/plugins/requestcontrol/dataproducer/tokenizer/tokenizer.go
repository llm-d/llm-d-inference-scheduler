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

// Package tokenizer provides DataProducer plugin for the scheduler.
package tokenizer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	tokenizerTypes "github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

type tokenizer interface {
	Render(prompt string) ([]uint32, []tokenizerTypes.Offset, error)
	RenderChat(req *tokenizerTypes.RenderChatRequest) ([]uint32, *tokenization.MultiModalFeatures, error)
}

const (
	// PluginType is the type name used to register the tokenizer plugin.
	PluginType = "tokenizer"

	// TokenizedPromptKey is the data key advertised by this plugin to indicate
	// that it produces tokenized prompt data.
	TokenizedPromptKey = "TokenizedPrompt"

	// TokenizedPromptStateKey is the CycleState key used by the tokenizer scorer
	// to store tokenized prompt data for downstream consumers.
	// Namespaced by PluginType to avoid collisions with other plugins.
	TokenizedPromptStateKey = plugin.StateKey(PluginType + "." + TokenizedPromptKey)
)

// tokenizerPluginConfig holds the configuration for the tokenizer plugin.
type tokenizerPluginConfig struct {
	// SocketFile is the path to the Unix domain socket used to communicate
	// with the tokenizer service. Optional, defaults to /tmp/tokenizer/tokenizer-uds.socket.
	TokenizerConfig tokenization.UdsTokenizerConfig `json:"udsTokenizerConfig,omitempty"`
	// ModelName is the name of the model whose tokenizer should be loaded.
	ModelName string `json:"modelName"`
}

// PluginFactory is the factory function for the tokenizer plugin.
func PluginFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	config := tokenizerPluginConfig{}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &config); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' plugin - %w", PluginType, err)
		}
	}

	if config.ModelName == "" {
		return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'modelName' must be specified", PluginType)
	}

	p, err := NewPlugin(handle.Context(), &config)
	if err != nil {
		return nil, err
	}

	return p.WithName(name), nil
}

// NewPlugin creates a new tokenizer plugin instance and initializes the UDS tokenizer.
func NewPlugin(ctx context.Context, config *tokenizerPluginConfig) (*Plugin, error) {
	tokenizer, err := tokenization.NewUdsTokenizer(ctx, &config.TokenizerConfig, config.ModelName)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize UDS tokenizer for '%s' plugin - %w", PluginType, err)
	}

	return &Plugin{
		typedName: plugin.TypedName{Type: PluginType},
		tokenizer: tokenizer,
	}, nil
}

// TokenizedPromptState holds the tokenization result for a single request,
// stored in CycleState for consumption by downstream scorers.
// This follows the standard IGW pattern where scorers share data via CycleState
// (same as NoHitLRU reading from prefix-cache scorer).
type TokenizedPromptState struct {
	TokenIDs        []uint32   // Concatenated token IDs (single prompt or flattened multi-prompt)
	PerPromptTokens [][]uint32 // Per-prompt token slices; set only for []string prompts (len > 1)
	MMFeatures      *tokenization.MultiModalFeatures
}

// Clone implements plugin.StateData.
func (t *TokenizedPromptState) Clone() plugin.StateData {
	if t == nil {
		return nil
	}
	ids := make([]uint32, len(t.TokenIDs))
	copy(ids, t.TokenIDs)
	var perPrompt [][]uint32
	if t.PerPromptTokens != nil {
		perPrompt = make([][]uint32, len(t.PerPromptTokens))
		for i, p := range t.PerPromptTokens {
			cp := make([]uint32, len(p))
			copy(cp, p)
			perPrompt[i] = cp
		}
	}
	return &TokenizedPromptState{
		TokenIDs:        ids,
		PerPromptTokens: perPrompt,
		MMFeatures:      cloneMMFeatures(t.MMFeatures),
	}
}

// cloneMMFeatures deep-copies the maps/slices so cloned CycleState entries
// are fully independent and safe from concurrent mutation.
func cloneMMFeatures(src *tokenization.MultiModalFeatures) *tokenization.MultiModalFeatures {
	if src == nil {
		return nil
	}
	dst := &tokenization.MultiModalFeatures{}
	if src.MMHashes != nil {
		dst.MMHashes = make(map[string][]string, len(src.MMHashes))
		for k, v := range src.MMHashes {
			cp := make([]string, len(v))
			copy(cp, v)
			dst.MMHashes[k] = cp
		}
	}
	if src.MMPlaceholders != nil {
		dst.MMPlaceholders = make(map[string][]kvblock.PlaceholderRange, len(src.MMPlaceholders))
		for k, v := range src.MMPlaceholders {
			cp := make([]kvblock.PlaceholderRange, len(v))
			copy(cp, v)
			dst.MMPlaceholders[k] = cp
		}
	}
	return dst
}

// Plugin tokenizes the prompt in the incoming request and stores
// the result in CycleState for downstream consumers (scorers).
type Plugin struct {
	typedName plugin.TypedName
	tokenizer tokenizer
}

// TypedName returns the typed name of the plugin.
func (p *Plugin) TypedName() plugin.TypedName {
	return p.typedName
}

// WithName sets the name of the plugin.
func (p *Plugin) WithName(name string) *Plugin {
	p.typedName.Name = name
	return p
}

// tokenize extracts token IDs and optional multimodal features from the request.
// Returns one []uint32 per prompt: single prompt = [[tokens]], []string = [[t1],[t2],...].
func (p *Plugin) tokenize(ctx context.Context, request *scheduling.LLMRequest) ([][]uint32, *tokenization.MultiModalFeatures) {
	logger := log.FromContext(ctx).WithName(p.typedName.String())
	traceLogger := logger.V(logging.TRACE)

	if request.Body == nil {
		traceLogger.Info("Request body is nil, skipping tokenization")
		return nil, nil
	}

	traceLogger.Info("Request body present",
		"hasCompletions", request.Body.Completions != nil,
		"hasChatCompletions", request.Body.ChatCompletions != nil)

	var allTokenIDs [][]uint32
	var mmFeatures *tokenization.MultiModalFeatures
	var err error

	switch {
	case request.Body.Completions != nil:
		prompt := request.Body.Completions.Prompt
		if len(prompt.Strings) > 0 {
			allTokenIDs = make([][]uint32, 0, len(prompt.Strings))
			for _, promptStr := range prompt.Strings {
				traceLogger.Info("Calling Render for completions string", "promptLength", len(promptStr))
				sTokenIDs, _, err := p.tokenizer.Render(promptStr)
				if renderErr != nil {
					logger.Error(renderErr, "String tokenization failed, skipping")
					continue
				}
				allTokenIDs = append(allTokenIDs, sTokenIDs)
			}
		} else {
			traceLogger.Info("Calling Render for completions", "prompt", prompt)
    tokenIDs, _, err := p.tokenizer.Render(prompt.PlainText())
			tokenIDs, _, err = p.tokenizer.Render(prompt.PlainText())
			if err == nil {
				allTokenIDs = [][]uint32{tokenIDs}
			}
		}
	case request.Body.ChatCompletions != nil:
		renderReq := ChatCompletionsToRenderChatRequest(request.Body.ChatCompletions)
		traceLogger.Info("Calling RenderChat for chat completions", "messageCount", len(request.Body.ChatCompletions.Messages))
		var tokenIDs []uint32
		tokenIDs, mmFeatures, err = p.tokenizer.RenderChat(renderReq)
		if err == nil {
			allTokenIDs = [][]uint32{tokenIDs}
		}
	default:
		traceLogger.Info("Unsupported request type, skipping tokenization")
		return nil, nil
	}

	if err != nil {
		logger.Error(err, "Tokenization failed, skipping")
		return nil, nil
	}

	totalTokens := 0
	for _, ids := range allTokenIDs {
		totalTokens += len(ids)
	}
	traceLogger.Info("Tokenization succeeded", "tokenCount", totalTokens, "promptCount", len(allTokenIDs))
	return allTokenIDs, mmFeatures
}

// ChatCompletionsToRenderChatRequest converts a ChatCompletionsRequest to a
// tokenization RenderChatRequest, including multimodal content blocks.
func ChatCompletionsToRenderChatRequest(chat *fwkrh.ChatCompletionsRequest) *tokenizerTypes.RenderChatRequest {
	conversation := make([]tokenizerTypes.Conversation, 0, len(chat.Messages))
	for _, msg := range chat.Messages {
		conv := tokenizerTypes.Conversation{
			Role:    msg.Role,
			Content: tokenizerTypes.Content{Raw: msg.Content.Raw},
		}
		for _, block := range msg.Content.Structured {
			conv.Content.Structured = append(conv.Content.Structured, tokenizerTypes.ContentBlock{
				Type:     block.Type,
				Text:     block.Text,
				ImageURL: tokenizerTypes.ImageBlock{URL: block.ImageURL.Url},
			})
		}
		conversation = append(conversation, conv)
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
