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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/test/sidecar/mock"
	. "github.com/onsi/ginkgo/v2" // nolint:revive
	. "github.com/onsi/gomega"    // nolint:revive
	"k8s.io/klog/v2/ktesting"
)

var _ = Describe("NIXL Connector (v2)", func() {
	var (
		ctx            context.Context
		decodeBackend  *httptest.Server
		decodeHandler  *mock.ChatCompletionHandler
		prefillBackend *httptest.Server
		prefillHandler *mock.ChatCompletionHandler
		decodeURL      *url.URL
		proxy          *Server
	)

	BeforeEach(func() {
		_, ctx = ktesting.NewTestContext(GinkgoT())

		// Decoder
		decodeHandler = &mock.ChatCompletionHandler{
			Connector: ConnectorNIXLV2,
			Role:      mock.RoleDecode,
		}
		decodeBackend = httptest.NewServer(decodeHandler)
		DeferCleanup(decodeBackend.Close)

		// Prefiller
		prefillHandler = &mock.ChatCompletionHandler{
			Connector: ConnectorNIXLV2,
			Role:      mock.RolePrefill,
		}
		prefillBackend = httptest.NewServer(prefillHandler)
		DeferCleanup(prefillBackend.Close)

		// Proxy
		url, err := url.Parse(decodeBackend.URL)
		Expect(err).ToNot(HaveOccurred())
		decodeURL = url
		cfg := Config{Connector: ConnectorNIXLV2}
		proxy = NewProxy("0", decodeURL, cfg) // port 0 to automatically choose one that's available.
	})

	It("should successfully send request to 1. prefill 2. decode with the correct fields", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			validator := &AllowlistValidator{enabled: false}
			err := proxy.Start(ctx, nil, validator)
			Expect(err).ToNot(HaveOccurred())
		}()

		time.Sleep(1 * time.Second)
		Expect(proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + proxy.addr.String()

		By("sending a /v1/chat/completions request with prefill header")
		//nolint:goconst
		body := `{
				"model": "Qwen/Qwen2-0.5B",
				"messages": [
				  {"role": "user", "content": "Hello"}
				],
				"max_tokens": 50
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ChatCompletionsPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Add(common.PrefillPodHeader, prefillBackend.URL[len("http://"):])

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		Expect(prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))

		Expect(prefillHandler.CompletionRequests).To(HaveLen(1))
		prq1 := prefillHandler.CompletionRequests[0]

		Expect(prq1).To(HaveKey(requestFieldKVTransferParams))
		kvTransferParams, ok := prq1[requestFieldKVTransferParams].(map[string]any)
		Expect(ok).To(BeTrue())

		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldDoRemoteDecode, true))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldDoRemotePrefill, false))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteBlockIDs, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteEngineID, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemoteHost, BeNil()))
		Expect(kvTransferParams).To(HaveKeyWithValue(requestFieldRemotePort, BeNil()))

		Expect(prq1).To(HaveKeyWithValue("max_tokens", BeNumerically("==", 1)))
		Expect(prq1).To(HaveKeyWithValue("stream", false))
		Expect(prq1).ToNot(HaveKey("stream_options"))

		Expect(prefillHandler.CompletionResponses).To(HaveLen(1))
		prp1 := prefillHandler.CompletionResponses[0]
		Expect(prp1).To(HaveKey(requestFieldKVTransferParams))

		Expect(decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(decodeHandler.CompletionRequests).To(HaveLen(1))
	})

	// Regression test for commit bb181d6: Ensure that max_completion_tokens=1 in Prefill
	It("should set max_completion_tokens=1 in prefill and restore original value in decode", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			err := proxy.Start(ctx)
			Expect(err).ToNot(HaveOccurred())
		}()

		time.Sleep(1 * time.Second)
		Expect(proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + proxy.addr.String()

		By("sending a /v1/chat/completions request with max_completion_tokens set")
		//nolint:goconst
		body := `{
				"model": "Qwen/Qwen2-0.5B",
				"messages": [
				  {"role": "user", "content": "Hello"}
				],
				"max_tokens": 50,
				"max_completion_tokens": 100
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ChatCompletionsPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Add(common.PrefillPodHeader, prefillBackend.URL[len("http://"):])

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		By("verifying prefill request has max_completion_tokens=1")
		Expect(prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(prefillHandler.CompletionRequests).To(HaveLen(1))
		prefillReq := prefillHandler.CompletionRequests[0]

		Expect(prefillReq).To(HaveKeyWithValue("max_tokens", BeNumerically("==", 1)))
		Expect(prefillReq).To(HaveKeyWithValue("max_completion_tokens", BeNumerically("==", 1)))

		By("verifying decode request has original max_completion_tokens=100")
		Expect(decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(decodeHandler.CompletionRequests).To(HaveLen(1))
		decodeReq := decodeHandler.CompletionRequests[0]

		// The decode request should have the original max_completion_tokens value restored
		Expect(decodeReq).To(HaveKeyWithValue("max_completion_tokens", BeNumerically("==", 100)))
	})

	// Regression test for commit bb181d6: Ensure max_completion_tokens is handled when not provided
	It("should set max_completion_tokens=1 in prefill when not provided in original request", func() {
		By("starting the proxy")
		go func() {
			defer GinkgoRecover()

			err := proxy.Start(ctx)
			Expect(err).ToNot(HaveOccurred())
		}()

		time.Sleep(1 * time.Second)
		Expect(proxy.addr).ToNot(BeNil())
		proxyBaseAddr := "http://" + proxy.addr.String()

		By("sending a /v1/chat/completions request without max_completion_tokens")
		//nolint:goconst
		body := `{
				"model": "Qwen/Qwen2-0.5B",
				"messages": [
				  {"role": "user", "content": "Hello"}
				],
				"max_tokens": 50
			}`

		req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+ChatCompletionsPath, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Add(common.PrefillPodHeader, prefillBackend.URL[len("http://"):])

		rp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())

		if rp.StatusCode != 200 {
			bp, _ := io.ReadAll(rp.Body) //nolint:all
			Fail(string(bp))
		}

		By("verifying prefill request has max_completion_tokens=1")
		Expect(prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(prefillHandler.CompletionRequests).To(HaveLen(1))
		prefillReq := prefillHandler.CompletionRequests[0]

		Expect(prefillReq).To(HaveKeyWithValue("max_tokens", BeNumerically("==", 1)))
		Expect(prefillReq).To(HaveKeyWithValue("max_completion_tokens", BeNumerically("==", 1)))

		By("verifying decode request does not have max_completion_tokens since it wasn't in original request")
		Expect(decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
		Expect(decodeHandler.CompletionRequests).To(HaveLen(1))
		decodeReq := decodeHandler.CompletionRequests[0]

		// The decode request should not have max_completion_tokens if it wasn't in the original request
		Expect(decodeReq).ToNot(HaveKey("max_completion_tokens"))
	})
})
