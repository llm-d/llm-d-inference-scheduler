package multi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/preparedata"
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
}

// contextRange represents a single context length range.
type contextRange struct {
	min int
	max int
}

var _ scheduling.Filter = &ContextLengthAware{}     // validate interface conformance
var _ scheduling.Scorer = &ContextLengthAware{}     // validate interface conformance
var _ plugin.ConsumerPlugin = &ContextLengthAware{} // validate interface conformance

// ContextLengthAwareFactory defines the factory function for the ContextLengthAware plugin.
func ContextLengthAwareFactory(name string, rawParameters json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
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

	return NewContextLengthAware(name, parameters), nil
}

// NewContextLengthAware creates and returns an instance of the ContextLengthAware plugin.
func NewContextLengthAware(name string, params *contextLengthAwareParams) *ContextLengthAware {
	return &ContextLengthAware{
		typedName:       plugin.TypedName{Type: ContextLengthAwareType, Name: name},
		labelName:       params.Label,
		enableFiltering: params.EnableFiltering,
	}
}

// ContextLengthAware is a plugin that filters or scores endpoints based on their association
// with input context length groups.
// It checks for a specific label on endpoints that defines the context length ranges they support.
// If filtering is enabled, endpoints that don't support the request's context length are filtered out.
// Additionally, it scores endpoints based on how well their context length ranges match the request.
//
// For precise token counting, this plugin consumes TokenizedPrompt from the request, which is
// produced by the tokenizer PrepareData plugin. When TokenizedPrompt is not available (tokenizer
// plugin not configured), it falls back to character-based estimation.
type ContextLengthAware struct {
	// typedName defines the plugin typed name
	typedName plugin.TypedName
	// labelName defines the name of the label to be checked
	labelName string
	// enableFiltering indicates whether filtering is enabled
	enableFiltering bool
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

// Consumes declares that this plugin reads TokenizedPrompt from the request,
// as produced by the tokenizer PrepareData plugin.
func (p *ContextLengthAware) Consumes() map[string]any {
	return map[string]any{preparedata.TokenizedPromptKey: scheduling.TokenizedPrompt{}}
}

// getContextLength returns the context length (token count) for the request.
// It uses the pre-computed TokenizedPrompt (set by the tokenizer PrepareData plugin) if available,
// otherwise falls back to character-based estimation.
// Returns the token count and a boolean indicating whether precise tokenization was used.
func (p *ContextLengthAware) getContextLength(ctx context.Context, request *scheduling.LLMRequest) (int, bool) {
	if request == nil || request.Body == nil {
		return 0, false
	}

	// Use pre-computed tokens from the tokenizer PrepareData plugin
	if request.TokenizedPrompt != nil && len(request.TokenizedPrompt.TokenIDs) > 0 {
		return len(request.TokenizedPrompt.TokenIDs), true
	}

	logger := log.FromContext(ctx).V(logutil.DEBUG).WithName("ContextLengthAware")
	logger.Info("TokenizedPrompt not available, falling back to character-based estimation")

	// Fall back to character-based estimation
	return estimateContextLength(request), false
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
