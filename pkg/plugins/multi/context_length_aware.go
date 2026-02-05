package multi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	preprocessing "github.com/llm-d/llm-d-kv-cache/pkg/preprocessing/chat_completions"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// ContextLengthAwareType is the type of the ContextLengthAware plugin
	ContextLengthAwareType = "context-length-aware"

	// DefaultContextLengthLabel is the default label name used to identify context length ranges on pods
	DefaultContextLengthLabel = "llm-d.ai/context-length-range"

	// charToTokenMultiplier defines the multiplier to convert characters to tokens
	// This is an approximate value and may vary based on the tokenizer used
	// Used as a fallback when no tokenizer is configured
	charToTokenMultiplier = 0.25
)

type contextLengthAwareParams struct {
	// Label is the pod label name to check for context length ranges
	// Format expected: "min-max" (e.g., "0-2048" or "2048-8192")
	// Multiple ranges can be specified with comma separation (e.g., "0-2048,8192-16384")
	Label string `json:"label"`

	// EnableFiltering determines whether the plugin also filters pods that don't match a
	// request's context length.
	// If false, the plugin only scores pods.
	// Default is false.
	EnableFiltering bool `json:"enableFiltering"`

	// ModelName is the name of the model to use for tokenization.
	// This is required when using precise tokenization.
	// The model name should match an entry in the LocalTokenizerConfig.ModelTokenizerMap
	// or be a valid HuggingFace model ID when using HFTokenizerConfig.
	ModelName string `json:"modelName,omitempty"`

	// LocalTokenizerConfig provides a mapping from model names to local tokenizer.json file paths.
	// When configured, the plugin uses precise tokenization instead of character-based estimation.
	// This is optional - if not configured, falls back to character-based estimation.
	LocalTokenizerConfig *tokenization.LocalTokenizerConfig `json:"localTokenizerConfig,omitempty"`

	// HFTokenizerConfig holds the configuration for the HuggingFace tokenizer.
	// When configured (and local tokenizer is not available), downloads tokenizers from HuggingFace.
	// This is optional - if not configured, falls back to character-based estimation.
	HFTokenizerConfig *tokenization.HFTokenizerConfig `json:"hfTokenizerConfig,omitempty"`
}

// contextRange represents a single context length range.
type contextRange struct {
	min int
	max int
}

var _ scheduling.Filter = &ContextLengthAware{} // validate interface conformance
var _ scheduling.Scorer = &ContextLengthAware{} // validate interface conformance

// ContextLengthAwareFactory defines the factory function for the ContextLengthAware plugin.
func ContextLengthAwareFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	parameters := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: false,
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' plugin - %w", ContextLengthAwareType, err)
		}
	}

	if parameters.Label == "" {
		return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'label' must be specified", ContextLengthAwareType)
	}

	// Initialize tokenizer if configuration is provided
	var tokenizer tokenization.Tokenizer
	if parameters.LocalTokenizerConfig != nil && parameters.LocalTokenizerConfig.IsEnabled() {
		if parameters.ModelName == "" {
			return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'modelName' is required when using localTokenizerConfig", ContextLengthAwareType)
		}

		var err error
		tokenizer, err = tokenization.NewCachedLocalTokenizer(handle.Context(), parameters.ModelName, *parameters.LocalTokenizerConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create local tokenizer for '%s' plugin: %w", ContextLengthAwareType, err)
		}
	} else if parameters.HFTokenizerConfig != nil && parameters.HFTokenizerConfig.IsEnabled() {
		if parameters.ModelName == "" {
			return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'modelName' is required when using hfTokenizerConfig", ContextLengthAwareType)
		}

		var err error
		tokenizer, err = tokenization.NewCachedHFTokenizer(handle.Context(), parameters.ModelName, parameters.HFTokenizerConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create HuggingFace tokenizer for '%s' plugin: %w", ContextLengthAwareType, err)
		}
	}

	return NewContextLengthAware(name, parameters, tokenizer), nil
}

// NewContextLengthAware creates and returns an instance of the ContextLengthAware plugin.
func NewContextLengthAware(name string, params *contextLengthAwareParams, tokenizer tokenization.Tokenizer) *ContextLengthAware {
	return &ContextLengthAware{
		typedName:       plugin.TypedName{Type: ContextLengthAwareType, Name: name},
		labelName:       params.Label,
		enableFiltering: params.EnableFiltering,
		modelName:       params.ModelName,
		tokenizer:       tokenizer,
	}
}

// ContextLengthAware is a plugin that filters or scores endpoints based on their association
// with input context length groups.
// It checks for a specific label on endpoints that defines the context length ranges they support.
// If filtering is enabled, endpoints that don't support the request's context length are filtered out.
// Additionally, it scores endpoints based on how well their context length ranges match the request.
type ContextLengthAware struct {
	// typedName defines the plugin typed name
	typedName plugin.TypedName
	// labelName defines the name of the label to be checked
	labelName string
	// enableFiltering indicates whether filtering is enabled
	enableFiltering bool
	// modelName is the model name used for tokenization
	modelName string
	// tokenizer is the tokenizer instance for precise token counting
	// If nil, falls back to character-based estimation
	tokenizer tokenization.Tokenizer
}

// TypedName returns the typed name of the plugin.
func (p *ContextLengthAware) TypedName() plugin.TypedName {
	return p.typedName
}

// WithName sets the name of the plugin.
func (p *ContextLengthAware) WithName(name string) *ContextLengthAware {
	p.typedName.Name = name
	return p
}

// Filter filters out endpoints that don't have a context length range matching the request
// This is only active when mode is "filter".
func (p *ContextLengthAware) Filter(ctx context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) []scheduling.Endpoint {
	if !p.enableFiltering {
		return endpoints // pass through if not in filter mode
	}

	logger := log.FromContext(ctx).V(logutil.DEBUG).WithName("ContextLengthAware.Filter")
	contextLength, usedTokenizer := p.getContextLength(ctx, request)
	logger.V(logutil.TRACE).Info("Filtering endpoints by context length", "contextLength", contextLength, "usedTokenizer", usedTokenizer)

	var filteredEndpoints []scheduling.Endpoint

	for _, endpoint := range endpoints {
		metadata := endpoint.GetMetadata()
		if metadata == nil {
			continue
		}

		rangeStr, hasLabel := metadata.Labels[p.labelName]
		if !hasLabel {
			// Endpoints without the label are included (they accept any context length)
			filteredEndpoints = append(filteredEndpoints, endpoint)
			continue
		}

		ranges, err := parseContextRanges(rangeStr)
		if err != nil {
			logger.Error(err, "Failed to parse context range label", "endpoint", metadata.NamespacedName, "rangeStr", rangeStr)
			continue
		}

		// Check if any range matches
		if matchesAnyRange(contextLength, ranges) {
			filteredEndpoints = append(filteredEndpoints, endpoint)
		}
	}

	logger.V(logutil.TRACE).Info("Filtered endpoints", "originalCount", len(endpoints),
		"filteredCount", len(filteredEndpoints))
	return filteredEndpoints
}

// Score scores endpoints based on how well their context length ranges match the request
// Endpoints with tighter/more specific ranges matching the request get higher scores.
func (p *ContextLengthAware) Score(ctx context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	logger := log.FromContext(ctx).V(logutil.DEBUG).WithName("ContextLengthAware.Score")
	contextLength, usedTokenizer := p.getContextLength(ctx, request)
	logger.V(logutil.TRACE).Info("Scoring endpoints by context length", "contextLength", contextLength, "usedTokenizer", usedTokenizer)

	scoredEndpoints := make(map[scheduling.Endpoint]float64)

	for _, endpoint := range endpoints {
		metadata := endpoint.GetMetadata()
		if metadata == nil {
			scoredEndpoints[endpoint] = 0.5
			continue
		}

		rangeStr, hasLabel := metadata.Labels[p.labelName]
		if !hasLabel {
			// Endpoints without the label get a neutral score
			scoredEndpoints[endpoint] = 0.5
			continue
		}

		ranges, err := parseContextRanges(rangeStr)
		if err != nil {
			logger.Error(err, "Failed to parse context range label", "endpoint", metadata.NamespacedName, "rangeStr", rangeStr)
			scoredEndpoints[endpoint] = 0.0
			continue
		}

		// Find the best matching range and calculate score
		score := calculateRangeScore(contextLength, ranges)
		scoredEndpoints[endpoint] = score
	}

	logger.V(logutil.TRACE).Info("Scored endpoints", "scores", scoredEndpoints)
	return scoredEndpoints
}

// Category returns the preference the scorer applies when scoring candidate endpoints.
func (p *ContextLengthAware) Category() scheduling.ScorerCategory {
	return scheduling.Affinity
}

// getContextLength returns the context length (token count) for the request.
// It uses precise tokenization if a tokenizer is configured, otherwise falls back to estimation.
// Returns the token count and a boolean indicating whether precise tokenization was used.
func (p *ContextLengthAware) getContextLength(ctx context.Context, request *scheduling.LLMRequest) (int, bool) {
	if request == nil || request.Body == nil {
		return 0, false
	}

	// If tokenizer is available, try to use precise tokenization
	if p.tokenizer != nil {
		tokenCount, err := p.computeTokenCount(request)
		if err == nil {
			return tokenCount, true
		}
		// Log the error and fall back to estimation
		logger := log.FromContext(ctx).V(logutil.DEBUG).WithName("ContextLengthAware")
		logger.Error(err, "Failed to tokenize request, falling back to character-based estimation")
	}

	// Fall back to character-based estimation
	return estimateContextLength(request), false
}

// computeTokenCount uses the tokenizer to compute the exact token count for the request.
func (p *ContextLengthAware) computeTokenCount(request *scheduling.LLMRequest) (int, error) {
	if request == nil || request.Body == nil {
		return 0, nil
	}

	var text string
	addSpecialToken := true

	// Handle chat completions
	if request.Body.ChatCompletions != nil {
		// Convert messages to conversation format
		conversations := make([]preprocessing.Conversation, len(request.Body.ChatCompletions.Messages))
		for i, msg := range request.Body.ChatCompletions.Messages {
			conversations[i] = preprocessing.Conversation{
				Role:    msg.Role,
				Content: msg.Content.Raw,
			}
		}

		renderReq := &preprocessing.ApplyChatTemplateRequest{
			Conversation:              [][]preprocessing.Conversation{conversations},
			Tools:                     request.Body.ChatCompletions.Tools,
			Documents:                 request.Body.ChatCompletions.Documents,
			ChatTemplate:              request.Body.ChatCompletions.ChatTemplate,
			ReturnAssistantTokensMask: request.Body.ChatCompletions.ReturnAssistantTokensMask,
			ContinueFinalMessage:      request.Body.ChatCompletions.ContinueFinalMessage,
			AddGenerationPrompt:       request.Body.ChatCompletions.AddGenerationPrompt,
			ChatTemplateKWArgs:        request.Body.ChatCompletions.ChatTemplateKWArgs,
		}

		var err error
		text, err = p.tokenizer.ApplyChatTemplate(p.modelName, renderReq)
		if err != nil {
			return 0, fmt.Errorf("failed to apply chat template for model %q: %w", p.modelName, err)
		}
		// https://github.com/vllm-project/vllm/blob/v0.11.2/vllm/entrypoints/openai/protocol.py#L613
		addSpecialToken = false
	}

	// Handle regular completions
	if request.Body.Completions != nil {
		text = request.Body.Completions.Prompt
	}

	if text == "" {
		return 0, nil
	}

	// Tokenize and count using the pre-initialized tokenizer
	tokens, _, err := p.tokenizer.Encode(text, p.modelName, addSpecialToken)
	if err != nil {
		return 0, fmt.Errorf("failed to encode text for model %q: %w", p.modelName, err)
	}

	return len(tokens), nil
}

// estimateContextLength estimates the context length from the request using character count.
// This is a fallback when no tokenizer is configured.
func estimateContextLength(request *scheduling.LLMRequest) int {
	if request == nil || request.Body == nil {
		return 0
	}

	totalChars := 0

	// Handle chat completions
	if request.Body.ChatCompletions != nil {
		for _, msg := range request.Body.ChatCompletions.Messages {
			totalChars += len(msg.Content.Raw)
		}
	}

	// Handle regular completions
	if request.Body.Completions != nil {
		totalChars += len(request.Body.Completions.Prompt)
	}

	// Convert characters to approximate token count
	estimatedTokens := int(float64(totalChars) * charToTokenMultiplier)
	return estimatedTokens
}

// parseContextRanges parses a label value into context ranges
// Expected format: "min-max" or "min-max,min-max,..."
// Examples: "0-2048", "2048-8192", "0-2048,8192-16384"
func parseContextRanges(rangeStr string) ([]contextRange, error) {
	if rangeStr == "" {
		return nil, fmt.Errorf("empty range string")
	}

	parts := strings.Split(rangeStr, ",")
	ranges := make([]contextRange, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		bounds := strings.Split(part, "-")
		if len(bounds) != 2 {
			return nil, fmt.Errorf("invalid range format: %s (expected 'min-max')", part)
		}

		minVal, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid min value: %s", bounds[0])
		}

		maxVal, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid max value: %s", bounds[1])
		}

		if minVal < 0 || maxVal < 0 {
			return nil, fmt.Errorf("negative values not allowed: min=%d, max=%d", minVal, maxVal)
		}

		if minVal > maxVal {
			return nil, fmt.Errorf("min (%d) cannot be greater than max (%d)", minVal, maxVal)
		}

		ranges = append(ranges, contextRange{min: minVal, max: maxVal})
	}

	return ranges, nil
}

// matchesAnyRange checks if the context length falls within any of the given ranges
func matchesAnyRange(contextLength int, ranges []contextRange) bool {
	for _, r := range ranges {
		if contextLength >= r.min && contextLength <= r.max {
			return true
		}
	}
	return false
}

// calculateRangeScore calculates a score based on how well the ranges match the context length
// Higher scores for:
// - Exact range matches
// - Tighter ranges (smaller width)
// Lower scores for:
// - No match
// - Very wide ranges
func calculateRangeScore(contextLength int, ranges []contextRange) float64 {
	var bestScore float64 = 0.0

	for _, r := range ranges {
		// Check if context length is within this range
		if contextLength >= r.min && contextLength <= r.max {
			rangeWidth := r.max - r.min
			if rangeWidth == 0 {
				// Exact match (degenerate range)
				return 1.0
			}

			// Score based on how tight the range is
			// Normalize by position within range and range width
			// Tighter ranges get higher scores (up to 1.0)
			// Wider ranges get lower scores (approaching 0.5)

			// Calculate range width score (narrower is better)
			// Use log scale to handle very large ranges
			widthScore := 1.0 / (1.0 + float64(rangeWidth)/10000.0)

			// Score based on distance from maximum (more headroom = better)
			headroom := float64(r.max - contextLength)
			positionScore := headroom / float64(rangeWidth)

			// Combine scores (width is more important than position)
			score := 0.7*widthScore + 0.3*positionScore

			if score > bestScore {
				bestScore = score
			}
		}
	}

	return bestScore
}
