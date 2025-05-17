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

package tokenization

import (
	"fmt"
	"path/filepath"
	"runtime"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/daulet/tokenizers"
)

// tokenizersCacheSize is the size of the LRU cache for tokenizers.
// 1 tokenizer per base-model (NOT LoRAs).
const tokenizersCacheSize = 20

// Tokenizer interface defines the methods for tokenization.
type Tokenizer interface {
	// Encode tokenizes the input string and returns the token IDs and offsets.
	Encode(input, modelName string) ([]uint32, []tokenizers.Offset, error)
}

// HFTokenizerConfig holds the configuration for the HuggingFace tokenizer.
type HFTokenizerConfig struct {
	HuggingFaceToken   string
	TokenizersCacheDir string
}

// DefaultHFTokenizerConfig returns a default configuration for the HuggingFace
// tokenizer.
func DefaultHFTokenizerConfig() *HFTokenizerConfig {
	return &HFTokenizerConfig{
		HuggingFaceToken:   "",
		TokenizersCacheDir: getTokenizerCacheDir(),
	}
}

// CachedHFTokenizer is implements the Tokenizer interface using
// bindings to HuggingFace's rust tokenizer.
// The implementation wraps an LRU-cache for holding loaded per-model
// tokenizers.
type CachedHFTokenizer struct {
	cfg   tokenizers.TokenizerConfigOption
	cache *lru.Cache[string, *tokenizers.Tokenizer]
}

// NewCachedHFTokenizer creates a new instance of HFTokenizer with the provided configuration.
func NewCachedHFTokenizer(config *HFTokenizerConfig) (Tokenizer, error) {
	var cfg tokenizers.TokenizerConfigOption

	if config.TokenizersCacheDir != "" {
		cfg = tokenizers.WithCacheDir(config.TokenizersCacheDir)
	}
	if config.HuggingFaceToken != "" {
		cfg = tokenizers.WithAuthToken(config.HuggingFaceToken)
	}

	tokenizersCache, err := lru.New[string, *tokenizers.Tokenizer](tokenizersCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize tokenizer cache: %w", err)
	}

	return &CachedHFTokenizer{
		cfg:   cfg,
		cache: tokenizersCache,
	}, nil
}

// Encode converts a string into token IDs.
func (t *CachedHFTokenizer) Encode(input, modelName string) ([]uint32, []tokenizers.Offset, error) {
	tk, ok := t.cache.Get(modelName)
	if !ok {
		tokenizer, err := tokenizers.FromPretrained(modelName, t.cfg)
		if err != nil {
			return nil, nil, err
		}

		t.cache.Add(modelName, tokenizer)
		tk = tokenizer
	}

	encodeOptions := []tokenizers.EncodeOption{
		tokenizers.WithReturnTypeIDs(),
		tokenizers.WithReturnOffsets(),
	}

	resp := tk.EncodeWithOptions(input, true, encodeOptions...)
	return resp.IDs, resp.Offsets, nil
}

// getTokenizerCacheDir returns the absolute path to the tokenizer cache directory relative to the project root.
func getTokenizerCacheDir() string {
	_, filename, _, _ := runtime.Caller(0) // this file
	base := filepath.Dir(filename)
	return filepath.Join(base, "..", "..", "bin")
}
