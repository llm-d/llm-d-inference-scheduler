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
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "wide-range"},
			"10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "0-2000"}),
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

	// With empty request body, context length is 0, matches 0-100 and 0-2000 ranges
	filteredEndpoints := plugin.Filter(ctx, nil, request, endpoints)

	gotNames := make([]string, len(filteredEndpoints))
	for i, endpoint := range filteredEndpoints {
		gotNames[i] = endpoint.GetMetadata().NamespacedName.Name
	}

	expectedEndpoints := []string{"short-range", "wide-range", "no-label"}
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

func TestParseContextRange(t *testing.T) {
	tests := []struct {
		name      string
		rangeStr  string
		expected  contextRange
		expectErr bool
	}{
		{
			name:     "valid range",
			rangeStr: "0-100",
			expected: contextRange{min: 0, max: 100},
		},
		{
			name:      "empty string",
			rangeStr:  "",
			expectErr: true,
		},
		{
			name:      "invalid format with three parts",
			rangeStr:  "0-100-200",
			expectErr: true,
		},
		{
			name:      "min greater than max",
			rangeStr:  "100-50",
			expectErr: true,
		},
		{
			name:      "non-numeric value",
			rangeStr:  "abc-100",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := parseContextRange(tt.rangeStr)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, r)
			}
		})
	}
}

func TestCalculateRangeScoreFallback(t *testing.T) {
	t.Run("exceeds range — prefer largest max", func(t *testing.T) {
		smallMax := calculateRangeScore(9000, contextRange{min: 0, max: 2048})
		largeMax := calculateRangeScore(9000, contextRange{min: 0, max: 8192})

		assert.Greater(t, smallMax, 0.0)
		assert.LessOrEqual(t, smallMax, 0.3)
		assert.Greater(t, largeMax, 0.0)
		assert.LessOrEqual(t, largeMax, 0.3)
		assert.Greater(t, largeMax, smallMax, "pod with larger max should score higher")
	})

	t.Run("below range — prefer smallest min", func(t *testing.T) {
		farMin := calculateRangeScore(50, contextRange{min: 500, max: 2048})
		closeMin := calculateRangeScore(50, contextRange{min: 100, max: 1024})

		assert.Greater(t, farMin, 0.0)
		assert.LessOrEqual(t, farMin, 0.3)
		assert.Greater(t, closeMin, 0.0)
		assert.LessOrEqual(t, closeMin, 0.3)
		assert.Greater(t, closeMin, farMin, "pod with smaller min should score higher")
	})
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
