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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/keys"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/runners/types"

	"github.com/llm-d/llm-d-inference-scheduler/test/sidecar/mock"
	. "github.com/onsi/ginkgo/v2" // nolint:revive
	. "github.com/onsi/gomega"    // nolint:revive
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type sidecarTestInfo struct {
	ctx            context.Context
	cancelFn       context.CancelFunc
	stoppedCh      chan struct{}
	decodeBackend  *httptest.Server
	decodeHandler  *mock.ChatCompletionHandler
	prefillBackend *httptest.Server
	prefillHandler *mock.ChatCompletionHandler
	decodeURL      *url.URL
	proxy          *proxy.Server
}

func newTestContext() context.Context {
	logger := zap.New(
		zap.WriteTo(GinkgoWriter),
		zap.UseDevMode(true),
	)
	log.SetLogger(logger)
	ctx := context.Background()
	log.IntoContext(ctx, logger) // not strictly needed since we called SetLogger to set default
	return ctx
}

var connectorsArr = []types.Connector{types.ConnectorSharedStorage, types.ConnectorNIXLV2}

var _ = Describe("Common Connector tests", func() {

	for _, connector := range connectorsArr {
		When(fmt.Sprintf("running with the %s connector", connector.String()), func() {
			// Regression test for commit bb181d6: Ensure that max_completion_tokens=1 in Prefill
			It("should set max_completion_tokens=1 in prefill and restore original value in decode", func() {
				testInfo := sidecarConnectionTestSetup(connector)

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

				req, err := http.NewRequest(http.MethodPost, proxyBaseAddr+proxy.ChatCompletionsPath, strings.NewReader(body))
				Expect(err).ToNot(HaveOccurred())
				req.Header.Add(common.PrefillPodHeader, testInfo.prefillBackend.URL[len("http://"):])

				rp, err := http.DefaultClient.Do(req)
				Expect(err).ToNot(HaveOccurred())

				if rp.StatusCode != 200 {
					bp, _ := io.ReadAll(rp.Body) //nolint:all
					Fail(string(bp))
				}

				By("verifying prefill request has max_completion_tokens=1")
				Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))
				Expect(testInfo.prefillHandler.CompletionRequests).To(HaveLen(1))
				prefillReq := testInfo.prefillHandler.CompletionRequests[0]

				Expect(prefillReq).To(HaveKeyWithValue(keys.RequestFieldMaxTokens, BeNumerically("==", 1)))
				Expect(prefillReq).To(HaveKeyWithValue(keys.RequestFieldMaxCompletionTokens, BeNumerically("==", 1)))

				By("verifying decode request has original max_completion_tokens=100")
				Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
				Expect(testInfo.decodeHandler.CompletionRequests).To(HaveLen(1))
				decodeReq := testInfo.decodeHandler.CompletionRequests[0]

				// The decode request should have the original max_completion_tokens value
				Expect(decodeReq).To(HaveKeyWithValue(keys.RequestFieldMaxCompletionTokens, BeNumerically("==", 100)))

				testInfo.cancelFn()
				<-testInfo.stoppedCh
			})

			// Regression test for commit bb181d6: Ensure max_completion_tokens is handled when not provided
			It("should set max_completion_tokens=1 in prefill when not provided in original request", func() {
				testInfo := sidecarConnectionTestSetup(connector)

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

				By("sending a /v1/chat/completions request without max_completion_tokens")
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

				By("verifying prefill request has max_completion_tokens=1")
				Expect(testInfo.prefillHandler.RequestCount.Load()).To(BeNumerically("==", 1))
				Expect(testInfo.prefillHandler.CompletionRequests).To(HaveLen(1))
				prefillReq := testInfo.prefillHandler.CompletionRequests[0]

				Expect(prefillReq).To(HaveKeyWithValue(keys.RequestFieldMaxTokens, BeNumerically("==", 1)))
				Expect(prefillReq).To(HaveKeyWithValue(keys.RequestFieldMaxCompletionTokens, BeNumerically("==", 1)))

				By("verifying decode request does not have max_completion_tokens since it wasn't in original request")
				Expect(testInfo.decodeHandler.RequestCount.Load()).To(BeNumerically("==", 1))
				Expect(testInfo.decodeHandler.CompletionRequests).To(HaveLen(1))
				decodeReq := testInfo.decodeHandler.CompletionRequests[0]

				// The decode request should not have max_completion_tokens if it wasn't in the original request
				Expect(decodeReq).ToNot(HaveKey(keys.RequestFieldMaxCompletionTokens))

				testInfo.cancelFn()
				<-testInfo.stoppedCh
			})
		})
	}
})

func sidecarConnectionTestSetup(connector types.Connector) *sidecarTestInfo {
	testInfo := sidecarTestInfo{}

	testInfo.ctx = newTestContext()
	testInfo.ctx, testInfo.cancelFn = context.WithCancel(testInfo.ctx)
	testInfo.stoppedCh = make(chan struct{})

	// Decoder
	testInfo.decodeHandler = &mock.ChatCompletionHandler{
		Connector: connector,
		Role:      mock.RoleDecode,
	}
	testInfo.decodeBackend = httptest.NewServer(testInfo.decodeHandler)
	DeferCleanup(testInfo.decodeBackend.Close)

	// Prefiller
	testInfo.prefillHandler = &mock.ChatCompletionHandler{
		Connector: connector,
		Role:      mock.RolePrefill,
	}
	testInfo.prefillBackend = httptest.NewServer(testInfo.prefillHandler)
	DeferCleanup(testInfo.prefillBackend.Close)

	// Proxy
	url, err := url.Parse(testInfo.decodeBackend.URL)
	Expect(err).ToNot(HaveOccurred())
	testInfo.decodeURL = url
	cfg := proxy.Config{Connector: connector}
	testInfo.proxy = proxy.NewProxy("0", testInfo.decodeURL, cfg) // port 0 to automatically choose one that's available.

	return &testInfo
}
