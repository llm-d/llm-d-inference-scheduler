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

	kvCacheScorerEnablementEnvVar      = "ENABLE_KVCACHE_AWARE_SCORER"
	loadAwareScorerEnablementEnvVar    = "ENABLE_LOAD_AWARE_SCORER"
	prefixScorerEnablementEnvVar       = "ENABLE_PREFIX_AWARE_SCORER"
	sessionAwareScorerEnablementEnvVar = "ENABLE_SESSION_AWARE_SCORER"

	kvCacheScorerWeightEnvVar      = "KVCACHE_AWARE_SCORER_WEIGHT"
	loadAwareScorerWeightEnvVar    = "LOAD_AWARE_SCORER_WEIGHT"
	prefixScorerWeightEnvVar       = "PREFIX_AWARE_SCORER_WEIGHT"
	sessionAwareScorerWeightEnvVar = "SESSION_AWARE_SCORER_WEIGHT"

	prefillKvCacheScorerEnablementEnvVar      = "PREFILL_ENABLE_KVCACHE_AWARE_SCORER"
	prefillLoadAwareScorerEnablementEnvVar    = "PREFILL_ENABLE_LOAD_AWARE_SCORER"
	prefillPrefixAwareScorerEnablementEnvVar  = "PREFILL_ENABLE_PREFIX_AWARE_SCORER"
	prefillSessionAwareScorerEnablementEnvVar = "PREFILL_ENABLE_SESSION_AWARE_SCORER"

	prefillKvCacheScorerWeightEnvVar      = "PREFILL_KVCACHE_AWARE_SCORER_WEIGHT"
	prefillLoadAwareScorerWeightEnvVar    = "PREFILL_LOAD_AWARE_SCORER_WEIGHT"
	prefillPrefixAwareScorerWeightEnvVar  = "PREFILL_PREFIX_AWARE_SCORER_WEIGHT"
	prefillSessionAwareScorerWeightEnvVar = "PREFILL_SESSION_AWARE_SCORER_WEIGHT"

	pdEnabledEnvKey             = "PD_ENABLED"
	pdPromptLenThresholdEnvKey  = "PD_PROMPT_LEN_THRESHOLD"
	pdPromptLenThresholdDefault = 100
)

// Config contains scheduler configuration, currently configuration is loaded from environment variables
type Config struct {
	logger                  logr.Logger
	DecodeSchedulerScorers  map[string]int
	PrefillSchedulerScorers map[string]int

	PDEnabled   bool
	PDThreshold int
}

// NewConfig creates a new instance if Config
func NewConfig(logger logr.Logger) *Config {
	return &Config{
		logger:                  logger,
		DecodeSchedulerScorers:  map[string]int{},
		PrefillSchedulerScorers: map[string]int{},
		PDEnabled:               false,
		PDThreshold:             math.MaxInt,
	}
}

// LoadConfig loads configuration from environment variables
func (c *Config) LoadConfig() {
	c.loadScorerInfo(c.DecodeSchedulerScorers, KVCacheScorerName, kvCacheScorerEnablementEnvVar, kvCacheScorerWeightEnvVar)
	c.loadScorerInfo(c.DecodeSchedulerScorers, LoadAwareScorerName, loadAwareScorerEnablementEnvVar, loadAwareScorerWeightEnvVar)
	c.loadScorerInfo(c.DecodeSchedulerScorers, PrefixScorerName, prefixScorerEnablementEnvVar, prefixScorerWeightEnvVar)
	c.loadScorerInfo(c.DecodeSchedulerScorers, SessionAwareScorerName, sessionAwareScorerEnablementEnvVar, sessionAwareScorerWeightEnvVar)

	c.loadScorerInfo(c.PrefillSchedulerScorers, KVCacheScorerName, prefillKvCacheScorerEnablementEnvVar, prefillKvCacheScorerWeightEnvVar)
	c.loadScorerInfo(c.PrefillSchedulerScorers, LoadAwareScorerName, prefillLoadAwareScorerEnablementEnvVar, prefillLoadAwareScorerWeightEnvVar)
	c.loadScorerInfo(c.PrefillSchedulerScorers, PrefixScorerName, prefillPrefixAwareScorerEnablementEnvVar, prefillPrefixAwareScorerWeightEnvVar)
	c.loadScorerInfo(c.PrefillSchedulerScorers, SessionAwareScorerName, prefillSessionAwareScorerEnablementEnvVar, prefillSessionAwareScorerWeightEnvVar)

	c.PDEnabled = env.GetEnvString(pdEnabledEnvKey, "false", c.logger) == "true"
	c.PDThreshold = env.GetEnvInt(pdPromptLenThresholdEnvKey, pdPromptLenThresholdDefault, c.logger)
}

func (c *Config) loadScorerInfo(scorers map[string]int, scorerName string, enablementKey string, weightKey string) {
	if env.GetEnvString(enablementKey, "false", c.logger) != "true" {
		c.logger.Info(fmt.Sprintf("Skipping %s creation as it is not enabled", scorerName))
		return
	}

	weight := env.GetEnvInt(weightKey, 1, c.logger)

	scorers[scorerName] = weight
	c.logger.Info("Initialized scorer", "scorer", scorerName, "weight", weight)
}
