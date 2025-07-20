package config_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"sigs.k8s.io/gateway-api-inference-extension/cmd/epp/runner"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/common/config/loader"
	giePlugins "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/profile"
	"sigs.k8s.io/gateway-api-inference-extension/test/utils"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

func TestPrefixCacheModes(t *testing.T) {
	tests := []struct {
		name               string
		pluginName         string
		configText         string
		expectEstimatemode bool
		expectBlock        int
	}{
		{
			name:       "default mode",
			pluginName: "prefixCacheDefault",
			configText: `
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- name: prefixCacheDefault
  type: prefix-cache-scorer
- name: profileHandler
  type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: prefixCacheDefault
`,
			expectEstimatemode: true,
			expectBlock:        prefix.DefaultHashBlockSize, // Default block size
		},
		{
			name:       "explicit estimate mode",
			pluginName: "prefixCacheEstimate",
			configText: `
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- name: prefixCacheEstimate
  type: prefix-cache-scorer
  parameters:
    mode: estimate
    hashBlockSize: 64
    maxPrefixBlocksToMatch: 128
    lruCapacityPerServer: 10000
- name: profileHandler
  type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: prefixCacheEstimate
`,
			expectEstimatemode: true,
			expectBlock:        prefix.DefaultHashBlockSize, // Default block size
		},
		{
			name:       "explicit cache_tracking mode",
			pluginName: "prefixCacheKV",
			configText: `
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- name: prefixCacheKV
  type: prefix-cache-scorer
  parameters:
    mode: cache_tracking
    kvCacheRedisAddr: "redis://localhost:6379"
- name: profileHandler
  type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: prefixCacheKV
`,

			expectEstimatemode: false,
			expectBlock:        256, // Default block size for cache tracking
		},
	}
	ctx := context.Background()

	// Register GIE profile plugins
	giePlugins.Register(profile.SingleProfileHandlerType, profile.SingleProfileHandlerFactory)
	// Register GIE plugins
	runner.RegisterAllPlugins()
	// Register llm-d-inference-scheduler plugins
	plugins.RegisterAllPlugins()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !test.expectEstimatemode {
				addr, stop := startMiniRedis(t) // Creating a mini Redis server for testing
				defer stop()
				test.configText = strings.ReplaceAll(test.configText, "redis://localhost:6379", "redis://"+addr) // Replace with the mini Redis address
			}

			_ = os.Setenv("HF_TOKEN", "dummy_token") // needed for cache_tracking
			handle := utils.NewTestHandle(ctx)
			_, err := loader.LoadConfig([]byte(test.configText), handle)
			fmt.Println("all plugins", handle.GetAllPluginsWithNames())

			if err != nil {
				t.Fatalf("unexpected error from LoadConfig: %v", err)
			}
			if test.expectEstimatemode {
				plugin, err := giePlugins.PluginByType[*prefix.Plugin](handle, test.pluginName)
				if err != nil {
					t.Fatalf("expected EstimatedPrefixCacheScorer, but got error: %v", err)
				}
				if got := plugin.HashBlockSize; got != test.expectBlock {
					t.Errorf("EstimatedPrefixCacheScorer block size mismatch: got %d, want %d", got, test.expectBlock)
				}

			} else {
				_, err := giePlugins.PluginByType[*scorer.PrefixCacheTrackingScorer](handle, test.pluginName)
				if err != nil {
					t.Fatalf("expected PrefixCacheTrackingScorer, but got error: %v", err)
				}

			}
		})
	}
}

// startMiniRedis starts a mini Redis server for testing.
// Returns the address and a stop function to close it after the test.
func startMiniRedis(t *testing.T) (addr string, stop func()) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	return s.Addr(), func() { s.Close() }
}
