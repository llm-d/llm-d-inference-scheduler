package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
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

// createPod creates a mock Pod with customizable IP and port.
func createPod(nsn k8stypes.NamespacedName, ipaddr, port string, labels map[string]string) types.Pod {
	return &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: nsn,
			Address:        ipaddr,
			Port:           port,
			Labels:         labels,
		},
		MetricsState: &backendmetrics.MetricsState{},
	}
}

// newMockProfileRunResult creates a ProfileRunResult with Pods using the given port.
func newMockProfileRunResult(port string, podNames ...string) *types.ProfileRunResult {
	pods := make([]types.Pod, 0, len(podNames))
	for i, name := range podNames {
		ip := fmt.Sprintf("10.0.0.%d", i+1)
		pods = append(pods, createPod(
			k8stypes.NamespacedName{Namespace: "default", Name: name},
			ip,
			port,
			map[string]string{},
		))
	}
	return &types.ProfileRunResult{
		TargetPods: pods,
	}
}

func newMockSchedulerProfile() *framework.SchedulerProfile {
	return &framework.SchedulerProfile{}
}

func TestPdProfileHandler_Pick(t *testing.T) {
	ctx := context.Background()
	request := &types.LLMRequest{
		Body: &types.LLMRequestBody{
			Completions: &types.CompletionsRequest{
				Prompt: "hello world",
			},
		},
	}

	profiles := map[string]*framework.SchedulerProfile{
		"decode":  newMockSchedulerProfile(),
		"prefill": newMockSchedulerProfile(),
	}

	tests := []struct {
		name             string
		pdThreshold      int
		hashBlockSize    int
		prefixPluginName string
		setupPrefixState func(*types.CycleState)
		profileResults   map[string]*types.ProfileRunResult
		expectedProfiles []string
	}{
		{
			name:             "decode not executed yet → run decode",
			pdThreshold:      100,
			hashBlockSize:    16,
			prefixPluginName: prefix.PrefixCachePluginType,
			profileResults:   map[string]*types.ProfileRunResult{},
			expectedProfiles: []string{"decode"},
		},
		{
			name:             "decode failed (nil result) → run nothing",
			pdThreshold:      100,
			hashBlockSize:    16,
			prefixPluginName: prefix.PrefixCachePluginType,
			profileResults: map[string]*types.ProfileRunResult{
				"decode": nil,
			},
			expectedProfiles: []string{},
		},
		{
			name:             "all profiles already executed → run nothing",
			pdThreshold:      100,
			hashBlockSize:    16,
			prefixPluginName: prefix.PrefixCachePluginType,
			profileResults: map[string]*types.ProfileRunResult{
				"decode":  newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				"prefill": newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectedProfiles: []string{},
		},
		{
			name:             "pd threshold NOT triggered → run prefill",
			pdThreshold:      5,
			hashBlockSize:    16,
			prefixPluginName: prefix.PrefixCachePluginType,
			setupPrefixState: func(cs *types.CycleState) {
				state := &prefix.SchedulingContextState{
					PrefixCacheServers: map[prefix.ServerID]int{
						prefix.ServerID(k8stypes.NamespacedName{Name: "pod1", Namespace: "default"}): 1,
					},
				}
				key := plugins.StateKey(fmt.Sprintf("%s/%s", prefix.PrefixCachePluginType, prefix.PrefixCachePluginType))
				cs.Write(key, state)
			},
			profileResults: map[string]*types.ProfileRunResult{
				"decode": newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectedProfiles: []string{"prefill"},
		},
		{
			name:             "pd threshold triggered (short non-cached suffix) → skip prefill",
			pdThreshold:      100,
			hashBlockSize:    16,
			prefixPluginName: prefix.PrefixCachePluginType,
			setupPrefixState: func(cs *types.CycleState) {
				state := &prefix.SchedulingContextState{
					PrefixCacheServers: map[prefix.ServerID]int{
						prefix.ServerID(k8stypes.NamespacedName{Name: "pod1", Namespace: "default"}): 5,
					},
				}
				key := plugins.StateKey(fmt.Sprintf("%s/%s", prefix.PrefixCachePluginType, prefix.PrefixCachePluginType))
				cs.Write(key, state)
			},
			profileResults: map[string]*types.ProfileRunResult{
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
				tt.prefixPluginName,
				tt.pdThreshold,
				tt.hashBlockSize,
				0,
			).WithName("test-handler")

			cs := &types.CycleState{}
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
		profileResults map[string]*types.ProfileRunResult
		expectError    bool
		checkResult    func(*testing.T, *types.SchedulingResult, map[string]string)
	}{
		{
			name: "decode failed → error",
			profileResults: map[string]*types.ProfileRunResult{
				"decode": nil,
			},
			expectError: true,
		},
		{
			name:        "decode success, no prefill, no primaryPort",
			primaryPort: 0,
			profileResults: map[string]*types.ProfileRunResult{
				"decode": newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *types.SchedulingResult, headers map[string]string) {
				assert.Equal(t, "decode", res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, "decode")
				assert.NotContains(t, res.ProfileResults, "prefill")
				pod := res.ProfileResults["decode"].TargetPods[0].GetPod()
				assert.Equal(t, DefaultTestPodPort, pod.Port)
				assert.Empty(t, headers[common.DataParallelPodHeader])
			},
		},
		{
			name:        "decode success, with prefill",
			primaryPort: 0,
			profileResults: map[string]*types.ProfileRunResult{
				"decode":  newMockProfileRunResult(DefaultTestPodPort, "pod1"),
				"prefill": newMockProfileRunResult(DefaultTestPodPort, "pod2"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *types.SchedulingResult, _ map[string]string) {
				assert.Equal(t, "decode", res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, "decode")
				assert.Contains(t, res.ProfileResults, "prefill")
			},
		},
		{
			name:        "with primaryPort → port updated and header set",
			primaryPort: 9000,
			profileResults: map[string]*types.ProfileRunResult{
				"decode": newMockProfileRunResult(DefaultTestPodPort, "pod1"),
			},
			expectError: false,
			checkResult: func(t *testing.T, res *types.SchedulingResult, headers map[string]string) {
				pod := res.ProfileResults["decode"].TargetPods[0].GetPod()
				assert.Equal(t, "9000", pod.Port)

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
				0,
				prefix.DefaultBlockSize,
				tt.primaryPort,
			).WithName("test-handler")

			headers := make(map[string]string)
			req := &types.LLMRequest{
				Headers: headers,
			}
			result, err := handler.ProcessResults(context.Background(), &types.CycleState{}, req, tt.profileResults)

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
