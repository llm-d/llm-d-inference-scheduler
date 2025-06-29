// Package config provides the configuration reading abilities
// Current version read configuration from environment variables
package config

import (
	"github.com/go-logr/logr"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
)

const (
	// For every plugin named below, there are four environment variables. They are:
	//  - "ENABLE_" + pluginName  Enables the named plugin for decode processing
	//  - pluginName + "_WEIGHT"  The weight for a scorer in decode processing
	//  - "PREFILL_ENABLE_" + pluginName  Enables the named plugin for prefill processing
	//  - "PREFILL_" + pluginName + "_WEIGHT"  The weight for a scorer in prefill processing

	prefillPrefix = "PREFILL_"
	enablePrefix  = "ENABLE_"
	weightSuffix  = "_WEIGHT"

	// KVCacheScorerName name of the kv-cache scorer in configuration
	KVCacheScorerName = "KVCACHE_AWARE_SCORER"
	// LoadAwareScorerName name of the load aware scorer in configuration
	LoadAwareScorerName = "LOAD_AWARE_SCORER"
	// PrefixScorerName name of the prefix scorer in configuration
	PrefixScorerName = "PREFIX_AWARE_SCORER"
	// SessionAwareScorerName name of the session aware scorer in configuration
	SessionAwareScorerName = "SESSION_AWARE_SCORER"

	// Plugins from Upstream

	// GIELeastKVCacheFilterName name of the GIE least kv-cache filter in configuration
	GIELeastKVCacheFilterName = "GIE_LEAST_KVCACHE_FILTER"
	// GIELeastQueueFilterName name of the GIE least queue filter in configuration
	GIELeastQueueFilterName = "GIE_LEAST_QUEUE_FILTER"
	// GIELoraAffinityFilterName name of the GIE LoRA affinity filter in configuration
	GIELoraAffinityFilterName = "GIE_LORA_AFFINITY_FILTER"
	// GIELowQueueFilterName name of the GIE low queue filter in configuration
	GIELowQueueFilterName = "GIE_LOW_QUEUE_FILTER"
	// GIEKVCacheUtilizationScorerName name of the GIE kv-cache utilization scorer in configuration
	GIEKVCacheUtilizationScorerName = "GIE_KVCACHE_UTILIZATION_SCORER"
	// GIEQueueScorerName name of the GIE queue scorer in configuration
	GIEQueueScorerName = "GIE_QUEUE_SCORER"
	// GIEPrefixScorerName name of the GIE prefix plugin in configuration
	GIEPrefixScorerName = "GIE_PREFIX_SCORER"

	pdEnabledEnvKey             = "PD_ENABLED"
	pdPromptLenThresholdEnvKey  = "PD_PROMPT_LEN_THRESHOLD"
	pdPromptLenThresholdDefault = 100
)

// Config contains scheduler configuration, currently configuration is loaded from environment variables
type Config struct {
	DecodeSchedulerPlugins  map[string]int
	PrefillSchedulerPlugins map[string]int
	PDEnabled               bool
	PDThreshold             int
	GIEPrefixConfig         *prefix.Config
	PrefixConfig            *scorer.PrefixStoreConfig // TBD should be removed.
}

// LoadConfig loads configuration from environment variables and returns a new instance of Config
func LoadConfig(logger logr.Logger) *Config {
	pluginNames := []string{
		KVCacheScorerName, LoadAwareScorerName, PrefixScorerName, SessionAwareScorerName,
		GIELeastKVCacheFilterName, GIELeastQueueFilterName, GIELoraAffinityFilterName,
		GIELowQueueFilterName,
		GIEKVCacheUtilizationScorerName, GIEQueueScorerName, GIEPrefixScorerName,
	}

	// Set GIE prefix Config
	giePrefixConfig := &prefix.Config{
		HashBlockSize:          env.GetEnvInt("PREFIX_CACHE_HASH_BLOCK_SIZE", prefix.DefaultHashBlockSize, logger),
		MaxPrefixBlocksToMatch: env.GetEnvInt("PREFIX_CACHE_MAX_PREFIX_BLOCKS", prefix.DefaultMaxPrefixBlocks, logger),
		LRUCapacityPerServer:   env.GetEnvInt("PREFIX_CACHE_LRU_CAPACITY_PER_SERVER", prefix.DefaultLRUCapacityPerServer, logger),
	}
	// prefix Config- TBD to be mremove
	prefixConfig := scorer.DefaultPrefixStoreConfig()
	prefixConfig.CacheBlockSize = env.GetEnvInt("PREFIX_SCORER_CACHE_BLOCK_SIZE", scorer.DefaultPrefixCacheBlockSize, logger)
	prefixConfig.CacheCapacity = env.GetEnvInt("PREFIX_SCORER_CACHE_CAPACITY", scorer.DefaultPrefixCacheCapacity, logger)

	return &Config{
		DecodeSchedulerPlugins:  loadPluginInfo(logger, false, pluginNames),
		PrefillSchedulerPlugins: loadPluginInfo(logger, true, pluginNames),
		PDEnabled:               env.GetEnvBool(pdEnabledEnvKey, false, logger),
		PDThreshold:             env.GetEnvInt(pdPromptLenThresholdEnvKey, pdPromptLenThresholdDefault, logger),
		GIEPrefixConfig:         giePrefixConfig,
		PrefixConfig:            prefixConfig,
	}
}

func loadPluginInfo(logger logr.Logger, prefill bool, pluginNames []string) map[string]int {
	result := map[string]int{}

	for _, pluginName := range pluginNames {
		var enablementKey string
		var weightKey string
		if prefill {
			enablementKey = prefillPrefix + enablePrefix + pluginName
			weightKey = prefillPrefix + pluginName + weightSuffix
		} else {
			enablementKey = enablePrefix + pluginName
			weightKey = pluginName + weightSuffix
		}

		if !env.GetEnvBool(enablementKey, false, logger) {
			logger.Info("Skipping plugin creation as it is not enabled", "name", pluginName)
		} else {
			weight := env.GetEnvInt(weightKey, 1, logger)
			result[pluginName] = weight
			logger.Info("Initialized plugin", "plugin", pluginName, "weight", weight)
		}
	}

	return result
}
