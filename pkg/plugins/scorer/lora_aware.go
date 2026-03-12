package scorer

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// LoRAAwareType is the type of the LoRAAware scorer.
	LoRAAwareType = "lora-aware-scorer"

	// minShardSize is the minimum number of endpoints per LoRA adapter shard.
	minShardSize = 2
)

// LoRAAwareParameters defines the parameters for the LoRAAware scorer.
type LoRAAwareParameters struct {
	// ShardSize defines the number of endpoints to assign to each LoRA adapter.
	// If not specified or set to 0, it will be calculated dynamically based on the
	// number of available endpoints using the formula: ceil(N / 2), where N is the
	// number of endpoints. This provides a conservative default that balances
	// isolation and redundancy without knowing the number of adapters in advance.
	// Minimum value is 2 to ensure redundancy.
	ShardSize int `json:"shardSize"`
	// BaseModel is the base model name. If TargetModel matches this,
	// all endpoints receive neutral score (0.5). This allows the scorer to distinguish
	// between the base model and LoRA adapters.
	BaseModel string `json:"baseModel"`
}

// compile-time type assertion
var _ scheduling.Scorer = &LoRAAware{}

// LoRAAwareFactory defines the factory function for the LoRAAware scorer.
func LoRAAwareFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	parameters := LoRAAwareParameters{ShardSize: 0} // 0 means auto-calculate
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' scorer - %w", LoRAAwareType, err)
		}
	}

	return NewLoRAAware(handle.Context(), &parameters).WithName(name), nil
}

// NewLoRAAware creates a new LoRAAware scorer.
func NewLoRAAware(ctx context.Context, params *LoRAAwareParameters) *LoRAAware {
	configuredShardSize := 0
	baseModel := ""
	logger := log.FromContext(ctx)

	if params != nil {
		configuredShardSize = params.ShardSize
		if configuredShardSize > 0 {
			logger.Info("Using configured shard size", "shardSize", configuredShardSize)
		} else {
			logger.Info("ShardSize not configured, will calculate dynamically based on endpoint count")
		}

		if params.BaseModel != "" {
			baseModel = params.BaseModel
			logger.Info("Configured base model", "model", baseModel)
		}
	}

	return &LoRAAware{
		typedName:           plugin.TypedName{Type: LoRAAwareType},
		configuredShardSize: configuredShardSize,
		baseModel:           baseModel,
		shardCache:          make(map[string][]string),
	}
}

// LoRAAware is a scorer that uses shuffle sharding to assign groups of
// vLLM servers to LoRA adapter names. Endpoints that belong to the assigned
// group for a given LoRA adapter receive a score of 1, while others receive 0.
//
// Shuffle sharding ensures:
// - Consistent assignment: same LoRA adapter always maps to same endpoints
// - Load distribution: different adapters map to different (but overlapping) endpoint sets
// - Fault isolation: issues with one adapter's endpoints don't affect all adapters
//
// If shardSize is not explicitly configured (set to 0), it will be calculated
// dynamically based on the number of available endpoints using a conservative
// formula: ceil(N / 2), which provides good balance without knowing the number
// of adapters in advance.
//
// The scorer caches shard assignments for each adapter to avoid recalculating
// them on every request, improving performance for frequently used adapters.
type LoRAAware struct {
	typedName           plugin.TypedName
	configuredShardSize int                 // 0 means auto-calculate based on endpoint count
	baseModel           string              // Base model name
	shardCache          map[string][]string // Cache of adapter -> endpoint names
	shardCacheMu        sync.RWMutex        // Protects shardCache
	cachedShardSize     int                 // Cached calculated shard size
	cachedEndpointCount int                 // Number of endpoints for cached shard size
	shardSizeMu         sync.RWMutex        // Protects shard size cache
}

// TypedName returns the typed name of the plugin.
func (s *LoRAAware) TypedName() plugin.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *LoRAAware) WithName(name string) *LoRAAware {
	s.typedName.Name = name
	return s
}

// Category returns the preference the scorer applies when scoring candidate endpoints.
func (s *LoRAAware) Category() scheduling.ScorerCategory {
	return scheduling.Affinity
}

// setNeutralScores creates a score map with neutral scores (0.5) for all endpoints.
// This is used when the scorer should not influence endpoint selection, allowing
// other scorers to make the decision.
func setNeutralScores(endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	scores := make(map[scheduling.Endpoint]float64, len(endpoints))
	for _, endpoint := range endpoints {
		scores[endpoint] = 0.5
	}
	return scores
}

// Score assigns scores to endpoints based on shuffle sharding for LoRA adapters.
// Endpoints in the assigned shard for the requested LoRA adapter get score 1.0,
// all others get score 0.0.
//
// The LoRA adapter name is extracted from the request's TargetModel field,
// which contains the model name from the request body.
//
// If TargetModel is empty or matches the configured base model name, all endpoints
// receive a neutral score of 0.5 to allow other scorers to make the decision.
//
// The shardSize is calculated dynamically if not explicitly configured, using the
// formula: max(2, ceil(N / 2)), where N is the number of available endpoints.
func (s *LoRAAware) Score(ctx context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	logger := log.FromContext(ctx).V(logutil.DEBUG)

	// Extract model name from TargetModel field
	modelName := request.TargetModel

	// If no model specified or it's a base model, return neutral scores
	if modelName == "" {
		logger.Info("No model specified in TargetModel, returning neutral scores")
		return setNeutralScores(endpoints)
	}

	// Check if this is the base model (not a LoRA adapter)
	if s.baseModel != "" && modelName == s.baseModel {
		logger.Info("Base model detected, returning neutral scores", "model", modelName)
		return setNeutralScores(endpoints)
	}

	// This is a LoRA adapter - apply shuffle sharding
	loraAdapter := modelName
	scoredEndpoints := make(map[scheduling.Endpoint]float64, len(endpoints))

	// Calculate effective shard size (cached based on endpoint count)
	shardSize := s.getShardSize(len(endpoints))
	logger.Info("Scoring endpoints for LoRA adapter", "adapter", loraAdapter, "shardSize", shardSize, "totalEndpoints", len(endpoints))

	// Get the shard of endpoints assigned to this LoRA adapter (with caching)
	assignedEndpointNames := s.getOrComputeShard(loraAdapter, endpoints, shardSize)

	// Create a set of assigned endpoint names for quick lookup
	assignedSet := make(map[string]bool, len(assignedEndpointNames))
	for _, name := range assignedEndpointNames {
		assignedSet[name] = true
	}

	// Score endpoints: 1.0 if in assigned shard, 0.0 otherwise
	for _, endpoint := range endpoints {
		endpointName := endpoint.GetMetadata().NamespacedName.String()
		if assignedSet[endpointName] {
			scoredEndpoints[endpoint] = 1.0
			logger.Info("Endpoint in assigned shard", "endpoint", endpointName, "score", 1.0)
		} else {
			scoredEndpoints[endpoint] = 0.0
			logger.Info("Endpoint not in assigned shard", "endpoint", endpointName, "score", 0.0)
		}
	}

	return scoredEndpoints
}

// calculateShardSize determines the effective shard size to use.
// If a shard size was explicitly configured, it uses that value.
// Otherwise, it calculates a conservative default: max(2, ceil(N / 2)),
// where N is the number of endpoints. This provides good balance between
// isolation and redundancy without knowing the number of adapters in advance.
func (s *LoRAAware) calculateShardSize(numEndpoints int) int {
	if s.configuredShardSize > 0 {
		return s.configuredShardSize
	}

	// Conservative default: ceil(N / 2)
	// This works well for unknown number of adapters
	calculatedSize := (numEndpoints + 1) / 2 // Integer ceiling division

	// Ensure minimum of 2 for redundancy
	if calculatedSize < minShardSize {
		return minShardSize
	}

	// Cap at total endpoints
	if calculatedSize > numEndpoints {
		return numEndpoints
	}

	return calculatedSize
}

// getShardSize returns the effective shard size, caching the result based on
// the number of endpoints to avoid recalculating when endpoint count is stable.
func (s *LoRAAware) getShardSize(numEndpoints int) int {
	// If shard size is explicitly configured, return it directly (no caching needed)
	if s.configuredShardSize > 0 {
		return s.configuredShardSize
	}

	// Check if we have a cached value for this endpoint count
	s.shardSizeMu.RLock()
	if s.cachedEndpointCount == numEndpoints && s.cachedShardSize > 0 {
		cachedSize := s.cachedShardSize
		s.shardSizeMu.RUnlock()
		return cachedSize
	}
	s.shardSizeMu.RUnlock()

	// Calculate and cache the shard size
	s.shardSizeMu.Lock()
	defer s.shardSizeMu.Unlock()

	// Double-check after acquiring write lock
	if s.cachedEndpointCount == numEndpoints && s.cachedShardSize > 0 {
		return s.cachedShardSize
	}

	// Calculate the shard size
	calculatedSize := s.calculateShardSize(numEndpoints)

	// Cache the result
	s.cachedShardSize = calculatedSize
	s.cachedEndpointCount = numEndpoints

	return calculatedSize
}

// getOrComputeShard retrieves the cached shard assignment for an adapter,
// or computes and caches it if not already present.
func (s *LoRAAware) getOrComputeShard(adapterName string, endpoints []scheduling.Endpoint, shardSize int) []string {
	// Try to get from cache first (read lock)
	s.shardCacheMu.RLock()
	cachedNames, found := s.shardCache[adapterName]
	s.shardCacheMu.RUnlock()

	if found {
		return cachedNames
	}

	// Not in cache, compute the shard (write lock)
	s.shardCacheMu.Lock()
	defer s.shardCacheMu.Unlock()

	// Double-check after acquiring write lock (another goroutine might have computed it)
	if cachedNames, found := s.shardCache[adapterName]; found {
		return cachedNames
	}

	// Compute the shard
	assignedEndpoints := s.getShardForAdapter(adapterName, endpoints, shardSize)

	// Extract endpoint names for caching
	endpointNames := make([]string, len(assignedEndpoints))
	for i, endpoint := range assignedEndpoints {
		endpointNames[i] = endpoint.GetMetadata().NamespacedName.String()
	}

	// Cache the result
	s.shardCache[adapterName] = endpointNames

	return endpointNames
}

// getShardForAdapter implements shuffle sharding to deterministically assign
// a subset of endpoints to a given LoRA adapter.
//
// Algorithm:
// 1. Sort all endpoints by name for consistency
// 2. Hash the adapter name to get a deterministic seed
// 3. Use the seed to shuffle the endpoint list deterministically
// 4. Take the top shardSize endpoints from the shuffled list
//
// This approach ensures that the same adapter always gets the same endpoints,
// regardless of pod name changes, as long as the total number of endpoints
// remains constant.
func (s *LoRAAware) getShardForAdapter(adapterName string, endpoints []scheduling.Endpoint, shardSize int) []scheduling.Endpoint {
	if len(endpoints) == 0 {
		return []scheduling.Endpoint{}
	}

	// If shard size >= total endpoints, return all endpoints
	if shardSize >= len(endpoints) {
		return endpoints
	}

	// Create a sorted copy of endpoints for deterministic ordering
	sortedEndpoints := make([]scheduling.Endpoint, len(endpoints))
	copy(sortedEndpoints, endpoints)
	sort.Slice(sortedEndpoints, func(i, j int) bool {
		return sortedEndpoints[i].GetMetadata().NamespacedName.String() <
			sortedEndpoints[j].GetMetadata().NamespacedName.String()
	})

	// Hash the adapter name to get a deterministic seed
	seed := int64(hashString(adapterName))

	// Create a new random source with the seed for deterministic shuffling
	rng := rand.New(rand.NewSource(seed))

	// Shuffle the endpoints deterministically based on the adapter name
	shuffled := make([]scheduling.Endpoint, len(sortedEndpoints))
	copy(shuffled, sortedEndpoints)
	rng.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	// Take the top shardSize endpoints from the shuffled list
	result := make([]scheduling.Endpoint, shardSize)
	copy(result, shuffled[:shardSize])

	return result
}

// hashString computes a hash value for a string using FNV-1a algorithm.
func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}
