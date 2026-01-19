package multi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

// Helper functions

func createPod(nsn k8stypes.NamespacedName, ipaddr string, labels map[string]string) types.Pod {
	return &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: nsn,
			Address:        ipaddr,
			Labels:         labels,
		},
		MetricsState: &backendmetrics.MetricsState{},
	}
}

func createRequest() *types.LLMRequest {
	return &types.LLMRequest{
		RequestId: "test-request",
	}
}

// Factory tests

func TestContextLengthAwareFactory(t *testing.T) {
	tests := []struct {
		name       string
		pluginName string
		jsonParams string
		expectErr  bool
	}{
		{
			name:       "valid configuration with defaults",
			pluginName: "ctx-aware",
			jsonParams: `{}`,
			expectErr:  false,
		},
		{
			name:       "valid configuration with custom label",
			pluginName: "custom-ctx",
			jsonParams: `{
				"label": "custom-label",
				"mode": "filter"
			}`,
			expectErr: false,
		},
		{
			name:       "valid configuration with score mode",
			pluginName: "score-mode",
			jsonParams: `{
				"label": "llm-d.ai/context-length-range",
				"mode": "score"
			}`,
			expectErr: false,
		},
		{
			name:       "empty plugin name should error",
			pluginName: "",
			jsonParams: `{}`,
			expectErr:  true,
		},
		{
			name:       "empty label should error",
			pluginName: "empty-label",
			jsonParams: `{
				"label": "",
				"mode": "filter"
			}`,
			expectErr: true,
		},
		{
			name:       "invalid mode should error",
			pluginName: "invalid-mode",
			jsonParams: `{
				"label": "test-label",
				"mode": "invalid"
			}`,
			expectErr: true,
		},
		{
			name:       "malformed JSON should error",
			pluginName: "malformed",
			jsonParams: `{"label": "test"`,
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rawParams json.RawMessage
			if tt.jsonParams != "" {
				rawParams = json.RawMessage(tt.jsonParams)
			}
			plugin, err := ContextLengthAwareFactory(tt.pluginName, rawParams, nil)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, plugin)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, plugin)
			}
		})
	}
}

// Filter and Score tests

func TestContextLengthAwareFilter(t *testing.T) {
	ctx := utils.NewTestContext(t)

	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "short-range"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-100"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "medium-range"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "100-500"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "long-range"},
			"10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "500-2000"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "multi-range"},
			"10.0.0.4",
			map[string]string{DefaultContextLengthLabel: "0-100,500-2000"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "no-label"},
			"10.0.0.5",
			map[string]string{}),
	}

	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
	}
	plugin := NewContextLengthAware("test-filter", params)
	request := createRequest()

	// With empty request body, context length is 0, matches 0-100 range
	filteredPods := plugin.Filter(ctx, nil, request, pods)

	gotNames := make([]string, len(filteredPods))
	for i, pod := range filteredPods {
		gotNames[i] = pod.GetPod().NamespacedName.Name
	}

	expectedPods := []string{"short-range", "multi-range", "no-label"}
	assert.ElementsMatch(t, expectedPods, gotNames)
}

func TestContextLengthAwareScore(t *testing.T) {
	ctx := utils.NewTestContext(t)

	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "tight-range"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-20"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "wide-range"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "0-10000"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "no-match"},
			"10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "500-1000"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "no-label"},
			"10.0.0.4",
			map[string]string{}),
	}

	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: false,
	}
	plugin := NewContextLengthAware("test-scorer", params)
	request := createRequest()

	scores := plugin.Score(ctx, nil, request, pods)

	// With context length 0:
	// - tight-range (0-20): should score high (tight match)
	// - wide-range (0-10000): should score lower (wide match)
	// - no-match (500-1000): should score 0 (no match)
	// - no-label: should score 0.5 (neutral)

	assert.GreaterOrEqual(t, scores[pods[0]], 0.69, "tight range should score high")
	assert.Greater(t, scores[pods[1]], 0.0, "wide range should score > 0")
	assert.Less(t, scores[pods[1]], 0.5, "wide range should score < 0.5")
	assert.Equal(t, 0.0, scores[pods[2]], "no match should score 0")
	assert.Equal(t, 0.5, scores[pods[3]], "no label should score 0.5")
}

// Helper function tests

func TestEstimateContextLength(t *testing.T) {
	tests := []struct {
		name     string
		request  *types.LLMRequest
		expected int
	}{
		{
			name:     "nil request",
			request:  nil,
			expected: 0,
		},
		{
			name:     "empty request",
			request:  &types.LLMRequest{},
			expected: 0,
		},
		{
			name:     "request without body",
			request:  createRequest(),
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := estimateContextLength(tt.request)
			assert.Equal(t, tt.expected, tokens)
		})
	}
}

func TestParseContextRanges(t *testing.T) {
	tests := []struct {
		name      string
		rangeStr  string
		expected  []contextRange
		expectErr bool
	}{
		{
			name:     "single range",
			rangeStr: "0-100",
			expected: []contextRange{{min: 0, max: 100}},
		},
		{
			name:     "multiple ranges",
			rangeStr: "0-100,100-500,500-2000",
			expected: []contextRange{
				{min: 0, max: 100},
				{min: 100, max: 500},
				{min: 500, max: 2000},
			},
		},
		{
			name:     "ranges with spaces",
			rangeStr: "0 - 100, 500 - 1000",
			expected: []contextRange{
				{min: 0, max: 100},
				{min: 500, max: 1000},
			},
		},
		{
			name:      "empty string",
			rangeStr:  "",
			expectErr: true,
		},
		{
			name:      "invalid format - no dash",
			rangeStr:  "0,100",
			expectErr: true,
		},
		{
			name:      "invalid format - too many parts",
			rangeStr:  "0-100-200",
			expectErr: true,
		},
		{
			name:      "negative values",
			rangeStr:  "-10-100",
			expectErr: true,
		},
		{
			name:      "min greater than max",
			rangeStr:  "100-50",
			expectErr: true,
		},
		{
			name:      "non-numeric values",
			rangeStr:  "zero-hundred",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranges, err := parseContextRanges(tt.rangeStr)

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, ranges)
			}
		})
	}
}

func TestMatchesAnyRange(t *testing.T) {
	ranges := []contextRange{
		{min: 0, max: 100},
		{min: 200, max: 300},
		{min: 500, max: 1000},
	}

	tests := []struct {
		contextLength int
		expected      bool
	}{
		{contextLength: 50, expected: true},
		{contextLength: 0, expected: true},
		{contextLength: 100, expected: true},
		{contextLength: 150, expected: false},
		{contextLength: 250, expected: true},
		{contextLength: 750, expected: true},
		{contextLength: 1001, expected: false},
		{contextLength: -1, expected: false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("length_%d", tt.contextLength), func(t *testing.T) {
			result := matchesAnyRange(tt.contextLength, ranges)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateRangeScore(t *testing.T) {
	tests := []struct {
		name          string
		contextLength int
		ranges        []contextRange
		minScore      float64
		maxScore      float64
	}{
		{
			name:          "exact match in tight range",
			contextLength: 10,
			ranges:        []contextRange{{min: 0, max: 20}},
			minScore:      0.7,
			maxScore:      1.0,
		},
		{
			name:          "match in wide range",
			contextLength: 100,
			ranges:        []contextRange{{min: 0, max: 10000}},
			minScore:      0.0,
			maxScore:      0.5,
		},
		{
			name:          "no match",
			contextLength: 100,
			ranges:        []contextRange{{min: 500, max: 1000}},
			minScore:      0.0,
			maxScore:      0.0,
		},
		{
			name:          "multiple ranges - picks best",
			contextLength: 10,
			ranges: []contextRange{
				{min: 0, max: 10000},
				{min: 0, max: 20},
			},
			minScore: 0.7,
			maxScore: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := calculateRangeScore(tt.contextLength, tt.ranges)
			assert.GreaterOrEqual(t, score, tt.minScore,
				"Score %f is less than expected minimum %f", score, tt.minScore)
			assert.LessOrEqual(t, score, tt.maxScore,
				"Score %f is greater than expected maximum %f", score, tt.maxScore)
		})
	}
}

func TestFilterPassthroughWhenNotInFilterMode(t *testing.T) {
	ctx := utils.NewTestContext(t)
	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: false,
	}
	plugin := NewContextLengthAware("test-scorer", params)

	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod1"}, "10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-100"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod2"}, "10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "500-1000"}),
	}

	request := createRequest()

	// In score mode, filter should pass through all pods
	filteredPods := plugin.Filter(ctx, nil, request, pods)
	assert.Equal(t, len(pods), len(filteredPods))
}

func TestInvalidRangeLabelsInFilter(t *testing.T) {
	ctx := utils.NewTestContext(t)
	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
	}
	plugin := NewContextLengthAware("test-filter", params)

	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "invalid-range"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "invalid-format"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "valid-range"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "0-100"}),
	}

	request := createRequest()

	// Pods with invalid range labels should be excluded
	filteredPods := plugin.Filter(ctx, nil, request, pods)
	assert.Equal(t, 1, len(filteredPods))
	assert.Equal(t, "valid-range", filteredPods[0].GetPod().NamespacedName.Name)
}

func TestInvalidRangeLabelsInScore(t *testing.T) {
	ctx := utils.NewTestContext(t)
	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: false,
	}
	plugin := NewContextLengthAware("test-scorer", params)

	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "invalid-range"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "invalid-format"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "valid-range"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "0-100"}),
	}

	request := createRequest()

	// Pods with invalid range labels should get score 0.0
	scores := plugin.Score(ctx, nil, request, pods)
	assert.Equal(t, 0.0, scores[pods[0]])
	assert.Greater(t, scores[pods[1]], 0.0)
}
