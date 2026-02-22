package profile

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func TestEdProfileHandlerFactory(t *testing.T) {
	ctx := utils.NewTestContext(t)
	tests := []struct {
		name       string
		pluginName string
		params     map[string]any
		expectErr  bool
	}{
		{
			name:       "valid configuration with all defaults",
			pluginName: "default-handler",
			params:     map[string]any{},
			expectErr:  false,
		},
		{
			name:       "valid configuration with custom values",
			pluginName: "custom-handler",
			params: map[string]any{
				"decodeProfile":     "my-decode",
				"encodeProfile":     "my-encode",
				"deciderPluginName": AlwaysDisaggDeciderPluginType,
			},
			expectErr: false,
		},
		{
			name:       "empty decodeProfile is valid",
			pluginName: "empty-decode",
			params:     map[string]any{"decodeProfile": ""},
			expectErr:  false,
		},
		{
			name:       "empty encodeProfile is valid",
			pluginName: "empty-encode",
			params:     map[string]any{"encodeProfile": ""},
			expectErr:  false,
		},
	}

	handle, err := createHandleWithDeciderPlugins(ctx)
	assert.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rawParams json.RawMessage
			if tt.params != nil {
				bytes, err := json.Marshal(tt.params)
				assert.NoError(t, err)
				rawParams = json.RawMessage(bytes)
			}
			plugin, err := EdProfileHandlerFactory(tt.pluginName, rawParams, handle)

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

func TestEdProfileHandlerFactoryInvalidJSON(t *testing.T) {
	ctx := utils.NewTestContext(t)

	invalidTests := []struct {
		name       string
		jsonParams string
	}{
		{
			name:       "malformed JSON",
			jsonParams: `{"deciderPluginName": `, // incomplete
		},
		{
			name:       "invalid decider plugin type",
			jsonParams: `{"deciderPluginName": "INVALID"}`,
		},
	}

	handle, err := createHandleWithDeciderPlugins(ctx)
	assert.NoError(t, err)

	for _, tt := range invalidTests {
		t.Run(tt.name, func(t *testing.T) {
			rawParams := json.RawMessage(tt.jsonParams)
			plugin, err := EdProfileHandlerFactory("test", rawParams, handle)

			assert.Error(t, err)
			assert.Nil(t, plugin)
		})
	}
}

// createChatRequest creates a chat completion request with optional multimodal content
func createChatRequest(hasImage, hasVideo, hasAudio bool) *scheduling.LLMRequest {
	messages := []scheduling.ChatMessage{
		{
			Role: "user",
			Content: scheduling.ChatContent{
				Structured: []scheduling.ContentBlock{
					{
						Type: "text",
						Text: "Hello, describe this content",
					},
				},
			},
		},
	}

	if hasImage {
		messages[0].Content.Structured = append(messages[0].Content.Structured, scheduling.ContentBlock{
			Type: "image_url",
			ImageURL: &scheduling.ImageURL{
				URL: "https://example.com/image.jpg",
			},
		})
	}

	if hasVideo {
		messages[0].Content.Structured = append(messages[0].Content.Structured, scheduling.ContentBlock{
			Type: "video_url",
		})
	}

	if hasAudio {
		messages[0].Content.Structured = append(messages[0].Content.Structured, scheduling.ContentBlock{
			Type: "input_audio",
		})
	}

	return &scheduling.LLMRequest{
		Body: &scheduling.LLMRequestBody{
			ChatCompletions: &scheduling.ChatCompletionsRequest{
				Messages: messages,
			},
		},
	}
}

func TestEdProfileHandler_Pick(t *testing.T) {
	ctx := utils.NewTestContext(t)

	profiles := map[string]scheduling.SchedulerProfile{
		"decode": newMockSchedulerProfile(),
		"encode": newMockSchedulerProfile(),
	}

	tests := []struct {
		name             string
		request          *scheduling.LLMRequest
		profileResults   map[string]*scheduling.ProfileRunResult
		expectedProfiles []string
	}{
		{
			name:             "decode not executed yet → run decode",
			request:          createChatRequest(true, false, false),
			profileResults:   map[string]*scheduling.ProfileRunResult{},
			expectedProfiles: []string{defaultDecodeProfile},
		},
		{
			name:    "decode failed (nil result) → run nothing",
			request: createChatRequest(true, false, false),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: nil,
			},
			expectedProfiles: []string{},
		},
		{
			name:    "all profiles already executed → run nothing",
			request: createChatRequest(true, false, false),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectedProfiles: []string{},
		},
		{
			name:    "decode done, no multimodal content → skip encode",
			request: createChatRequest(false, false, false),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{},
		},
		{
			name:    "decode done, has image → run encode",
			request: createChatRequest(true, false, false),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{defaultEdEncodeProfile},
		},
		{
			name:    "decode done, has video → run encode",
			request: createChatRequest(false, true, false),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{defaultEdEncodeProfile},
		},
		{
			name:    "decode done, has audio → run encode",
			request: createChatRequest(false, false, true),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{defaultEdEncodeProfile},
		},
		{
			name:    "decode done, has multiple multimodal types → run encode",
			request: createChatRequest(true, true, true),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{defaultEdEncodeProfile},
		},
		{
			name:    "encode failed (nil result) → run nothing",
			request: createChatRequest(true, false, false),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile:   newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: nil,
			},
			expectedProfiles: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewEdProfileHandler(
				defaultDecodeProfile,
				defaultEdEncodeProfile,
				nil, // no decider plugin
			)

			result := handler.Pick(ctx, nil, tt.request, profiles, tt.profileResults)
			assert.ElementsMatch(t, tt.expectedProfiles, getProfilesFromResult(result))
		})
	}
}

func TestEdProfileHandler_PickWithDecider(t *testing.T) {
	ctx := utils.NewTestContext(t)

	profiles := map[string]scheduling.SchedulerProfile{
		"decode": newMockSchedulerProfile(),
		"encode": newMockSchedulerProfile(),
	}

	tests := []struct {
		name             string
		request          *scheduling.LLMRequest
		decider          epdDeciderPlugin
		profileResults   map[string]*scheduling.ProfileRunResult
		expectedProfiles []string
	}{
		{
			name:    "decider says disaggregate → encode runs",
			request: createChatRequest(true, false, false),
			decider: &mockEpdDecider{shouldDisaggregate: true},
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile:   newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectedProfiles: []string{},
		},
		{
			name:    "decider says no disaggregate → skip encode",
			request: createChatRequest(true, false, false),
			decider: &mockEpdDecider{shouldDisaggregate: false},
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile:   newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectedProfiles: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewEdProfileHandler(
				defaultDecodeProfile,
				defaultEdEncodeProfile,
				tt.decider,
			)

			result := handler.Pick(ctx, nil, tt.request, profiles, tt.profileResults)
			assert.ElementsMatch(t, tt.expectedProfiles, getProfilesFromResult(result))
		})
	}
}

func TestEdProfileHandler_ProcessResults(t *testing.T) {
	tests := []struct {
		name           string
		profileResults map[string]*scheduling.ProfileRunResult
		expectError    bool
		checkResult    func(*testing.T, *scheduling.SchedulingResult)
	}{
		{
			name: "decode failed → error",
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: nil,
			},
			expectError: true,
		},
		{
			name: "decode success, no encode",
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Equal(t, defaultDecodeProfile, res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.NotContains(t, res.ProfileResults, defaultEdEncodeProfile)
			},
		},
		{
			name: "decode success, with encode",
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile:   newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Equal(t, defaultDecodeProfile, res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.Contains(t, res.ProfileResults, defaultEdEncodeProfile)
			},
		},
		{
			name: "decode success, encode failed (nil) → only decode in results",
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile:   newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: nil,
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Equal(t, defaultDecodeProfile, res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.NotContains(t, res.ProfileResults, defaultEdEncodeProfile)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewEdProfileHandler(
				defaultDecodeProfile,
				defaultEdEncodeProfile,
				nil,
			)

			result, err := handler.ProcessResults(context.Background(), &scheduling.CycleState{}, &scheduling.LLMRequest{}, tt.profileResults)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, result)
			tt.checkResult(t, result)
		})
	}
}

func TestHasMultimodalContent(t *testing.T) {
	tests := []struct {
		name     string
		request  *scheduling.LLMRequest
		expected bool
	}{
		{
			name:     "nil request",
			request:  nil,
			expected: false,
		},
		{
			name: "nil body",
			request: &scheduling.LLMRequest{
				Body: nil,
			},
			expected: false,
		},
		{
			name: "nil chat completions",
			request: &scheduling.LLMRequest{
				Body: &scheduling.LLMRequestBody{
					ChatCompletions: nil,
				},
			},
			expected: false,
		},
		{
			name:     "text only",
			request:  createChatRequest(false, false, false),
			expected: false,
		},
		{
			name:     "has image",
			request:  createChatRequest(true, false, false),
			expected: true,
		},
		{
			name:     "has video",
			request:  createChatRequest(false, true, false),
			expected: true,
		},
		{
			name:     "has audio",
			request:  createChatRequest(false, false, true),
			expected: true,
		},
		{
			name:     "has all multimodal types",
			request:  createChatRequest(true, true, true),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasMultimodalContent(tt.request)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// mockEpdDecider is a mock implementation of epdDeciderPlugin for testing
type mockEpdDecider struct {
	typedName          plugin.TypedName
	shouldDisaggregate bool
}

func (m *mockEpdDecider) TypedName() plugin.TypedName {
	return m.typedName
}

func (m *mockEpdDecider) disaggregateEncode(_ context.Context, _ *scheduling.LLMRequest, _ scheduling.Endpoint) bool {
	return m.shouldDisaggregate
}
