// Package config provides the configuration reading abilities
// Current version read configuration from environment variables
package config

import (
	"fmt"
	"math"

	"github.com/go-logr/logr"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
)

const (
	// KVCacheScorerName name of the kv-cache scorer in configuration
	KVCacheScorerName = "KVCACHE_AWARE_SCORER"
	// LoadAwareScorerName name of the load aware scorer in configuration
	LoadAwareScorerName = "LOAD_AWARE_SCORER"
	// PrefixScorerName name of the prefix scorer in configuration
	PrefixScorerName = "PREFIX_AWARE_SCORER"
	// SessionAwareScorerName name of the session aware scorer in configuration
	SessionAwareScorerName = "SESSION_AWARE_SCORER"
	// K8SKVCacheScorer name of the k8s kv-cache scorer in configuration
	K8SKVCacheScorerName = "K8S_KVCACHE_SCORER"
	// K8SQueueScorer name of the k8s queue scorer in configuration
	K8SQueueScorerName = "K8S_QUEUE_SCORER"
	// K8SPrefixPlugin name of the k8s prefix plugin in configuration
	K8SPrefixScorerName = "K8S_PREFIX_SCORER"

	pdEnabledEnvKey             = "PD_ENABLED"
	pdPromptLenThresholdEnvKey  = "PD_PROMPT_LEN_THRESHOLD"
	pdPromptLenThresholdDefault = 100
)

// Config contains scheduler configuration, currently configuration is loaded from environment variables
type Config struct {
	logger                  logr.Logger
	DecodeSchedulerPlugins  map[string]int
	PrefillSchedulerPlugins map[string]int

	PDEnabled   bool
	PDThreshold int
}

// NewConfig creates a new instance if Config
func NewConfig(logger logr.Logger) *Config {
	return &Config{
		logger:                  logger,
		DecodeSchedulerPlugins:  map[string]int{},
		PrefillSchedulerPlugins: map[string]int{},
		PDEnabled:               false,
		PDThreshold:             math.MaxInt,
	}
}

// LoadConfig loads configuration from environment variables
func (c *Config) LoadConfig() {
	c.loadPluginInfo(c.DecodeSchedulerPlugins, false,
		KVCacheScorerName, LoadAwareScorerName, PrefixScorerName, SessionAwareScorerName,
		K8SKVCacheScorerName, K8SQueueScorerName, K8SPrefixScorerName)

	c.loadPluginInfo(c.PrefillSchedulerPlugins, true,
		KVCacheScorerName, LoadAwareScorerName, PrefixScorerName, SessionAwareScorerName,
		K8SKVCacheScorerName, K8SQueueScorerName, K8SPrefixScorerName)

	c.PDEnabled = env.GetEnvString(pdEnabledEnvKey, "false", c.logger) == "true"
	c.PDThreshold = env.GetEnvInt(pdPromptLenThresholdEnvKey, pdPromptLenThresholdDefault, c.logger)
}

func (c *Config) loadPluginInfo(plugins map[string]int, prefill bool, pluginNames ...string) {
	for _, pluginName := range pluginNames {
		var enablementKey string
		var weightKey string
		if prefill {
			enablementKey = "PREFILL_ENABLE_" + pluginName
			weightKey = "PREFILL_" + pluginName + "_WEIGHT"
		} else {
			enablementKey = "ENABLE_" + pluginName
			weightKey = pluginName + "_WEIGHT"
		}

		if env.GetEnvString(enablementKey, "false", c.logger) != "true" {
			c.logger.Info(fmt.Sprintf("Skipping %s creation as it is not enabled", pluginName))
			return
		}

		weight := env.GetEnvInt(weightKey, 1, c.logger)

		plugins[pluginName] = weight
		c.logger.Info("Initialized plugin", "plugin", pluginName, "weight", weight)
	}
}
