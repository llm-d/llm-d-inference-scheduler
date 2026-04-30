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
	"errors"
	"fmt"

	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	tokenizerTypes "github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requestcontrol"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

// compile-time type assertion.
var _ requestcontrol.DataProducer = &Plugin{}

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

// Produces returns the data keys this plugin produces.
func (p *Plugin) Produces() map[string]any {
	return map[string]any{TokenizedPromptKey: scheduling.TokenizedPrompt{}}
}

// Consumes returns the data keys this plugin requires.
func (p *Plugin) Consumes() map[string]any {
	return nil
}

// PrepareRequestData tokenizes the request prompt and stores the result
// on the InferenceRequest so that scorers and filters can use it.
// If the request already contains tokenized data, it is left unchanged.
// Returns an error if the request body is nil or if the tokenizer fails.
func (p *Plugin) PrepareRequestData(ctx context.Context, request *scheduling.InferenceRequest, pods []scheduling.Endpoint) error {
	tp, err := p.tokenize(ctx, request)
	if err != nil {
		return err
	}

	request.Body.TokenizedPrompt = tp
	return nil
}

// tokenize extracts token IDs and optional multimodal features from the request.
// Returns the existing TokenizedPrompt unchanged if one is already set.
// Returns (nil, nil) if the request has no recognized body type to tokenize.
// Returns a non-nil error if the request body is nil or if the tokenizer fails.
func (p *Plugin) tokenize(ctx context.Context, request *scheduling.InferenceRequest) (*fwkrh.TokenizedPrompt, error) {
	logger := log.FromContext(ctx).WithName(p.typedName.String())
	traceLogger := logger.V(logging.TRACE)

	if request.Body == nil {
		return nil, errors.New("request body is nil")
	}

	if request.Body.TokenizedPrompt != nil {
		traceLogger.Info("TokenizedPrompt already present, skipping")
		return request.Body.TokenizedPrompt, nil
	}

	traceLogger.Info("Request body present",
		"hasCompletions", request.Body.Completions != nil,
		"hasChatCompletions", request.Body.ChatCompletions != nil)

	var tokenIDs []uint32
	var mmFeatures *tokenization.MultiModalFeatures
	var err error

	switch {
	case request.Body.Completions != nil:
		traceLogger.Info("Calling Render for completions", "prompt", request.Body.Completions.Prompt)
		tokenIDs, _, err = p.tokenizer.Render(request.Body.Completions.Prompt.Raw)
	case request.Body.ChatCompletions != nil:
		renderReq := ChatCompletionsToRenderChatRequest(request.Body.ChatCompletions)
		traceLogger.Info("Calling RenderChat for chat completions", "messageCount", len(request.Body.ChatCompletions.Messages))
		tokenIDs, mmFeatures, err = p.tokenizer.RenderChat(renderReq)
	default:
		traceLogger.Info("Unsupported request type, skipping tokenization")
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("tokenization failed: %w", err)
	}

	traceLogger.Info("Tokenization succeeded", "tokenCount", len(tokenIDs))
	return &scheduling.TokenizedPrompt{
		TokenIDs:           tokenIDs,
		MultiModalFeatures: multiModalFeaturesToSlice(mmFeatures),
	}, nil
}

// multiModalFeaturesToSlice converts the tokenizer's map-based MultiModalFeatures
// into the flat []MultiModalFeature slice used by the scheduling interface.
// Within each modality, items follow the order returned by the tokenizer.
func multiModalFeaturesToSlice(mmf *tokenization.MultiModalFeatures) []scheduling.MultiModalFeature {
	if mmf == nil || len(mmf.MMHashes) == 0 {
		return nil
	}

	var features []scheduling.MultiModalFeature
	for modality, hashes := range mmf.MMHashes {
		placeholders := mmf.MMPlaceholders[modality]
		for i, hash := range hashes {
			feat := scheduling.MultiModalFeature{
				Modality: scheduling.Modality(modality),
				Hash:     hash,
			}
			if i < len(placeholders) {
				feat.Offset = placeholders[i].Offset
				feat.Length = placeholders[i].Length
			}
			features = append(features, feat)
		}
	}
	return features
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
				ImageURL: tokenizerTypes.ImageBlock{URL: block.ImageURL.URL},
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
