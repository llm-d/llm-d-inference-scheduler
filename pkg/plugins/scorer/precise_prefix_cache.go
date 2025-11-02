package scorer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache/kvevents"
	chat_completions "github.com/llm-d/llm-d-kv-cache-manager/pkg/preprocessing/chat_completions"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// PrecisePrefixCachePluginType is the type-name of the PrecisePrefixCacheScorer plugin.
	PrecisePrefixCachePluginType = "precise-prefix-cache-scorer"
)

// PrecisePrefixCachePluginConfig holds the configuration for the
// PrecisePrefixCacheScorer plugin.
type PrecisePrefixCachePluginConfig struct {
	// IndexerConfig holds the configuration for the `kvcache.Indexer` which is
	// used to score pods based on the KV-cache index state.
	IndexerConfig *kvcache.Config `json:"indexerConfig"`
	// KVEventsConfig holds the configuration for the `kvevents.Pool` which is
	// used to subscribe to KV-cache events and update the internal KV-cache
	// index state.
	KVEventsConfig *kvevents.Config `json:"kvEventsConfig"`
}

// compile-time type assertion
var _ framework.Scorer = &PrecisePrefixCacheScorer{}

// PrecisePrefixCachePluginFactory defines the factory function for creating
// a new instance of the PrefixCacheTrackingPlugin.
func PrecisePrefixCachePluginFactory(name string, rawParameters json.RawMessage,
	handle plugins.Handle) (plugins.Plugin, error) {
	parameters := PrecisePrefixCachePluginConfig{
		IndexerConfig:  kvcache.NewDefaultConfig(),
		KVEventsConfig: kvevents.DefaultConfig(),
	}

	// read hugging face token from environment variable if set
	if token := os.Getenv("HF_TOKEN"); token != "" {
		parameters.IndexerConfig.TokenizersPoolConfig.HuggingFaceToken = token
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse %s plugin config: %w", PrecisePrefixCachePluginType, err)
		}
	}

	scorer, err := New(handle.Context(), parameters)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s plugin: %w", PrecisePrefixCachePluginType, err)
	}

	return scorer.WithName(name), nil
}

// New initializes a new prefix Plugin and returns its pointer.
// It sets up the `kvcache.Indexer` and `kvevents.Pool`
// based on the provided configuration. The `kvevents.Pool` is started
// in a goroutine to listen for KV-cache events and update the internal
// KV-cache index state. The `kvcache.Indexer` is also started in a goroutine
// to score pods based on the KV-cache index state.
//
// If the configuration is invalid or if the indexer fails to initialize,
// an error is returned.
func New(ctx context.Context, config PrecisePrefixCachePluginConfig) (*PrecisePrefixCacheScorer, error) {
	// initialize the indexer
	kvCacheIndexer, err := kvcache.NewKVCacheIndexer(ctx, config.IndexerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create `kvcache.Indexer`: %w", err)
	}

	go kvCacheIndexer.Run(ctx)

	// initialize the KV-events pool
	pool := kvevents.NewPool(config.KVEventsConfig, kvCacheIndexer.KVBlockIndex())
	pool.Start(ctx)

	return &PrecisePrefixCacheScorer{
		typedName:      plugins.TypedName{Type: PrecisePrefixCachePluginType},
		kvCacheIndexer: kvCacheIndexer,
	}, nil
}

// PrecisePrefixCacheScorer implements the framework.Scorer interface.
// The scorer implements precise prefix-cache KV-block locality scoring.
// It uses the `kvcache.Indexer` to score pods based on the KV-cache index
// state, and the `kvevents.Pool` to subscribe to KV-cache events
// to keep the internal KV-cache index state up-to-date.
type PrecisePrefixCacheScorer struct {
	typedName      plugins.TypedName
	kvCacheIndexer *kvcache.Indexer
}

// TypedName returns the typed name of the plugin.
func (s *PrecisePrefixCacheScorer) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *PrecisePrefixCacheScorer) WithName(name string) *PrecisePrefixCacheScorer {
	s.typedName.Name = name
	return s
}

// Score scores the provided pod based on the KVCache index state.
// The returned scores are normalized to a range of 0-1.
func (s *PrecisePrefixCacheScorer) Score(ctx context.Context, _ *types.CycleState, request *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
	logger := log.FromContext(ctx).WithName(s.typedName.String())
	
	if request == nil {
		logger.V(logutil.DEBUG).Info("Request is nil, skipping scoring")
		return nil
	}

	// Extract the flattened prompt from the request
	logger.V(logutil.DEBUG).Info("Extracting prompt from request", 
		"target_model", request.TargetModel,
		"has_chat_completions", request.Body != nil && request.Body.ChatCompletions != nil,
		"has_completions", request.Body != nil && request.Body.Completions != nil)
	
	prompt, err := s.extractPrompt(ctx, request)
	if err != nil {
		logger.Error(err, "Failed to extract prompt from request", "target_model", request.TargetModel)
		return nil
	}

	logger.V(logutil.DEBUG).Info("Getting pod scores", 
		"prompt_length", len(prompt),
		"target_model", request.TargetModel)
	
	scores, err := s.kvCacheIndexer.GetPodScores(ctx, prompt, request.TargetModel, nil)
	if err != nil {
		logger.Error(err, "Failed to get pod scores", "target_model", request.TargetModel)
		return nil
	}
	
	logger.V(logutil.DEBUG).Info("Got pod scores", "scores_count", len(scores), "scores", scores, "target_model", request.TargetModel)

	podToKey := func(pod types.Pod) (string, bool) {
		metricsPod := pod.GetPod()
		if metricsPod == nil {
			return "", false
		}

		return metricsPod.Address, true
	}

	return indexedScoresToNormalizedScoredPods(pods, podToKey, scores)
}

// extractPrompt extracts the flattened prompt from the request.
// For chat completions, it renders the messages using the model's chat template.
// For regular completions, it uses the prompt directly.
func (s *PrecisePrefixCacheScorer) extractPrompt(ctx context.Context, request *types.LLMRequest) (string, error) {
	logger := log.FromContext(ctx).WithName(s.typedName.String())

	// If it's a chat completion request, render the chat template
	if request.Body != nil && request.Body.ChatCompletions != nil {
		logger.V(logutil.DEBUG).Info("Processing chat completion request", 
			"messages_count", len(request.Body.ChatCompletions.Messages),
			"target_model", request.TargetModel)

		// Create render request
		renderReq := &chat_completions.RenderJinjaTemplateRequest{
			Conversations:             make([]chat_completions.ChatMessage, 0),
			Tools:                     request.Body.ChatCompletions.Tools,
			Documents:                 request.Body.ChatCompletions.Documents,
			ChatTemplate:              request.Body.ChatCompletions.ChatTemplate,
			ReturnAssistantTokensMask: request.Body.ChatCompletions.ReturnAssistantTokensMask,
			ContinueFinalMessage:      request.Body.ChatCompletions.ContinueFinalMessage,
			AddGenerationPrompt:       request.Body.ChatCompletions.AddGenerationPrompt,
			ChatTemplateKWArgs:        request.Body.ChatCompletions.ChatTemplateKWArgs,
		}

		// Convert messages to the format expected by the renderer
		for _, msg := range request.Body.ChatCompletions.Messages {
			renderReq.Conversations = append(renderReq.Conversations, chat_completions.ChatMessage{
				Role:    msg.Role,
				Content: msg.Content.Raw, 
			})
		}

		// Initialize the chat templating processor
		processor := chat_completions.NewChatTemplatingProcessor()
		if err := processor.Initialize(); err != nil {
			return "", fmt.Errorf("failed to initialize chat templating processor: %w", err)
		}

		// Fetch the chat template from the model
		fetchReq := chat_completions.FetchChatTemplateRequest{
			Model: request.TargetModel,
		}
		logger.V(logutil.DEBUG).Info("Fetching chat template", "model", request.TargetModel)
		chatTemplate, chatTemplateKWArgs, err := processor.FetchChatTemplate(ctx, fetchReq)
		if err != nil {
			logger.Error(err, "Failed to fetch chat template", "model", request.TargetModel)
			return "", fmt.Errorf("failed to fetch chat template: %w", err)
		}
		logger.V(logutil.DEBUG).Info("Chat template fetched", 
			"model", request.TargetModel,
			"template_length", len(chatTemplate),
			"has_kwargs", len(chatTemplateKWArgs) > 0)

		// Set the fetched template in the render request
		renderReq.ChatTemplate = chatTemplate
		renderReq.ChatTemplateKWArgs = chatTemplateKWArgs

		// Render the template to get flattened prompt
		logger.V(logutil.DEBUG).Info("Rendering chat template", 
			"conversations_count", len(renderReq.Conversations))
		resp, err := processor.RenderChatTemplate(ctx, renderReq)
		if err != nil {
			logger.Error(err, "Failed to render chat template")
			return "", fmt.Errorf("failed to render chat template: %w", err)
		}

		if len(resp.RenderedChats) == 0 {
			logger.Error(nil, "No rendered chat returned from template rendering")
			return "", fmt.Errorf("no rendered chat returned from template rendering")
		}

		prompt := resp.RenderedChats[0]
		logger.V(logutil.DEBUG).Info("Chat template rendered successfully", "prompt_length", len(prompt))
		return prompt, nil
	}

	// For regular completions, use the prompt directly
	if request.Body != nil && request.Body.Completions != nil {
		prompt := request.Body.Completions.Prompt
		logger.V(logutil.DEBUG).Info("Using completion prompt directly", "prompt_length", len(prompt))
		return prompt, nil
	}

	// Fallback: try to extract prompt from request body if available
	if request.Body != nil {
		// Try to marshal and extract prompt from raw data
		if dataBytes, err := json.Marshal(request.Body); err == nil {
			var rawData map[string]interface{}
			if err := json.Unmarshal(dataBytes, &rawData); err == nil {
				if prompt, ok := rawData["prompt"].(string); ok && prompt != "" {
					logger.V(logutil.DEBUG).Info("Extracted prompt from raw data", "prompt_length", len(prompt))
					return prompt, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no valid prompt found in request")
}
