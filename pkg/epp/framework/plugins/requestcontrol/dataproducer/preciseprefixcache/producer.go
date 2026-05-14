/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package preciseprefixcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents/engineadapter"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requestcontrol"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	attrprefix "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/prefix"
	tokenproducer "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/dataproducer/tokenizer"
)

// PluginType is the type-name of the precise-prefix-cache producer plugin.
const PluginType = "precise-prefix-cache-producer"

// PluginConfig mirrors the historical `precise-prefix-cache-scorer` config so
// legacy YAML still parses.
type PluginConfig struct {
	TokenProcessorConfig *kvblock.TokenProcessorConfig `json:"tokenProcessorConfig"`
	IndexerConfig        *kvcache.Config               `json:"indexerConfig"`
	KVEventsConfig       *kvevents.Config              `json:"kvEventsConfig"`
	// SpeculativeIndexing: after the routing decision, seed predicted cache
	// entries for the selected endpoint(s) so the next same-prefix request
	// can hit without waiting for engine confirmation.
	SpeculativeIndexing bool `json:"speculativeIndexing"`
	// SpeculativeTTL is a Go duration string. Defaults to defaultSpeculativeTTL.
	SpeculativeTTL string `json:"speculativeTTL"`
}

var _ requestcontrol.DataProducer = &Producer{}

// Producer owns the KV-block index and publishes per-endpoint
// PrefixCacheMatchInfo for the slim scorer to read. Speculative indexing
// lives in PreRequest (prerequest.go); per-pod ZMQ subscriber lifecycle in
// ExtractEndpoint (extractor.go).
type Producer struct {
	typedName      plugin.TypedName
	kvCacheIndexer kvCacheIndexer

	subscribersManager *kvevents.SubscriberManager
	kvEventsConfig     *kvevents.Config

	kvBlockScorer kvcache.KVBlockScorer

	pluginState *plugin.PluginState

	speculativeCache   *ttlcache.Cache[string, *speculativeEntries]
	speculativeTTL     time.Duration
	speculativeEnabled bool

	blockSizeTokens int

	// extractorActive flips on the first ExtractEndpoint call. From then on,
	// EnsureSubscribersForEndpoints is a no-op so the data layer owns
	// subscriber lifecycle.
	extractorActive atomic.Bool

	// subscriberCtx is plugin-lifetime, NOT request-scoped: SubscriberManager
	// binds each subscriber's goroutine to the ctx passed at registration,
	// so using a request ctx would tear subscribers down at the end of the
	// request that opened them.
	subscriberCtx context.Context
}

// PluginFactory is exported so the slim scorer's legacy factory can
// instantiate a Producer for the all-in-one mode.
func PluginFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	indexerConfig, err := kvcache.NewDefaultConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize indexer config: %w", err)
	}

	parameters := PluginConfig{
		IndexerConfig:  indexerConfig,
		KVEventsConfig: kvevents.DefaultConfig(),
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse %s plugin config: %w", PluginType, err)
		}
	}

	if parameters.IndexerConfig == nil {
		return nil, errors.New("indexerConfig is required")
	}

	p, err := New(handle.Context(), parameters)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s plugin: %w", PluginType, err)
	}

	return p.WithName(name), nil
}

// New initializes a Producer. The kvcache indexer and KV-events pool are
// started in background goroutines bound to ctx.
func New(ctx context.Context, config PluginConfig) (*Producer, error) {
	if config.TokenProcessorConfig == nil {
		config.TokenProcessorConfig = kvblock.DefaultTokenProcessorConfig()
	}

	tokenProcessor, err := kvblock.NewChunkedTokenDatabase(config.TokenProcessorConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create token processor: %w", err)
	}

	indexer, err := kvcache.NewKVCacheIndexer(ctx, config.IndexerConfig, tokenProcessor)
	if err != nil {
		return nil, fmt.Errorf("failed to create kvcache.Indexer: %w", err)
	}
	go indexer.Run(ctx)

	scorerConfig := kvcache.DefaultKVBlockScorerConfig()
	if config.IndexerConfig != nil && config.IndexerConfig.BackendConfigs != nil {
		scorerConfig.BackendConfigs = config.IndexerConfig.BackendConfigs
	}
	kvBlockScorer, err := kvcache.NewKVBlockScorer(scorerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create KVBlockScorer: %w", err)
	}

	pool := kvevents.NewPool(config.KVEventsConfig, indexer.KVBlockIndex(), tokenProcessor, engineadapter.NewVLLMAdapter())
	pool.Start(ctx)

	subscribersManager := kvevents.NewSubscriberManager(pool)
	if config.KVEventsConfig.ZMQEndpoint != "" {
		if err := subscribersManager.EnsureSubscriber(ctx, "local-subscriber",
			config.KVEventsConfig.ZMQEndpoint, config.KVEventsConfig.TopicFilter, false); err != nil {
			return nil, fmt.Errorf("failed to create local subscriber for global socket mode: %w", err)
		}
	}

	speculativeCache, speculativeTTL, err := buildSpeculativeCache(ctx, config, indexer.KVBlockIndex())
	if err != nil {
		return nil, err
	}

	return &Producer{
		typedName:          plugin.TypedName{Type: PluginType},
		kvCacheIndexer:     indexer,
		kvBlockScorer:      kvBlockScorer,
		subscribersManager: subscribersManager,
		kvEventsConfig:     config.KVEventsConfig,
		pluginState:        plugin.NewPluginState(ctx),
		speculativeCache:   speculativeCache,
		speculativeTTL:     speculativeTTL,
		speculativeEnabled: config.SpeculativeIndexing,
		blockSizeTokens:    config.TokenProcessorConfig.BlockSize,
		subscriberCtx:      ctx,
	}, nil
}

func (p *Producer) TypedName() plugin.TypedName {
	return p.typedName
}

func (p *Producer) WithName(name string) *Producer {
	p.typedName.Name = name
	return p
}

func (p *Producer) Produces() map[string]any {
	return map[string]any{attrprefix.PrefixCacheMatchInfoKey: attrprefix.PrefixCacheMatchInfo{}}
}

// Consumes declares the TokenizedPrompt dependency so the data-layer DAG
// orders `token-producer` first.
func (p *Producer) Consumes() map[string]any {
	return map[string]any{tokenproducer.TokenizedPromptKey: scheduling.TokenizedPrompt{}}
}

func (p *Producer) Produce(ctx context.Context,
	request *scheduling.InferenceRequest, endpoints []scheduling.Endpoint,
) error {
	blockKeys, err := computeBlockKeys(ctx, p.kvCacheIndexer, request, p.blockSizeTokens)
	if err != nil {
		return fmt.Errorf("failed to compute block keys: %w", err)
	}
	return p.ProduceFromBlockKeys(ctx, request, endpoints, blockKeys)
}

// ProduceFromBlockKeys runs the post-tokenization pipeline. Exposed for the
// legacy wrapper, which sources block keys via ComputeBlockKeysFromRequest.
func (p *Producer) ProduceFromBlockKeys(ctx context.Context,
	request *scheduling.InferenceRequest, endpoints []scheduling.Endpoint,
	blockKeys []kvblock.BlockHash,
) error {
	if len(blockKeys) == 0 {
		return nil
	}

	logger := log.FromContext(ctx).WithName(p.typedName.String())

	keyToPods, err := p.kvCacheIndexer.KVBlockIndex().Lookup(ctx, blockKeys, extractPodSet(endpoints))
	if err != nil {
		return fmt.Errorf("failed to lookup block keys: %w", err)
	}

	scores, err := p.kvBlockScorer.Score(ctx, blockKeys, keyToPods)
	if err != nil {
		return fmt.Errorf("failed to score block keys: %w", err)
	}

	totalBlocks := len(blockKeys)
	for _, ep := range endpoints {
		md := ep.GetMetadata()
		if md == nil {
			continue
		}
		addr := fmt.Sprintf("%s:%s", md.Address, md.Port)
		matchLen := int(scores[addr])
		ep.Put(attrprefix.PrefixCacheMatchInfoKey,
			attrprefix.NewPrefixCacheMatchInfo(matchLen, totalBlocks, p.blockSizeTokens))
	}

	if p.speculativeEnabled {
		p.pluginState.Write(request.RequestID, blockKeysStateKey,
			&blockKeysState{blockKeys: blockKeys})
	}

	logger.V(logging.TRACE).Info("Produce completed",
		"blockKeys", totalBlocks, "scores", scores)
	return nil
}

// ComputeBlockKeysFromRequest hashes chat/completions or completions via the
// indexer's internal tokenizers pool. (nil, nil) on no resolvable input or
// ErrInternalTokenizationDisabled. Legacy-wrapper-only.
func (p *Producer) ComputeBlockKeysFromRequest(ctx context.Context,
	request *scheduling.InferenceRequest,
) ([]kvblock.BlockHash, error) {
	if request == nil || request.Body == nil {
		return nil, nil
	}
	var (
		keys []kvblock.BlockHash
		err  error
	)
	switch {
	case request.Body.ChatCompletions != nil:
		renderReq := tokenproducer.ChatCompletionsToRenderChatRequest(request.Body.ChatCompletions)
		//nolint:staticcheck // SA1019: legacy path retained for tokenizersPoolConfig configs.
		keys, err = p.kvCacheIndexer.ComputeBlockKeys(ctx, renderReq, "", request.TargetModel)
	case request.Body.Completions != nil:
		//nolint:staticcheck // SA1019: legacy path retained for tokenizersPoolConfig configs.
		keys, err = p.kvCacheIndexer.ComputeBlockKeys(ctx, nil, request.Body.Completions.Prompt.Raw, request.TargetModel)
	default:
		return nil, nil
	}
	if errors.Is(err, kvcache.ErrInternalTokenizationDisabled) {
		return nil, nil
	}
	return keys, err
}
