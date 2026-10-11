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

package kvevents

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/llm-d/llm-d-router/pkg/kvcache/kvblock"
)

const defaultSubCanonicalCacheSize = 4096

// subCanonicalBlock holds the accumulated prefix state from the start of a
// canonical block up to a sub-canonical engine block boundary.
type subCanonicalBlock struct {
	parentReqKey    kvblock.BlockHash
	engineBlockSize int
	tokens          []uint32
	engineKeys      []kvblock.BlockHash
	extra           []*kvblock.BlockExtraFeatures
}

// subCanonicalTracker tracks sub-canonical engine block prefixes per pod so
// child BlockStored events referencing a sub-canonical parent hash can resolve
// the canonical parent key and accumulate tokens across events until complete
// canonical blocks are formed.
type subCanonicalTracker struct {
	maxEntriesPerPod int
	activeEntries    atomic.Int64
	mu               sync.Mutex
	pods             map[string]*simplelru.LRU[kvblock.BlockHash, subCanonicalBlock]
}

func newSubCanonicalTracker(maxEntriesPerPod int) *subCanonicalTracker {
	if maxEntriesPerPod <= 0 {
		maxEntriesPerPod = defaultSubCanonicalCacheSize
	}
	return &subCanonicalTracker{
		maxEntriesPerPod: maxEntriesPerPod,
		pods:             make(map[string]*simplelru.LRU[kvblock.BlockHash, subCanonicalBlock]),
	}
}

func (t *subCanonicalTracker) get(podID string, engineKey kvblock.BlockHash) (subCanonicalBlock, bool) {
	if t.activeEntries.Load() == 0 {
		return subCanonicalBlock{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	cache, ok := t.pods[podID]
	if !ok {
		return subCanonicalBlock{}, false
	}
	return cache.Get(engineKey)
}

func (t *subCanonicalTracker) recordSubBlocks(
	podID string,
	canonicalBlockSize int,
	engineBlockSize int,
	ratio int,
	prefixBlocks int,
	parentRequestKey kvblock.BlockHash,
	requestKeys []kvblock.BlockHash,
	combinedTokens []uint32,
	combinedEngineKeys []kvblock.BlockHash,
	combinedExtra []*kvblock.BlockExtraFeatures,
) {
	totalEngineBlocks := len(combinedEngineKeys)
	hasSubBlock := false
	for i := prefixBlocks; i < totalEngineBlocks; i++ {
		if (i%ratio)+1 < ratio {
			hasSubBlock = true
			break
		}
	}
	if !hasSubBlock {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	cache, ok := t.pods[podID]
	if !ok {
		cache, _ = simplelru.NewLRU[kvblock.BlockHash, subCanonicalBlock](t.maxEntriesPerPod, nil)
		t.pods[podID] = cache
	}

	before := cache.Len()
	for i := prefixBlocks; i < totalEngineBlocks; i++ {
		r := (i % ratio) + 1
		if r == ratio {
			continue
		}
		c := i / ratio
		chunkParent := parentRequestKey
		if c > 0 {
			chunkParent = requestKeys[c-1]
		}
		tokStart := c * canonicalBlockSize
		tokEnd := tokStart + r*engineBlockSize
		keyStart := c * ratio
		keyEnd := keyStart + r
		var subExtra []*kvblock.BlockExtraFeatures
		if combinedExtra != nil {
			subExtra = combinedExtra[keyStart:keyEnd:keyEnd]
		}
		cache.Add(combinedEngineKeys[i], subCanonicalBlock{
			parentReqKey:    chunkParent,
			engineBlockSize: engineBlockSize,
			tokens:          combinedTokens[tokStart:tokEnd:tokEnd],
			engineKeys:      combinedEngineKeys[keyStart:keyEnd:keyEnd],
			extra:           subExtra,
		})
	}
	if delta := cache.Len() - before; delta != 0 {
		t.activeEntries.Add(int64(delta))
	}
}

func (t *subCanonicalTracker) evict(podID string, hashes []uint64) {
	if t.activeEntries.Load() == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	cache, ok := t.pods[podID]
	if !ok {
		return
	}
	var removed int64
	for _, h := range hashes {
		if cache.Remove(kvblock.BlockHash(h)) {
			removed++
		}
	}
	if removed > 0 {
		t.activeEntries.Add(-removed)
	}
}

func (t *subCanonicalTracker) clearPod(podID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cache, ok := t.pods[podID]; ok {
		t.activeEntries.Add(-int64(cache.Len()))
		delete(t.pods, podID)
	}
}

func (p *Pool) processSubCanonicalStore(
	ctx context.Context,
	debugLogger logr.Logger,
	ev *BlockStoredEvent,
	podIdentifier string,
	effectiveModelName string,
	canonicalBlockSize int,
	engineBlockSize int,
	parentRequestKey kvblock.BlockHash,
	hasPrefix bool,
	prefix subCanonicalBlock,
	engineKeys []kvblock.BlockHash,
	extraFeatures []*kvblock.BlockExtraFeatures,
	podEntries []kvblock.PodEntry,
	storeScope blockScope,
) {
	ratio := canonicalBlockSize / engineBlockSize
	prefixBlocks := len(prefix.engineKeys)

	combinedTokens := ev.Tokens
	combinedEngineKeys := engineKeys
	if hasPrefix {
		combinedTokens = make([]uint32, len(prefix.tokens)+len(ev.Tokens))
		copy(combinedTokens, prefix.tokens)
		copy(combinedTokens[len(prefix.tokens):], ev.Tokens)

		combinedEngineKeys = make([]kvblock.BlockHash, prefixBlocks+len(engineKeys))
		copy(combinedEngineKeys, prefix.engineKeys)
		copy(combinedEngineKeys[prefixBlocks:], engineKeys)
	}

	var combinedExtra []*kvblock.BlockExtraFeatures
	if prefix.extra != nil || extraFeatures != nil {
		combinedExtra = make([]*kvblock.BlockExtraFeatures, len(combinedEngineKeys))
		copy(combinedExtra, prefix.extra)
		copy(combinedExtra[prefixBlocks:], extraFeatures)
	}

	fullCanonicalBlocks := len(combinedEngineKeys) / ratio
	fullEngineBlocks := fullCanonicalBlocks * ratio
	var requestKeys []kvblock.BlockHash
	if fullCanonicalBlocks > 0 {
		var alignedExtra []*kvblock.BlockExtraFeatures
		if combinedExtra != nil {
			alignedExtra = realignExtraFeatures(combinedExtra[:fullEngineBlocks], fullCanonicalBlocks)
		}
		var err error
		requestKeys, err = p.tokenProcessor.TokensToKVBlockKeys(
			parentRequestKey,
			combinedTokens[:fullCanonicalBlocks*canonicalBlockSize],
			effectiveModelName,
			alignedExtra,
		)
		if err != nil {
			debugLogger.Error(err, "Failed to generate request keys",
				"podIdentifier", podIdentifier, "effectiveModelName", effectiveModelName)
			return
		}
		if err := p.index.Add(ctx, combinedEngineKeys[:fullEngineBlocks], requestKeys, podEntries); err != nil {
			debugLogger.Error(err, "Failed to add event to index",
				"podIdentifier", podIdentifier, "event", ev)
			return
		}
	}

	p.subBlocks.recordSubBlocks(
		podIdentifier,
		canonicalBlockSize,
		engineBlockSize,
		ratio,
		prefixBlocks,
		parentRequestKey,
		requestKeys,
		combinedTokens,
		combinedEngineKeys,
		combinedExtra,
	)
	p.dedup.trackStore(storeScope, ev.BlockHashes)
}
