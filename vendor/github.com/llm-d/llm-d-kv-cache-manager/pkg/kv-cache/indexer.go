/*
Copyright 2025 The llm-d Authors.

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

package kvcache

import (
	"context"
	"fmt"

	"github.com/llm-d/llm-d-kv-cache-manager/pkg/prefixstore"
	"k8s.io/apimachinery/pkg/util/sets"

	"k8s.io/klog/v2"

	"github.com/llm-d/llm-d-kv-cache-manager/pkg/tokenization"
)

// Config holds the configuration for the Indexer module.
// The configuration cover the different components found in the Indexer
// module.
type Config struct {
	PrefixStoreConfig    *prefixstore.Config
	TokenProcessorConfig *TokenProcessorConfig
	KVBlockIndexerConfig *KVBlockIndexerConfig
	KVBLockScorerConfig  *KVBlockScorerConfig
	TokenizersPoolConfig *tokenization.Config
}

// NewDefaultConfig returns a default configuration for the Indexer module.
func NewDefaultConfig() *Config {
	return &Config{
		PrefixStoreConfig:    prefixstore.DefaultConfig(),
		TokenProcessorConfig: DefaultTokenProcessorConfig(),
		KVBlockIndexerConfig: DefaultKVBlockIndexerConfig(),
		KVBLockScorerConfig:  DefaultKVBlockScorerConfig(),
		TokenizersPoolConfig: tokenization.DefaultConfig(),
	}
}

// Indexer is a concrete implementation of the KVCacheIndex interface.
type Indexer struct {
	tokensIndexer   prefixstore.Indexer // gets tokens for a prompt
	tokensProcessor TokenProcessor      // turns tokens to kv block keys
	kvBlockIndexer  KVBlockIndexer      // looks up pods for block keys
	kvBlockScorer   KVBlockScorer       // scores pods based on block hits

	tokenizersPool *tokenization.Pool
}

// NewKVCacheIndexer creates a KVCacheIndex given a Config.
func NewKVCacheIndexer(config *Config) (*Indexer, error) {
	tokensIndexer, err := prefixstore.NewLRUTokenStore(config.PrefixStoreConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create prefixstore.Indexer: %w", err)
	}

	tokensProcessor := NewChunkedTokenDatabase(config.TokenProcessorConfig)

	kvBlockIndexer, err := NewRedisKVBlockIndexer(config.KVBlockIndexerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create RedisKVBlockIndexer: %w", err)
	}

	scorer, err := NewKVBlockScorer(config.KVBLockScorerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create KVBlockScorer: %w", err)
	}

	tokenizersPool, err := tokenization.NewTokenizationPool(config.TokenizersPoolConfig, tokensIndexer)
	if err != nil {
		return nil, fmt.Errorf("failed to create tokenizers pool: %w", err)
	}

	return &Indexer{
		tokensIndexer:   tokensIndexer,
		tokensProcessor: tokensProcessor,
		kvBlockIndexer:  kvBlockIndexer,
		kvBlockScorer:   scorer,
		tokenizersPool:  tokenizersPool,
	}, nil
}

// Run starts the indexer.
func (k *Indexer) Run(ctx context.Context) {
	k.tokenizersPool.Run(ctx)
}

// GetPodScores retrieves the pod scores for a given prompt and model name.
// The function receives the mentioned information and a list of relevant pod
// identifiers. A Pod identifier should be its address.
// If the set of pod identifiers is empty, the function assumes all pods are
// relevant.
//
// The function returns a map of pod identifiers to scores.
func (k *Indexer) GetPodScores(ctx context.Context, prompt, modelName string,
	podIdentifiers []string,
) (map[string]int, error) {
	traceLogger := klog.FromContext(ctx).V(5)
	// 0. add to tokenizers pool
	k.tokenizersPool.AddTask(prompt, modelName)

	// 1. get available tokens of longest prefix
	tokens := k.tokensIndexer.FindLongestContainedTokens(prompt, modelName)
	if len(tokens) == 0 {
		//nolint:nilnil // no need to return an error
		return nil, nil
	}

	// 2. get block keys
	blockKeys := k.tokensProcessor.TokensToKVBlockKeys(tokens, modelName)
	traceLogger.Info("found tokens", "tokens", tokens, "block-keys", blockKeys)

	// 3. query kvblock indexer for pods
	strBlockKeys, keyToPods, err := k.kvBlockIndexer.GetPodsForKeys(ctx, blockKeys, sets.New(podIdentifiers...))
	if err != nil {
		return nil, fmt.Errorf("failed to query kvblock indexer: %w", err)
	}
	traceLogger.Info("found block keys", "block-keys", blockKeys, "pods", keyToPods)

	// 4. score pods
	podScores, err := k.kvBlockScorer.Score(strBlockKeys, keyToPods)
	if err != nil {
		return nil, fmt.Errorf("failed to query kvblock scorer: %w", err)
	}
	traceLogger.Info("found pod scores", "pod-scores", podScores)

	return podScores, nil
}
