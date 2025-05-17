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

package prefixstore

import (
	"github.com/daulet/tokenizers"
)

// Config holds the configuration for the Indexer module.
type Config struct {
	*LRUStoreConfig
}

// DefaultConfig returns the default configuration for the Indexer module.
func DefaultConfig() *Config {
	return &Config{
		LRUStoreConfig: defaultLRUStoreConfig(),
	}
}

// Indexer interface defines the methods for managing tokenization data.
// It allows looking up the longest tokenization prefix for a given
// model-name and prompt.
// TODO: generalize interface to a generic prefix-based store.
type Indexer interface {
	// AddTokenization adds the full tokenization of a string to the
	// indexer for a given model.
	// The function assumes tokens and offsets are of the same length.
	// The function assumes that tokens will not be mutated after the call.
	AddTokenization(modelName string, prompt string, tokens []uint32, offsets []tokenizers.Offset) error
	// FindLongestContainedTokens finds the sequence of contained tokens for
	// the longest matching prefix.
	FindLongestContainedTokens(prompt, modelName string) []uint32
}
