// Package config provides the configuration reading abilities
// Current version read configuration from environment variables
package config

import (
	"fmt"
	"math"

	"github.com/go-logr/logr"
)

const (
	kvCacheScorerName      = "KVCACHE_AWARE_SCORER"
	loadAwareScorerName    = "LOAD_AWARE_SCORER"
	prefixScorerName       = "PREFIX_AWARE_SCORER"
	sessionAwareScorerName = "SESSION_AWARE_SCORER"

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

	decodeKvCacheScorerEnablementEnvVar      = "DECODE_ENABLE_KVCACHE_AWARE_SCORER"
	decodeLoadAwareScorerEnablementEnvVar    = "DECODE_ENABLE_LOAD_AWARE_SCORER"
	decodePrefixAwareScorerEnablementEnvVar  = "DECODE_ENABLE_PREFIX_AWARE_SCORER"
	decodeSessionAwareScorerEnablementEnvVar = "DECODE_ENABLE_SESSION_AWARE_SCORER"

	decodeKvCacheScorerWeightEnvVar      = "DECODE_KVCACHE_AWARE_SCORER_WEIGHT"
	decodeLoadAwareScorerWeightEnvVar    = "DECODE_LOAD_AWARE_SCORER_WEIGHT"
	decodePrefixAwareScorerWeightEnvVar  = "DECODE_PREFIX_AWARE_SCORER_WEIGHT"
	decodeSessionAwareScorerWeightEnvVar = "DECODE_SESSION_AWARE_SCORER_WEIGHT"

	pdEnabledEnvKey             = "PD_ENABLED"
	pdPromptLenThresholdEnvKey  = "PD_PROMPT_LEN_THRESHOLD"
	pdPromptLenThresholdDefault = 100
)

// Config contains scheduler configuration, currently configuration is loaded from environment variables
type Config struct {
	logger                  logr.Logger
	DefaultSchedulerScorers map[string]float64
	DecodeSchedulerScorers  map[string]float64
	PrefillSchedulerScorers map[string]float64

	PDEnabled   bool
	PDThreshold int
}

// NewConfig creates a new instance if Config
func NewConfig(logger logr.Logger) *Config {
	return &Config{
		logger:                  logger,
		DefaultSchedulerScorers: map[string]float64{},
		DecodeSchedulerScorers:  map[string]float64{},
		PrefillSchedulerScorers: map[string]float64{},
		PDEnabled:               false,
		PDThreshold:             math.MaxInt,
	}
}

// LoadConfig loads configuration from environment variables
func (c *Config) LoadConfig() {
	c.loadScorerInfo(c.DefaultSchedulerScorers, kvCacheScorerName, kvCacheScorerEnablementEnvVar, kvCacheScorerWeightEnvVar)
	c.loadScorerInfo(c.DefaultSchedulerScorers, loadAwareScorerName, loadAwareScorerEnablementEnvVar, loadAwareScorerWeightEnvVar)
	c.loadScorerInfo(c.DefaultSchedulerScorers, prefixScorerName, prefixScorerEnablementEnvVar, prefixScorerWeightEnvVar)
	c.loadScorerInfo(c.DefaultSchedulerScorers, sessionAwareScorerName, sessionAwareScorerEnablementEnvVar, sessionAwareScorerWeightEnvVar)

	c.loadScorerInfo(c.DecodeSchedulerScorers, kvCacheScorerName, decodeKvCacheScorerEnablementEnvVar, decodeKvCacheScorerWeightEnvVar)
	c.loadScorerInfo(c.DecodeSchedulerScorers, loadAwareScorerName, decodeLoadAwareScorerEnablementEnvVar, decodeLoadAwareScorerWeightEnvVar)
	c.loadScorerInfo(c.DecodeSchedulerScorers, prefixScorerName, decodePrefixAwareScorerEnablementEnvVar, decodePrefixAwareScorerWeightEnvVar)
	c.loadScorerInfo(c.DecodeSchedulerScorers, sessionAwareScorerName, decodeSessionAwareScorerEnablementEnvVar, decodeSessionAwareScorerWeightEnvVar)

	c.loadScorerInfo(c.PrefillSchedulerScorers, kvCacheScorerName, prefillKvCacheScorerEnablementEnvVar, prefillKvCacheScorerWeightEnvVar)
	c.loadScorerInfo(c.PrefillSchedulerScorers, loadAwareScorerName, prefillLoadAwareScorerEnablementEnvVar, prefillLoadAwareScorerWeightEnvVar)
	c.loadScorerInfo(c.PrefillSchedulerScorers, prefixScorerName, prefillPrefixAwareScorerEnablementEnvVar, prefillPrefixAwareScorerWeightEnvVar)
	c.loadScorerInfo(c.PrefillSchedulerScorers, sessionAwareScorerName, prefillSessionAwareScorerEnablementEnvVar, prefillSessionAwareScorerWeightEnvVar)

	c.PDEnabled = GetEnvString(pdEnabledEnvKey, "false", c.logger) == "true"
	c.PDThreshold = GetEnvInt(pdPromptLenThresholdEnvKey, pdPromptLenThresholdDefault, c.logger)
}

func (c *Config) loadScorerInfo(scorers map[string]float64, scorerName string, enablementKey string, weightKey string) {
	if GetEnvString(enablementKey, "false", c.logger) != "true" {
		c.logger.Info(fmt.Sprintf("Skipping %s creation as it is not enabled", scorerName))
		return
	}

	weight := GetEnvFloat(weightKey, 1, c.logger)

	scorers[scorerName] = weight
	c.logger.Info("Initialized scorer", "scorer", scorerName, "weight", weight)
}
