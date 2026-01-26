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

package runners

import (
	"maps"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/keys"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/runners/types"
)

// SharedStorageRequestBuilderFactory creates request builders for the shared-storage connector.
// This is the P/D protocol that uses shared storage for KV-cache transfer.
type SharedStorageRequestBuilderFactory struct{}

// New creates a new request builder for the shared-storage connector.
func (f *SharedStorageRequestBuilderFactory) New() types.RequestBuilder {
	return &sharedStorageRequestBuilder{}
}

type sharedStorageRequestBuilder struct {
	stream, streamOptions, maxTokens, maxCompletionTokens *any
}

func (b *sharedStorageRequestBuilder) PreparePrefillRequest(completionRequest map[string]any) map[string]any {
	prefillRequest := maps.Clone(completionRequest)
	if stream, ok := prefillRequest[keys.RequestFieldStream]; ok {
		b.stream = &stream
	}
	if streamOptions, ok := prefillRequest[keys.RequestFieldStreamOptions]; ok {
		b.streamOptions = &streamOptions
	}
	if maxTokens, ok := prefillRequest[keys.RequestFieldMaxTokens]; ok {
		b.maxTokens = &maxTokens
	}
	if maxCompletionTokens, ok := prefillRequest[keys.RequestFieldMaxCompletionTokens]; ok {
		b.maxCompletionTokens = &maxCompletionTokens
	}

	prefillRequest[keys.RequestFieldStream] = false
	prefillRequest[keys.RequestFieldMaxTokens] = 1
	prefillRequest[keys.RequestFieldMaxCompletionTokens] = 1
	prefillRequest[keys.RequestFieldCacheHitThreshold] = 0
	delete(prefillRequest, keys.RequestFieldStreamOptions)
	return prefillRequest
}

func (b *sharedStorageRequestBuilder) PrepareDecodeRequest(completionRequest map[string]any, _ map[string]any) map[string]any {
	decodeRequest := maps.Clone(completionRequest)
	delete(decodeRequest, keys.RequestFieldStream)
	if b.stream != nil {
		decodeRequest[keys.RequestFieldStream] = *b.stream
	}
	if b.streamOptions != nil {
		decodeRequest[keys.RequestFieldStreamOptions] = *b.streamOptions
	}
	delete(decodeRequest, keys.RequestFieldMaxTokens)
	if b.maxTokens != nil {
		decodeRequest[keys.RequestFieldMaxTokens] = *b.maxTokens
	}
	delete(decodeRequest, keys.RequestFieldMaxCompletionTokens)
	if b.maxCompletionTokens != nil {
		decodeRequest[keys.RequestFieldMaxCompletionTokens] = *b.maxCompletionTokens
	}
	return decodeRequest
}

// NIXLV2RequestBuilderFactory creates request builders for the NIXL v2 connector.
// NIXL v2 is the modern P/D protocol that transfers KV-cache metadata via request/response fields.
type NIXLV2RequestBuilderFactory struct{}

// New creates a new request builder for the NIXL v2 connector.
func (f *NIXLV2RequestBuilderFactory) New() types.RequestBuilder {
	return &nixlV2RequestBuilder{}
}

type nixlV2RequestBuilder struct {
	sharedStorageRequestBuilder
}

func (b *nixlV2RequestBuilder) PreparePrefillRequest(completionRequest map[string]any) map[string]any {
	prefillRequest := b.sharedStorageRequestBuilder.PreparePrefillRequest(completionRequest)
	prefillRequest[keys.RequestFieldKVTransferParams] = map[string]any{
		keys.RequestFieldDoRemoteDecode:  true,
		keys.RequestFieldDoRemotePrefill: false,
		keys.RequestFieldRemoteEngineID:  nil,
		keys.RequestFieldRemoteBlockIDs:  nil,
		keys.RequestFieldRemoteHost:      nil,
		keys.RequestFieldRemotePort:      nil,
	}
	return prefillRequest
}

func (b *nixlV2RequestBuilder) PrepareDecodeRequest(completionRequest map[string]any, prefillResponse map[string]any) map[string]any {
	decodeRequest := b.sharedStorageRequestBuilder.PrepareDecodeRequest(completionRequest, prefillResponse)
	decodeRequest[keys.RequestFieldKVTransferParams] = prefillResponse[keys.RequestFieldKVTransferParams]
	return decodeRequest
}
