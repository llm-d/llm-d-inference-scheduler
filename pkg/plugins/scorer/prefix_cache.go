package scorer

import (
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// PrefixCachePluginMode defines the mode of the prefix cache plugin. It can be either `estimate` or `cache_tracking`.
type PrefixCachePluginMode string

const (
	// PrefixCachePluginModeEstimate is the mode where the plugin builds a prefix-cache estimation
	// index based on scheduling history.
	PrefixCachePluginModeEstimate PrefixCachePluginMode = "estimate"
	// PrefixCachePluginModeCacheTracking is the mode where the plugin tracks vLLM prefix-cache
	// states.
	PrefixCachePluginModeCacheTracking PrefixCachePluginMode = "cache_tracking"
)

// PrefixCachePluginConfig holds the configuration for the PrefixCachePlugin.
type PrefixCachePluginConfig struct {
	// Mode defines the mode of the prefix cache plugin.
	Mode PrefixCachePluginMode `json:"mode"` // `estimate` or `cache_tracking`
}

// PrefixCachePluginFactory creates a new instance of the PrefixCachePlugin based on the provided configuration.
func PrefixCachePluginFactory(name string, rawParameters json.RawMessage, handle plugins.Handle) (plugins.Plugin, error) {
	var cfg PrefixCachePluginConfig

	logger := log.FromContext(handle.Context()).WithName("PrefixCachePluginFactory").V(logutil.DEFAULT)

	// Parse parameters if provided
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse %s plugin config: %w", prefix.PrefixCachePluginType, err)
		}
	}

	mode := cfg.Mode
	if mode == "" {
		mode = PrefixCachePluginModeEstimate
	}

	switch mode {
	case PrefixCachePluginModeEstimate:
		logger.Info("Creating PrefixCachePlugin in estimate mode", "parameters", rawParameters)
		return prefix.PrefixCachePluginFactory(name, rawParameters, handle)

	case PrefixCachePluginModeCacheTracking:
		logger.Info("Creating PrefixCachePluginConfig in cache tracking mode", "parameters", rawParameters)
		return PrefixCacheTrackingPluginFactory(name, rawParameters, handle)

	default:
		return nil, fmt.Errorf("unknown mode for %s plugin: %s", prefix.PrefixCachePluginType, mode)
	}
}
