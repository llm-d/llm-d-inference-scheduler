package multi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	preprocessing "github.com/llm-d/llm-d-kv-cache/pkg/preprocessing/chat_completions"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

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
	plugin := NewContextLengthAware("test-filter", params, nil)
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
	plugin := NewContextLengthAware("test-scorer", params, nil)
	request := createRequest()

	scores := plugin.Score(ctx, nil, request, endpoints)

	// With context length 0:
	// - tight-range (0-20): should score high (tight match)
	// - wide-range (0-10000): should score lower than tight
	// - no-match (500-1000): should score 0
	// - no-label: should score 0.5 (neutral)
	assert.Greater(t, scores[endpoints[0]], scores[endpoints[1]], "tight range should score higher than wide range")
	assert.Equal(t, 0.0, scores[endpoints[2]], "no match should score 0")
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

// Tokenization tests

func getTestTokenizerConfig(t *testing.T) tokenization.LocalTokenizerConfig {
	d, err := os.Getwd()
	require.NoError(t, err)
	modelDir := filepath.Join(d, "testdata")
	return tokenization.LocalTokenizerConfig{
		ModelTokenizerMap: map[string]string{
			"test-model": filepath.Join(modelDir, "test-model/tokenizer.json"),
		},
	}
}

func TestContextLengthAwareWithTokenizer(t *testing.T) {
	ctx := utils.NewTestContext(t)

	localConfig := getTestTokenizerConfig(t)
	tokenizer, err := tokenization.NewCachedLocalTokenizer(ctx, "test-model", localConfig)
	require.NoError(t, err)

	prompt := "One morning, when Gregor Samsa woke from troubled dreams, " +
		"he found himself transformed in his bed into a horrible vermin."

	tokens, _, err := tokenizer.Encode(prompt, "test-model", true)
	require.NoError(t, err)
	actualTokenCount := len(tokens)

	endpoints := []scheduling.Endpoint{
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "tight-match"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: fmt.Sprintf("0-%d", actualTokenCount+10)}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "no-match"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: fmt.Sprintf("%d-%d", actualTokenCount+100, actualTokenCount+200)}),
	}

	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
		ModelName:       "test-model",
	}
	plugin := NewContextLengthAware("test-tokenizer", params, tokenizer)

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
	assert.Equal(t, "tight-match", filteredEndpoints[0].GetMetadata().NamespacedName.Name)
}

func TestContextLengthAwareWithChatCompletions(t *testing.T) {
	ctx := utils.NewTestContext(t)

	localConfig := getTestTokenizerConfig(t)
	tokenizer, err := tokenization.NewCachedLocalTokenizer(ctx, "test-model", localConfig)
	require.NoError(t, err)

	messages := []scheduling.Message{
		{Role: "system", Content: scheduling.Content{Raw: "You are a helpful assistant."}},
		{Role: "user", Content: scheduling.Content{Raw: "Hello, how are you?"}},
	}

	// Count tokens using ApplyChatTemplate (matching implementation behavior)
	conversations := make([]preprocessing.Conversation, len(messages))
	for i, msg := range messages {
		conversations[i] = preprocessing.Conversation{
			Role:    msg.Role,
			Content: msg.Content.Raw,
		}
	}
	renderReq := &preprocessing.ApplyChatTemplateRequest{
		Conversation: [][]preprocessing.Conversation{conversations},
	}
	renderedText, err := tokenizer.ApplyChatTemplate("test-model", renderReq)
	require.NoError(t, err)

	tokens, _, err := tokenizer.Encode(renderedText, "test-model", false)
	require.NoError(t, err)
	actualTokenCount := len(tokens)

	endpoints := []scheduling.Endpoint{
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "matching-range"},
			"10.0.0.1",
			map[string]string{DefaultContextLengthLabel: fmt.Sprintf("0-%d", actualTokenCount+50)}),
		createEndpoint(k8stypes.NamespacedName{Namespace: "default", Name: "non-matching-range"},
			"10.0.0.2",
			map[string]string{DefaultContextLengthLabel: fmt.Sprintf("%d-%d", actualTokenCount+100, actualTokenCount+200)}),
	}

	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
		ModelName:       "test-model",
	}
	plugin := NewContextLengthAware("test-chat-tokenizer", params, tokenizer)

	request := &scheduling.LLMRequest{
		RequestId:   "test-request",
		TargetModel: "test-model",
		Body: &scheduling.LLMRequestBody{
			ChatCompletions: &scheduling.ChatCompletionsRequest{
				Messages: messages,
			},
		},
	}

	filteredEndpoints := plugin.Filter(ctx, nil, request, endpoints)
	assert.Equal(t, 1, len(filteredEndpoints))
	assert.Equal(t, "matching-range", filteredEndpoints[0].GetMetadata().NamespacedName.Name)
}
