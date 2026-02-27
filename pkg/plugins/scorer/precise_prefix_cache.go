package scorer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/prefix"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

const (
	// PrecisePrefixCachePluginType is the type-name of the PrecisePrefixCacheScorer plugin.
	PrecisePrefixCachePluginType = "precise-prefix-cache-scorer"

	// defaultSpeculativeTTL is the default TTL for speculative entries.
	// This should be long enough for the vLLM engine to process the request
	// and publish BlockStored events, after which confirmed entries replace them.
	defaultSpeculativeTTL = 30 * time.Second

	// stateKey is the PluginState key used to share data between
	// PrepareRequestData, Score, and PreRequest.
	stateKey = plugin.StateKey("precise-prefix-cache-state")

	// prefixCacheMatchInfoKey is the endpoint data key for prefix cache match
	// information. Once the upstream GIE attrprefix package is available in the
	// pinned dependency, replace this with attrprefix.PrefixCacheMatchInfoKey.
	prefixCacheMatchInfoKey = "PrefixCacheMatchInfoKey"

	// experimentalPrefillProfile is the profile name for P/D disaggregation mode.
	experimentalPrefillProfile = "prefill"
)

// prefixCacheMatchInfo mirrors the upstream attrprefix.PrefixCacheMatchInfo.
// Replace with the upstream type once the GIE dependency is updated.
type prefixCacheMatchInfo struct {
	matchBlocks     int
	totalBlocks     int
	blockSizeTokens int
}

// Clone implements datalayer.Cloneable.
func (p *prefixCacheMatchInfo) Clone() fwkdl.Cloneable {
	return &prefixCacheMatchInfo{
		matchBlocks:     p.matchBlocks,
		totalBlocks:     p.totalBlocks,
		blockSizeTokens: p.blockSizeTokens,
	}
}

// PrecisePrefixCachePluginConfig holds the configuration for the
// PrecisePrefixCacheScorer plugin.
type PrecisePrefixCachePluginConfig struct {
	// TokenProcessorConfig holds the configuration for the `kvblock.TokenProcessor` which is
	// used to process tokens into KV-block keys.
	TokenProcessorConfig *kvblock.TokenProcessorConfig `json:"tokenProcessorConfig"`
	// IndexerConfig holds the configuration for the `kvcache.Indexer` which is
	// used to score endpoints based on the KV-cache index state.
	IndexerConfig *kvcache.Config `json:"indexerConfig"`
	// KVEventsConfig holds the configuration for the `kvevents.Pool` which is
	// used to subscribe to KV-cache events and update the internal KV-cache
	// index state.
	KVEventsConfig *kvevents.Config `json:"kvEventsConfig"`
	// SpeculativeIndexing enables speculative indexing. When true, the plugin
	// proactively adds predicted cache entries to the index immediately after
	// a routing decision (via PrepareRequestData and PreRequest), closing the
	// blind spot between routing and KV event arrival.
	// When false, only confirmed KV events populate the index.
	SpeculativeIndexing bool `json:"speculativeIndexing"`
	// SpeculativeTTL is the time-to-live for speculative index entries.
	// After this duration, speculative entries are evicted from the index.
	// If zero, defaultSpeculativeTTL is used. Only used when SpeculativeIndexing is true.
	SpeculativeTTL time.Duration `json:"speculativeTTL"`
}

// compile-time type assertions
var (
	_ scheduling.Scorer              = &PrecisePrefixCacheScorer{}
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

// PrecisePrefixCachePluginFactory defines the factory function for creating
// a new instance of the PrefixCacheTrackingPlugin.
func PrecisePrefixCachePluginFactory(name string, rawParameters json.RawMessage,
	handle plugin.Handle) (plugin.Plugin, error) {
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

	// Validate model name is set
	if parameters.IndexerConfig == nil || parameters.IndexerConfig.TokenizersPoolConfig == nil || parameters.IndexerConfig.TokenizersPoolConfig.ModelName == "" {
		return nil, errors.New("modelName is required in indexerConfig.tokenizersPoolConfig")
	}

	scorer, err := New(handle.Context(), parameters)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s plugin: %w", PrecisePrefixCachePluginType, err)
	}

	return scorer.WithName(name), nil
}

// New initializes a new prefix Plugin and returns its pointer.
// It sets up the `kvcache.Indexer` and `kvevents.Pool`
// based on the provided configuration. The `kvevents.Pool` is started
// in a goroutine to listen for KV-cache events and update the internal
// KV-cache index state. The `kvcache.Indexer` is also started in a goroutine
// to score endpoints based on the KV-cache index state.
//
// If the configuration is invalid or if the indexer fails to initialize,
// an error is returned.
func New(ctx context.Context, config PrecisePrefixCachePluginConfig) (*PrecisePrefixCacheScorer, error) {
	if config.TokenProcessorConfig == nil {
		config.TokenProcessorConfig = kvblock.DefaultTokenProcessorConfig()
	}

	tokenProcessor := kvblock.NewChunkedTokenDatabase(config.TokenProcessorConfig)

	// initialize the indexer
	kvCacheIndexer, err := kvcache.NewKVCacheIndexer(ctx, config.IndexerConfig, tokenProcessor)
	if err != nil {
		return nil, fmt.Errorf("failed to create `kvcache.Indexer`: %w", err)
	}

	go kvCacheIndexer.Run(ctx)

	// initialize the KV-events pool
	pool := kvevents.NewPool(config.KVEventsConfig, kvCacheIndexer.KVBlockIndex(), tokenProcessor)
	pool.Start(ctx)

	subscribersManager := kvevents.NewSubscriberManager(pool)
	var subscribersCache *ttlcache.Cache[string, struct{}]

	// initialize the subscribers cache only if endpoint discovery is enabled
	if config.KVEventsConfig.DiscoverPods {
		// initialize the subscribers TTL cache
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
		// setup local subscriber to support global socket mode
		if err := subscribersManager.EnsureSubscriber(ctx, "local-subscriber",
			config.KVEventsConfig.ZMQEndpoint, config.KVEventsConfig.TopicFilter, false); err != nil {
			return nil, fmt.Errorf("failed to create local subscriber for global socket mode: %w", err)
		}
	}

	// Initialize speculative indexing components only when enabled
	var speculativeCache *ttlcache.Cache[string, *speculativeEntries]
	var speculativeTTL time.Duration
	if config.SpeculativeIndexing {
		speculativeTTL = config.SpeculativeTTL
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
				// Evict speculative entries from the index.
				// Speculative entries were added without engineKey mapping (nil engineKeys),
				// so Evict() falls back to treating the key as a requestKey directly.
				//nolint:errcheck // best-effort cleanup on TTL expiry
				kvCacheIndexer.KVBlockIndex().Evict(context.Background(), reqKey, entries.podEntries)
			}
		})
		go speculativeCache.Start()
	}

	return &PrecisePrefixCacheScorer{
		typedName:          plugin.TypedName{Type: PrecisePrefixCachePluginType},
		kvCacheIndexer:     kvCacheIndexer,
		subscribersCache:   subscribersCache,
		subscribersManager: subscribersManager,
		kvEventsConfig:     config.KVEventsConfig,
		pluginState:        plugin.NewPluginState(ctx),
		speculativeCache:   speculativeCache,
		speculativeTTL:     speculativeTTL,
		blockSizeTokens:    config.TokenProcessorConfig.BlockSize,
		speculativeEnabled: config.SpeculativeIndexing,
	}, nil
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
	kvCacheIndexer *kvcache.Indexer

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

	// blockSizeTokens is the number of tokens per KV-block, used for
	// constructing PrefixCacheMatchInfo in PrepareRequestData.
	blockSizeTokens int

	// speculativeEnabled controls whether speculative indexing is active.
	speculativeEnabled bool
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
		prefixCacheMatchInfoKey: prefixCacheMatchInfo{},
	}
}

// Consumes declares the data keys this plugin requires from other plugins.
func (s *PrecisePrefixCacheScorer) Consumes() map[string]any {
	return map[string]any{}
}

// PrepareRequestData computes block keys, looks up the index, and stores
// per-endpoint prefix match information. The computed block keys and scores
// are saved to PluginState for reuse by Score() and PreRequest().
// This is a no-op when speculative indexing is disabled.
func (s *PrecisePrefixCacheScorer) PrepareRequestData(ctx context.Context,
	request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) error {
	if !s.speculativeEnabled {
		return nil
	}

	logger := log.FromContext(ctx).WithName(s.typedName.String())

	if request == nil || request.Body == nil {
		return nil
	}

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

	// 4. Compute per-pod scores (longest prefix match)
	scores := computeScoresFromKeyToPods(blockKeys, keyToPods)

	// 5. Store PrefixCacheMatchInfo on each endpoint
	blockSize := s.getBlockSizeTokens()
	for _, ep := range endpoints {
		addr := fmt.Sprintf("%s:%s", ep.GetMetadata().Address, ep.GetMetadata().Port)
		matchLen := int(scores[addr])
		ep.Put(prefixCacheMatchInfoKey, &prefixCacheMatchInfo{
			matchBlocks:     matchLen,
			totalBlocks:     len(blockKeys),
			blockSizeTokens: blockSize,
		})
	}

	// 6. Save to PluginState for Score() and PreRequest()
	s.pluginState.Write(request.RequestId, stateKey, &precisePluginState{
		blockKeys: blockKeys,
		scores:    scores,
	})

	logger.V(logutil.TRACE).Info("PrepareRequestData completed",
		"blockKeys", len(blockKeys), "scores", scores)

	return nil
}

// --- Scorer implementation ---

// Score scores the provided endpoint based on the KVCache index state.
// The returned scores are normalized to a range of 0-1.
// If PrepareRequestData was called beforehand, Score reuses the pre-computed
// results from PluginState. Otherwise, it falls back to computing scores
// directly via getScores (backward compatible).
func (s *PrecisePrefixCacheScorer) Score(ctx context.Context, cycleState *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	// Start tracing span for scoring operation
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(ctx, "llm_d.epp.scorer.prefix_cache",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	logger := log.FromContext(ctx).WithName(s.typedName.String())
	debugLogger := logger.V(logutil.DEBUG)

	// Set initial attributes
	span.SetAttributes(
		attribute.Int("llm_d.scorer.candidate_endpoints", len(endpoints)),
	)

	// Handle pod discovery and subscriber management
	if s.kvEventsConfig.DiscoverPods {
		// update subscribers here temporarily
		for _, endpoint := range endpoints {
			endpointObj := endpoint.GetMetadata()
			if endpointObj == nil {
				continue
			}
			endpointKey := endpointObj.NamespacedName.String()
			s.subscribersCache.Set(endpointKey, struct{}{}, 0) // use default TTL

			if err := s.subscribersManager.EnsureSubscriber(context.Background(), endpointKey, // dont use request ctx
				fmt.Sprintf("tcp://%s:%d", endpointObj.Address, s.kvEventsConfig.PodDiscoveryConfig.SocketPort),
				s.kvEventsConfig.TopicFilter, true); err != nil {
				logger.Error(err, "Failed to ensure KV-events subscriber for endpoint", "endpoint", endpointKey,
					"endpoint", endpointObj.Address)
				continue
			}
		}
	}

	// Early return if request is nil
	if request == nil {
		debugLogger.Info("Request is nil, skipping scoring")
		span.SetAttributes(attribute.String("llm_d.scorer.result", "skipped_nil_request"))
		return nil
	}

	// Set optional request attributes
	if request.TargetModel != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", request.TargetModel))
	}
	if request.RequestId != "" {
		span.SetAttributes(attribute.String("gen_ai.request.id", request.RequestId))
	}

	// Try to reuse pre-computed scores from PrepareRequestData
	var scores map[string]float64
	if state, err := plugin.ReadPluginStateKey[*precisePluginState](
		s.pluginState, request.RequestId, stateKey); err == nil {
		scores = state.scores
		debugLogger.Info("Reusing pre-computed scores from PrepareRequestData")
	} else {
		// Fallback: compute scores directly (backward compatible path)
		var scoreErr error
		scores, scoreErr = s.getScores(ctx, request)
		if scoreErr != nil {
			logger.Error(scoreErr, "Failed to get endpoint scores")
			span.SetStatus(codes.Error, scoreErr.Error())
			return nil
		}
	}
	debugLogger.Info("Got endpoint scores", "scores", scores)

	// Track scoring statistics
	span.SetAttributes(
		attribute.Int("llm_d.scorer.scores_computed", len(scores)),
	)

	endpointToKey := func(endpoint scheduling.Endpoint) (string, bool) {
		metadata := endpoint.GetMetadata()
		if metadata == nil {
			return "", false
		}

		return fmt.Sprintf("%s:%s", metadata.Address, metadata.Port), true
	}

	// Write prefix cache state to cycle state
	state := &prefix.SchedulingContextState{
		PrefixHashes:       []prefix.BlockHash{},
		PrefixCacheServers: map[prefix.ServerID]int{},
	}
	for _, endpoint := range endpoints {
		key, ok := endpointToKey(endpoint)
		if !ok {
			continue
		}
		state.PrefixCacheServers[prefix.ServerID(endpoint.GetMetadata().NamespacedName)] = int(scores[key])
	}
	cycleState.Write(plugin.StateKey(s.typedName.String()), state)

	normalizedScores := indexedScoresToNormalizedScoredPods(endpoints, endpointToKey, scores)

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

// --- PreRequest implementation ---

// PreRequest records speculative entries in the index for the selected endpoint
// immediately after the scheduling decision. This closes the blind spot between
// the routing decision and the arrival of actual KV events from the engine.
// The speculative entries are associated with a TTL and will be automatically
// evicted when the TTL expires.
// This is a no-op when speculative indexing is disabled.
func (s *PrecisePrefixCacheScorer) PreRequest(ctx context.Context,
	request *scheduling.LLMRequest, schedulingResult *scheduling.SchedulingResult) {
	if !s.speculativeEnabled {
		return
	}

	logger := log.FromContext(ctx).WithName(s.typedName.String())

	// 1. Read block keys from PluginState
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

	// 2. Get target endpoint from scheduling result
	primaryResult := schedulingResult.ProfileResults[schedulingResult.PrimaryProfileName]
	if primaryResult == nil || len(primaryResult.TargetEndpoints) == 0 {
		return
	}
	targetEndpoint := primaryResult.TargetEndpoints[0]

	// 3. Build speculative pod entry and add to index
	targetMeta := targetEndpoint.GetMetadata()
	speculativePod := kvblock.PodEntry{
		PodIdentifier: fmt.Sprintf("%s:%s", targetMeta.Address, targetMeta.Port),
		Annotation:    "speculative",
	}

	allPodEntries := []kvblock.PodEntry{speculativePod}

	index := s.kvCacheIndexer.KVBlockIndex()
	// Pass nil engineKeys: speculative entries only need requestKey -> PodEntry mapping.
	// Engine keys will be linked later when confirmed KV events arrive.
	if err := index.Add(ctx, nil, state.blockKeys, []kvblock.PodEntry{speculativePod}); err != nil {
		logger.Error(err, "Failed to add speculative entries to index",
			"pod", speculativePod.PodIdentifier)
	}

	// 4. Handle P/D disaggregation: also add speculative entry for prefill endpoint
	if pr, exists := schedulingResult.ProfileResults[experimentalPrefillProfile]; exists && len(pr.TargetEndpoints) > 0 {
		prefillMeta := pr.TargetEndpoints[0].GetMetadata()
		prefillPod := kvblock.PodEntry{
			PodIdentifier: fmt.Sprintf("%s:%s", prefillMeta.Address, prefillMeta.Port),
			Annotation:    "speculative",
		}
		if err := index.Add(ctx, nil, state.blockKeys, []kvblock.PodEntry{prefillPod}); err != nil {
			logger.Error(err, "Failed to add speculative entries for prefill endpoint",
				"pod", prefillPod.PodIdentifier)
		}
		allPodEntries = append(allPodEntries, prefillPod)
	}

	// 5. Register in TTL cache for automatic eviction
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
		conversations := make([]types.Conversation, len(request.Body.ChatCompletions.Messages))
		for i, msg := range request.Body.ChatCompletions.Messages {
			conversations[i] = types.Conversation{
				Role:    msg.Role,
				Content: msg.Content.Raw,
			}
		}

		renderReq := &types.RenderChatRequest{
			Conversation:              conversations,
			Tools:                     request.Body.ChatCompletions.Tools,
			Documents:                 request.Body.ChatCompletions.Documents,
			ChatTemplate:              request.Body.ChatCompletions.ChatTemplate,
			ReturnAssistantTokensMask: request.Body.ChatCompletions.ReturnAssistantTokensMask,
			ContinueFinalMessage:      request.Body.ChatCompletions.ContinueFinalMessage,
			AddGenerationPrompt:       request.Body.ChatCompletions.AddGenerationPrompt,
			ChatTemplateKWArgs:        request.Body.ChatCompletions.ChatTemplateKWArgs,
		}

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

// computeScoresFromKeyToPods computes per-pod longest prefix match scores
// from the index lookup result. This mirrors the intersection logic in the
// LongestPrefixScorer: a pod's prefix chain ends as soon as it is missing
// from any block key, even if other pods still have that key.
func computeScoresFromKeyToPods(blockKeys []kvblock.BlockHash,
	keyToPods map[kvblock.BlockHash][]kvblock.PodEntry) map[string]float64 {
	podScores := make(map[string]float64)

	if len(blockKeys) == 0 {
		return podScores
	}

	// Initialize active pods from the first key
	firstKeyPods := keyToPods[blockKeys[0]]
	activePods := sets.New[string]()
	for _, pod := range firstKeyPods {
		activePods.Insert(pod.PodIdentifier)
		podScores[pod.PodIdentifier] = 1
	}

	// For each subsequent key, intersect active pods with current key's pods.
	// Pods not present in the current key are dropped from the active set.
	for i := 1; i < len(blockKeys); i++ {
		if activePods.Len() == 0 {
			break
		}

		pods := keyToPods[blockKeys[i]]
		currentPods := sets.New[string]()
		for _, pod := range pods {
			currentPods.Insert(pod.PodIdentifier)
		}

		activePods = activePods.Intersection(currentPods)
		for pod := range activePods {
			podScores[pod]++
		}
	}

	return podScores
}

// getBlockSizeTokens returns the block size in tokens from the token processor config.
func (s *PrecisePrefixCacheScorer) getBlockSizeTokens() int {
	return s.blockSizeTokens
}

// getScores retrieves the endpoint scores from the KV-cache indexer
// based on the provided LLM request.
// If the request contains chat completions, it processes them accordingly.
// If the request contains regular completions, it uses the prompt directly.
func (s *PrecisePrefixCacheScorer) getScores(ctx context.Context, request *scheduling.LLMRequest) (map[string]float64, error) {
	logger := log.FromContext(ctx).WithName(s.typedName.String())
	traceLogger := logger.V(logutil.TRACE)

	traceLogger.Info("Getting scores",
		"isChatCompletions", request.Body != nil && request.Body.ChatCompletions != nil,
		"isCompletions", request.Body != nil && request.Body.Completions != nil)

	// The upstream parser guarantees exactly one body is populated, but we defensively prioritize chat completions.
	// If an unexpected dual payload slips through (parser regression/new client), log it and use chat semantics.
	if request.Body != nil && request.Body.ChatCompletions != nil {
		if request.Body.Completions != nil {
			traceLogger.Info("Both chat/completions and completions present; defaulting to chat/completions")
		}

		// Convert messages to conversation format
		conversations := make([]types.Conversation, len(request.Body.ChatCompletions.Messages))
		for i, msg := range request.Body.ChatCompletions.Messages {
			conversations[i] = types.Conversation{
				Role:    msg.Role,
				Content: msg.Content.Raw,
			}
		}

		renderReq := &types.RenderChatRequest{
			Conversation:              conversations,
			Tools:                     request.Body.ChatCompletions.Tools,
			Documents:                 request.Body.ChatCompletions.Documents,
			ChatTemplate:              request.Body.ChatCompletions.ChatTemplate,
			ReturnAssistantTokensMask: request.Body.ChatCompletions.ReturnAssistantTokensMask,
			ContinueFinalMessage:      request.Body.ChatCompletions.ContinueFinalMessage,
			AddGenerationPrompt:       request.Body.ChatCompletions.AddGenerationPrompt,
			ChatTemplateKWArgs:        request.Body.ChatCompletions.ChatTemplateKWArgs,
		}

		traceLogger.Info("Processing chat completion request",
			"messagesCount", len(conversations),
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
