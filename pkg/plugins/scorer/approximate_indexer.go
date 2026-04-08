package scorer

import (
	"context"
	"math"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
)

// ApproximateServerID identifies a model server pod by its namespaced name.
type ApproximateServerID = k8stypes.NamespacedName

// podSet is a set of server IDs.
type podSet = map[ApproximateServerID]struct{}

// approximateIndexer maintains an approximate view of which pods are likely
// to have which prefix blocks cached. It is updated via self-reporting:
// when the scheduler routes a request to a pod, the expected cache entries
// are recorded in the indexer.
//
// Internally it uses a dual-map structure:
//   - hashToPods: forward index from block hash to the set of pods that have it
//   - podToLRU:   per-pod LRU cache that automatically evicts old entries and
//     keeps hashToPods consistent via eviction callbacks
//
// All operations are thread-safe.
type approximateIndexer struct {
	mu             sync.RWMutex
	hashToPods     map[ApproximateBlockHash]podSet
	podToLRU       map[ApproximateServerID]*lru.Cache[ApproximateBlockHash, time.Time]
	defaultLRUSize int
}

// newApproximateIndexer creates a new indexer and starts a background goroutine
// that periodically logs the indexer size metrics.
func newApproximateIndexer(ctx context.Context, defaultLRUSize int) *approximateIndexer {
	idx := &approximateIndexer{
		hashToPods:     make(map[ApproximateBlockHash]podSet),
		podToLRU:       make(map[ApproximateServerID]*lru.Cache[ApproximateBlockHash, time.Time]),
		defaultLRUSize: defaultLRUSize,
	}
	go idx.reportLRUSize(ctx)
	return idx
}

// Add records block hashes for a server. This is called from PreRequest
// after a routing decision to self-report the expected cache contents.
//
// If the pod has no LRU yet, one is created with the given capacity
// (numGPUBlocks, or defaultLRUSize if <= 0). When the LRU is full,
// the oldest entries are evicted and automatically removed from hashToPods.
func (idx *approximateIndexer) Add(hashes []ApproximateBlockHash, server ApproximateServerID, numGPUBlocks int) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	podLRU, exists := idx.podToLRU[server]
	if !exists {
		capacity := idx.defaultLRUSize
		if numGPUBlocks > 0 {
			capacity = numGPUBlocks
		}
		podLRU, _ = lru.NewWithEvict(capacity, idx.makeEvictionFn(server))
		idx.podToLRU[server] = podLRU
	} else if numGPUBlocks > 0 {
		// AutoTune: resize LRU if reported capacity changed.
		// Resize is a no-op when size matches current capacity.
		podLRU.Resize(numGPUBlocks)
	}

	for _, hash := range hashes {
		podLRU.Add(hash, time.Now())
		if _, ok := idx.hashToPods[hash]; !ok {
			idx.hashToPods[hash] = make(podSet)
		}
		idx.hashToPods[hash][server] = struct{}{}
	}
}

// Get returns a copy of the set of servers that have the given hash cached.
// Returns nil if no server has it.
func (idx *approximateIndexer) Get(hash ApproximateBlockHash) podSet {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	ps, exists := idx.hashToPods[hash]
	if !exists {
		return nil
	}
	// Return a copy to avoid races on the caller side.
	result := make(podSet, len(ps))
	for k, v := range ps {
		result[k] = v
	}
	return result
}

// GetWithTimestamp returns the set of servers with their recorded timestamps
// for the given hash. Returns nil if no server has it.
func (idx *approximateIndexer) GetWithTimestamp(hash ApproximateBlockHash) map[ApproximateServerID]time.Time {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	ps, exists := idx.hashToPods[hash]
	if !exists {
		return nil
	}
	result := make(map[ApproximateServerID]time.Time, len(ps))
	for server := range ps {
		if podLRU, ok := idx.podToLRU[server]; ok {
			if ts, ok := podLRU.Peek(hash); ok {
				result[server] = ts
			}
		}
	}
	return result
}

// MatchLongestPrefix finds the longest contiguous prefix match per server.
// Only servers present from the very first hash are candidates. Once a server
// misses any hash in the chain, it is permanently eliminated. This ensures
// only truly contiguous prefixes starting from block 0 are scored.
func (idx *approximateIndexer) MatchLongestPrefix(hashes []ApproximateBlockHash) map[ApproximateServerID]int {
	result := make(map[ApproximateServerID]int)
	if len(hashes) == 0 {
		return result
	}

	// Initialize candidates from the first hash only.
	firstServers := idx.Get(hashes[0])
	if len(firstServers) == 0 {
		return result
	}
	active := make(map[ApproximateServerID]bool, len(firstServers))
	for server := range firstServers {
		result[server] = 1
		active[server] = true
	}

	// Walk remaining hashes, eliminating servers that miss any block.
	for _, hash := range hashes[1:] {
		cachedServers := idx.Get(hash)
		if len(cachedServers) == 0 {
			break
		}
		for server := range active {
			if _, has := cachedServers[server]; has {
				result[server]++
			} else {
				delete(active, server)
			}
		}
		if len(active) == 0 {
			break
		}
	}
	return result
}

// MatchLongestPrefixWeighted finds the longest contiguous prefix match per server
// and returns a recency-weighted score instead of a raw block count.
//
// Each matched block contributes a weight based on how recently it was recorded:
//
//	weight = exp(-age * ln2 / halfLife)
//
// where age = time since the block was self-reported, and halfLife controls how
// fast old entries decay. A halfLife of 30s means an entry recorded 30s ago
// contributes exactly 50% of a fresh entry's weight.
//
// This improves scoring accuracy because older self-reported entries are more
// likely to have been evicted from the actual GPU cache.
func (idx *approximateIndexer) MatchLongestPrefixWeighted(hashes []ApproximateBlockHash, now time.Time, halfLife time.Duration) map[ApproximateServerID]float64 {
	result := make(map[ApproximateServerID]float64)
	if len(hashes) == 0 || halfLife <= 0 {
		return result
	}

	// Initialize candidates from the first hash.
	firstServers := idx.GetWithTimestamp(hashes[0])
	if len(firstServers) == 0 {
		return result
	}
	halfLifeSec := halfLife.Seconds()
	active := make(map[ApproximateServerID]bool, len(firstServers))
	for server, ts := range firstServers {
		age := now.Sub(ts).Seconds()
		result[server] = math.Exp(-age * math.Ln2 / halfLifeSec)
		active[server] = true
	}

	for _, hash := range hashes[1:] {
		serverTimestamps := idx.GetWithTimestamp(hash)
		if len(serverTimestamps) == 0 {
			break
		}
		for server := range active {
			if ts, has := serverTimestamps[server]; has {
				age := now.Sub(ts).Seconds()
				result[server] += math.Exp(-age * math.Ln2 / halfLifeSec)
			} else {
				delete(active, server)
			}
		}
		if len(active) == 0 {
			break
		}
	}
	return result
}

// RemovePod removes all cached entries for a server and cleans up hashToPods.
func (idx *approximateIndexer) RemovePod(server ApproximateServerID) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if podLRU, exists := idx.podToLRU[server]; exists {
		// Purge triggers eviction callbacks for each entry, which
		// clean up hashToPods.
		podLRU.Purge()
		delete(idx.podToLRU, server)
	}
}

// Pods returns the set of servers currently tracked by the indexer.
func (idx *approximateIndexer) Pods() []ApproximateServerID {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	pods := make([]ApproximateServerID, 0, len(idx.podToLRU))
	for pod := range idx.podToLRU {
		pods = append(pods, pod)
	}
	return pods
}

// TotalEntries returns the total number of hash entries across all pods.
func (idx *approximateIndexer) TotalEntries() int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var total int64
	for _, podLRU := range idx.podToLRU {
		total += int64(podLRU.Len())
	}
	return total
}

// makeEvictionFn returns the LRU eviction callback for a specific pod.
// When a hash is evicted from the pod's LRU, this callback removes the pod
// from the forward map (hashToPods). If the pod was the last server for that
// hash, the hash entry is deleted entirely.
//
// IMPORTANT: The hashicorp LRU library calls onEvictedCB OUTSIDE its
// internal lock. This callback is safe because it is only ever invoked
// during Add(), RemovePod(), or Resize() calls, all of which hold
// idx.mu.Lock(). Therefore the callback must NOT acquire idx.mu (it's
// already held by the caller).
func (idx *approximateIndexer) makeEvictionFn(server ApproximateServerID) func(ApproximateBlockHash, time.Time) {
	return func(key ApproximateBlockHash, _ time.Time) {
		if ps, ok := idx.hashToPods[key]; ok {
			delete(ps, server)
			if len(ps) == 0 {
				delete(idx.hashToPods, key)
			}
		}
	}
}

// reportLRUSize periodically logs the indexer size for observability.
func (idx *approximateIndexer) reportLRUSize(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idx.mu.RLock()
			total := int64(0)
			podCount := len(idx.podToLRU)
			maxEntries := 0
			for _, podLRU := range idx.podToLRU {
				n := podLRU.Len()
				total += int64(n)
				if n > maxEntries {
					maxEntries = n
				}
			}
			idx.mu.RUnlock()

			logger := log.FromContext(ctx).V(logutil.TRACE)
			logger.Info("Approximate indexer size",
				"totalEntries", total,
				"pods", podCount,
				"maxEntriesPerPod", maxEntries)
		}
	}
}
