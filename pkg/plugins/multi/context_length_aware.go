package multi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// ContextLengthAwareType is the type of the ContextLengthAware plugin
	ContextLengthAwareType = "context-length-aware"

	// DefaultContextLengthLabel is the default label name used to identify context length ranges on pods
	DefaultContextLengthLabel = "llm-d.ai/context-length-range"

	// charToTokenMultiplier defines the multiplier to convert characters to tokens
	// This is an approximate value and may vary based on the tokenizer used
	charToTokenMultiplier = 0.25
)

type contextLengthAwareParams struct {
	// Label is the pod label name to check for context length ranges
	// Format expected: "min-max" (e.g., "0-2048" or "2048-8192")
	// Multiple ranges can be specified with comma separation (e.g., "0-2048,8192-16384")
	Label string `json:"label"`

	// Mode determines the behavior: "filter" or "score"
	// - "filter": filters out pods that don't match the request's context length
	// - "score": scores all pods, giving higher scores to better matches
	Mode string `json:"mode"`
}

// contextRange represents a single context length range
type contextRange struct {
	min int
	max int
}

var _ framework.Filter = &ContextLengthAware{} // validate interface conformance
var _ framework.Scorer = &ContextLengthAware{} // validate interface conformance

// ContextLengthAwareFactory defines the factory function for the ContextLengthAware plugin.
func ContextLengthAwareFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := contextLengthAwareParams{
		Label: DefaultContextLengthLabel,
		Mode:  "score", // default to scoring mode
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' plugin - %w", ContextLengthAwareType, err)
		}
	}

	if name == "" {
		return nil, fmt.Errorf("invalid configuration for '%s' plugin: name cannot be empty", ContextLengthAwareType)
	}

	if parameters.Label == "" {
		return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'label' must be specified", ContextLengthAwareType)
	}

	if parameters.Mode != "filter" && parameters.Mode != "score" {
		return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'mode' must be either 'filter' or 'score'", ContextLengthAwareType)
	}

	return NewContextLengthAware(name, parameters.Label, parameters.Mode), nil
}

// NewContextLengthAware creates and returns an instance of the ContextLengthAware plugin
// name - the plugin name
// labelName - the name of the label to check for context length ranges
// mode - either "filter" or "score"
func NewContextLengthAware(name string, labelName string, mode string) *ContextLengthAware {
	return &ContextLengthAware{
		typedName: plugins.TypedName{Type: ContextLengthAwareType, Name: name},
		labelName: labelName,
		mode:      mode,
	}
}

// ContextLengthAware is a plugin that filters or scores pods based on their association
// with input context length groups.
// It checks for a specific label on pods and either:
// - filters out pods from a group that does not match the input context length
// - scores pods higher if they belong to a group that matches the input context length
//   - if a pod belongs to multiple groups, the one with tighter boundaries to the input context length is preferred
type ContextLengthAware struct {
	// typedName defines the plugin typed name
	typedName plugins.TypedName
	// labelName defines the name of the label to be checked
	labelName string
	// mode determines whether to filter or score
	mode string
}

// TypedName returns the typed name of the plugin.
func (p *ContextLengthAware) TypedName() plugins.TypedName {
	return p.typedName
}

// WithName sets the name of the plugin.
func (p *ContextLengthAware) WithName(name string) *ContextLengthAware {
	p.typedName.Name = name
	return p
}

// Filter filters out pods that don't have a context length range matching the request
// This is only active when mode is "filter".
func (p *ContextLengthAware) Filter(ctx context.Context, _ *types.CycleState, request *types.LLMRequest, pods []types.Pod) []types.Pod {
	if p.mode != "filter" {
		return pods // pass through if not in filter mode
	}

	contextLength := estimateContextLength(request)
	logger := log.FromContext(ctx).V(logutil.DEBUG).WithName("ContextLengthAware.Filter")
	logger.Info("Filtering pods by context length", "estimatedContextLength", contextLength)

	filteredPods := []types.Pod{}

	for _, pod := range pods {
		rangeStr, hasLabel := pod.GetPod().Labels[p.labelName]
		if !hasLabel {
			// Pods without the label are included (they accept any context length)
			filteredPods = append(filteredPods, pod)
			continue
		}

		ranges, err := parseContextRanges(rangeStr)
		if err != nil {
			logger.Error(err, "Failed to parse context range label", "pod", pod.GetPod().NamespacedName, "rangeStr", rangeStr)
			continue
		}

		// Check if any range matches
		if matchesAnyRange(contextLength, ranges) {
			filteredPods = append(filteredPods, pod)
		}
	}

	logger.Info("Filtered pods", "originalCount", len(pods), "filteredCount", len(filteredPods))
	return filteredPods
}

// Score scores pods based on how well their context length ranges match the request
// Pods with tighter/more specific ranges matching the request get higher scores.
func (p *ContextLengthAware) Score(ctx context.Context, _ *types.CycleState, request *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
	contextLength := estimateContextLength(request)
	logger := log.FromContext(ctx).V(logutil.DEBUG).WithName("ContextLengthAware.Score")
	logger.Info("Scoring pods by context length", "estimatedContextLength", contextLength)

	scoredPods := make(map[types.Pod]float64)

	for _, pod := range pods {
		rangeStr, hasLabel := pod.GetPod().Labels[p.labelName]
		if !hasLabel {
			// Pods without the label get a neutral score
			scoredPods[pod] = 0.5
			continue
		}

		ranges, err := parseContextRanges(rangeStr)
		if err != nil {
			logger.Error(err, "Failed to parse context range label", "pod", pod.GetPod().NamespacedName, "rangeStr", rangeStr)
			scoredPods[pod] = 0.0
			continue
		}

		// Find the best matching range and calculate score
		score := calculateRangeScore(contextLength, ranges)
		scoredPods[pod] = score
	}

	logger.Info("Scored pods", "scores", scoredPods)
	return scoredPods
}

// estimateContextLength estimates the context length from the request
// It uses the body content to estimate token count
func estimateContextLength(request *types.LLMRequest) int {
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

			// Calculate position score (closer to middle is better)
			rangeMiddle := (r.min + r.max) / 2
			distanceFromMiddle := abs(contextLength - rangeMiddle)
			positionScore := 1.0 - (float64(distanceFromMiddle) / float64(rangeWidth/2.0))

			// Combine scores (width is more important than position)
			score := 0.7*widthScore + 0.3*positionScore

			if score > bestScore {
				bestScore = score
			}
		}
	}

	return bestScore
}

// abs returns the absolute value of an integer
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
