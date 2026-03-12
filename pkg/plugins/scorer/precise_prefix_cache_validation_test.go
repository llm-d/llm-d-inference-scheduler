package scorer

import (
	"context"
	"testing"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_PodDiscoveryValidation(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		config      PrecisePrefixCachePluginConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config with pod discovery disabled",
			config: PrecisePrefixCachePluginConfig{
				IndexerConfig: &kvcache.Config{},
				KVEventsConfig: &kvevents.Config{
					DiscoverPods: false,
				},
			},
			expectError: false,
		},
		{
			name: "missing podDiscoveryConfig when discoverPods enabled",
			config: PrecisePrefixCachePluginConfig{
				IndexerConfig: &kvcache.Config{},
				KVEventsConfig: &kvevents.Config{
					DiscoverPods:        true,
					PodDiscoveryConfig:  nil,
				},
			},
			expectError: true,
			errorMsg:    "podDiscoveryConfig is required when discoverPods is enabled",
		},
		{
			name: "invalid socket port - zero",
			config: PrecisePrefixCachePluginConfig{
				IndexerConfig: &kvcache.Config{},
				KVEventsConfig: &kvevents.Config{
					DiscoverPods: true,
					PodDiscoveryConfig: &kvevents.PodDiscoveryConfig{
						SocketPort: 0,
					},
				},
			},
			expectError: true,
			errorMsg:    "invalid socket port: must be between 1 and 65535, got 0",
		},
		{
			name: "invalid socket port - negative",
			config: PrecisePrefixCachePluginConfig{
				IndexerConfig: &kvcache.Config{},
				KVEventsConfig: &kvevents.Config{
					DiscoverPods: true,
					PodDiscoveryConfig: &kvevents.PodDiscoveryConfig{
						SocketPort: -1,
					},
				},
			},
			expectError: true,
			errorMsg:    "invalid socket port: must be between 1 and 65535, got -1",
		},
		{
			name: "invalid socket port - too large",
			config: PrecisePrefixCachePluginConfig{
				IndexerConfig: &kvcache.Config{},
				KVEventsConfig: &kvevents.Config{
					DiscoverPods: true,
					PodDiscoveryConfig: &kvevents.PodDiscoveryConfig{
						SocketPort: 65536,
					},
				},
			},
			expectError: true,
			errorMsg:    "invalid socket port: must be between 1 and 65535, got 65536",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(ctx, tt.config)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				// Note: This will still fail due to missing tokenizer config,
				// but it should pass the pod discovery validation
				if err != nil {
					assert.NotContains(t, err.Error(), "podDiscoveryConfig")
					assert.NotContains(t, err.Error(), "socket port")
				}
			}
		})
	}
}
