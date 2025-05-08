// Package config provides the configuration reading abilities
// Current version read configuration from environment variables
package config

const (
	kvCacheScorerEnablementEnvVar      = "ENABLE_KVCACHE_AWARE_SCORER"
	loadAwareScorerEnablementEnvVar    = "ENABLE_LOAD_AWARE_SCORER"
	prefixScorerEnablementEnvVar       = "ENABLE_PREFIX_AWARE_SCORER"
	sessionAwareScorerEnablementEnvVar = "ENABLE_SESSION_AWARE_SCORER"

	kvCacheScorerWeightEnvVar      = "KVCACHE_AWARE_SCORER_WEIGHT"
	loadAwareScorerWeightEnvVar    = "LOAD_AWARE_SCORER_WEIGHT"
	prefixScorerWeightEnvVar       = "PREFIX_AWARE_SCORER_WEIGHT"
	sessionAwareScorerWeightEnvVar = "SESSION_AWARE_SCORER_WEIGHT"

	prefillKvCacheScorerEnablementEnvVar   = "PREFILL_ENABLE_KVCACHE_AWARE_SCORER"
	prefillLoadAwareScorerEnablementEnvVar = "PREFILL_ENABLE_LOAD_AWARE_SCORER"
	decodeKvCacheScorerEnablementEnvVar    = "DECODE_ENABLE_KVCACHE_AWARE_SCORER"
	decodeLoadAwareScorerEnablementEnvVar  = "DECODE_ENABLE_LOAD_AWARE_SCORER"

	prefillKvCacheScorerWeightEnvVar   = "PREFILL_KVCACHE_AWARE_SCORER_WEIGHT"
	prefillLoadAwareScorerWeightEnvVar = "PREFILL_LOAD_AWARE_SCORER_WEIGHT"
	decodeKvCacheScorerWeightEnvVar    = "DECODE_KVCACHE_AWARE_SCORER_WEIGHT"
	decodeLoadAwareScorerWeightEnvVar  = "DECODE_LOAD_AWARE_SCORER_WEIGHT"

	pdEnabledEnvKey             = "PD_ENABLED"
	pdPromptLenThresholdEnvKey  = "PD_PROMPT_LEN_THRESHOLD"
	pdPromptLenThresholdDefault = 10
)

type Config struct {
	DefaultSchedulerScorers map[string]float64
	DecodeSchedulerScorers  map[string]float64
	PrefillSchedulerScorers map[string]float64
}
