package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/prefix"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func TestPdProfileHandlerFactory(t *testing.T) {
	tests := []struct {
		name       string
		pluginName string
		jsonParams string
		expectErr  bool
	}{
		{
			name:       "valid configuration with all defaults",
			pluginName: "default-handler",
			jsonParams: "{}",
			expectErr:  false,
		},
		{
			name:       "valid configuration with custom values",
			pluginName: "custom-handler",
			jsonParams: `{
				"threshold": 100,
				"decodeProfile": "my-decode",
				"prefillProfile": "my-prefill",
				"prefixPluginName": "my-prefix-cache",
				"hashBlockSize": 32,
				"primaryPort": 8080
			}`,
			expectErr: false,
		},
		{
			name:       "zero primaryPort is allowed",
			pluginName: "zero-port",
			jsonParams: `{"primaryPort": 0}`,
			expectErr:  false,
		},
		{
			name:       "threshold = 0 is allowed",
			pluginName: "zero-threshold",
			jsonParams: `{"threshold": 0}`,
			expectErr:  false,
		},
		{
			name:       "negative threshold should error",
			pluginName: "neg-threshold",
			jsonParams: `{"threshold": -1}`,
			expectErr:  true,
		},
		{
			name:       "hashBlockSize = 0 should error",
			pluginName: "zero-block-size",
			jsonParams: `{"hashBlockSize": 0}`,
			expectErr:  true,
		},
		{
			name:       "negative hashBlockSize should error",
			pluginName: "neg-block-size",
			jsonParams: `{"hashBlockSize": -5}`,
			expectErr:  true,
		},
		{
			name:       "primaryPort below range should error",
			pluginName: "port-too-low",
			jsonParams: `{"primaryPort": 0}`, // OK
			expectErr:  false,
		},
		{
			name:       "primaryPort = 1 is valid",
			pluginName: "port-min",
			jsonParams: `{"primaryPort": 1}`,
			expectErr:  false,
		},
		{
			name:       "primaryPort = 65535 is valid",
			pluginName: "port-max",
			jsonParams: `{"primaryPort": 65535}`,
			expectErr:  false,
		},
		{
			name:       "empty decodeProfile is valid",
			pluginName: "empty-decode",
			jsonParams: `{"decodeProfile": ""}`,
			expectErr:  false,
		},
		{
			name:       "empty prefillProfile is valid",
			pluginName: "empty-prefill",
			jsonParams: `{"prefillProfile": ""}`,
			expectErr:  false,
		},
		{
			name:       "empty prefixPluginName is valid",
			pluginName: "empty-prefix-plugin",
			jsonParams: `{"prefixPluginName": ""}`,
			expectErr:  false,
		},
		{
			name:       "primaryPort = 65536 should error",
			pluginName: "port-too-high",
			jsonParams: `{"primaryPort": 65536}`,
			expectErr:  true,
		},
		{
			name:       "primaryPort = -10 should error",
			pluginName: "port-negative",
			jsonParams: `{"primaryPort": -10}`,
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rawParams json.RawMessage
			if tt.jsonParams != "" {
				rawParams = json.RawMessage(tt.jsonParams)
			}
			plugin, err := PdProfileHandlerFactory(tt.pluginName, rawParams, nil)

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

func TestPdProfileHandlerFactoryInvalidJSON(t *testing.T) {
	invalidTests := []struct {
		name       string
		jsonParams string
	}{
		{
			name:       "malformed JSON",
			jsonParams: `{"threshold": 100, "hashBlockSize":`, // incomplete
		},
		{
			name:       "threshold as string instead of int",
			jsonParams: `{"threshold": "100"}`,
		},
		{
			name:       "hashBlockSize as boolean",
			jsonParams: `{"hashBlockSize": true}`,
		},
		{
			name:       "primaryPort as float",
			jsonParams: `{"primaryPort": 8080.5}`,
		},
	}

	for _, tt := range invalidTests {
		t.Run(tt.name, func(t *testing.T) {
			rawParams := json.RawMessage(tt.jsonParams)
			plugin, err := PdProfileHandlerFactory("test", rawParams, nil)

			assert.Error(t, err)
			assert.Nil(t, plugin)
		})
	}
}

const DefaultTestPodPort = "8000"

// createEndpoint creates a mock Pod with customizable IP and port.
func createEndpoint(nsn k8stypes.NamespacedName, ipaddr, port string, labels map[string]string) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: nsn,
			Address:        ipaddr,
			Port:           port,
			Labels:         labels,
		},
		&fwkdl.Metrics{},
		nil,
	)
}

// newMockProfileRunResult creates a ProfileRunResult with Pods using the given port.
func newMockProfileRunResult(port string, endpointNames ...string) *scheduling.ProfileRunResult {
	endpoints := make([]scheduling.Endpoint, 0, len(endpointNames))
	for i, name := range endpointNames {
		ip := fmt.Sprintf("10.0.0.%d", i+1)
		endpoints = append(endpoints, createEndpoint(
			k8stypes.NamespacedName{Namespace: "default", Name: name},
			ip,
			port,
			map[string]string{},
		))
	}
	return &scheduling.ProfileRunResult{
		TargetEndpoints: endpoints,
	}
}

func newMockSchedulerProfile() scheduling.SchedulerProfile {
	return &mockSchedulerProfile{}
}

type mockSchedulerProfile struct{}

func (p *mockSchedulerProfile) Run(_ context.Context, _ *scheduling.LLMRequest, _ *scheduling.CycleState, _ []scheduling.Endpoint) (*scheduling.ProfileRunResult, error) {
	return &scheduling.ProfileRunResult{}, nil
}

func TestPdProfileHandler_Pick(t *testing.T) {
	ctx := utils.NewTestContext(t)
	request := &scheduling.LLMRequest{
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: "hello world",
			},
		},
	}

	profiles := map[string]scheduling.SchedulerProfile{
		"decode":  newMockSchedulerProfile(),
		"prefill": newMockSchedulerProfile(),
	}

	tests := []struct {
		name             string
		pdThreshold      int
		hashBlockSize    int
		prefixPluginType string
		prefixPluginName string
		setupPrefixState func(*scheduling.CycleState)
		profileResults   map[string]*scheduling.ProfileRunResult
		expectedProfiles []string
	}{
		{
			name:             "decode not executed yet → run decode",
			pdThreshold:      100,
			hashBlockSize:    16,
			prefixPluginType: prefix.PrefixCachePluginType,
			prefixPluginName: prefix.PrefixCachePluginType,
			profileResults:   map[string]*scheduling.ProfileRunResult{},
			expectedProfiles: []string{"decode"},
		},
		{
			name:             "decode failed (nil result) → run nothing",
			pdThreshold:      100,
			hashBlockSize:    16,
			prefixPluginType: prefix.PrefixCachePluginType,
			prefixPluginName: prefix.PrefixCachePluginType,
			profileResults: map[string]*scheduling.ProfileRunResult{
				"decode": nil,
			},
			expectedProfiles: []string{},
		},
		{
			name:             "all profiles already executed → run nothing",
			pdThreshold:      100,
			hashBlockSize:    16,
			prefixPluginType: prefix.PrefixCachePluginType,
			prefixPluginName: prefix.PrefixCachePluginType,
			profileResults: map[string]*scheduling.ProfileRunResult{
				"decode":  newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				"prefill": newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectedProfiles: []string{},
		},
		{
			name:             "pd threshold NOT triggered → run prefill",
			pdThreshold:      5,
			hashBlockSize:    16,
			prefixPluginType: prefix.PrefixCachePluginType,
			prefixPluginName: prefix.PrefixCachePluginType,
			setupPrefixState: func(cs *scheduling.CycleState) {
				state := &prefix.SchedulingContextState{
					PrefixCacheServers: map[prefix.ServerID]int{
						prefix.ServerID(k8stypes.NamespacedName{Name: "pod1", Namespace: "default"}): 1,
					},
				}
				key := plugin.StateKey(fmt.Sprintf("%s/%s", prefix.PrefixCachePluginType, prefix.PrefixCachePluginType))
				cs.Write(key, state)
			},
			profileResults: map[string]*scheduling.ProfileRunResult{
				"decode": newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{"prefill"},
		},
		{
			name:             "pd threshold triggered (short non-cached suffix) → skip prefill",
			pdThreshold:      100,
			hashBlockSize:    16,
			prefixPluginType: prefix.PrefixCachePluginType,
			prefixPluginName: prefix.PrefixCachePluginType,
			setupPrefixState: func(cs *scheduling.CycleState) {
				state := &prefix.SchedulingContextState{
					PrefixCacheServers: map[prefix.ServerID]int{
						prefix.ServerID(k8stypes.NamespacedName{Name: "pod1", Namespace: "default"}): 5,
					},
				}
				key := plugin.StateKey(fmt.Sprintf("%s/%s", prefix.PrefixCachePluginType, prefix.PrefixCachePluginType))
				cs.Write(key, state)
			},
			profileResults: map[string]*scheduling.ProfileRunResult{
				"decode": newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewPdProfileHandler(
				"prefill",
				"decode",
				tt.prefixPluginType,
				tt.prefixPluginName,
				tt.pdThreshold,
				tt.hashBlockSize,
				0,
			).WithName("test-handler")

			cs := &scheduling.CycleState{}
			if tt.setupPrefixState != nil {
				tt.setupPrefixState(cs)
			}

			result := handler.Pick(ctx, cs, request, profiles, tt.profileResults)

			var actual []string
			for name := range result {
				actual = append(actual, name)
			}

			assert.ElementsMatch(t, tt.expectedProfiles, actual)
		})
	}
}

func TestPdProfileHandler_ProcessResults(t *testing.T) {
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
				"decode": nil,
			},
			expectError: true,
		},
		{
			name:        "decode success, no prefill, no primaryPort",
			primaryPort: 0,
			profileResults: map[string]*scheduling.ProfileRunResult{
				"decode": newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult, headers map[string]string) {
				assert.Equal(t, "decode", res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, "decode")
				assert.NotContains(t, res.ProfileResults, "prefill")
				metadata := res.ProfileResults["decode"].TargetEndpoints[0].GetMetadata()
				assert.Equal(t, DefaultTestPodPort, metadata.Port)
				assert.Empty(t, headers[common.DataParallelPodHeader])
			},
		},
		{
			name:        "decode success, with prefill",
			primaryPort: 0,
			profileResults: map[string]*scheduling.ProfileRunResult{
				"decode":  newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				"prefill": newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult, _ map[string]string) {
				assert.Equal(t, "decode", res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, "decode")
				assert.Contains(t, res.ProfileResults, "prefill")
			},
		},
		{
			name:        "with primaryPort → port updated and header set",
			primaryPort: 9000,
			profileResults: map[string]*scheduling.ProfileRunResult{
				"decode": newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *scheduling.SchedulingResult, headers map[string]string) {
				metadata := res.ProfileResults["decode"].TargetEndpoints[0].GetMetadata()
				assert.Equal(t, "9000", metadata.Port)

				hostPort := headers[common.DataParallelPodHeader]
				assert.Equal(t, "10.0.0.1:8000", hostPort)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewPdProfileHandler(
				"prefill",
				"decode",
				prefix.PrefixCachePluginType,
				prefix.PrefixCachePluginType,
				0,
				prefix.DefaultBlockSizeTokens*averageCharactersPerToken,
				tt.primaryPort,
			).WithName("test-handler")

			headers := make(map[string]string)
			req := &scheduling.LLMRequest{
				Headers: headers,
			}
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
