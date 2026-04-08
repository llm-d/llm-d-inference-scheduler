package scorer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents/engineadapter"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	dl_prefix "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/datalayer/attribute/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/prefix"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/preparedata"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

const (
	// PrecisePrefixCachePluginType is the type-name of the PrecisePrefixCacheScorer plugin.
	PrecisePrefixCachePluginType = "precise-prefix-cache-scorer"

	// defaultSpeculativeTTL is the default TTL for speculative entries.
	// This should be just long enough to cover the blind spot between
	// routing decision and KV event arrival, maintaining high confidence
	// in speculations while avoiding stale routing affinity.
	defaultSpeculativeTTL = 2 * time.Second

	// stateKey is the PluginState key used to share data between
	// PrepareRequestData, Score, and PreRequest for precise mode.
	stateKey = plugin.StateKey("prefix-cache-state")

	// approxStateKey is the PluginState key for approximate mode data.
	approxStateKey = plugin.StateKey("approx-prefix-cache-state")

	// experimentalPrefillProfile is the profile name for P/D disaggregation mode.
	experimentalPrefillProfile = "prefill"

	// Scoring mode constants.
	ModePrecise     = "precise"
	ModeApproximate = "approximate"
	ModeUnified     = "unified"

	// defaultPodCleanupInterval is the interval for removing inactive pods
	// from the approximate indexer.
	defaultPodCleanupInterval = 2 * time.Minute

	// defaultApproxLRUCapacity is the default LRU capacity per server for approximate mode.
	// Sized for H100 80GB: ~64GB HBM for cache → 500K tokens → 31.25K blocks at block size 16.
	defaultApproxLRUCapacity = 31250
)

type kvCacheIndexer interface {
	GetPodScores(ctx context.Context, renderReq *types.RenderChatRequest, prompt, modelName string, podIdentifiers []string) (map[string]float64, error)
	ScoreTokens(ctx context.Context, tokens []uint32, modelName string, podIdentifiers []string, extraFeatures []*kvblock.BlockExtraFeatures) (map[string]float64, error)
	ComputeBlockKeys(ctx context.Context, renderReq *types.RenderChatRequest, prompt, modelName string) ([]kvblock.BlockHash, error)
	KVBlockIndex() kvblock.Index
}

// PrecisePrefixCachePluginConfig holds the configuration for the
// PrecisePrefixCacheScorer plugin.
type PrecisePrefixCachePluginConfig struct {
	// Mode selects the scoring mode:
	//   "precise"     - KV events + tokenizer (default, current behavior)
	//   "approximate" - xxhash + self-reported LRU (no KV events/tokenizer needed)
	//   "unified"     - both modes combined
	// If empty, defaults to "precise".
	Mode string `json:"mode"`
	// TokenProcessorConfig holds the configuration for the `kvblock.TokenProcessor` which is
	// used to process tokens into KV-block keys.
	// Only used in "precise" and "unified" modes.
	TokenProcessorConfig *kvblock.TokenProcessorConfig `json:"tokenProcessorConfig"`
	// IndexerConfig holds the configuration for the `kvcache.Indexer` which is
	// used to score endpoints based on the KV-cache index state.
	// Only used in "precise" and "unified" modes.
	IndexerConfig *kvcache.Config `json:"indexerConfig"`
	// KVEventsConfig holds the configuration for the `kvevents.Pool` which is
	// used to subscribe to KV-cache events and update the internal KV-cache
	// index state.
	// Only used in "precise" and "unified" modes.
	KVEventsConfig *kvevents.Config `json:"kvEventsConfig"`
	// SpeculativeIndexing enables speculative indexing. When true, the plugin
	// proactively adds predicted cache entries to the index immediately after
	// a routing decision (via PrepareRequestData and PreRequest), closing the
	// blind spot between routing and KV event arrival.
	// When false, only confirmed KV events populate the index.
	// Only used in "precise" and "unified" modes.
	SpeculativeIndexing bool `json:"speculativeIndexing"`
	// SpeculativeTTL is the time-to-live for speculative index entries.
	// After this duration, speculative entries are evicted from the index.
	// If empty, defaultSpeculativeTTL is used. Only used when SpeculativeIndexing is true.
	// Accepts Go duration strings (e.g. "2s", "500ms").
	SpeculativeTTL string `json:"speculativeTTL"`
	// ApproximateConfig holds configuration for approximate scoring mode.
	// Only used when Mode is "approximate" or "unified".
	ApproximateConfig *ApproximateConfig `json:"approximateConfig"`
}

// ApproximateConfig holds configuration for the approximate scoring mode.
type ApproximateConfig struct {
	// BlockSizeTokens is the block size in tokens. Each block is approximately
	// blockSizeTokens * 4 characters wide. Default: 16.
	BlockSizeTokens int `json:"blockSizeTokens"`
	// MaxPrefixBlocksToMatch limits how many blocks to hash per request.
	// Longer prefixes are truncated. Default: 256.
	MaxPrefixBlocksToMatch int `json:"maxPrefixBlocksToMatch"`
	// LRUCapacityPerServer is the maximum number of LRU entries per pod.
	// Default: 31250 (sized for H100 80GB).
	LRUCapacityPerServer int `json:"lruCapacityPerServer"`
	// AutoTune enables dynamic block size and LRU capacity from endpoint metrics.
	// Default: true.
	AutoTune bool `json:"autoTune"`
	// RecencyHalfLife enables recency-weighted scoring when set to a positive duration.
	// Older self-reported entries decay exponentially: weight = exp(-age * ln2 / halfLife).
	// A half-life of 30s means an entry recorded 30s ago contributes 50% of a fresh
	// entry's weight. This improves accuracy because older entries are more likely to
	// have been evicted from the actual GPU cache.
	// If empty or zero, standard block-count scoring is used (no decay).
	// Accepts Go duration strings (e.g. "30s", "1m").
	RecencyHalfLife string `json:"recencyHalfLife"`
}

// defaultApproximateConfig returns a default ApproximateConfig.
func defaultApproximateConfig() *ApproximateConfig {
	return &ApproximateConfig{
		BlockSizeTokens:        defaultApproxBlockSizeTokens,
		MaxPrefixBlocksToMatch: defaultApproxMaxPrefixBlocks,
		LRUCapacityPerServer:   defaultApproxLRUCapacity,
		AutoTune:               true,
	}
}

// compile-time type assertions
var (
	_ scheduling.Scorer                = &PrecisePrefixCacheScorer{}
	_ requestcontrol.PrepareDataPlugin = &PrecisePrefixCacheScorer{}
	_ requestcontrol.PreRequest        = &PrecisePrefixCacheScorer{}
)

// speculativeEntries holds the data needed to evict speculative entries
// from the index when the TTL expires.
type speculativeEntries struct {
	blockKeys  []kvblock.BlockHash
	podEntries []kvblock.PodEntry
}

// precisePluginState holds data shared between PrepareRequestData, Score,
// and PreRequest via PluginState.
type precisePluginState struct {
	blockKeys []kvblock.BlockHash
	scores    map[string]float64 // pod addr → score
}

// Clone implements plugin.StateData.
func (s *precisePluginState) Clone() plugin.StateData {
	blockKeys := make([]kvblock.BlockHash, len(s.blockKeys))
	copy(blockKeys, s.blockKeys)
	scores := make(map[string]float64, len(s.scores))
	for k, v := range s.scores {
		scores[k] = v
	}
	return &precisePluginState{
		blockKeys: blockKeys,
		scores:    scores,
	}
}

// approximatePluginState holds data shared between PrepareRequestData, Score,
// and PreRequest for approximate mode via PluginState.
type approximatePluginState struct {
	hashes       []ApproximateBlockHash
	serverScores map[ApproximateServerID]float64 // server → score (block count or weighted)
	totalBlocks  int
}

// Clone implements plugin.StateData.
func (s *approximatePluginState) Clone() plugin.StateData {
	hashes := make([]ApproximateBlockHash, len(s.hashes))
	copy(hashes, s.hashes)
	scores := make(map[ApproximateServerID]float64, len(s.serverScores))
	for k, v := range s.serverScores {
		scores[k] = v
	}
	return &approximatePluginState{
		hashes:       hashes,
		serverScores: scores,
		totalBlocks:  s.totalBlocks,
	}
}

// PrecisePrefixCachePluginFactory defines the factory function for creating
// a new instance of the PrefixCacheTrackingPlugin.
func PrecisePrefixCachePluginFactory(name string, rawParameters json.RawMessage,
	handle plugin.Handle,
) (plugin.Plugin, error) {
	indexerConfig, err := kvcache.NewDefaultConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize indexer config: %w", err)
	}

	parameters := PrecisePrefixCachePluginConfig{
		IndexerConfig:  indexerConfig,
		KVEventsConfig: kvevents.DefaultConfig(),
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse %s plugin config: %w", PrecisePrefixCachePluginType, err)
		}
	}

	mode := parameters.Mode
	if mode == "" {
		mode = ModePrecise
	}

	// Validate model name is set for modes that require precise scoring.
	if mode == ModePrecise || mode == ModeUnified {
		if parameters.IndexerConfig == nil || parameters.IndexerConfig.TokenizersPoolConfig == nil || parameters.IndexerConfig.TokenizersPoolConfig.ModelName == "" {
			return nil, errors.New("modelName is required in indexerConfig.tokenizersPoolConfig for precise/unified mode")
		}
	}

	scorer, err := New(handle.Context(), parameters, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s plugin: %w", PrecisePrefixCachePluginType, err)
	}

	return scorer.WithName(name), nil
}

// New initializes a new prefix Plugin and returns its pointer.
// It sets up components based on the configured mode:
//   - "precise": KV events + tokenizer + optional speculative indexing
//   - "approximate": xxhash + self-reported LRU (no KV events/tokenizer)
//   - "unified": both modes combined
//
// The handle parameter is optional and only required for approximate/unified
// modes (provides PodList for inactive pod cleanup). Pass nil for precise-only.
func New(ctx context.Context, config PrecisePrefixCachePluginConfig, handle plugin.Handle) (*PrecisePrefixCacheScorer, error) {
	mode := config.Mode
	if mode == "" {
		mode = ModePrecise
	}

	switch mode {
	case ModePrecise, ModeApproximate, ModeUnified:
		// valid
	default:
		return nil, fmt.Errorf("invalid mode %q: must be one of %q, %q, %q",
			mode, ModePrecise, ModeApproximate, ModeUnified)
	}

	scorer := &PrecisePrefixCacheScorer{
		typedName: plugin.TypedName{Type: PrecisePrefixCachePluginType},
		mode:      mode,
	}

	// --- Precise mode initialization ---
	if mode == ModePrecise || mode == ModeUnified {
		if err := scorer.initPrecise(ctx, config); err != nil {
			return nil, err
		}
	}

	// --- Approximate mode initialization ---
	if mode == ModeApproximate || mode == ModeUnified {
		scorer.initApproximate(ctx, config, handle)
	}

	// For approximate-only mode, we still need pluginState and blockSizeTokens.
	if scorer.pluginState == nil {
		scorer.pluginState = plugin.NewPluginState(ctx)
	}
	if scorer.blockSizeTokens == 0 {
		scorer.blockSizeTokens = defaultApproxBlockSizeTokens
	}

	return scorer, nil
}

// initPrecise sets up KV events, tokenizer, speculative cache, and subscribers
// for precise scoring mode. This is the existing Phase 1 initialization.
func (s *PrecisePrefixCacheScorer) initPrecise(ctx context.Context, config PrecisePrefixCachePluginConfig) error {
	if config.TokenProcessorConfig == nil {
		config.TokenProcessorConfig = kvblock.DefaultTokenProcessorConfig()
	}

	tokenProcessor, err := kvblock.NewChunkedTokenDatabase(config.TokenProcessorConfig)
	if err != nil {
		return fmt.Errorf("failed to create token processor: %w", err)
	}

	// initialize the indexer
	kvCacheIndexer, err := kvcache.NewKVCacheIndexer(ctx, config.IndexerConfig, tokenProcessor)
	if err != nil {
		return fmt.Errorf("failed to create `kvcache.Indexer`: %w", err)
	}

	go kvCacheIndexer.Run(ctx)

	// initialize the KV block scorer with the same config the indexer uses
	scorerConfig := kvcache.DefaultKVBlockScorerConfig()
	if config.IndexerConfig != nil && config.IndexerConfig.BackendConfigs != nil {
		scorerConfig.BackendConfigs = config.IndexerConfig.BackendConfigs
	}
	kvBlockScorer, err := kvcache.NewKVBlockScorer(scorerConfig)
	if err != nil {
		return fmt.Errorf("failed to create KVBlockScorer: %w", err)
	}

	// initialize the KV-events pool
	pool := kvevents.NewPool(config.KVEventsConfig, kvCacheIndexer.KVBlockIndex(), tokenProcessor, engineadapter.NewVLLMAdapter())
	pool.Start(ctx)

	subscribersManager := kvevents.NewSubscriberManager(pool)
	var subscribersCache *ttlcache.Cache[string, struct{}]

	// initialize the subscribers cache only if endpoint discovery is enabled
	if config.KVEventsConfig.DiscoverPods {
		subscriptionTimeout := 10 * time.Minute
		subscribersCache = ttlcache.New[string, struct{}](
			ttlcache.WithTTL[string, struct{}](subscriptionTimeout),
		)
		subscribersCache.OnEviction(func(ctx context.Context, reason ttlcache.EvictionReason,
			item *ttlcache.Item[string, struct{}],
		) {
			if reason == ttlcache.EvictionReasonExpired {
				subscribersManager.RemoveSubscriber(ctx, item.Key())
			}
		})
		go cleanCachePeriodically(ctx, subscribersCache, subscriptionTimeout)
	}
	if config.KVEventsConfig.ZMQEndpoint != "" {
		if err := subscribersManager.EnsureSubscriber(ctx, "local-subscriber",
			config.KVEventsConfig.ZMQEndpoint, config.KVEventsConfig.TopicFilter, false); err != nil {
			return fmt.Errorf("failed to create local subscriber for global socket mode: %w", err)
		}
	}

	// Initialize speculative indexing components only when enabled
	var speculativeCache *ttlcache.Cache[string, *speculativeEntries]
	var speculativeTTL time.Duration
	if config.SpeculativeIndexing {
		if config.SpeculativeTTL != "" {
			speculativeTTL, err = time.ParseDuration(config.SpeculativeTTL)
			if err != nil {
				return fmt.Errorf("invalid speculativeTTL %q: %w", config.SpeculativeTTL, err)
			}
		}
		if speculativeTTL <= 0 {
			speculativeTTL = defaultSpeculativeTTL
		}

		speculativeCache = ttlcache.New[string, *speculativeEntries](
			ttlcache.WithTTL[string, *speculativeEntries](speculativeTTL),
		)
		speculativeCache.OnEviction(func(_ context.Context, reason ttlcache.EvictionReason,
			item *ttlcache.Item[string, *speculativeEntries],
		) {
			if reason != ttlcache.EvictionReasonExpired {
				return
			}
			entries := item.Value()
			for _, reqKey := range entries.blockKeys {
				//nolint:errcheck // best-effort cleanup on TTL expiry
				kvCacheIndexer.KVBlockIndex().Evict(context.Background(), reqKey, kvblock.RequestKey, entries.podEntries)
			}
		})
		go cleanCachePeriodically(ctx, speculativeCache, speculativeTTL)
	}

	s.kvCacheIndexer = kvCacheIndexer
	s.kvBlockScorer = kvBlockScorer
	s.subscribersCache = subscribersCache
	s.subscribersManager = subscribersManager
	s.kvEventsConfig = config.KVEventsConfig
	s.pluginState = plugin.NewPluginState(ctx)
	s.speculativeCache = speculativeCache
	s.speculativeTTL = speculativeTTL
	s.blockSizeTokens = config.TokenProcessorConfig.BlockSize
	s.speculativeEnabled = config.SpeculativeIndexing

	return nil
}

// initApproximate sets up the xxhash-based indexer and inactive pod cleanup
// for approximate scoring mode.
func (s *PrecisePrefixCacheScorer) initApproximate(ctx context.Context, config PrecisePrefixCachePluginConfig, handle plugin.Handle) {
	approxCfg := config.ApproximateConfig
	if approxCfg == nil {
		approxCfg = defaultApproximateConfig()
	}
	if approxCfg.BlockSizeTokens <= 0 {
		approxCfg.BlockSizeTokens = defaultApproxBlockSizeTokens
	}
	if approxCfg.MaxPrefixBlocksToMatch <= 0 {
		approxCfg.MaxPrefixBlocksToMatch = defaultApproxMaxPrefixBlocks
	}
	if approxCfg.LRUCapacityPerServer <= 0 {
		approxCfg.LRUCapacityPerServer = defaultApproxLRUCapacity
	}

	s.approxIndexer = newApproximateIndexer(ctx, approxCfg.LRUCapacityPerServer)
	s.approxBlockSize = approxCfg.BlockSizeTokens
	s.approxMaxBlocks = approxCfg.MaxPrefixBlocksToMatch
	s.approxAutoTune = approxCfg.AutoTune
	s.approxPluginState = plugin.NewPluginState(ctx)

	if approxCfg.RecencyHalfLife != "" {
		hl, err := time.ParseDuration(approxCfg.RecencyHalfLife)
		if err != nil {
			log.FromContext(ctx).WithName(s.typedName.String()).Error(err,
				"Invalid recencyHalfLife, falling back to unweighted scoring",
				"recencyHalfLife", approxCfg.RecencyHalfLife)
		} else if hl <= 0 {
			log.FromContext(ctx).WithName(s.typedName.String()).Info(
				"Non-positive recencyHalfLife, using unweighted scoring",
				"recencyHalfLife", approxCfg.RecencyHalfLife)
		} else {
			s.approxRecencyHalfLife = hl
		}
	}

	// Start inactive pod cleanup goroutine if handle is available.
	if handle != nil {
		go s.cleanUpInactivePods(ctx, handle)
	}
}

// PrecisePrefixCacheScorer implements the framework.Scorer interface.
// The scorer implements precise prefix-cache KV-block locality scoring.
// It uses the `kvcache.Indexer` to score endpoints based on the KV-cache index
// state, and the `kvevents.Pool` to subscribe to KV-cache events
// to keep the internal KV-cache index state up-to-date.
//
// With speculative indexing, the scorer also implements PrepareDataPlugin and
// PreRequest to proactively populate the index with expected cache entries
// immediately after a routing decision, closing the blind spot between the
// routing decision and the arrival of actual KV events from the engine.
type PrecisePrefixCacheScorer struct {
	typedName      plugin.TypedName
	kvCacheIndexer kvCacheIndexer

	// until the IGW data-layer is ready to provide endpoint events,
	// we maintain a TTL cache of known endpoints that are discovered through
	// the scoring process. If a endpoint is not in the received endpoints list
	// during scoring for a certain period, we consider it gone and
	// stop its KV events subscription.
	subscribersCache   *ttlcache.Cache[string, struct{}]
	subscribersManager *kvevents.SubscriberManager
	kvEventsConfig     *kvevents.Config

	// pluginState stores per-request data (block keys, scores) shared
	// between PrepareRequestData, Score, and PreRequest extension points.
	pluginState *plugin.PluginState

	// speculativeCache tracks speculative entries added to the index so that
	// they can be evicted when their TTL expires.
	speculativeCache *ttlcache.Cache[string, *speculativeEntries]
	speculativeTTL   time.Duration

	// kvBlockScorer scores pods based on block hits with device-backend weights.
	kvBlockScorer kvcache.KVBlockScorer

	// blockSizeTokens is the number of tokens per KV-block, used for
	// constructing PrefixCacheMatchInfo in PrepareRequestData.
	blockSizeTokens int

	// speculativeEnabled controls whether speculative indexing is active.
	speculativeEnabled bool

	// --- Approximate mode fields ---

	// mode is the active scoring mode: "precise", "approximate", or "unified".
	mode string

	// approxIndexer is the self-reported LRU indexer for approximate mode.
	approxIndexer *approximateIndexer

	// approxBlockSize is the block size in tokens for approximate hashing.
	approxBlockSize int

	// approxMaxBlocks is the max prefix blocks to hash per request.
	approxMaxBlocks int

	// approxAutoTune enables dynamic block size from endpoint metrics.
	approxAutoTune bool

	// approxRecencyHalfLife is the half-life for recency-weighted scoring.
	// Zero means disabled (use standard block-count scoring).
	approxRecencyHalfLife time.Duration

	// approxPluginState stores per-request data for approximate mode,
	// separate from the precise mode pluginState to avoid key collisions in unified mode.
	approxPluginState *plugin.PluginState
}

// TypedName returns the typed name of the plugin.
func (s *PrecisePrefixCacheScorer) TypedName() plugin.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *PrecisePrefixCacheScorer) WithName(name string) *PrecisePrefixCacheScorer {
	s.typedName.Name = name
	return s
}

// Category returns the preference the scorer applies when scoring candidate endpoints.
func (s *PrecisePrefixCacheScorer) Category() scheduling.ScorerCategory {
	return scheduling.Affinity
}

// --- PrepareDataPlugin implementation ---

// Produces declares the data keys this plugin writes to endpoints.
func (s *PrecisePrefixCacheScorer) Produces() map[string]any {
	return map[string]any{
		dl_prefix.PrefixCacheMatchInfoKey: dl_prefix.PrefixCacheMatchInfo{},
	}
}

// Consumes declares the data keys this plugin requires from other plugins.
func (s *PrecisePrefixCacheScorer) Consumes() map[string]any {
	return map[string]any{}
}

// PrepareRequestData computes block keys, looks up the index, and stores
// per-endpoint prefix match information. The computed block keys and scores
// are saved to PluginState for reuse by Score() and PreRequest().
func (s *PrecisePrefixCacheScorer) PrepareRequestData(ctx context.Context,
	request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) error {
	if request == nil || request.Body == nil {
		return nil
	}

	switch s.mode {
	case ModeApproximate:
		return s.prepareDataApproximate(ctx, request, endpoints)
	case ModeUnified:
		// Always run precise in unified mode so endpoint PrefixCacheMatchInfo
		// and PluginState reflect precise hits. Errors are non-fatal.
		if err := s.prepareDataPrecise(ctx, request, endpoints); err != nil {
			log.FromContext(ctx).WithName(s.typedName.String()).Error(err,
				"Precise PrepareRequestData failed, continuing with approximate")
		}
		return s.prepareDataApproximate(ctx, request, endpoints)
	default: // ModePrecise
		if !s.speculativeEnabled {
			return nil
		}
		return s.prepareDataPrecise(ctx, request, endpoints)
	}
}

// prepareDataPrecise is the existing precise mode PrepareRequestData logic.
func (s *PrecisePrefixCacheScorer) prepareDataPrecise(ctx context.Context,
	request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) error {
	logger := log.FromContext(ctx).WithName(s.typedName.String())

	// 1. Compute block keys from the request
	blockKeys, err := s.computeBlockKeys(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to compute block keys: %w", err)
	}
	if len(blockKeys) == 0 {
		return nil
	}

	// 2. Build pod set from endpoints for filtered lookup
	podSet := extractPodSet(endpoints)

	// 3. Lookup index for matching pods
	keyToPods, err := s.kvCacheIndexer.KVBlockIndex().Lookup(ctx, blockKeys, podSet)
	if err != nil {
		return fmt.Errorf("failed to lookup block keys: %w", err)
	}

	// 4. Compute per-pod scores using KVBlockScorer (supports device-backend weights)
	scores, err := s.kvBlockScorer.Score(ctx, blockKeys, keyToPods)
	if err != nil {
		return fmt.Errorf("failed to score block keys: %w", err)
	}

	// 5. Store PrefixCacheMatchInfo on each endpoint
	blockSize := s.getBlockSizeTokens()
	for _, ep := range endpoints {
		md := ep.GetMetadata()
		if md == nil {
			continue
		}
		addr := fmt.Sprintf("%s:%s", md.Address, md.Port)
		matchLen := int(scores[addr])
		ep.Put(dl_prefix.PrefixCacheMatchInfoKey, dl_prefix.NewPrefixCacheMatchInfo(matchLen, len(blockKeys), blockSize))
	}

	// 6. Save to PluginState for Score() and PreRequest()
	s.pluginState.Write(request.RequestId, stateKey, &precisePluginState{
		blockKeys: blockKeys,
		scores:    scores,
	})

	logger.V(logutil.TRACE).Info("PrepareRequestData (precise) completed",
		"blockKeys", len(blockKeys), "scores", scores)

	return nil
}

// prepareDataApproximate hashes the prompt with xxhash, matches against
// the self-reported LRU indexer, and stores per-endpoint match info.
func (s *PrecisePrefixCacheScorer) prepareDataApproximate(ctx context.Context,
	request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) error {
	logger := log.FromContext(ctx).WithName(s.typedName.String())

	blockSize := s.getApproxBlockSize(endpoints)
	hashes := hashPrompt(ctx, request, blockSize, s.approxMaxBlocks)
	if len(hashes) == 0 {
		return nil
	}

	// Match longest prefix in local LRU indexer.
	// Use recency-weighted scoring when half-life is configured.
	var serverScores map[ApproximateServerID]float64
	if s.approxRecencyHalfLife > 0 {
		serverScores = s.approxIndexer.MatchLongestPrefixWeighted(hashes, time.Now(), s.approxRecencyHalfLife)
	} else {
		intScores := s.approxIndexer.MatchLongestPrefix(hashes)
		serverScores = make(map[ApproximateServerID]float64, len(intScores))
		for k, v := range intScores {
			serverScores[k] = float64(v)
		}
	}

	// Store PrefixCacheMatchInfo on endpoints.
	// In unified mode, only overwrite if approximate has a better match
	// (more cached tokens) than what precise already wrote.
	for _, ep := range endpoints {
		md := ep.GetMetadata()
		if md == nil {
			continue
		}
		sid := ApproximateServerID(md.NamespacedName)
		matchLen := int(math.Round(serverScores[sid]))
		approxInfo := dl_prefix.NewPrefixCacheMatchInfo(matchLen, len(hashes), blockSize)

		if s.mode == ModeUnified {
			if existing, ok := ep.Get(dl_prefix.PrefixCacheMatchInfoKey); ok {
				if existingInfo, ok := existing.(*dl_prefix.PrefixCacheMatchInfo); ok {
					if existingInfo.MatchBlocks()*existingInfo.BlockSizeTokens() >= approxInfo.MatchBlocks()*approxInfo.BlockSizeTokens() {
						continue // keep precise result
					}
				}
			}
		}
		ep.Put(dl_prefix.PrefixCacheMatchInfoKey, approxInfo)
	}

	// Save to PluginState for Score() and PreRequest()
	s.approxPluginState.Write(request.RequestId, approxStateKey, &approximatePluginState{
		hashes:       hashes,
		serverScores: serverScores,
		totalBlocks:  len(hashes),
	})

	logger.V(logutil.TRACE).Info("PrepareRequestData (approximate) completed",
		"hashes", len(hashes), "serverScores", fmt.Sprintf("%v", serverScores))

	return nil
}

// --- Scorer implementation ---

// Score scores the provided endpoint based on cache index state.
// The returned scores are normalized to a range of 0-1.
func (s *PrecisePrefixCacheScorer) Score(ctx context.Context, cycleState *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(ctx, "llm_d.epp.scorer.prefix_cache",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	span.SetAttributes(
		attribute.Int("llm_d.scorer.candidate_endpoints", len(endpoints)),
		attribute.String("llm_d.scorer.mode", s.mode),
	)

	if request == nil {
		span.SetAttributes(attribute.String("llm_d.scorer.result", "skipped_nil_request"))
		return nil
	}
	if request.TargetModel != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", request.TargetModel))
	}
	if request.RequestId != "" {
		span.SetAttributes(attribute.String("gen_ai.request.id", request.RequestId))
	}

	var normalizedScores map[scheduling.Endpoint]float64

	switch s.mode {
	case ModeApproximate:
		normalizedScores = s.scoreApproximate(ctx, cycleState, request, endpoints)
	case ModeUnified:
		precise := s.scorePrecise(ctx, cycleState, request, endpoints)
		approx := s.scoreApproximate(ctx, cycleState, request, endpoints)
		normalizedScores = combineScores(precise, approx)
		// Merge approximate hits into cycleState so downstream plugins (e.g. no_hit_lru)
		// see the combined cache state, not just precise hits.
		s.mergeApproxIntoCycleState(cycleState, request, endpoints)
	default: // ModePrecise
		normalizedScores = s.scorePrecise(ctx, cycleState, request, endpoints)
	}

	// Calculate score distribution for observability
	if len(normalizedScores) > 0 {
		maxScore := 0.0
		totalScore := 0.0
		for _, score := range normalizedScores {
			if score > maxScore {
				maxScore = score
			}
			totalScore += score
		}
		avgScore := totalScore / float64(len(normalizedScores))
		span.SetAttributes(
			attribute.Float64("llm_d.scorer.score.max", maxScore),
			attribute.Float64("llm_d.scorer.score.avg", avgScore),
			attribute.Int("llm_d.scorer.endpoints_scored", len(normalizedScores)),
		)
	}

	return normalizedScores
}

// scorePrecise is the existing precise scoring logic.
func (s *PrecisePrefixCacheScorer) scorePrecise(ctx context.Context, cycleState *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	logger := log.FromContext(ctx).WithName(s.typedName.String())
	debugLogger := logger.V(logutil.DEBUG)

	// Handle pod discovery and subscriber management
	if s.kvEventsConfig != nil && s.kvEventsConfig.DiscoverPods {
		for _, endpoint := range endpoints {
			endpointObj := endpoint.GetMetadata()
			if endpointObj == nil {
				continue
			}
			endpointKey := endpointObj.NamespacedName.String()
			s.subscribersCache.Set(endpointKey, struct{}{}, 0)

			if err := s.subscribersManager.EnsureSubscriber(context.Background(), endpointKey,
				fmt.Sprintf("tcp://%s:%d", endpointObj.Address, s.kvEventsConfig.PodDiscoveryConfig.SocketPort),
				s.kvEventsConfig.TopicFilter, true); err != nil {
				logger.Error(err, "Failed to ensure KV-events subscriber for endpoint", "endpoint", endpointKey,
					"endpoint", endpointObj.Address)
				continue
			}
		}
	}

	// Try to reuse pre-computed scores from PrepareRequestData
	var scores map[string]float64
	if pluginStateData, err := plugin.ReadPluginStateKey[*precisePluginState](
		s.pluginState, request.RequestId, stateKey); err == nil {
		scores = pluginStateData.scores
		debugLogger.Info("Reusing pre-computed scores from PrepareRequestData")
	} else {
		var scoreErr error
		scores, scoreErr = s.getScores(ctx, cycleState, request)
		if scoreErr != nil {
			logger.Error(scoreErr, "Failed to get endpoint scores")
			return nil
		}
	}
	debugLogger.Info("Got endpoint scores (precise)", "scores", scores)

	endpointToKey := func(endpoint scheduling.Endpoint) (string, bool) {
		metadata := endpoint.GetMetadata()
		if metadata == nil {
			return "", false
		}
		return fmt.Sprintf("%s:%s", metadata.Address, metadata.Port), true
	}

	// Write prefix cache state to cycle state.
	prefixCacheState := &prefix.SchedulingContextState{
		PrefixHashes:       []prefix.BlockHash{},
		PrefixCacheServers: map[prefix.ServerID]int{},
	}
	for _, endpoint := range endpoints {
		key, ok := endpointToKey(endpoint)
		if !ok {
			continue
		}
		if sc, exists := scores[key]; exists && sc > 0 {
			prefixCacheState.PrefixCacheServers[prefix.ServerID(endpoint.GetMetadata().NamespacedName)] = int(sc)
		}
	}
	cycleState.Write(plugin.StateKey(s.typedName.String()), prefixCacheState)

	return indexedScoresToNormalizedScoredPods(endpoints, endpointToKey, scores)
}

// scoreApproximate scores endpoints using the self-reported LRU indexer.
func (s *PrecisePrefixCacheScorer) scoreApproximate(ctx context.Context, cycleState *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	logger := log.FromContext(ctx).WithName(s.typedName.String())

	// Try PluginState first (from PrepareRequestData)
	var serverScores map[ApproximateServerID]float64
	var totalBlocks int

	if state, err := plugin.ReadPluginStateKey[*approximatePluginState](
		s.approxPluginState, request.RequestId, approxStateKey); err == nil {
		serverScores = state.serverScores
		totalBlocks = state.totalBlocks
		logger.V(logutil.DEBUG).Info("Reusing pre-computed approximate scores")
	} else {
		// Fallback: compute inline
		blockSize := s.getApproxBlockSize(endpoints)
		hashes := hashPrompt(ctx, request, blockSize, s.approxMaxBlocks)
		if len(hashes) == 0 {
			return nil
		}

		if s.approxRecencyHalfLife > 0 {
			serverScores = s.approxIndexer.MatchLongestPrefixWeighted(hashes, time.Now(), s.approxRecencyHalfLife)
		} else {
			intScores := s.approxIndexer.MatchLongestPrefix(hashes)
			serverScores = make(map[ApproximateServerID]float64, len(intScores))
			for k, v := range intScores {
				serverScores[k] = float64(v)
			}
		}
		totalBlocks = len(hashes)

		// Save for PreRequest
		s.approxPluginState.Write(request.RequestId, approxStateKey, &approximatePluginState{
			hashes:       hashes,
			serverScores: serverScores,
			totalBlocks:  totalBlocks,
		})
	}

	if totalBlocks == 0 {
		return nil
	}

	// Write prefix cache state to cycle state (same format as precise).
	prefixCacheState := &prefix.SchedulingContextState{
		PrefixHashes:       []prefix.BlockHash{},
		PrefixCacheServers: map[prefix.ServerID]int{},
	}

	result := make(map[scheduling.Endpoint]float64, len(endpoints))
	for _, ep := range endpoints {
		md := ep.GetMetadata()
		if md == nil {
			continue
		}
		sid := ApproximateServerID(md.NamespacedName)
		score := serverScores[sid]
		result[ep] = score / float64(totalBlocks)

		if score > 0 {
			prefixCacheState.PrefixCacheServers[prefix.ServerID(md.NamespacedName)] = int(math.Round(score))
		}
	}

	// Only write to cycleState if we're in approximate-only mode (avoid conflict with precise).
	if s.mode == ModeApproximate {
		cycleState.Write(plugin.StateKey(s.typedName.String()), prefixCacheState)
	}

	return result
}

// combineScores merges precise and approximate scores for unified mode.
//
// When precise scores show no differentiation (all equal — which happens when
// min-max normalization maps all-zero raw scores to all-1.0), we use
// approximate scores directly. Otherwise we take the max per endpoint.
// This prevents precise all-1.0 scores from masking approximate cache signals.
func combineScores(precise, approx map[scheduling.Endpoint]float64) map[scheduling.Endpoint]float64 {
	if len(precise) == 0 {
		return approx
	}
	if len(approx) == 0 {
		return precise
	}

	// Check if precise scores are all identical (no differentiation).
	// This happens when min-max normalization maps all-zero raw scores to all-1.0.
	// In that case, precise adds no signal, so use approximate scores directly.
	// Only applies when there are 2+ endpoints (single endpoint is trivially "all same").
	if len(precise) >= 2 {
		preciseUndifferentiated := true
		var firstScore float64
		first := true
		for _, ps := range precise {
			if first {
				firstScore = ps
				first = false
				continue
			}
			if ps != firstScore {
				preciseUndifferentiated = false
				break
			}
		}
		if preciseUndifferentiated {
			return approx
		}
	}

	combined := make(map[scheduling.Endpoint]float64, len(precise))
	for ep, ps := range precise {
		combined[ep] = ps
	}
	for ep, as := range approx {
		if as > combined[ep] {
			combined[ep] = as
		}
	}
	return combined
}

// mergeApproxIntoCycleState reads the approximate PluginState and merges any
// approximate hits into the existing cycleState written by scorePrecise.
// This ensures downstream plugins (e.g. no_hit_lru) see the combined view.
func (s *PrecisePrefixCacheScorer) mergeApproxIntoCycleState(cycleState *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) {
	state, err := plugin.ReadPluginStateKey[*approximatePluginState](
		s.approxPluginState, request.RequestId, approxStateKey)
	if err != nil || len(state.serverScores) == 0 {
		return
	}

	// Read existing state written by scorePrecise.
	existing, readErr := scheduling.ReadCycleStateKey[*prefix.SchedulingContextState](
		cycleState, plugin.StateKey(s.typedName.String()))
	if readErr != nil {
		return
	}

	// Merge approximate hits: take max of precise vs approximate per server.
	for _, ep := range endpoints {
		md := ep.GetMetadata()
		if md == nil {
			continue
		}
		sid := prefix.ServerID(md.NamespacedName)
		approxMatch := int(math.Round(state.serverScores[ApproximateServerID(md.NamespacedName)]))
		if approxMatch > existing.PrefixCacheServers[sid] {
			existing.PrefixCacheServers[sid] = approxMatch
		}
	}
	cycleState.Write(plugin.StateKey(s.typedName.String()), existing)
}

// --- PreRequest implementation ---

// PreRequest records cache entries after a scheduling decision.
// In precise mode, it adds speculative entries to the KV index.
// In approximate mode, it self-reports routing decisions to the LRU indexer.
func (s *PrecisePrefixCacheScorer) PreRequest(ctx context.Context,
	request *scheduling.LLMRequest, schedulingResult *scheduling.SchedulingResult) {
	switch s.mode {
	case ModeApproximate:
		s.preRequestApproximate(ctx, request, schedulingResult)
	case ModeUnified:
		if s.speculativeEnabled {
			s.preRequestPrecise(ctx, request, schedulingResult)
		}
		s.preRequestApproximate(ctx, request, schedulingResult)
	default: // ModePrecise
		if !s.speculativeEnabled {
			return
		}
		s.preRequestPrecise(ctx, request, schedulingResult)
	}
}

// preRequestPrecise adds speculative entries to the precise KV index.
func (s *PrecisePrefixCacheScorer) preRequestPrecise(ctx context.Context,
	request *scheduling.LLMRequest, schedulingResult *scheduling.SchedulingResult) {
	logger := log.FromContext(ctx).WithName(s.typedName.String())

	state, err := plugin.ReadPluginStateKey[*precisePluginState](
		s.pluginState, request.RequestId, stateKey)
	if err != nil {
		logger.V(logutil.TRACE).Info("No plugin state found for PreRequest, skipping speculative indexing",
			"requestID", request.RequestId)
		return
	}
	s.pluginState.Delete(request.RequestId)

	if len(state.blockKeys) == 0 {
		return
	}

	primaryResult := schedulingResult.ProfileResults[schedulingResult.PrimaryProfileName]
	if primaryResult == nil || len(primaryResult.TargetEndpoints) == 0 {
		return
	}
	targetEndpoint := primaryResult.TargetEndpoints[0]

	targetMeta := targetEndpoint.GetMetadata()
	speculativePod := kvblock.PodEntry{
		PodIdentifier: fmt.Sprintf("%s:%s", targetMeta.Address, targetMeta.Port),
		Speculative:   true,
	}

	allPodEntries := []kvblock.PodEntry{speculativePod}

	index := s.kvCacheIndexer.KVBlockIndex()
	if err := index.Add(ctx, nil, state.blockKeys, []kvblock.PodEntry{speculativePod}); err != nil {
		logger.Error(err, "Failed to add speculative entries to index",
			"pod", speculativePod.PodIdentifier)
	}

	// Handle P/D disaggregation
	if pr, exists := schedulingResult.ProfileResults[experimentalPrefillProfile]; exists && len(pr.TargetEndpoints) > 0 {
		prefillMeta := pr.TargetEndpoints[0].GetMetadata()
		prefillPod := kvblock.PodEntry{
			PodIdentifier: fmt.Sprintf("%s:%s", prefillMeta.Address, prefillMeta.Port),
			Speculative:   true,
		}
		if err := index.Add(ctx, nil, state.blockKeys, []kvblock.PodEntry{prefillPod}); err != nil {
			logger.Error(err, "Failed to add speculative entries for prefill endpoint",
				"pod", prefillPod.PodIdentifier)
		}
		allPodEntries = append(allPodEntries, prefillPod)
	}

	s.speculativeCache.Set(request.RequestId, &speculativeEntries{
		blockKeys:  state.blockKeys,
		podEntries: allPodEntries,
	}, s.speculativeTTL)

	logger.V(logutil.TRACE).Info("Added speculative entries",
		"requestID", request.RequestId,
		"pod", speculativePod.PodIdentifier,
		"blockKeys", len(state.blockKeys),
		"ttl", s.speculativeTTL)
}

// preRequestApproximate records the routing decision in the self-reported LRU indexer.
func (s *PrecisePrefixCacheScorer) preRequestApproximate(ctx context.Context,
	request *scheduling.LLMRequest, schedulingResult *scheduling.SchedulingResult) {
	logger := log.FromContext(ctx).WithName(s.typedName.String())

	state, err := plugin.ReadPluginStateKey[*approximatePluginState](
		s.approxPluginState, request.RequestId, approxStateKey)
	if err != nil || len(state.hashes) == 0 {
		return
	}
	s.approxPluginState.Delete(request.RequestId)

	primaryResult := schedulingResult.ProfileResults[schedulingResult.PrimaryProfileName]
	if primaryResult == nil || len(primaryResult.TargetEndpoints) == 0 {
		return
	}

	targetMeta := primaryResult.TargetEndpoints[0].GetMetadata()
	server := ApproximateServerID(targetMeta.NamespacedName)
	numGPUBlocks := 0
	if s.approxAutoTune {
		if m := primaryResult.TargetEndpoints[0].GetMetrics(); m != nil {
			numGPUBlocks = m.CacheNumGPUBlocks
		}
	}
	s.approxIndexer.Add(state.hashes, server, numGPUBlocks)

	// P/D disaggregation: also record for prefill endpoint
	if pr, exists := schedulingResult.ProfileResults[experimentalPrefillProfile]; exists && len(pr.TargetEndpoints) > 0 {
		prefillMeta := pr.TargetEndpoints[0].GetMetadata()
		prefillServer := ApproximateServerID(prefillMeta.NamespacedName)
		prefillGPUBlocks := 0
		if s.approxAutoTune {
			if m := pr.TargetEndpoints[0].GetMetrics(); m != nil {
				prefillGPUBlocks = m.CacheNumGPUBlocks
			}
		}
		s.approxIndexer.Add(state.hashes, prefillServer, prefillGPUBlocks)
	}

	logger.V(logutil.TRACE).Info("Recorded approximate routing decision",
		"requestID", request.RequestId,
		"server", server,
		"hashes", len(state.hashes))
}

// --- Internal helper methods ---

// computeBlockKeys extracts block keys from an LLM request by tokenizing
// the prompt and computing KV-block hashes.
func (s *PrecisePrefixCacheScorer) computeBlockKeys(ctx context.Context,
	request *scheduling.LLMRequest) ([]kvblock.BlockHash, error) {
	if request.Body == nil {
		return nil, nil
	}

	// Chat completions path
	if request.Body.ChatCompletions != nil {
		renderReq := preparedata.ChatCompletionsToRenderChatRequest(request.Body.ChatCompletions)

		return s.kvCacheIndexer.ComputeBlockKeys(ctx, renderReq, "", request.TargetModel)
	}

	// Regular completions path
	if request.Body.Completions != nil {
		return s.kvCacheIndexer.ComputeBlockKeys(ctx, nil, request.Body.Completions.Prompt, request.TargetModel)
	}

	return nil, nil
}

// extractPodSet builds a set of pod identifiers from endpoints for filtered index lookups.
func extractPodSet(endpoints []scheduling.Endpoint) sets.Set[string] {
	podSet := sets.New[string]()
	for _, ep := range endpoints {
		if m := ep.GetMetadata(); m != nil {
			podSet.Insert(fmt.Sprintf("%s:%s", m.Address, m.Port))
		}
	}
	return podSet
}

// getBlockSizeTokens returns the block size in tokens from the token processor config.
func (s *PrecisePrefixCacheScorer) getBlockSizeTokens() int {
	return s.blockSizeTokens
}

// getScores retrieves the endpoint scores from the KV-cache indexer
// based on the provided LLM request.
// If tokenized prompt data is found in CycleState (written by the tokenizer
// scorer plugin), it calls ScoreTokens directly, bypassing prompt/chat tokenization.
// Otherwise, chat completions and regular completions are tokenized internally.
func (s *PrecisePrefixCacheScorer) getScores(ctx context.Context, cycleState *scheduling.CycleState, request *scheduling.LLMRequest) (map[string]float64, error) {
	logger := log.FromContext(ctx).WithName(s.typedName.String())
	traceLogger := logger.V(logutil.TRACE)

	traceLogger.Info("Getting scores",
		"isChatCompletions", request.Body != nil && request.Body.ChatCompletions != nil,
		"isCompletions", request.Body != nil && request.Body.Completions != nil)

	// Read tokenized prompt from CycleState, written by the tokenizer scorer plugin.
	if tp, err := scheduling.ReadCycleStateKey[*preparedata.TokenizedPromptState](
		cycleState, preparedata.TokenizedPromptStateKey); err == nil && len(tp.TokenIDs) > 0 {
		traceLogger.Info("tokens found in CycleState, skipping tokenization")

		var extraFeatures []*kvblock.BlockExtraFeatures
		if tp.MMFeatures != nil {
			extraFeatures = kvblock.ComputeBlockExtraFeatures(
				tp.MMFeatures.MMHashes, tp.MMFeatures.MMPlaceholders,
				s.blockSizeTokens, len(tp.TokenIDs))
		}

		scores, err := s.kvCacheIndexer.ScoreTokens(ctx, tp.TokenIDs, request.TargetModel, nil, extraFeatures)
		if err != nil {
			return nil, fmt.Errorf("failed to get endpoint scores for tokens: %w", err)
		}
		return scores, nil
	}

	// The upstream parser guarantees exactly one body is populated, but we defensively prioritize chat completions.
	// If an unexpected dual payload slips through (parser regression/new client), log it and use chat semantics.
	if request.Body != nil && request.Body.ChatCompletions != nil {
		if request.Body.Completions != nil {
			traceLogger.Info("Both chat/completions and completions present; defaulting to chat/completions")
		}

		renderReq := preparedata.ChatCompletionsToRenderChatRequest(request.Body.ChatCompletions)

		traceLogger.Info("Processing chat completion request",
			"messagesCount", len(renderReq.Conversation),
			"toolsCount", len(renderReq.Tools),
			"documentsCount", len(renderReq.Documents))

		scores, err := s.kvCacheIndexer.GetPodScores(ctx, renderReq, "", request.TargetModel, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get endpoint scores for chat/completions: %w", err)
		}
		return scores, nil
	}

	// For regular completions, use the prompt directly
	if request.Body != nil && request.Body.Completions != nil {
		prompt := request.Body.Completions.Prompt
		traceLogger.Info("Using completion prompt directly", "promptLength", len(prompt))

		scores, err := s.kvCacheIndexer.GetPodScores(ctx, nil, prompt, request.TargetModel, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get endpoint scores for completions: %w", err)
		}
		return scores, nil
	}

	return nil, errors.New("no valid input found in request")
}

// --- Approximate mode helpers ---

// getApproxBlockSize returns the block size for approximate hashing,
// optionally using endpoint metrics if AutoTune is enabled.
func (s *PrecisePrefixCacheScorer) getApproxBlockSize(endpoints []scheduling.Endpoint) int {
	if s.approxAutoTune && len(endpoints) > 0 {
		if m := endpoints[0].GetMetrics(); m != nil && m.CacheBlockSize > 0 {
			return m.CacheBlockSize
		}
	}
	return s.approxBlockSize
}

// cleanUpInactivePods periodically removes pods from the approximate indexer
// that are no longer in the active pod list.
func (s *PrecisePrefixCacheScorer) cleanUpInactivePods(ctx context.Context, handle plugin.Handle) {
	ticker := time.NewTicker(defaultPodCleanupInterval)
	defer ticker.Stop()

	logger := log.FromContext(ctx).WithName(s.typedName.String())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			activePods := make(map[ApproximateServerID]struct{})
			for _, pod := range handle.PodList() {
				activePods[ApproximateServerID(pod)] = struct{}{}
			}
			for _, tracked := range s.approxIndexer.Pods() {
				if _, active := activePods[tracked]; !active {
					s.approxIndexer.RemovePod(tracked)
					logger.V(logutil.VERBOSE).Info("Removed inactive pod from approximate indexer",
						"pod", tracked)
				}
			}
		}
	}
}
