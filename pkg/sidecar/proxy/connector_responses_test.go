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

package proxy

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	. "github.com/onsi/ginkgo/v2" // nolint:revive
	. "github.com/onsi/gomega"    // nolint:revive
)

var _ = Describe("NIXL Connector (v2) for Responses API", func() {

	var testInfo *sidecarTestInfo

	BeforeEach(func() {
		testInfo = sidecarConnectionTestSetup(ConnectorNIXLV2)
	})

	It("should successfully send responses API request to 1. prefill 2. decode with the correct fields", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.addr.String()

		By("sending a /v1/responses request with prefill header")
		body := `{
				"model": "gpt-4o",
				"input": "Hello, how are you?",
				"max_output_tokens": 50
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ResponsesPath, strings.NewReader(body))
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

		Expect(prq1).To(HaveKey(requestFieldKVTransferParams))
		kvTransferParams, ok := prq1[requestFieldKVTransferParams].(map[string]any)
		Expect(ok).To(BeTrue())

		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldDoRemoteDecode, true))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldDoRemotePrefill, false))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteBlockIDs, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteEngineID, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteHost, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemotePort, BeNil()))

		// Responses API uses max_output_tokens instead of max_tokens
		Expect(prq1).To(HaveKeyWithValue("max_output_tokens", BeNumerically("==", 1)))
		Expect(prq1).To(HaveKeyWithValue("stream", false))
		Expect(prq1).ToNot(HaveKey("stream_options"))

		Expect(testInfo.prefillHandler.CompletionResponses).To(HaveLen(1))
		prp1 := testInfo.prefillHandler.CompletionResponses[0]
		Expect(prp1).To(HaveKey(requestFieldKVTransferParams))

		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.decodeHandler.CompletionRequests).To(HaveLen(1))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})

	It("should set max_output_tokens=1 in prefill and restore original value in decode", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.addr.String()

		By("sending a /v1/responses request with max_output_tokens set")
		body := `{
				"model": "gpt-4o",
				"input": "Tell me a story",
				"max_output_tokens": 100
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ResponsesPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Add(common.PrefillPodHeader, testInfo.prefillBackend.URL[len("http://"):])

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		By("verifying prefill request has max_output_tokens=1")
		Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.prefillHandler.CompletionRequests).To(HaveLen(1))
		prefillReq := testInfo.prefillHandler.CompletionRequests[0]

		Expect(prefillReq).To(HaveKeyWithValue("max_output_tokens", BeNumerically("==", 1)))

		By("verifying decode request has original max_output_tokens=100")
		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.decodeHandler.CompletionRequests).To(HaveLen(1))
		decodeReq := testInfo.decodeHandler.CompletionRequests[0]

		// The decode request should have the original max_output_tokens value
		Expect(decodeReq).To(HaveKeyWithValue("max_output_tokens", BeNumerically("==", 100)))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})

	It("should handle responses API request without max_output_tokens", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.addr.String()

		By("sending a /v1/responses request without max_output_tokens")
		body := `{
				"model": "gpt-4o",
				"input": "Hello!"
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ResponsesPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Add(common.PrefillPodHeader, testInfo.prefillBackend.URL[len("http://"):])

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		By("verifying prefill request has max_output_tokens=1")
		Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.prefillHandler.CompletionRequests).To(HaveLen(1))
		prefillReq := testInfo.prefillHandler.CompletionRequests[0]

		Expect(prefillReq).To(HaveKeyWithValue("max_output_tokens", BeNumerically("==", 1)))

		By("verifying decode request does not have max_output_tokens since it wasn't in original request")
		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.decodeHandler.CompletionRequests).To(HaveLen(1))
		decodeReq := testInfo.decodeHandler.CompletionRequests[0]

		// The decode request should not have max_output_tokens if it wasn't in the original request
		Expect(decodeReq).ToNot(HaveKey("max_output_tokens"))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})

	It("should pass through responses API request when no prefill header is set", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.addr.String()

		By("sending a /v1/responses request without prefill header")
		body := `{
				"model": "gpt-4o",
				"input": "Hello, how are you?",
				"max_output_tokens": 50
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ResponsesPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		// Note: No prefill header is set

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		// Prefill should not be called
		Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 0))

		// Only decoder should be called (passthrough)
		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})

	It("should preserve stream settings in responses API request", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.addr.String()

		By("sending a /v1/responses request with streaming enabled")
		body := `{
				"model": "gpt-4o",
				"input": "Hello!",
				"max_output_tokens": 50,
				"stream": true
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ResponsesPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Add(common.PrefillPodHeader, testInfo.prefillBackend.URL[len("http://"):])

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		By("verifying prefill request has stream=false")
		Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		prefillReq := testInfo.prefillHandler.CompletionRequests[0]
		Expect(prefillReq).To(HaveKeyWithValue("stream", false))

		By("verifying decode request has stream=true restored")
		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		decodeReq := testInfo.decodeHandler.CompletionRequests[0]
		Expect(decodeReq).To(HaveKeyWithValue("stream", true))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})
})

var _ = Describe("NIXL Connector (v2) for Conversations API", func() {

	var testInfo *sidecarTestInfo

	BeforeEach(func() {
		testInfo = sidecarConnectionTestSetup(ConnectorNIXLV2)
	})

	It("should successfully send conversations API request to 1. prefill 2. decode with the correct fields", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.addr.String()

		By("sending a /v1/conversations request with prefill header")
		body := `{
				"items": [
					{"type": "message", "role": "user", "content": "Hello, how are you?"}
				],
				"max_output_tokens": 50
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ConversationsPath, strings.NewReader(body))
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

		Expect(prq1).To(HaveKey(requestFieldKVTransferParams))
		kvTransferParams, ok := prq1[requestFieldKVTransferParams].(map[string]any)
		Expect(ok).To(BeTrue())

		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldDoRemoteDecode, true))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldDoRemotePrefill, false))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteBlockIDs, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteEngineID, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteHost, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemotePort, BeNil()))

		// Conversations API uses max_output_tokens instead of max_tokens
		Expect(prq1).To(HaveKeyWithValue("max_output_tokens", BeNumerically("==", 1)))
		Expect(prq1).To(HaveKeyWithValue("stream", false))
		Expect(prq1).ToNot(HaveKey("stream_options"))

		Expect(testInfo.prefillHandler.CompletionResponses).To(HaveLen(1))
		prp1 := testInfo.prefillHandler.CompletionResponses[0]
		Expect(prp1).To(HaveKey(requestFieldKVTransferParams))

		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.decodeHandler.CompletionRequests).To(HaveLen(1))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})

	It("should set max_output_tokens=1 in prefill and restore original value in decode for conversations", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.addr.String()

		By("sending a /v1/conversations request with max_output_tokens set")
		body := `{
				"items": [
					{"type": "message", "role": "user", "content": "Tell me a story"}
				],
				"max_output_tokens": 100
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ConversationsPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Add(common.PrefillPodHeader, testInfo.prefillBackend.URL[len("http://"):])

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		By("verifying prefill request has max_output_tokens=1")
		Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.prefillHandler.CompletionRequests).To(HaveLen(1))
		prefillReq := testInfo.prefillHandler.CompletionRequests[0]

		Expect(prefillReq).To(HaveKeyWithValue("max_output_tokens", BeNumerically("==", 1)))

		By("verifying decode request has original max_output_tokens=100")
		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(testInfo.decodeHandler.CompletionRequests).To(HaveLen(1))
		decodeReq := testInfo.decodeHandler.CompletionRequests[0]

		// The decode request should have the original max_output_tokens value
		Expect(decodeReq).To(HaveKeyWithValue("max_output_tokens", BeNumerically("==", 100)))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})

	It("should pass through conversations API request when no prefill header is set", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := testInfo.proxy.Start(testInfo.ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())

			testInfo.stoppedCh <- struct{}{}
		}()

		time.Sleep(1 * time.Second)
		Expect(testInfo.proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + testInfo.proxy.addr.String()

		By("sending a /v1/conversations request without prefill header")
		body := `{
				"items": [
					{"type": "message", "role": "user", "content": "Hello!"}
				],
				"max_output_tokens": 50
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ConversationsPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		// Note: No prefill header is set

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		// Prefill should not be called
		Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 0))

		// Only decoder should be called (passthrough)
		Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))

		testInfo.cancelFn()
		<-testInfo.stoppedCh
	})
})
