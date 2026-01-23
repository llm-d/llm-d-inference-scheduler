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

package runners_test

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/keys"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/runners/types"
	. "github.com/onsi/ginkgo/v2" // nolint:revive
	. "github.com/onsi/gomega"    // nolint:revive
)

var _ = Describe("NIXL Connector (v2)", func() {

	var testInfo *sidecarTestInfo

	BeforeEach(func() {
		testInfo = sidecarConnectionTestSetup(types.ConnectorNIXLV2)
	})

	It("should successfully send request to 1. prefill 2. decode with the correct fields", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := proxy.NewDisabledAllowlistValidator()
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.Addr()).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.Addr().String()

		By("sending a /v1/chat/completions request with prefill header")
		//nolint:goconst
		body := `{
				"model": "Qwen/Qwen2-0.5B",
				"messages": [
				  {"role": "user", "content": "Hello"}
				],
				"max_tokens": 50
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+proxy.ChatCompletionsPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Add(common.PrefillPodHeader, testInfo.prefillBackend.URL[len("http://"):])

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))

		Expect(testInfo.prefillHandler.CompletionRequests).To(HaveLen(1))
		prq1 := testInfo.prefillHandler.CompletionRequests[0]

		Expect(prq1).To(HaveKey(keys.RequestFieldKVTransferParams))
		kvTransferParams, ok := prq1[keys.RequestFieldKVTransferParams].(map[string]any)
		Expect(ok).To(BeTrue())

		Expect(kvTransferParams).To(HaveKeyWithValue(keys.RequestFieldDoRemoteDecode, true))
		Expect(kvTransferParams).To(HaveKeyWithValue(keys.RequestFieldDoRemotePrefill, false))
		Expect(kvTransferParams).To(HaveKeyWithValue(keys.RequestFieldRemoteBlockIDs, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(keys.RequestFieldRemoteEngineID, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(keys.RequestFieldRemoteHost, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(keys.RequestFieldRemotePort, BeNil()))

		Expect(prq1).To(HaveKeyWithValue("max_tokens", BeNumerically("==", 1)))
		Expect(prq1).To(HaveKeyWithValue("stream", false))
		Expect(prq1).ToNot(HaveKey("stream_options"))

		Expect(testInfo.prefillHandler.CompletionResponses).To(HaveLen(1))
		prp1 := testInfo.prefillHandler.CompletionResponses[0]
		Expect(prp1).To(HaveKey(keys.RequestFieldKVTransferParams))

		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.decodeHandler.CompletionRequests).To(HaveLen(1))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})
})
