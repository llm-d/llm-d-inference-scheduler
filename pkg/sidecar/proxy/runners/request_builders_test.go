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

package runners_test

import (
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/keys"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/runners"
	. "github.com/onsi/ginkgo/v2" //nolint:revive
	. "github.com/onsi/gomega"    //nolint:revive
)

var _ = Describe("SharedStorageRequestBuilder", func() {
	var factory *runners.SharedStorageRequestBuilderFactory

	BeforeEach(func() {
		factory = &runners.SharedStorageRequestBuilderFactory{}
	})

	Describe("PreparePrefillRequest", func() {
		It("should set stream=false, max_tokens=1, max_completion_tokens=1, cache_hit_threshold=0 and remove stream_options", func() {
			original := map[string]any{
				"model":                              "test-model",
				keys.RequestFieldStream:              true,
				keys.RequestFieldStreamOptions:       map[string]any{"include_usage": true},
				keys.RequestFieldMaxTokens:           50,
				keys.RequestFieldMaxCompletionTokens: 100,
			}

			builder := factory.New()
			prefill := builder.PreparePrefillRequest(original)

			Expect(prefill[keys.RequestFieldStream]).To(BeFalse())
			Expect(prefill[keys.RequestFieldMaxTokens]).To(Equal(1))
			Expect(prefill[keys.RequestFieldMaxCompletionTokens]).To(Equal(1))
			Expect(prefill[keys.RequestFieldCacheHitThreshold]).To(Equal(0))
			Expect(prefill).ToNot(HaveKey(keys.RequestFieldStreamOptions))
			Expect(prefill["model"]).To(Equal("test-model"))
		})

		It("should not modify the original request", func() {
			original := map[string]any{
				"model":                              "test-model",
				keys.RequestFieldStream:              true,
				keys.RequestFieldStreamOptions:       map[string]any{"include_usage": true},
				keys.RequestFieldMaxTokens:           50,
				keys.RequestFieldMaxCompletionTokens: 100,
			}

			builder := factory.New()
			builder.PreparePrefillRequest(original)

			Expect(original[keys.RequestFieldStream]).To(BeTrue())
			Expect(original[keys.RequestFieldMaxTokens]).To(Equal(50))
			Expect(original[keys.RequestFieldMaxCompletionTokens]).To(Equal(100))
			Expect(original).To(HaveKey(keys.RequestFieldStreamOptions))
		})
	})

	Describe("PrepareDecodeRequest", func() {
		It("should return a clone matching the original", func() {
			original := map[string]any{
				"model":                              "test-model",
				keys.RequestFieldStream:              true,
				keys.RequestFieldStreamOptions:       map[string]any{"include_usage": true},
				keys.RequestFieldMaxTokens:           50,
				keys.RequestFieldMaxCompletionTokens: 100,
			}

			builder := factory.New()
			builder.PreparePrefillRequest(original)
			decode := builder.PrepareDecodeRequest(original, nil)

			Expect(decode[keys.RequestFieldStream]).To(BeTrue())
			Expect(decode[keys.RequestFieldMaxTokens]).To(Equal(50))
			Expect(decode[keys.RequestFieldMaxCompletionTokens]).To(Equal(100))
			Expect(decode[keys.RequestFieldStreamOptions]).To(Equal(map[string]any{"include_usage": true}))
			Expect(decode["model"]).To(Equal("test-model"))
		})

		It("should not include fields that were absent from the original", func() {
			original := map[string]any{
				"model":                    "test-model",
				keys.RequestFieldMaxTokens: 50,
			}

			builder := factory.New()
			builder.PreparePrefillRequest(original)
			decode := builder.PrepareDecodeRequest(original, nil)

			Expect(decode).ToNot(HaveKey(keys.RequestFieldStream))
			Expect(decode).ToNot(HaveKey(keys.RequestFieldStreamOptions))
			Expect(decode).ToNot(HaveKey(keys.RequestFieldMaxCompletionTokens))
			Expect(decode[keys.RequestFieldMaxTokens]).To(Equal(50))
		})
	})
})

var _ = Describe("NIXLV2RequestBuilder", func() {
	var factory *runners.NIXLV2RequestBuilderFactory

	BeforeEach(func() {
		factory = &runners.NIXLV2RequestBuilderFactory{}
	})

	Describe("PreparePrefillRequest", func() {
		It("should include kv_transfer_params in addition to shared storage fields", func() {
			original := map[string]any{
				"model":                              "test-model",
				keys.RequestFieldStream:              true,
				keys.RequestFieldMaxTokens:           50,
				keys.RequestFieldMaxCompletionTokens: 100,
			}

			builder := factory.New()
			prefill := builder.PreparePrefillRequest(original)

			Expect(prefill[keys.RequestFieldStream]).To(BeFalse())
			Expect(prefill[keys.RequestFieldMaxTokens]).To(Equal(1))
			Expect(prefill[keys.RequestFieldMaxCompletionTokens]).To(Equal(1))
			Expect(prefill[keys.RequestFieldCacheHitThreshold]).To(Equal(0))

			kvParams, ok := prefill[keys.RequestFieldKVTransferParams].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(kvParams[keys.RequestFieldDoRemoteDecode]).To(BeTrue())
			Expect(kvParams[keys.RequestFieldDoRemotePrefill]).To(BeFalse())
			Expect(kvParams[keys.RequestFieldRemoteEngineID]).To(BeNil())
			Expect(kvParams[keys.RequestFieldRemoteBlockIDs]).To(BeNil())
			Expect(kvParams[keys.RequestFieldRemoteHost]).To(BeNil())
			Expect(kvParams[keys.RequestFieldRemotePort]).To(BeNil())
		})

		It("should not modify the original request", func() {
			original := map[string]any{
				"model":                    "test-model",
				keys.RequestFieldMaxTokens: 50,
			}

			builder := factory.New()
			builder.PreparePrefillRequest(original)

			Expect(original).ToNot(HaveKey(keys.RequestFieldKVTransferParams))
			Expect(original[keys.RequestFieldMaxTokens]).To(Equal(50))
		})
	})

	Describe("PrepareDecodeRequest", func() {
		It("should clone the original and add kv_transfer_params from prefill response", func() {
			original := map[string]any{
				"model":                              "test-model",
				keys.RequestFieldStream:              true,
				keys.RequestFieldMaxTokens:           50,
				keys.RequestFieldMaxCompletionTokens: 100,
			}
			prefillResponse := map[string]any{
				keys.RequestFieldKVTransferParams: map[string]any{
					"remote_block_ids": []any{1, 2, 3},
					"remote_engine_id": "test-engine",
				},
			}

			builder := factory.New()
			builder.PreparePrefillRequest(original)
			decode := builder.PrepareDecodeRequest(original, prefillResponse)

			Expect(decode[keys.RequestFieldStream]).To(BeTrue())
			Expect(decode[keys.RequestFieldMaxTokens]).To(Equal(50))
			Expect(decode[keys.RequestFieldMaxCompletionTokens]).To(Equal(100))
			Expect(decode[keys.RequestFieldKVTransferParams]).To(Equal(prefillResponse[keys.RequestFieldKVTransferParams]))
		})

		It("should not modify the original request", func() {
			original := map[string]any{
				"model":                    "test-model",
				keys.RequestFieldMaxTokens: 50,
			}
			prefillResponse := map[string]any{
				keys.RequestFieldKVTransferParams: map[string]any{
					"remote_block_ids": []any{1, 2, 3},
				},
			}

			builder := factory.New()
			builder.PreparePrefillRequest(original)
			builder.PrepareDecodeRequest(original, prefillResponse)

			Expect(original).ToNot(HaveKey(keys.RequestFieldKVTransferParams))
		})
	})
})
