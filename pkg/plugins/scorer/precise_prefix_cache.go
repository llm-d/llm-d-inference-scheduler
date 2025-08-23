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
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/prefix"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

const (
	// PrecisePrefixCachePluginType is the type-name of the PrecisePrefixCacheScorer plugin.
	PrecisePrefixCachePluginType = "precise-prefix-cache-scorer"
)

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
}

// compile-time type assertion
var _ scheduling.Scorer = &PrecisePrefixCacheScorer{}

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

	return &PrecisePrefixCacheScorer{
		typedName:          plugin.TypedName{Type: PrecisePrefixCachePluginType},
		kvCacheIndexer:     kvCacheIndexer,
		subscribersCache:   subscribersCache,
		subscribersManager: subscribersManager,
		kvEventsConfig:     config.KVEventsConfig,
	}, nil
}

// PrecisePrefixCacheScorer implements the framework.Scorer interface.
// The scorer implements precise prefix-cache KV-block locality scoring.
// It uses the `kvcache.Indexer` to score endpoints based on the KV-cache index
// state, and the `kvevents.Pool` to subscribe to KV-cache events
// to keep the internal KV-cache index state up-to-date.
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

// Score scores the provided endpoint based on the KVCache index state.
// The returned scores are normalized to a range of 0-1.
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

	scores, err := s.getScores(ctx, request)
	if err != nil {
		logger.Error(err, "Failed to get endpoint scores")
		span.SetStatus(codes.Error, err.Error())
		return nil
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

		return metadata.Address, true
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

	span.SetAttributes(attribute.String("llm_d.scorer.result", "success"))
	return normalizedScores
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
