package scorer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	kvcache "github.com/llm-d/llm-d-kv-cache-manager/pkg/kv-cache"
	"github.com/redis/go-redis/v9"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// PrefixCachePluginMode defines the mode of the prefix cache plugin. It can be either `estimate` or `cache_tracking`.
type PrefixCachePluginMode string

const (
	// PrefixCachePluginModeEstimate is the mode where the plugin use estimated prefix.
	PrefixCachePluginModeEstimate PrefixCachePluginMode = "estimate"
	// PrefixCachePluginModeCacheTracking is the mode where the plugin uses cache tracking using KVevents.
	PrefixCachePluginModeCacheTracking PrefixCachePluginMode = "cache_tracking"
	// huggingFaceTokenEnvVar is the environment variable that holds the Hugging Face token.
	huggingFaceTokenEnvVar = "HF_TOKEN"
)

// PrefixCachePluginConfig holds the configuration for the PrefixCachePlugin.
type PrefixCachePluginConfig struct {
	// Mode defines the mode of the prefix cache plugin.
	Mode PrefixCachePluginMode `json:"mode"` // "prefix" or "cache_tracking"
	// Config holds the configuration for the prefix cache plugin.
	prefix.Config
	// kvCacheRedisAddr is the address of the Redis instance used for cache tracking.
	KVCacheRedisAddr string `json:"kvCacheRedisAddr"`
}

// compile-time type assertion
var _ framework.Scorer = &KVCacheAwareScorer{}

// PrefixCachePluginFactory creates a new instance of the PrefixCachePlugin based on the provided configuration.
func PrefixCachePluginFactory(name string, rawParameters json.RawMessage, handle plugins.Handle) (plugins.Plugin, error) {
	var cfg PrefixCachePluginConfig

	logger := log.FromContext(handle.Context()).WithName("PrefixCachePluginFactory").V(logutil.DEFAULT)
	// Fallback to empty JSON if parameters are missing
	if rawParameters == nil {
		rawParameters = []byte(`{}`)
	}
	// Unmarshal directly into the flat config struct
	if err := json.Unmarshal(rawParameters, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s plugin config: %w", prefix.PrefixCachePluginType, err)
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

		plugin, err := NewKVCacheAwareScorer(handle.Context(), &cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create %s plugin: %w", prefix.PrefixCachePluginType, err)
		}
		return plugin.WithName(name), nil

	default:
		return nil, fmt.Errorf("unknown mode for %s plugin: %s", prefix.PrefixCachePluginType, mode)
	}
}

// NewKVCacheAwareScorer creates a new KVCacheAwareScorer instance.
// It initializes the KVCacheIndexer from environment variables.
//
// If the environment variables are not set, or if the indexer
// fails to initialize, an error is returned.
func NewKVCacheAwareScorer(ctx context.Context, cfg *PrefixCachePluginConfig) (*KVCacheAwareScorer, error) {
	config := kvcache.NewDefaultConfig()

	redisAddr := cfg.KVCacheRedisAddr
	if redisAddr == "" {
		return nil, errors.New("environment variable kvCacheRedisAddr is not set")
	}

	// to keep compatibility with deployments only specifying hostname:port: need to add protocol to front to enable parsing
	if !strings.HasPrefix(redisAddr, "redis://") && !strings.HasPrefix(redisAddr, "rediss://") && !strings.HasPrefix(redisAddr, "unix://") {
		redisAddr = "redis://" + redisAddr
	}

	redisOpt, err := redis.ParseURL(redisAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redisURL: %w", err)
	}
	config.KVBlockIndexerConfig.RedisOpt = redisOpt

	hfToken := os.Getenv(huggingFaceTokenEnvVar)
	if hfToken == "" {
		return nil, fmt.Errorf("environment variable '%s' is not set", huggingFaceTokenEnvVar)
	}

	config.TokenizersPoolConfig.HuggingFaceToken = hfToken

	kvCacheIndexer, err := kvcache.NewKVCacheIndexer(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create KVCacheIndexer: %w", err)
	}

	go kvCacheIndexer.Run(ctx)

	return &KVCacheAwareScorer{
		typedName:      plugins.TypedName{Type: prefix.PrefixCachePluginType},
		kvCacheIndexer: kvCacheIndexer,
	}, nil
}

// KVCacheAwareScorer uses the KVCacheIndexer to score pods based on KVCache awareness.
type KVCacheAwareScorer struct {
	typedName      plugins.TypedName
	kvCacheIndexer *kvcache.Indexer
}

// TypedName returns the typed name of the plugin.
func (s *KVCacheAwareScorer) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *KVCacheAwareScorer) WithName(name string) *KVCacheAwareScorer {
	s.typedName.Name = name
	return s
}

// Score scores the provided pod based on the KVCache index state.
// The returned scores are normalized to a range of 0-1.
func (s *KVCacheAwareScorer) Score(ctx context.Context, _ *types.CycleState, request *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
	loggerDebug := log.FromContext(ctx).WithName(s.typedName.String()).V(logutil.DEBUG)
	if request == nil {
		loggerDebug.Info("Request is nil, skipping scoring")
		return nil
	}

	scores, err := s.kvCacheIndexer.GetPodScores(ctx, request.Prompt, request.TargetModel, nil)
	if err != nil {
		loggerDebug.Error(err, "Failed to get pod scores")
		return nil
	}
	loggerDebug.Info("Got pod scores", "scores", scores)

	podToKey := func(pod types.Pod) (string, bool) {
		metricsPod := pod.GetPod()
		if metricsPod == nil {
			return "", false
		}

		return metricsPod.Address, true
	}

	return indexedScoresToNormalizedScoredPods(pods, podToKey, scores)
}
