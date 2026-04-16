package scorer

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"

	"github.com/cespare/xxhash/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// ApproximateBlockHash is an xxhash64-based block hash for approximate prefix matching.
// Unlike the precise scorer's kvblock.BlockHash (SHA256, token-based), this uses
// character-level chunking with xxhash for fast, tokenizer-free hashing.
type ApproximateBlockHash uint64

const (
	// averageCharactersPerToken is the estimated average characters per token,
	// used to approximate token-level block boundaries without a tokenizer.
	averageCharactersPerToken = 4

	// defaultApproxBlockSizeTokens is the default block size in tokens for approximate mode.
	// Matches vLLM's default block size.
	defaultApproxBlockSizeTokens = 16

	// defaultApproxMaxPrefixBlocks is the maximum number of prefix blocks to hash per request.
	defaultApproxMaxPrefixBlocks = 256
)

// hashPrompt computes rolling xxhash64 block hashes from the request prompt.
//
// The algorithm:
//  1. Extract serialized user input from the request (supports 4 API types)
//  2. Convert block size from tokens to characters (tokens * 4)
//  3. Seed the hash chain with the model name + optional CacheSalt
//  4. For each fixed-size character block: hash(block_content + prev_hash)
//
// The rolling chain ensures that identical prefixes produce identical hash sequences
// regardless of suffix, enabling greedy longest-prefix matching.
func hashPrompt(ctx context.Context, request *scheduling.LLMRequest, blockSizeTokens, maxPrefixBlocks int) []ApproximateBlockHash {
	logger := log.FromContext(ctx).V(logutil.DEBUG)

	if request == nil || request.Body == nil {
		logger.Info("Request or request data is nil, skipping hashing")
		return nil
	}

	userInput, err := getUserInputBytes(request)
	if err != nil {
		logger.Error(err, "Failed to get user input bytes")
		return nil
	}

	// Convert block size from tokens to characters.
	cacheBlockSizeChars := blockSizeTokens * averageCharactersPerToken

	if len(userInput) < cacheBlockSizeChars {
		logger.Info("Request body too small for prefix cache",
			"size", len(userInput), "blockSizeChars", cacheBlockSizeChars)
		return nil
	}

	// Truncate to maxPrefixBlocks to bound hashing cost.
	if len(userInput) > cacheBlockSizeChars*maxPrefixBlocks {
		logger.Info("Truncating input",
			"size", len(userInput), "maxPrefixBlocks", maxPrefixBlocks,
			"blockSizeChars", cacheBlockSizeChars)
		userInput = userInput[:maxPrefixBlocks*cacheBlockSizeChars]
	}

	res := make([]ApproximateBlockHash, 0, len(userInput)/cacheBlockSizeChars)

	// Seed the chain with model name + optional CacheSalt so that
	// different models produce different hashes for the same prompt.
	h := xxhash.New()
	_, _ = h.Write([]byte(request.TargetModel))
	if cacheSalt := request.Body.CacheSalt(); cacheSalt != "" {
		_, _ = h.Write([]byte(cacheSalt))
	}
	prevBlockHash := ApproximateBlockHash(h.Sum64())

	// Rolling hash chain: each block depends on the previous block's hash.
	for i := 0; i+cacheBlockSizeChars <= len(userInput); i += cacheBlockSizeChars {
		h.Reset()
		_, _ = h.Write(userInput[i : i+cacheBlockSizeChars])
		_, _ = h.Write(approxHashToBytes(prevBlockHash))
		hash := ApproximateBlockHash(h.Sum64())
		res = append(res, hash)
		prevBlockHash = hash
	}

	return res
}

// getUserInputBytes extracts a serializable byte representation of the user's
// input from the request body. Each API format is handled to produce a
// deterministic byte sequence suitable for hashing.
func getUserInputBytes(request *scheduling.LLMRequest) ([]byte, error) {
	if request.Body == nil {
		return nil, errors.New("request body is nil")
	}

	switch {
	case request.Body.Conversations != nil:
		return json.Marshal(request.Body.Conversations.Items)

	case request.Body.Responses != nil:
		if request.Body.Responses.Input == nil {
			return nil, errors.New("responses request has nil input")
		}
		// Ordered slice ensures deterministic marshaling:
		// instructions -> tools -> input
		var combined []map[string]interface{}
		if request.Body.Responses.Instructions != nil {
			combined = append(combined, map[string]interface{}{
				"instructions": request.Body.Responses.Instructions,
			})
		}
		if request.Body.Responses.Tools != nil {
			combined = append(combined, map[string]interface{}{
				"tools": request.Body.Responses.Tools,
			})
		}
		combined = append(combined, map[string]interface{}{
			"input": request.Body.Responses.Input,
		})
		return json.Marshal(combined)

	case request.Body.ChatCompletions != nil:
		return json.Marshal(request.Body.ChatCompletions.Messages)

	case request.Body.Completions != nil:
		return []byte(request.Body.Completions.Prompt), nil

	default:
		return nil, errors.New("invalid request body: no recognized API format found")
	}
}

// approxHashToBytes converts an ApproximateBlockHash to an 8-byte little-endian
// representation for use in the rolling hash chain.
func approxHashToBytes(h ApproximateBlockHash) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(h))
	return b
}
