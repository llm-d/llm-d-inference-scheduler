package scorer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache/kvevents"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// PrefixCacheTrackingConfig holds the configuration for the
// PrefixCacheTrackingScorer.
type PrefixCacheTrackingConfig struct {
	// IndexerConfig holds the configuration for the `kvcache.Indexer` which is
	// used to score pods based on the KV-cache index state.
	IndexerConfig *kvcache.Config `json:"indexerConfig"`
	// KVEventsConfig holds the configuration for the `kvevents.Pool` which is
	// used to subscribe to KV-cache events and update the internal KV-cache
	// index state.
	KVEventsConfig *kvevents.Config `json:"kvEventsConfig"`
}

// compile-time type assertion
var _ framework.Scorer = &PrefixCacheTrackingScorer{}

// PrefixCacheTrackingPluginFactory defines the factory function for creating
// a new instance of the PrefixCacheTrackingPlugin.
func PrefixCacheTrackingPluginFactory(name string, rawParameters json.RawMessage,
	handle plugins.Handle) (plugins.Plugin, error) {
	parameters := PrefixCacheTrackingConfig{
		IndexerConfig:  kvcache.NewDefaultConfig(),
		KVEventsConfig: kvevents.DefaultConfig(),
	}

	// read hugging face token from environment variable if set
	if token := os.Getenv("HF_TOKEN"); token != "" {
		parameters.IndexerConfig.TokenizersPoolConfig.HuggingFaceToken = token
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse %s plugin config: %w", prefix.PrefixCachePluginType, err)
		}
	}

	scorer, err := New(handle.Context(), parameters)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s plugin: %w", prefix.PrefixCachePluginType, err)
	}

	return scorer.WithName(name), nil
}

// New initializes a new prefix Plugin and returns its pointer.
// It sets up the `kvcache.Indexer` and `kvevents.Pool`
// based on the provided configuration. The `kvevents.Pool` is started
// in a goroutine to listen for KV-cache events and update the internal
// KV-cache index state. The `kvcache.Indexer` is also started in a goroutine
// to score pods based on the KV-cache index state.
//
// If the configuration is invalid or if the indexer fails to initialize,
// an error is returned.
func New(ctx context.Context, config PrefixCacheTrackingConfig) (*PrefixCacheTrackingScorer, error) {
	// initialize the indexer
	kvCacheIndexer, err := kvcache.NewKVCacheIndexer(ctx, config.IndexerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create `kvcache.Indexer`: %w", err)
	}

	go kvCacheIndexer.Run(ctx)

	// initialize the KV-events pool
	pool := kvevents.NewPool(config.KVEventsConfig, kvCacheIndexer.KVBlockIndex())
	pool.Start(ctx)

	return &PrefixCacheTrackingScorer{
		typedName:      plugins.TypedName{Type: prefix.PrefixCachePluginType},
		kvCacheIndexer: kvCacheIndexer,
	}, nil
}

// PrefixCacheTrackingScorer implements the framework.Scorer interface.
// The scorer implements the `cache_tracking` mode of the prefix cache plugin.
// It uses the `kvcache.Indexer` to score pods based on the KV-cache index
// state, and the `kvevents.Pool` to subscribe to KV-cache events
// to update the internal KV-cache index state.
type PrefixCacheTrackingScorer struct {
	typedName      plugins.TypedName
	kvCacheIndexer *kvcache.Indexer
}

// TypedName returns the typed name of the plugin.
func (s *PrefixCacheTrackingScorer) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *PrefixCacheTrackingScorer) WithName(name string) *PrefixCacheTrackingScorer {
	s.typedName.Name = name
	return s
}

// Score scores the provided pod based on the KVCache index state.
// The returned scores are normalized to a range of 0-1.
func (s *PrefixCacheTrackingScorer) Score(ctx context.Context, _ *types.CycleState, request *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
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
