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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"k8s.io/klog/v2"

	"github.com/llm-d/llm-d-kv-cache-manager/pkg/utils"
)

const defaultChunkSize = 256

// TokenProcessorConfig holds the configuration for the token processor.
type TokenProcessorConfig struct {
	ChunkSize int
	*TemporaryTokenProcessorConfig
}

// DefaultTokenProcessorConfig returns the default configuration for the token processor.
func DefaultTokenProcessorConfig() *TokenProcessorConfig {
	return &TokenProcessorConfig{
		ChunkSize: defaultChunkSize,
		TemporaryTokenProcessorConfig: &TemporaryTokenProcessorConfig{
			Fmt:       "vllm",
			WorldSize: 1,
			WorkerID:  0,
		},
	}
}

// TemporaryTokenProcessorConfig is a temporary structure to hold
// configuration to align with the current LMCache state.
// TODO: remove after updating LMCacheEngine.
type TemporaryTokenProcessorConfig struct {
	Fmt       string
	WorldSize int
	WorkerID  int
}

// TokenProcessor defines the interface for converting tokens to
// KVBlockKeys.
type TokenProcessor interface {
	// TokensToKVBlockKeys converts tokens into KVBlockKeys.
	TokensToKVBlockKeys(tokens []uint32, modelName string) []KVBlockKey
}

// KVBlockKey is equivalent to the LMCacheEngineKey in the Python code.
type KVBlockKey struct {
	ModelName string
	ChunkHash string
}

// String returns a string representation of the CacheEngineKey.
func (c KVBlockKey) String() string {
	return fmt.Sprintf("%s@%s", c.ModelName, c.ChunkHash)
}

// ChunkedTokenDatabase is a concrete implementation of TokenDatabase.
// It mimics the ChunkedTokenDatabase in the Python code.
type ChunkedTokenDatabase struct {
	TokenProcessorConfig
}

var _ TokenProcessor = &ChunkedTokenDatabase{}

// NewChunkedTokenDatabase creates a new instance with the given config and metadata.
func NewChunkedTokenDatabase(config *TokenProcessorConfig) TokenProcessor {
	if config == nil {
		config = DefaultTokenProcessorConfig()
	} // TODO: validate?

	return &ChunkedTokenDatabase{
		TokenProcessorConfig: *config,
	}
}

// getInitHash returns the initial hash.
func (db *ChunkedTokenDatabase) getInitHash() string {
	return ""
}

// hash computes the SHA-256 hash of the concatenation of the prefixHash and the binary
// representation of the tokens slice. It returns the hex-encoded string.
func (db *ChunkedTokenDatabase) hash(tokens []uint32, prefixHash string) string {
	buf := new(bytes.Buffer)
	// write the prefixHash bytes (ASCII encoding)
	buf.WriteString(prefixHash)
	// write each token to the buffer as binary data (using 64-bit big-endian format)
	for _, token := range tokens {
		// convert token to int64 for binary consistency
		// LittleEndian is important to match the Python code
		if err := binary.Write(buf, binary.LittleEndian, token); err != nil {
			klog.FromContext(context.Background()).Error(err, "failed to write token to buffer")
		}
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// chunkTokens splits the input slice of tokens into chunks of size chunkSize.
func (db *ChunkedTokenDatabase) chunkTokens(tokens []uint32) [][]uint32 {
	var chunks [][]uint32
	for i := 0; i < len(tokens); i += db.ChunkSize {
		end := i + db.ChunkSize
		if end > len(tokens) {
			break // no partial blocks
		}

		chunks = append(chunks, tokens[i:end])
	}

	return chunks
}

// prefixHashes computes the rolling (prefix) hash for each chunk and
// returns a slice of hash strings. It starts from the initial hash
// and then for each token chunk it computes the new hash.
func (db *ChunkedTokenDatabase) prefixHashes(tokenChunks [][]uint32) []string {
	prefixHash := db.getInitHash()
	hashes := make([]string, len(tokenChunks))
	for i, chunk := range tokenChunks {
		prefixHash = db.hash(chunk, prefixHash)
		hashes[i] = prefixHash
	}
	return hashes
}

// TokensToKVBlockKeys converts tokens into KVBlockKeys.
func (db *ChunkedTokenDatabase) TokensToKVBlockKeys(tokens []uint32, modelName string) []KVBlockKey {
	tokenChunks := db.chunkTokens(tokens)
	prefixHashes := db.prefixHashes(tokenChunks)

	return utils.SliceMap(prefixHashes, func(hashVal string) KVBlockKey {
		return KVBlockKey{
			ModelName: modelName,
			ChunkHash: hashVal,
		}
	})
}
