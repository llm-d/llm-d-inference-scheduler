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
	// init the preProcessor, maroon fork let him handle

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
	loggerDebug := logger.V(logutil.DEBUG)
	
	if request == nil {
		logger.Info("PREPROCESSING: Request is nil, skipping scoring")
		loggerDebug.Info("Request is nil, skipping scoring")
		return nil
	}

	// Preprocess the request to get the flattened prompt
	logger.Info("PREPROCESSING: Starting preprocessing", 
		"target_model", request.TargetModel,
		"has_chat_completions", request.Body != nil && request.Body.ChatCompletions != nil,
		"has_completions", request.Body != nil && request.Body.Completions != nil)
	
	prompt, err := s.preprocessRequest(ctx, request)
	if err != nil {
		logger.Error(err, "PREPROCESSING: Failed to preprocess request", "target_model", request.TargetModel)
		loggerDebug.Error(err, "Failed to preprocess request")
		return nil
	}

	logger.Info("PREPROCESSING: Preprocessing complete, getting pod scores", 
		"prompt_length", len(prompt),
		"target_model", request.TargetModel)
	
	scores, err := s.kvCacheIndexer.GetPodScores(ctx, prompt, request.TargetModel, nil)
	if err != nil {
		logger.Error(err, "PREPROCESSING: Failed to get pod scores", "target_model", request.TargetModel)
		loggerDebug.Error(err, "Failed to get pod scores")
		return nil
	}
	
	logger.Info("PREPROCESSING: Got pod scores", "scores_count", len(scores), "target_model", request.TargetModel)
	loggerDebug.Info("Got pod scores", "scores", scores)

	podToKey := func(pod types.Pod) (string, bool) {
		metricsPod := pod.GetPod()
		if metricsPod == nil {
			return "", false
		}

		return metricsPod.Address, true
	}

	return indexedScoresToNormalizedScoredPods(pods, podToKey, scores)
}

// preprocessRequest handles preprocessing of the request to extract the flattened prompt
// For chat completions, it converts messages to a templated prompt
// For regular completions, it uses the prompt directly
func (s *PrecisePrefixCacheScorer) preprocessRequest(ctx context.Context, request *types.LLMRequest) (string, error) {
	logger := log.FromContext(ctx).WithName(s.typedName.String())
	loggerDebug := logger.V(logutil.DEBUG)

	// If it's a chat completion request, apply preprocessing
	if request.Body != nil && request.Body.ChatCompletions != nil {
		// INFO level logging for verification
		logger.Info("PREPROCESSING: Processing chat completion request", 
			"messages_count", len(request.Body.ChatCompletions.Messages),
			"target_model", request.TargetModel)
		loggerDebug.Info("Processing chat completion request", "messages_count", len(request.Body.ChatCompletions.Messages))

		// Create preprocessing request
		preprocessReq := &chat_completions.RenderJinjaTemplateRequest{
			Conversations:             make([]chat_completions.ChatMessage, 0),
			Tools:                     request.Body.ChatCompletions.Tools,
			Documents:                 request.Body.ChatCompletions.Documents,
			ChatTemplate:              request.Body.ChatCompletions.ChatTemplate,
			ReturnAssistantTokensMask: request.Body.ChatCompletions.ReturnAssistantTokensMask,
			ContinueFinalMessage:      request.Body.ChatCompletions.ContinueFinalMessage,
			AddGenerationPrompt:       request.Body.ChatCompletions.AddGenerationPrompt,
			ChatTemplateKWArgs:        request.Body.ChatCompletions.ChatTemplateKWArgs,
		}

		// Convert messages to the format expected by preprocessing
		for _, msg := range request.Body.ChatCompletions.Messages {
			preprocessReq.Conversations = append(preprocessReq.Conversations, chat_completions.ChatMessage{
				Role:    msg.Role,
				Content: msg.Content.Raw, 
			})
		}

		// Create preprocessing processor
		processor := chat_completions.NewChatTemplatingProcessor()

		// Initialize the processor (this starts Python interpreter if not already started)
		// The C code handles global initialization tracking to prevent multiple initializations
		if err := processor.Initialize(); err != nil {
			return "", fmt.Errorf("failed to initialize chat templating processor: %w", err)
		}

		// First, fetch the chat template from the model
		fetchReq := chat_completions.FetchChatTemplateRequest{
			Model: request.TargetModel,
		}
		logger.Info("PREPROCESSING: Fetching chat template", "model", request.TargetModel)
		chatTemplate, chatTemplateKWArgs, err := processor.FetchChatTemplate(ctx, fetchReq)
		if err != nil {
			logger.Error(err, "PREPROCESSING: Failed to fetch chat template", "model", request.TargetModel)
			return "", fmt.Errorf("failed to fetch chat template: %w", err)
		}
		logger.Info("PREPROCESSING: Chat template fetched successfully", 
			"model", request.TargetModel,
			"template_length", len(chatTemplate),
			"has_kwargs", len(chatTemplateKWArgs) > 0)

		// Set the fetched template in the render request
		preprocessReq.ChatTemplate = chatTemplate
		preprocessReq.ChatTemplateKWArgs = chatTemplateKWArgs

		// Render the template to get flattened prompt
		logger.Info("PREPROCESSING: Rendering chat template", 
			"conversations_count", len(preprocessReq.Conversations))
		resp, err := processor.RenderChatTemplate(ctx, preprocessReq)
		if err != nil {
			logger.Error(err, "PREPROCESSING: Failed to render chat template")
			return "", fmt.Errorf("failed to render chat template: %w", err)
		}

		if len(resp.RenderedChats) == 0 {
			logger.Error(nil, "PREPROCESSING: No rendered chat returned from preprocessing")
			return "", fmt.Errorf("no rendered chat returned from preprocessing")
		}

		promptLength := len(resp.RenderedChats[0])
		promptPreview := resp.RenderedChats[0]
		if len(promptPreview) > 100 {
			promptPreview = promptPreview[:100] + "..."
		}
		logger.Info("PREPROCESSING: Successfully preprocessed chat completion request", 
			"prompt_length", promptLength,
			"prompt_preview", promptPreview)
		loggerDebug.Info("Successfully preprocessed chat completion request", "prompt_length", promptLength)
		return resp.RenderedChats[0], nil
	}

	// For regular completions, use the prompt directly
	if request.Body != nil && request.Body.Completions != nil {
		prompt := request.Body.Completions.Prompt
		logger.Info("PREPROCESSING: Using completion prompt directly", 
			"prompt_length", len(prompt),
			"target_model", request.TargetModel)
		loggerDebug.Info("Using completion prompt directly", "prompt_length", len(prompt))
		return prompt, nil
	}

	// Fallback: try to extract prompt from request body if available
	if request.Body != nil {
		// Try to marshal and extract prompt from raw data
		if dataBytes, err := json.Marshal(request.Body); err == nil {
			var rawData map[string]interface{}
			if err := json.Unmarshal(dataBytes, &rawData); err == nil {
				if prompt, ok := rawData["prompt"].(string); ok && prompt != "" {
					loggerDebug.Info("Extracted prompt from raw data", "prompt_length", len(prompt))
					return prompt, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no valid prompt found in request")
}
