package profile

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

// mockEpdDecider is a controllable epdDeciderPlugin for testing.
type mockEpdDecider struct {
	typedName    plugin.TypedName
	shouldEncode bool
}

func (m *mockEpdDecider) TypedName() plugin.TypedName { return m.typedName }
func (m *mockEpdDecider) disaggregateEncode(_ context.Context, _ *scheduling.LLMRequest, _ scheduling.Endpoint) bool {
	return m.shouldEncode
}

func newMockEpdDecider(shouldEncode bool) *mockEpdDecider {
	return &mockEpdDecider{
		typedName:    plugin.TypedName{Type: AlwaysEncodeDeciderPluginType, Name: AlwaysEncodeDeciderPluginType},
		shouldEncode: shouldEncode,
	}
}

func createHandleWithAlwaysEncodeDecider(ctx context.Context) plugin.Handle {
	handle := plugin.NewEppHandle(ctx, nil)
	decider := newAlwaysEncodeDecider().WithName(AlwaysEncodeDeciderPluginType)
	handle.AddPlugin(AlwaysEncodeDeciderPluginType, decider)
	return handle
}

func TestAlwaysEncodeDecider(t *testing.T) {
	ctx := utils.NewTestContext(t)
	decider := newAlwaysEncodeDecider().WithName(AlwaysEncodeDeciderPluginType)

	assert.Equal(t, AlwaysEncodeDeciderPluginType, decider.TypedName().Type)
	assert.Equal(t, AlwaysEncodeDeciderPluginType, decider.TypedName().Name)

	request := createRequest("hello world")
	endpoint := newMockProfileRunResult(DefaultTestPodPort, "pod1").TargetEndpoints[0]

	assert.True(t, decider.disaggregateEncode(ctx, request, endpoint), "AlwaysEncodeDecider should always return true")
}

func TestAlwaysEncodeDeciderFactory(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handle := plugin.NewEppHandle(ctx, nil)

	p, err := AlwaysEncodeDeciderPluginFactory("my-decider", nil, handle)
	assert.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, AlwaysEncodeDeciderPluginType, p.TypedName().Type)
	assert.Equal(t, "my-decider", p.TypedName().Name)
}

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
			pluginName: "default-ed-handler",
			params:     map[string]any{},
			expectErr:  false,
		},
		{
			name:       "valid configuration with always-encode-decider",
			pluginName: "custom-ed-handler",
			params: map[string]any{
				"decodeProfile":     "my-decode",
				"encodeProfile":     "my-encode",
				"deciderPluginName": AlwaysEncodeDeciderPluginType,
			},
			expectErr: false,
		},
		{
			name:       "invalid decider plugin type should error",
			pluginName: "bad-decider",
			params:     map[string]any{"deciderPluginName": "INVALID"},
			expectErr:  true,
		},
		{
			name:       "empty decider plugin name → nil decider, no error",
			pluginName: "no-decider",
			params:     map[string]any{"deciderPluginName": ""},
			expectErr:  false,
		},
	}

	handle := createHandleWithAlwaysEncodeDecider(ctx)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bytes, err := json.Marshal(tt.params)
			assert.NoError(t, err)
			rawParams := json.RawMessage(bytes)

			p, err := EdProfileHandlerFactory(tt.pluginName, rawParams, handle)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, p)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, p)
			}
		})
	}
}

func TestEdProfileHandlerFactory_InvalidJSON(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handle := createHandleWithAlwaysEncodeDecider(ctx)

	p, err := EdProfileHandlerFactory("test", json.RawMessage(`{"deciderPluginName": `), handle)
	assert.Error(t, err)
	assert.Nil(t, p)
}

func createMultimodalRequest(contentType string) *scheduling.LLMRequest {
	return &scheduling.LLMRequest{
		Body: &scheduling.LLMRequestBody{
			ChatCompletions: &scheduling.ChatCompletionsRequest{
				Messages: []scheduling.Message{
					{
						Role: "user",
						Content: scheduling.Content{
							Structured: []scheduling.ContentBlock{
								{Type: contentType},
							},
						},
					},
				},
			},
		},
	}
}

func TestEdProfileHandler_Pick(t *testing.T) {
	ctx := utils.NewTestContext(t)
	textRequest := createRequest("hello world")
	imageRequest := createMultimodalRequest("image_url")
	videoRequest := createMultimodalRequest("video_url")

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile:   newMockSchedulerProfile(),
		defaultEdEncodeProfile: newMockSchedulerProfile(),
	}

	tests := []struct {
		name             string
		request          *scheduling.LLMRequest
		decider          epdDeciderPlugin
		profileResults   map[string]*scheduling.ProfileRunResult
		expectedProfiles []string
	}{
		{
			name:             "decode not yet executed → run decode",
			request:          imageRequest,
			decider:          newMockEpdDecider(true),
			profileResults:   map[string]*scheduling.ProfileRunResult{},
			expectedProfiles: []string{defaultDecodeProfile},
		},
		{
			name:    "decode failed (nil result) → run nothing",
			request: imageRequest,
			decider: newMockEpdDecider(true),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: nil,
			},
			expectedProfiles: []string{},
		},
		{
			name:    "all profiles already executed → run nothing",
			request: imageRequest,
			decider: newMockEpdDecider(true),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile:   newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectedProfiles: []string{},
		},
		{
			name:    "image request, decider approves → run encode",
			request: imageRequest,
			decider: newAlwaysEncodeDecider().WithName(AlwaysEncodeDeciderPluginType),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{defaultEdEncodeProfile},
		},
		{
			name:    "video request, decider approves → run encode",
			request: videoRequest,
			decider: newAlwaysEncodeDecider().WithName(AlwaysEncodeDeciderPluginType),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{defaultEdEncodeProfile},
		},
		{
			name:    "text-only request, decider approves → decode only (no multimedia)",
			request: textRequest,
			decider: newAlwaysEncodeDecider().WithName(AlwaysEncodeDeciderPluginType),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{},
		},
		{
			name:    "image request, mock decider rejects → decode only",
			request: imageRequest,
			decider: newMockEpdDecider(false),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{},
		},
		{
			name:    "image request, nil decider → decode only",
			request: imageRequest,
			decider: nil,
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
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
			name: "decode only, no encode",
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
			name: "decode and encode → both results included",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewEdProfileHandler(
				defaultDecodeProfile,
				defaultEdEncodeProfile,
				newMockEpdDecider(true),
			)

			result, err := handler.ProcessResults(context.Background(), &scheduling.CycleState{}, nil, tt.profileResults)

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
