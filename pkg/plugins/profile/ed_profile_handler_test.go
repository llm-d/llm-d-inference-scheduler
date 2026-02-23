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
				"deciderPluginName": AlwaysEncodeDeciderPluginType,
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

	handle, err := createEdHandleWithDeciderPlugins(ctx)
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

	handle, err := createEdHandleWithDeciderPlugins(ctx)
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

func TestEdProfileHandler_PickSeries(t *testing.T) {
	ctx := context.Background()

	// Create requests with different multimodal content
	requestWithoutMultimodal := &scheduling.LLMRequest{
		Body: &scheduling.LLMRequestBody{
			ChatCompletions: &scheduling.ChatCompletionsRequest{
				Messages: []scheduling.Message{
					{
						Content: scheduling.Content{
							Structured: []scheduling.ContentBlock{},
						},
					},
				},
			},
		},
	}

	requestWithMultimodal := &scheduling.LLMRequest{
		Body: &scheduling.LLMRequestBody{
			ChatCompletions: &scheduling.ChatCompletionsRequest{
				Messages: []scheduling.Message{
					{
						Content: scheduling.Content{
							Structured: []scheduling.ContentBlock{
								{
									Type: "image_url",
								},
							},
						},
					},
				},
			},
		},
	}

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile:   newMockSchedulerProfile(),
		defaultEdEncodeProfile: newMockSchedulerProfile(),
	}

	type testData struct {
		request              *scheduling.LLMRequest
		expectedProfilesStep map[int][]string // step number -> expected profiles
	}

	tests := []struct {
		name  string
		tests []testData
	}{
		{
			name: "non-multimodal request series: decode then stop",
			tests: []testData{
				{
					request: requestWithoutMultimodal,
					expectedProfilesStep: map[int][]string{
						1: {defaultDecodeProfile}, // First call: return decode
						2: {},                     // Second call: no multimodal, return empty
					},
				},
				{
					request: requestWithoutMultimodal,
					expectedProfilesStep: map[int][]string{
						1: {}, // Decode already done, return empty
					},
				},
			},
		},
		{
			name: "multimodal request series: decode then encode",
			tests: []testData{
				{
					request: requestWithMultimodal,
					expectedProfilesStep: map[int][]string{
						1: {defaultDecodeProfile},   // First call: return decode
						2: {defaultEdEncodeProfile}, // Second call: multimodal, return encode
						3: {},                       // Third call: all done, return empty
					},
				},
			},
		},
		{
			name: "mixed multimodal and non-multimodal",
			tests: []testData{
				{
					request: requestWithMultimodal,
					expectedProfilesStep: map[int][]string{
						1: {defaultDecodeProfile},   // First: return decode
						2: {defaultEdEncodeProfile}, // Second: multimodal, return encode
						3: {},                       // Third: all done, return empty
					},
				},
				{
					request: requestWithoutMultimodal,
					expectedProfilesStep: map[int][]string{
						1: {}, // Decode done, no multimodal, return empty
					},
				},
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

			profileResults := map[string]*scheduling.ProfileRunResult{}

			// Run series of requests
			for seriesIdx, innerTest := range tt.tests {
				stepNum := 1

				// Get expected profiles for all steps in this test data
				for expectedStep, expectedProfiles := range innerTest.expectedProfilesStep {
					cs := &scheduling.CycleState{}

					// Call Pick with current state
					result := handler.Pick(ctx, cs, innerTest.request, profiles, profileResults)
					actualProfiles := getProfilesFromResult(result)

					// Verify expected profiles match
					assert.ElementsMatch(t, expectedProfiles, actualProfiles,
						"Series %d, Step %d: expected profiles %v but got %v",
						seriesIdx, expectedStep, expectedProfiles, actualProfiles)

					// Update profileResults with completed profiles
					for profileName := range result {
						profileResults[profileName] = newMockProfileRunResult(DefaultTestPodPort, "pod1")
					}

					stepNum++
				}
			}
		})
	}
}

func createEdHandleWithDeciderPlugins(ctx context.Context) (plugin.Handle, error) {
	handle := plugin.NewEppHandle(ctx, nil)
	plugin := newAlwaysEncodeDecider()
	handle.AddPlugin(AlwaysEncodeDeciderPluginType, plugin)
	return handle, nil
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
