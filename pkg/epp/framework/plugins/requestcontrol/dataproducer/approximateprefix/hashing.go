/*
Copyright 2026 The Kubernetes Authors.

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

package approximateprefix

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/cespare/xxhash/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	logutil "github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

const (
	multiModalPlaceHolder = "P"
)

type KVCacheBlock struct {
	// PseudoTokens are used when we just have prompts
	PseudoTokens []string `json:"PseudoTokens"`
	// PseudoExtraHash are used when we just have prompts
	PseudoExtraHash []string `json:"PseudoExtraHash"`
	// Tokens are used when we have token input, ExtraHash will be added once we have better idea about the extra hash format of token input
	Tokens []uint32 `json:"Tokens"`
}

// getBlockHashes divides the prompt into blocks and calculates a prefix cache hash for each block.
// The first block hash includes the model name and cache salt (if provided).
// For subsequent blocks, the hash is calculated as: hash(block i content, hash(i-1)).
func getBlockHashes(ctx context.Context, request *scheduling.InferenceRequest, blockSizeTokens int, maxPrefixBlocks int, tokenEstimator TokenEstimator) []blockHash {
	loggerDebug := log.FromContext(ctx).V(logutil.DEBUG)
	if request == nil || request.Body == nil {
		loggerDebug.Info("Request or request data is nil, skipping hashing")
		return nil
	}

	kvCacheBlocks, err := getKVCacheBlocksFromRawPrompt(ctx, request, blockSizeTokens, tokenEstimator)
	if err != nil {
		loggerDebug.Error(err, "Failed to get kv cache blocks")
		return nil
	}

	if len(kvCacheBlocks) == 0 {
		loggerDebug.Info("No kv cache block found")
		return nil
	}

	if len(kvCacheBlocks) > maxPrefixBlocks {
		loggerDebug.Info("Truncating input kv cache blocks", "blocks", len(kvCacheBlocks), "max prefix blocks", maxPrefixBlocks)
		kvCacheBlocks = kvCacheBlocks[:maxPrefixBlocks]
	}

	return computeBlockHashes(kvCacheBlocks, request)
}

// computeBlockHashes calculates the hash for content blocks.
func computeBlockHashes(kvCacheBlocks []KVCacheBlock, request *scheduling.InferenceRequest) []blockHash {
	blockHashes := make([]blockHash, 0, len(kvCacheBlocks))

	h := xxhash.New()
	// Different models should have different hashes even with the same body.
	_, _ = h.Write([]byte(request.TargetModel))
	if cacheSalt := request.Body.CacheSalt(); cacheSalt != "" {
		_, _ = h.Write([]byte(cacheSalt))
	}

	prevBlockHash := blockHash(h.Sum64())

	for _, block := range kvCacheBlocks {
		h.Reset()
		blockBytes, _ := json.Marshal(block)
		_, _ = h.Write(blockBytes)
		_, _ = h.Write(toBytes(prevBlockHash))
		blockHashes = append(blockHashes, blockHash(h.Sum64()))

		prevBlockHash = blockHashes[len(blockHashes)-1]
	}

	return blockHashes
}

func toBytes(i blockHash) []byte {
	bytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(bytes, uint64(i))
	return bytes
}

func getKVCacheBlocksFromRawPrompt(ctx context.Context, request *scheduling.InferenceRequest, blockSizeTokens int, tokenEstimator TokenEstimator) ([]KVCacheBlock, error) {
	switch {
	case request.Body.Conversations != nil:
		rawBytes, err := json.Marshal(request.Body.Conversations.Items)
		if err != nil {
			return nil, errors.New("failed to marshal conversations")
		}
		return getKVCacheBlocksFromRawBytes(rawBytes, blockSizeTokens), nil
	case request.Body.Responses != nil:
		var combined []map[string]interface{}
		if request.Body.Responses.Instructions != nil {
			combined = append(combined, map[string]interface{}{"instructions": request.Body.Responses.Instructions})
		}
		if request.Body.Responses.Tools != nil {
			combined = append(combined, map[string]interface{}{"tools": request.Body.Responses.Tools})
		}
		combined = append(combined, map[string]interface{}{"input": request.Body.Responses.Input})
		rawBytes, err := json.Marshal(combined)
		if err != nil {
			return nil, errors.New("failed to marshal responses")
		}
		return getKVCacheBlocksFromRawBytes(rawBytes, blockSizeTokens), nil

	case request.Body.ChatCompletions != nil:
		return getKVCacheBlocksFromChatCompletions(ctx, request, blockSizeTokens, tokenEstimator), nil

	case request.Body.Completions != nil:
		rawBytes := []byte(request.Body.Completions.Prompt.PlainText())
		return getKVCacheBlocksFromRawBytes(rawBytes, blockSizeTokens), nil

	case request.Body.Embeddings != nil:
		rawBytes, err := json.Marshal(request.Body.Embeddings.Input)
		if err != nil {
			return nil, errors.New("failed to marshal embeddings")
		}
		return getKVCacheBlocksFromRawBytes(rawBytes, blockSizeTokens), nil

	default:
		return nil, errors.New("invalid request body: no recognized API format found")
	}
}

func getKVCacheBlocksFromRawBytes(rawBytes []byte, blockSizeTokens int) []KVCacheBlock {
	if len(rawBytes) == 0 {
		return nil
	}

	blockSizeBytes := blockSizeTokens * averageCharactersPerToken

	numBlocks := (len(rawBytes) + blockSizeBytes - 1) / blockSizeBytes
	blocks := make([]KVCacheBlock, 0, numBlocks)

	for i := 0; i < len(rawBytes); i += blockSizeBytes {
		blockEnd := i + blockSizeBytes
		if blockEnd > len(rawBytes) {
			blockEnd = len(rawBytes)
		}

		// Pre-allocate tokens slice for this specific block
		numTokens := (blockEnd - i + averageCharactersPerToken - 1) / averageCharactersPerToken
		pseudoTokens := make([]string, 0, numTokens)

		// Slice out the individual tokens for this block
		for j := i; j < blockEnd; j += averageCharactersPerToken {
			tokenEnd := j + averageCharactersPerToken
			if tokenEnd > blockEnd {
				tokenEnd = blockEnd
			}

			// Safe type conversion
			pseudoTokens = append(pseudoTokens, string(rawBytes[j:tokenEnd]))
		}

		blocks = append(blocks, KVCacheBlock{
			PseudoTokens: pseudoTokens,
		})
	}

	return blocks
}

func getKVCacheBlocksFromChatCompletions(ctx context.Context, request *scheduling.InferenceRequest, blockSizeTokens int, tokenEstimator TokenEstimator) []KVCacheBlock {
	logger := log.FromContext(ctx).V(logutil.DEBUG)
	messages := request.Body.ChatCompletions.Messages
	var allPseudoTokens []string
	var pseudoTokensExtraHashes []string

	for _, msg := range messages {
		if msg.Content.Raw != "" {
			appendTextTokens(msg.Content.Raw, &allPseudoTokens, &pseudoTokensExtraHashes)
		} else if len(msg.Content.Structured) > 0 {
			for _, block := range msg.Content.Structured {
				switch block.Type {
				case "text":
					appendTextTokens(block.Text, &allPseudoTokens, &pseudoTokensExtraHashes)
				case "image_url":
					url := block.ImageURL.URL
					numPlaceHolders := tokenEstimator.Estimate(fwkrh.ContentBlock{
						Type:     "image_url",
						ImageURL: fwkrh.ImageBlock{URL: url},
					})

					h := xxhash.New()
					_, _ = h.Write([]byte(url))
					imgHash := strconv.FormatUint(h.Sum64(), 16)
					for i := 0; i < numPlaceHolders; i++ {
						allPseudoTokens = append(allPseudoTokens, multiModalPlaceHolder)
						pseudoTokensExtraHashes = append(pseudoTokensExtraHashes, imgHash)
					}
				case "video_url":
					// Add video support later
					appendTextTokens(block.VideoURL.URL, &allPseudoTokens, &pseudoTokensExtraHashes)
				case "input_audio", "audio_url":
					// Add audio support later
					appendTextTokens(block.InputAudio.Data, &allPseudoTokens, &pseudoTokensExtraHashes)
				default:
					logger.Info("Unsupported block type: " + block.Type)

				}
			}
		}
	}

	var kvCacheBlocks []KVCacheBlock
	for i := 0; i < len(allPseudoTokens); i += blockSizeTokens {
		end := i + blockSizeTokens
		if end > len(allPseudoTokens) {
			end = len(allPseudoTokens)
		}

		blockTokens := allPseudoTokens[i:end]
		var extraHashes []string
		seenHashes := make(map[string]bool)
		for j := i; j < end; j++ {
			if pseudoTokensExtraHashes[j] != "" && !seenHashes[pseudoTokensExtraHashes[j]] {
				extraHashes = append(extraHashes, pseudoTokensExtraHashes[j])
				seenHashes[pseudoTokensExtraHashes[j]] = true
			}
		}

		kvCacheBlocks = append(kvCacheBlocks, KVCacheBlock{
			PseudoTokens:    blockTokens,
			PseudoExtraHash: extraHashes,
		})
	}

	return kvCacheBlocks
}

func appendTextTokens(text string, allTokens *[]string, tokenImageHashes *[]string) {
	for i := 0; i < len(text); i += averageCharactersPerToken {
		end := i + averageCharactersPerToken
		if end > len(text) {
			end = len(text)
		}
		*allTokens = append(*allTokens, text[i:end])
		*tokenImageHashes = append(*tokenImageHashes, "")
	}
}
