package scorer

import (
	"sync"
	"testing"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

func newTestIndexer(size int) *approximateIndexer {
	// Create indexer without background goroutine for tests.
	return &approximateIndexer{
		hashToPods:     make(map[ApproximateBlockHash]podSet),
		podToLRU:       make(map[ApproximateServerID]*lru.Cache[ApproximateBlockHash, time.Time]),
		defaultLRUSize: size,
	}
}

func serverID(name string) ApproximateServerID {
	return k8stypes.NamespacedName{Namespace: "default", Name: name}
}

func TestApproxIndexer_AddAndGet(t *testing.T) {
	idx := newTestIndexer(100)

	server1 := serverID("pod-a")
	server2 := serverID("pod-b")

	idx.Add([]ApproximateBlockHash{1, 2, 3}, server1, 0)
	idx.Add([]ApproximateBlockHash{2, 3, 4}, server2, 0)

	// Hash 1: only server1
	ps := idx.Get(1)
	require.Len(t, ps, 1)
	assert.Contains(t, ps, server1)

	// Hash 2, 3: both servers
	ps = idx.Get(2)
	require.Len(t, ps, 2)
	assert.Contains(t, ps, server1)
	assert.Contains(t, ps, server2)

	// Hash 4: only server2
	ps = idx.Get(4)
	require.Len(t, ps, 1)
	assert.Contains(t, ps, server2)

	// Hash 5: nobody
	ps = idx.Get(5)
	assert.Nil(t, ps)
}

func TestApproxIndexer_LRUEviction(t *testing.T) {
	// LRU capacity = 2 per pod
	idx := newTestIndexer(2)
	server := serverID("pod-a")

	// Add hashes 1, 2 (fills capacity)
	idx.Add([]ApproximateBlockHash{1, 2}, server, 0)
	assert.NotNil(t, idx.Get(1))
	assert.NotNil(t, idx.Get(2))

	// Add hash 3 → should evict hash 1 (LRU)
	idx.Add([]ApproximateBlockHash{3}, server, 0)
	assert.Nil(t, idx.Get(1), "hash 1 should be evicted from LRU")
	assert.NotNil(t, idx.Get(2))
	assert.NotNil(t, idx.Get(3))
}

func TestApproxIndexer_LRUEviction_CleansHashToPods(t *testing.T) {
	idx := newTestIndexer(2)
	server1 := serverID("pod-a")
	server2 := serverID("pod-b")

	// Both servers have hash 1
	idx.Add([]ApproximateBlockHash{1}, server1, 0)
	idx.Add([]ApproximateBlockHash{1}, server2, 0)
	require.Len(t, idx.Get(1), 2)

	// Evict hash 1 from server1 by filling its LRU
	idx.Add([]ApproximateBlockHash{2, 3}, server1, 0)

	// Hash 1 should still exist for server2
	ps := idx.Get(1)
	require.Len(t, ps, 1)
	assert.Contains(t, ps, server2)
}

func TestApproxIndexer_NumGPUBlocksOverride(t *testing.T) {
	idx := newTestIndexer(100) // default capacity = 100

	server := serverID("pod-a")

	// Override with numGPUBlocks=2
	idx.Add([]ApproximateBlockHash{1, 2}, server, 2)
	assert.NotNil(t, idx.Get(1))
	assert.NotNil(t, idx.Get(2))

	// Adding 3 should evict 1 (capacity=2 from numGPUBlocks override)
	idx.Add([]ApproximateBlockHash{3}, server, 2)
	assert.Nil(t, idx.Get(1), "should evict due to numGPUBlocks=2 capacity")
}

func TestApproxIndexer_RemovePod(t *testing.T) {
	idx := newTestIndexer(100)
	server1 := serverID("pod-a")
	server2 := serverID("pod-b")

	idx.Add([]ApproximateBlockHash{1, 2, 3}, server1, 0)
	idx.Add([]ApproximateBlockHash{2, 3, 4}, server2, 0)

	// Remove server1
	idx.RemovePod(server1)

	// Hash 1: was only in server1, should be gone entirely
	assert.Nil(t, idx.Get(1))

	// Hash 2, 3: should now only have server2
	ps := idx.Get(2)
	require.Len(t, ps, 1)
	assert.Contains(t, ps, server2)

	// Hash 4: still server2
	assert.NotNil(t, idx.Get(4))

	// server1 should not be in Pods()
	pods := idx.Pods()
	assert.Len(t, pods, 1)
	assert.Equal(t, server2, pods[0])
}

func TestApproxIndexer_MatchLongestPrefix(t *testing.T) {
	idx := newTestIndexer(100)
	server1 := serverID("pod-a")
	server2 := serverID("pod-b")

	// server1 has blocks [1, 2, 3]
	// server2 has blocks [1, 2]
	idx.Add([]ApproximateBlockHash{1, 2, 3}, server1, 0)
	idx.Add([]ApproximateBlockHash{1, 2}, server2, 0)

	// Query with [1, 2, 3, 4]
	// - At hash 1: both servers match
	// - At hash 2: both servers match
	// - At hash 3: only server1 matches (but server2 also increments to 2 since it won't be updated past here)
	// - At hash 4: nobody matches → stop
	result := idx.MatchLongestPrefix([]ApproximateBlockHash{1, 2, 3, 4})

	// server1 should have 3 (matched blocks 1, 2, 3)
	assert.Equal(t, 3, result[server1])
	// server2 should have 2 (matched blocks 1, 2; hash 3 had no cachedServers including server2, but wait...)
	// Actually: at hash 3, Get(3) returns {server1} only. len > 0, so we continue.
	// server2 doesn't have hash 3, so it's NOT incremented. server1 gets 3.
	// At hash 4, Get(4) returns nil, so we stop.
	assert.Equal(t, 2, result[server2])
}

func TestApproxIndexer_MatchLongestPrefix_Gap(t *testing.T) {
	idx := newTestIndexer(100)
	server := serverID("pod-a")

	// Server has blocks [1, 3] but NOT block 2
	idx.Add([]ApproximateBlockHash{1, 3}, server, 0)

	// Query [1, 2, 3]: should stop at hash 2 (gap)
	result := idx.MatchLongestPrefix([]ApproximateBlockHash{1, 2, 3})
	assert.Equal(t, 1, result[server], "should stop at first gap")
}

func TestApproxIndexer_MatchLongestPrefix_MultiServerGap(t *testing.T) {
	idx := newTestIndexer(100)
	serverA := serverID("pod-a")
	serverB := serverID("pod-b")

	// serverA has blocks [0, 2] (gap at block 1)
	// serverB has blocks [0, 1, 2] (contiguous)
	idx.Add([]ApproximateBlockHash{0, 2}, serverA, 0)
	idx.Add([]ApproximateBlockHash{0, 1, 2}, serverB, 0)

	result := idx.MatchLongestPrefix([]ApproximateBlockHash{0, 1, 2})

	// serverA: contiguous prefix is only block 0 (gap at block 1 eliminates it)
	assert.Equal(t, 1, result[serverA], "serverA should only count contiguous prefix (block 0)")
	// serverB: all 3 blocks contiguous
	assert.Equal(t, 3, result[serverB], "serverB should count all 3 contiguous blocks")
}

func TestApproxIndexer_MatchLongestPrefix_MidStreamJoin(t *testing.T) {
	idx := newTestIndexer(100)
	serverA := serverID("pod-a")
	serverC := serverID("pod-c")

	// serverA has blocks [1, 2, 3] (contiguous from start)
	// serverC has blocks [2, 3] only (missing block 1 — joins mid-stream)
	idx.Add([]ApproximateBlockHash{1, 2, 3}, serverA, 0)
	idx.Add([]ApproximateBlockHash{2, 3}, serverC, 0)

	result := idx.MatchLongestPrefix([]ApproximateBlockHash{1, 2, 3})

	// serverA: all 3 contiguous from start
	assert.Equal(t, 3, result[serverA])
	// serverC: NOT present at first hash → should NOT be counted at all
	assert.Equal(t, 0, result[serverC], "server missing first hash should have 0 score")
}

func TestApproxIndexer_MatchLongestPrefix_CrossElimination(t *testing.T) {
	idx := newTestIndexer(100)
	serverA := serverID("pod-a")
	serverB := serverID("pod-b")

	// serverA has [1, 2, 4] (gap at 3)
	// serverB has [1, 3, 4] (gap at 2)
	idx.Add([]ApproximateBlockHash{1, 2, 4}, serverA, 0)
	idx.Add([]ApproximateBlockHash{1, 3, 4}, serverB, 0)

	result := idx.MatchLongestPrefix([]ApproximateBlockHash{1, 2, 3, 4})

	// serverA: present at hash 1 and 2, but missing 3 → eliminated → score 2
	assert.Equal(t, 2, result[serverA])
	// serverB: present at hash 1, missing hash 2 → eliminated → score 1
	assert.Equal(t, 1, result[serverB])
}

func TestApproxIndexer_MatchLongestPrefix_Empty(t *testing.T) {
	idx := newTestIndexer(100)

	// No data in indexer
	result := idx.MatchLongestPrefix([]ApproximateBlockHash{1, 2, 3})
	assert.Empty(t, result)

	// Empty hashes
	result = idx.MatchLongestPrefix(nil)
	assert.Empty(t, result)
}

func TestApproxIndexer_Pods(t *testing.T) {
	idx := newTestIndexer(100)

	assert.Empty(t, idx.Pods())

	server1 := serverID("pod-a")
	server2 := serverID("pod-b")

	idx.Add([]ApproximateBlockHash{1}, server1, 0)
	idx.Add([]ApproximateBlockHash{2}, server2, 0)

	pods := idx.Pods()
	assert.Len(t, pods, 2)
}

func TestApproxIndexer_TotalEntries(t *testing.T) {
	idx := newTestIndexer(100)

	assert.Equal(t, int64(0), idx.TotalEntries())

	idx.Add([]ApproximateBlockHash{1, 2, 3}, serverID("pod-a"), 0)
	idx.Add([]ApproximateBlockHash{4, 5}, serverID("pod-b"), 0)

	assert.Equal(t, int64(5), idx.TotalEntries())
}

func TestApproxIndexer_ConcurrentAddRemovePod(t *testing.T) {
	// Stress test for race conditions
	for i := 0; i < 100; i++ {
		idx := newTestIndexer(10)
		server := serverID("pod-a")
		hashes := []ApproximateBlockHash{1, 2, 3, 4, 5}

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			idx.Add(hashes, server, 0)
		}()

		go func() {
			defer wg.Done()
			idx.RemovePod(server)
		}()

		wg.Wait()

		// After both goroutines complete, verify consistency:
		// If pod was removed, no hash should reference it.
		idx.mu.RLock()
		_, podExists := idx.podToLRU[server]
		for hash, ps := range idx.hashToPods {
			if _, hasPod := ps[server]; hasPod && !podExists {
				t.Errorf("iteration %d: hash %d references removed pod %s", i, hash, server.Name)
			}
		}
		idx.mu.RUnlock()
	}
}

func TestApproxIndexer_GetReturnsCopy(t *testing.T) {
	idx := newTestIndexer(100)
	server := serverID("pod-a")

	idx.Add([]ApproximateBlockHash{1}, server, 0)

	// Get should return a copy; mutating it shouldn't affect the indexer
	ps := idx.Get(1)
	delete(ps, server)

	// Original should be intact
	ps2 := idx.Get(1)
	assert.Contains(t, ps2, server)
}

func TestApproxIndexer_GetWithTimestamp(t *testing.T) {
	idx := newTestIndexer(100)
	server := serverID("pod-a")

	before := time.Now()
	idx.Add([]ApproximateBlockHash{1}, server, 0)
	after := time.Now()

	ts := idx.GetWithTimestamp(1)
	require.Len(t, ts, 1)
	assert.True(t, !ts[server].Before(before) && !ts[server].After(after),
		"timestamp should be between before and after Add()")

	// Non-existent hash
	assert.Nil(t, idx.GetWithTimestamp(999))
}

func TestApproxIndexer_MatchLongestPrefixWeighted(t *testing.T) {
	idx := newTestIndexer(100)
	server := serverID("pod-a")

	// Add hashes with known timestamps
	idx.Add([]ApproximateBlockHash{1, 2, 3}, server, 0)

	now := time.Now()
	halfLife := 30 * time.Second

	// All entries are fresh (just added) → weights should be close to 1.0 each
	result := idx.MatchLongestPrefixWeighted([]ApproximateBlockHash{1, 2, 3}, now, halfLife)
	// 3 blocks, each ~1.0 weight → total ~3.0
	assert.InDelta(t, 3.0, result[server], 0.1,
		"fresh entries should have weights close to 1.0 each")
}

func TestApproxIndexer_MatchLongestPrefixWeighted_Decay(t *testing.T) {
	idx := newTestIndexer(100)
	server := serverID("pod-a")

	idx.Add([]ApproximateBlockHash{1, 2, 3}, server, 0)

	// Simulate scoring 30 seconds later (exactly one half-life)
	futureNow := time.Now().Add(30 * time.Second)
	halfLife := 30 * time.Second

	result := idx.MatchLongestPrefixWeighted([]ApproximateBlockHash{1, 2, 3}, futureNow, halfLife)
	// age ≈ 30s, halfLife = 30s → weight per block ≈ 0.5
	// total ≈ 3 * 0.5 = 1.5
	assert.InDelta(t, 1.5, result[server], 0.1,
		"at exactly one half-life, each block should contribute ~0.5")
}

func TestApproxIndexer_MatchLongestPrefixWeighted_Empty(t *testing.T) {
	idx := newTestIndexer(100)
	result := idx.MatchLongestPrefixWeighted(nil, time.Now(), 30*time.Second)
	assert.Empty(t, result)

	result = idx.MatchLongestPrefixWeighted([]ApproximateBlockHash{1}, time.Now(), 0)
	assert.Empty(t, result, "zero half-life should return empty")
}

func TestApproxIndexer_MatchLongestPrefixWeighted_MidStreamJoin(t *testing.T) {
	idx := newTestIndexer(100)
	serverA := serverID("pod-a")
	serverC := serverID("pod-c")

	idx.Add([]ApproximateBlockHash{1, 2, 3}, serverA, 0)
	idx.Add([]ApproximateBlockHash{2, 3}, serverC, 0) // missing first hash

	result := idx.MatchLongestPrefixWeighted(
		[]ApproximateBlockHash{1, 2, 3}, time.Now(), 30*time.Second)

	assert.Greater(t, result[serverA], 0.0, "serverA should have score")
	assert.Equal(t, 0.0, result[serverC], "serverC missing first hash should have 0")
}
