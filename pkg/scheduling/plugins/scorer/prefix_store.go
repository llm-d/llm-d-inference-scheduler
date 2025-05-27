package scorer

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/cespare/xxhash/v2"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
)

// PrefixStoreConfig contains initialization configuration for PrefixStore.
type PrefixStoreConfig struct {
	// CacheSize sets the maximum number of blocks the LRU cache can store.
	CacheSize int
	// BlockSize defines how many runes each block contains in the prefix cache.
	BlockSize int
	// BlockCacheSize sets the maximum number of pods a block can store.
	BlockCacheSize int
}

// DefaultPrefixStoreConfig returns an PrefixStoreConfig instance with default
// configuration.
func DefaultPrefixStoreConfig() *PrefixStoreConfig {
	return &PrefixStoreConfig{
		CacheSize:      config.DefaultPrefixMaxCacheSize,
		BlockSize:      config.DefaultPrefixBlockSize,
		BlockCacheSize: config.DefaultPrefixMaxBlockCacheSize,
	}
}

// block holds the tokens contained in the block.
type block struct {
	Pods *lru.Cache[types.NamespacedName, time.Time] //TODO: implement Pod eviction based on staleness
}

// PrefixStore is an in-memory prefix-to-block cache with xxhash keys and LRU
// eviction.
type PrefixStore struct {
	sync.RWMutex

	cacheSize      int
	blockSize      int
	blockCacheSize int

	store map[string]*lru.Cache[uint64, *block]
}

// NewPrefixStore initializes the PrefixStore with LRU cache.
// If the configuration is nil, default is used.
func NewPrefixStore(config *PrefixStoreConfig) *PrefixStore {
	if config == nil {
		config = DefaultPrefixStoreConfig()
	}

	return &PrefixStore{
		cacheSize:      config.CacheSize,
		blockSize:      config.BlockSize,
		blockCacheSize: config.BlockCacheSize,
		store:          make(map[string]*lru.Cache[uint64, *block]),
	}
}

// AddEntry adds a new entry to the prefix store.
func (s *PrefixStore) AddEntry(modelName string, prompt string, pod *types.NamespacedName) error {
	if prompt == "" || pod == nil || len(prompt) < s.blockSize /* skip if prompt is too short */ {
		return nil
	}

	s.Lock()
	// Get or create the LRU cache for the model
	cache, ok := s.store[modelName]
	if !ok {
		var err error
		cache, err = lru.New[uint64, *block](s.cacheSize)
		if err != nil {
			return fmt.Errorf("failed to create LRU cache for model %s: %w", modelName, err)
		}

		s.store[modelName] = cache
	}
	s.Unlock()

	promptBytes := []byte(prompt)
	previousHash := uint64(0)
	digest := xxhash.New()

	// Chunk the text into blocks and populate the cache
	for start := 0; start < len(promptBytes); start += s.blockSize {
		end := start + s.blockSize
		if end > len(promptBytes) {
			break // skip partial blocks
		}

		// Compute the hash for the current block
		digest.Reset()
		if err := binary.Write(digest, binary.LittleEndian, previousHash); err != nil {
			return fmt.Errorf("failed to write previous hash: %w", err)
		}
		if _, err := digest.Write(promptBytes[start:end]); err != nil {
			return fmt.Errorf("failed to write prompt bytes: %w", err)
		}

		blockHash := digest.Sum64()
		previousHash = blockHash

		b, ok := cache.Get(blockHash)
		if !ok {
			pods, err := lru.New[types.NamespacedName, time.Time](s.blockCacheSize)
			if err != nil {
				return fmt.Errorf("failed to create LRU cache for block: %w", err)
			}

			b = &block{Pods: pods}
			cache.Add(blockHash, b)
		}

		b.Pods.Add(*pod, time.Now()) // thread-safe
	}

	return nil
}

// FindMatchingPods finds all pods that match the given prompt and model name.
// It returns a map of pods and the number of blocks they match.
func (s *PrefixStore) FindMatchingPods(prompt, modelName string) map[string]int {
	if prompt == "" || modelName == "" || len(prompt) < s.blockSize /* skip if prompt is too short */ {
		return nil
	}

	s.RLock()
	cache, ok := s.store[modelName] // cache is thread-safe
	s.RUnlock()

	if !ok {
		return nil
	}

	promptBytes := []byte(prompt)
	previousHash := uint64(0)
	digest := xxhash.New()

	matchedPods := make(map[string]int)
	for start := 0; start < len(promptBytes); start += s.blockSize {
		end := start + s.blockSize
		if end > len(promptBytes) {
			break // skip partial blocks
		}

		digest.Reset()
		if err := binary.Write(digest, binary.LittleEndian, previousHash); err != nil {
			break
		}
		if _, err := digest.Write(promptBytes[start:end]); err != nil {
			break
		}

		blockHash := digest.Sum64()
		previousHash = blockHash

		b, ok := cache.Get(blockHash)
		if !ok {
			break // match consecutive blocks
		}

		for _, pod := range b.Pods.Keys() {
			matchedPods[pod.String()]++
		}
	}

	return matchedPods
}
