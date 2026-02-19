package profile

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
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
				"primaryPort":       8080,
				"deciderPluginName": AlwaysEncodeDeciderPluginType,
			},
			expectErr: false,
		},
		{
			name:       "primaryPort = 1 is valid",
			pluginName: "port-min",
			params:     map[string]any{"primaryPort": 1},
			expectErr:  false,
		},
		{
			name:       "primaryPort = 65535 is valid",
			pluginName: "port-max",
			params:     map[string]any{"primaryPort": 65535},
			expectErr:  false,
		},
		{
			name:       "primaryPort = 65536 should error",
			pluginName: "port-too-high",
			params:     map[string]any{"primaryPort": 65536},
			expectErr:  true,
		},
		{
			name:       "primaryPort = -1 should error",
			pluginName: "port-negative",
			params:     map[string]any{"primaryPort": -1},
			expectErr:  true,
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

	invalidTests := []struct {
		name       string
		jsonParams string
	}{
		{
			name:       "malformed JSON",
			jsonParams: `{"deciderPluginName": `,
		},
		{
			name:       "primaryPort as float",
			jsonParams: `{"primaryPort": 8080.5}`,
		},
	}

	for _, tt := range invalidTests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := EdProfileHandlerFactory("test", json.RawMessage(tt.jsonParams), handle)
			assert.Error(t, err)
			assert.Nil(t, p)
		})
	}
}

func TestEdProfileHandler_Pick(t *testing.T) {
	ctx := utils.NewTestContext(t)
	request := createRequest("hello world")

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile:   newMockSchedulerProfile(),
		defaultEdEncodeProfile: newMockSchedulerProfile(),
	}

	tests := []struct {
		name             string
		decider          epdDeciderPlugin
		profileResults   map[string]*scheduling.ProfileRunResult
		expectedProfiles []string
	}{
		{
			name:             "decode not yet executed → run decode",
			decider:          newMockEpdDecider(true),
			profileResults:   map[string]*scheduling.ProfileRunResult{},
			expectedProfiles: []string{defaultDecodeProfile},
		},
		{
			name:    "decode failed (nil result) → run nothing",
			decider: newMockEpdDecider(true),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: nil,
			},
			expectedProfiles: []string{},
		},
		{
			name:    "all profiles already executed → run nothing",
			decider: newMockEpdDecider(true),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile:   newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectedProfiles: []string{},
		},
		{
			name:    "always-encode-decider approves → run encode",
			decider: newAlwaysEncodeDecider().WithName(AlwaysEncodeDeciderPluginType),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{defaultEdEncodeProfile},
		},
		{
			name:    "mock decider rejects encode → decode only",
			decider: newMockEpdDecider(false),
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{},
		},
		{
			name:    "nil decider → decode only (no encode)",
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
				0,
				tt.decider,
			)
			result := handler.Pick(ctx, nil, request, profiles, tt.profileResults)
			assert.ElementsMatch(t, tt.expectedProfiles, getProfilesFromResult(result))
		})
	}
}

func TestEdProfileHandler_ProcessResults(t *testing.T) {
	tests := []struct {
		name           string
		primaryPort    int
		profileResults map[string]*scheduling.ProfileRunResult
		expectError    bool
		checkResult    func(*testing.T, *scheduling.SchedulingResult, map[string]string)
	}{
		{
			name: "decode failed → error",
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: nil,
			},
			expectError: true,
		},
		{
			name:        "decode only, no encode, no primaryPort",
			primaryPort: 0,
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult, headers map[string]string) {
				assert.Equal(t, defaultDecodeProfile, res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.NotContains(t, res.ProfileResults, defaultEdEncodeProfile)
				assert.Empty(t, headers[common.EncoderHostsPortsHeader])
				assert.Empty(t, headers[common.DataParallelPodHeader])
			},
		},
		{
			name:        "decode and encode → encode header injected",
			primaryPort: 0,
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile:   newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				defaultEdEncodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult, headers map[string]string) {
				assert.Equal(t, defaultDecodeProfile, res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.Contains(t, res.ProfileResults, defaultEdEncodeProfile)
				assert.Equal(t, "10.0.0.1:"+DefaultTestPodPort, headers[common.EncoderHostsPortsHeader])
			},
		},
		{
			name:        "with primaryPort → decode port rewritten and DP header set",
			primaryPort: 9000,
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult, headers map[string]string) {
				metadata := res.ProfileResults[defaultDecodeProfile].TargetEndpoints[0].GetMetadata()
				assert.Equal(t, "9000", metadata.Port)
				assert.Equal(t, "10.0.0.1:"+DefaultTestPodPort, headers[common.DataParallelPodHeader])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewEdProfileHandler(
				defaultDecodeProfile,
				defaultEdEncodeProfile,
				tt.primaryPort,
				newMockEpdDecider(true),
			)

			headers := make(map[string]string)
			req := &scheduling.LLMRequest{Headers: headers}
			result, err := handler.ProcessResults(context.Background(), &scheduling.CycleState{}, req, tt.profileResults)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, result)
			tt.checkResult(t, result, headers)
		})
	}
}
