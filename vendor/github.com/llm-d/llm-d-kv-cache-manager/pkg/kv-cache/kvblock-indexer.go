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
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/redis/go-redis/v9"
	"golang.org/x/net/context"
)

// KVBlockIndexerConfig holds the configuration for the KVBlockIndexer.
type KVBlockIndexerConfig struct {
	*RedisKVBlockIndexerConfig
}

// DefaultKVBlockIndexerConfig returns the default configuration for the KVBlockIndexer.
func DefaultKVBlockIndexerConfig() *KVBlockIndexerConfig {
	return &KVBlockIndexerConfig{
		RedisKVBlockIndexerConfig: defaultRedisKVBlockIndexerConfig(),
	}
}

// KVBlockIndexer defines the interactions with the KVCache indexing backend.
type KVBlockIndexer interface {
	// GetPodsForKeys receives a list of keys and a set of pod identifiers,
	// and retrieves the filtered pods associated with those keys.
	// The filtering is done based on the pod identifiers provided.
	// If the podIdentifierSet is empty, all pods are returned.
	//
	// It returns:
	// 1. A slice of strings representing the keys.
	// 2. A map where the keys are those in (1) and the values are pod names.
	// 3. An error if any occurred during the operation.
	GetPodsForKeys(ctx context.Context,
		keys []KVBlockKey, podIdentifierSet sets.Set[string]) ([]string, map[string][]string, error)
}

var _ KVBlockIndexer = &RedisKVBlockIndexer{}

// RedisKVBlockIndexerConfig holds the configuration for the RedisKVBlockIndexer.
type RedisKVBlockIndexerConfig struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

func defaultRedisKVBlockIndexerConfig() *RedisKVBlockIndexerConfig {
	return &RedisKVBlockIndexerConfig{
		RedisAddr:     "localhost:6379",
		RedisPassword: "",
		RedisDB:       0,
	}
}

type RedisKVBlockIndexer struct {
	// RedisClient is the Redis client used for communication.
	RedisClient *redis.Client
}

// NewRedisKVBlockIndexer creates a new RedisKVBlockIndexer instance.
func NewRedisKVBlockIndexer(config *KVBlockIndexerConfig) (KVBlockIndexer, error) {
	if config == nil {
		config = DefaultKVBlockIndexerConfig()
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     config.RedisAddr,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	})

	_, err := redisClient.Ping(context.Background()).Result()
	if err != nil {
		return nil, fmt.Errorf("could not connect to Redis: %w", err)
	}

	return &RedisKVBlockIndexer{
		RedisClient: redisClient,
	}, nil
}

// GetPodsForKeys receives a list of keys and a set of pod identifiers,
// and retrieves the filtered pods associated with those keys.
// The filtering is done based on the pod identifiers provided.
// If the podIdentifierSet is empty, all pods are returned.
//
// It returns:
// 1. A slice of strings representing the keys.
// 2. A map where the keys are those in (1) and the values are pod names.
// 3. An error if any occurred during the operation.
//
// The function uses a Redis pipeline to optimize the retrieval of keys.
// The function assumes that the redis structure is a hash where the keys are
// the KVBlockKeys, the fields are the pod identifiers, and the values are some
// associated data that we don't need to retrieve.
//
//nolint:gocritic // no need named return values here
func (r *RedisKVBlockIndexer) GetPodsForKeys(ctx context.Context,
	keys []KVBlockKey, podIdentifierSet sets.Set[string],
) ([]string, map[string][]string, error) {
	if len(keys) == 0 {
		return nil, nil, nil
	}

	logger := klog.FromContext(ctx).WithName("RedisKVBlockIndexer")
	podsPerKey := make(map[string][]string)

	// pipeline for single RTT
	pipe := r.RedisClient.Pipeline()
	results := make([]*redis.StringSliceCmd, len(keys))
	redisKeys := make([]string, len(keys))

	// queue an HKeys command for each key in the pipeline
	for i, key := range keys {
		redisKey := key.String()
		redisKeys[i] = redisKey
		// HKeys gets all field names
		results[i] = pipe.HKeys(ctx, redisKey)
	}

	_, execErr := pipe.Exec(ctx)
	if execErr != nil {
		return nil, nil, fmt.Errorf("redis pipeline execution failed: %w", execErr)
	}

	filterPods := len(podIdentifierSet) > 0 // predicate for filtering
	for i, cmd := range results {
		currentRedisKey := redisKeys[i]

		// cmd.Result() returns the slice of strings (pod IDs) which is the first layer in the mapping
		pods, cmdErr := cmd.Result()
		if cmdErr != nil {
			podsPerKey[currentRedisKey] = []string{} // assign an empty slice
			if !errors.Is(cmdErr, redis.Nil) {
				logger.Error(cmdErr, "failed to get pods for key", "key", currentRedisKey)
			}

			continue
		}

		var filteredPods []string
		for _, p := range pods {
			ip := strings.SplitN(p, ":", 2)[0]
			if !filterPods || podIdentifierSet.Has(ip) {
				filteredPods = append(filteredPods, ip)
			}
		}

		podsPerKey[currentRedisKey] = filteredPods
	}

	return redisKeys, podsPerKey, nil
}
