package scorer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	kvcache "github.com/llm-d/llm-d-kv-cache-manager/pkg/kv-cache"
	"github.com/redis/go-redis/v9"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// KvCacheAwareScorerType is the type of the KvCacheAwareScorer
	KvCacheAwareScorerType = "kvcache-aware-scorer"

	kvCacheRedisEnvVar     = "KVCACHE_INDEXER_REDIS_ADDR"
	huggingFaceTokenEnvVar = "HF_TOKEN"
)

// compile-time type assertion
var _ framework.Scorer = &KVCacheAwareScorer{}

// KvCacheAwareScorerFactory defines the factory function for the KVCacheAwareScorer
func KvCacheAwareScorerFactory(name string, _ json.RawMessage, handle plugins.Handle) (plugins.Plugin, error) {
	plugin, err := NewKVCacheAwareScorer(handle.Context())
	if err != nil {
		return nil, err
	}
	return plugin.WithName(name), nil
}

// NewKVCacheAwareScorer creates a new KVCacheAwareScorer instance.
// It initializes the KVCacheIndexer from environment variables.
//
// If the environment variables are not set, or if the indexer
// fails to initialize, an error is returned.
func NewKVCacheAwareScorer(ctx context.Context) (*KVCacheAwareScorer, error) {
	config := kvcache.NewDefaultConfig()

	redisAddr := os.Getenv(kvCacheRedisEnvVar)
	if redisAddr == "" {
		return nil, fmt.Errorf("environment variable '%s' is not set", kvCacheRedisEnvVar)
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
		typedName:      plugins.TypedName{Type: KvCacheAwareScorerType},
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
