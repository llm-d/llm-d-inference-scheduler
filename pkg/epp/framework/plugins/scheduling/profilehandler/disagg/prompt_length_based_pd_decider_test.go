package disagg

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func TestPromptLengthBasedPDDeciderConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		config    PromptLengthBasedPDDeciderConfig
		expectErr bool
	}{
		{
			name:      "zero is valid",
			config:    PromptLengthBasedPDDeciderConfig{PromptTokens: 0},
			expectErr: false,
		},
		{
			name:      "positive is valid",
			config:    PromptLengthBasedPDDeciderConfig{PromptTokens: 100},
			expectErr: false,
		},
		{
			name:      "negative is invalid",
			config:    PromptLengthBasedPDDeciderConfig{PromptTokens: -1},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPromptLengthBasedPDDecider(tt.config)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPromptLengthBasedPDDeciderFactory(t *testing.T) {
	tests := []struct {
		name               string
		pluginName         string
		rawParams          string
		expectErr          bool
		expectPromptTokens int
		expectPluginName   string
	}{
		{
			name:               "default parameters (nil)",
			pluginName:         "my-decider",
			rawParams:          "",
			expectErr:          false,
			expectPromptTokens: 0,
			expectPluginName:   "my-decider",
		},
		{
			name:               "custom promptTokens",
			pluginName:         "custom-decider",
			rawParams:          `{"promptTokens": 50}`,
			expectErr:          false,
			expectPromptTokens: 50,
			expectPluginName:   "custom-decider",
		},
		{
			name:       "negative promptTokens",
			pluginName: "bad-decider",
			rawParams:  `{"promptTokens": -5}`,
			expectErr:  true,
		},
		{
			name:       "invalid json",
			pluginName: "bad-json",
			rawParams:  `{invalid}`,
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.rawParams != "" {
				raw = json.RawMessage(tt.rawParams)
			}

			p, err := PromptLengthBasedPDDeciderPluginFactory(tt.pluginName, raw, nil)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, p)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, p)

			decider, ok := p.(*PromptLengthBasedPDDecider)
			require.True(t, ok)
			assert.Equal(t, tt.expectPluginName, decider.TypedName().Name)
			assert.Equal(t, tt.expectPromptTokens, decider.config.PromptTokens)
		})
	}
}

func TestPromptLengthBasedPDDeciderDisaggregate(t *testing.T) {
	ctx := utils.NewTestContext(t)

	tests := []struct {
		name               string
		promptTokens       int
		request            *scheduling.InferenceRequest
		expectDisaggregate bool
	}{
		{
			name:               "threshold zero disables disaggregation",
			promptTokens:       0,
			request:            makeRequestWithTokens(10),
			expectDisaggregate: false,
		},
		{
			name:               "input shorter than threshold",
			promptTokens:       20,
			request:            makeRequestWithTokens(10),
			expectDisaggregate: false,
		},
		{
			name:               "input equals threshold",
			promptTokens:       10,
			request:            makeRequestWithTokens(10),
			expectDisaggregate: true,
		},
		{
			name:               "input exceeds threshold",
			promptTokens:       5,
			request:            makeRequestWithTokens(10),
			expectDisaggregate: true,
		},
		{
			name:               "nil request returns false",
			promptTokens:       5,
			request:            nil,
			expectDisaggregate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider, err := NewPromptLengthBasedPDDecider(PromptLengthBasedPDDeciderConfig{PromptTokens: tt.promptTokens})
			require.NoError(t, err)

			result := decider.disaggregate(ctx, tt.request, nil)
			assert.Equal(t, tt.expectDisaggregate, result)
		})
	}
}

func TestPromptLengthBasedPDDeciderWithName(t *testing.T) {
	decider, err := NewPromptLengthBasedPDDecider(PromptLengthBasedPDDeciderConfig{PromptTokens: 0})
	require.NoError(t, err)

	decider.WithName("my-decider")
	assert.Equal(t, "my-decider", decider.TypedName().Name)

	decider.WithName("renamed")
	assert.Equal(t, "renamed", decider.TypedName().Name)
}
