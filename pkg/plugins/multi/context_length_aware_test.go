package multi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/preparedata"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

// Helper functions

func createEndpoint(nsn k8stypes.NamespacedName, ipaddr string, labels map[string]string) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: nsn,
			Address:        ipaddr,
			Labels:         labels,
		},
		nil,
		nil,
	)
}

func createRequest() *scheduling.LLMRequest {
	return &scheduling.LLMRequest{
		RequestId: "test-request",
	}
}

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
			name:       "empty label should error",
			pluginName: "empty-label",
			jsonParams: `{"label": ""}`,
			expectErr:  true,
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

func TestContextLengthAwareFilter(t *testing.T) {
	ctx := utils.NewTestContext(t)

	endpoints := []scheduling.Endpoint{
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "short-range"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-100"}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "medium-range"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "100-500"}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "multi-range"},
			"10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "0-100,500-2000"}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "no-label"},
			"10.0.0.4",
			map[string]string{}),
	}

	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
	}
	plugin := NewContextLengthAware("test-filter", params)
	request := createRequest()

	// With empty request body, context length is 0, matches 0-100 range
	filteredEndpoints := plugin.Filter(ctx, nil, request, endpoints)

	gotNames := make([]string, len(filteredEndpoints))
	for i, endpoint := range filteredEndpoints {
		gotNames[i] = endpoint.GetMetadata().NamespacedName.Name
	}

	expectedEndpoints := []string{"short-range", "multi-range", "no-label"}
	assert.ElementsMatch(t, expectedEndpoints, gotNames)
}

func TestContextLengthAwareScore(t *testing.T) {
	ctx := utils.NewTestContext(t)

	endpoints := []scheduling.Endpoint{
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "tight-range"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-20"}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "wide-range"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "0-10000"}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "no-match"},
			"10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "500-1000"}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "no-label"},
			"10.0.0.4",
			map[string]string{}),
	}

	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: false,
	}
	plugin := NewContextLengthAware("test-scorer", params)
	request := createRequest()

	scores := plugin.Score(ctx, nil, request, endpoints)

	// With context length 0:
	// - tight-range (0-20): should score high (in-range match)
	// - wide-range (0-10000): should score lower than tight (in-range but wide)
	// - no-match (500-1000): out-of-range fallback, scored by proximity (0 < score <= 0.3)
	// - no-label: should score 0.5 (neutral)
	assert.Greater(t, scores[endpoints[0]], scores[endpoints[1]], "tight range should score higher than wide range")
	assert.Greater(t, scores[endpoints[2]], 0.0, "out-of-range should get a fallback score > 0")
	assert.LessOrEqual(t, scores[endpoints[2]], 0.3, "out-of-range fallback should not exceed 0.3")
	assert.Greater(t, scores[endpoints[1]], scores[endpoints[2]], "in-range match should outscore out-of-range fallback")
	assert.Equal(t, 0.5, scores[endpoints[3]], "no label should score 0.5")
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
			rangeStr: "0-100,500-2000",
			expected: []contextRange{{min: 0, max: 100}, {min: 500, max: 2000}},
		},
		{
			name:      "empty string",
			rangeStr:  "",
			expectErr: true,
		},
		{
			name:      "invalid format",
			rangeStr:  "0-100-200",
			expectErr: true,
		},
		{
			name:      "min greater than max",
			rangeStr:  "100-50",
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

func TestCalculateRangeScoreFallback(t *testing.T) {
	tests := []struct {
		name          string
		contextLength int
		ranges        []contextRange
		wantHigher    string // which range index should score higher, described
	}{
		{
			name:          "exceeds all ranges — prefer largest max",
			contextLength: 9000,
			ranges: []contextRange{
				{min: 0, max: 2048}, // index 0
				{min: 0, max: 8192}, // index 1 — closer to 9000
			},
		},
		{
			name:          "below all ranges — prefer smallest min",
			contextLength: 50,
			ranges: []contextRange{
				{min: 500, max: 2048}, // index 0 — min=500, distance=450
				{min: 100, max: 1024}, // index 1 — min=100, distance=50 (closer)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scores := make([]float64, len(tt.ranges))
			for i, r := range tt.ranges {
				scores[i] = calculateRangeScore(tt.contextLength, []contextRange{r})
			}

			// All fallback scores must be in (0, 0.3]
			for i, s := range scores {
				assert.Greater(t, s, 0.0, "range %d should have positive fallback score", i)
				assert.LessOrEqual(t, s, 0.3, "range %d fallback should not exceed 0.3", i)
			}

			if tt.name == "exceeds all ranges — prefer largest max" {
				// index 1 (max=8192) should score higher than index 0 (max=2048)
				assert.Greater(t, scores[1], scores[0], "pod with larger max should score higher")
			}
			if tt.name == "below all ranges — prefer smallest min" {
				// index 1 (min=100) should score higher than index 0 (min=500)
				assert.Greater(t, scores[1], scores[0], "pod with smaller min should score higher")
			}
		})
	}
}

func TestContextLengthAwareConsumes(t *testing.T) {
	params := &contextLengthAwareParams{Label: DefaultContextLengthLabel}
	plugin := NewContextLengthAware("test-consumes", params)

	consumed := plugin.Consumes()
	require.NotNil(t, consumed)
	assert.Contains(t, consumed, preparedata.TokenizedPromptKey)
	assert.IsType(t, scheduling.TokenizedPrompt{}, consumed[preparedata.TokenizedPromptKey])
}

// TokenizedPrompt tests — plugin consumes tokens from the tokenizer PrepareData plugin

func TestContextLengthAwareWithTokenizedPrompt(t *testing.T) {
	ctx := utils.NewTestContext(t)

	tokenCount := 42

	endpoints := []scheduling.Endpoint{
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "tight-match"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: fmt.Sprintf("0-%d", tokenCount+10)}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "no-match"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: fmt.Sprintf("%d-%d", tokenCount+100, tokenCount+200)}),
	}

	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
	}
	plugin := NewContextLengthAware("test-tokenized", params)

	// Simulate tokenizer PrepareData plugin having set TokenizedPrompt
	tokenIDs := make([]uint32, tokenCount)
	for i := range tokenIDs {
		tokenIDs[i] = uint32(i + 1)
	}

	request := &scheduling.LLMRequest{
		RequestId:   "test-request",
		TargetModel: "test-model",
		TokenizedPrompt: &scheduling.TokenizedPrompt{
			TokenIDs: tokenIDs,
		},
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: "some prompt text",
			},
		},
	}

	filteredEndpoints := plugin.Filter(ctx, nil, request, endpoints)
	assert.Equal(t, 1, len(filteredEndpoints))
	assert.Equal(t, "tight-match", filteredEndpoints[0].GetMetadata().NamespacedName.Name)
}

func TestContextLengthAwareFallbackWithoutTokenizedPrompt(t *testing.T) {
	ctx := utils.NewTestContext(t)

	// Without TokenizedPrompt, falls back to char estimation (len * 0.25)
	prompt := "Hello, how are you?" // 19 chars => ~4 tokens estimated

	endpoints := []scheduling.Endpoint{
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "matching-range"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-50"}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "non-matching-range"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "100-200"}),
	}

	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
	}
	plugin := NewContextLengthAware("test-fallback", params)

	request := &scheduling.LLMRequest{
		RequestId:   "test-request",
		TargetModel: "test-model",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: prompt,
			},
		},
	}

	filteredEndpoints := plugin.Filter(ctx, nil, request, endpoints)
	assert.Equal(t, 1, len(filteredEndpoints))
	assert.Equal(t, "matching-range", filteredEndpoints[0].GetMetadata().NamespacedName.Name)
}
