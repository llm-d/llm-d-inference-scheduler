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

package coordinate2e

import (
	_ "embed"
	"io"
	"net/http"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
)

//go:embed testdata/sglang_worker.py
var sglangWorker string

var _ = ginkgo.Describe("Coordinator SGLang streaming prefill", func() {
	ginkgo.When("prefill waits for decode", ginkgo.Ordered, testWrapper(func() {
		ginkgo.It("starts decode from the selected prefill response headers", func() {
			nsName := getNamespace()
			createInferencePool(nsName, true)
			expectPoolExists()
			for _, e := range eppsToCreate() {
				e.config = eppConfigSGLang
				createOneEndPointPicker(nsName, e)
			}
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "sglang-worker", Namespace: nsName},
				Data:       map[string]string{"worker.py": sglangWorker},
			}
			gomega.Expect(testConfig.K8sClient.Create(testConfig.Context, cm)).To(gomega.Succeed())
			specWorkload = append(specWorkload, "ConfigMap/sglang-worker")
			for _, role := range []string{"prefill", "decode"} {
				applyManifest(nsName, "testdata/sglang-worker.yaml", map[string]string{
					"${ROLE}": role, "${POOL_NAME}": poolNameBase,
				}, &specWorkload)
			}
			createCoordinator(nsName, coordinatorConfigSGLang)

			client := &http.Client{Timeout: requestTimeout}
			resp, err := client.Post(gatewayBaseURL()+reqcommon.PathSGLangGenerate, "application/json",
				strings.NewReader(`{"text":"hello","stream":true,"bootstrap_host":"untrusted","sampling_params":{"max_new_tokens":1}}`))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK), "body=%s", body)
			gomega.Expect(string(body)).To(gomega.Equal("data: {\"text\":\"decode\"}\n\ndata: [DONE]\n\n"))
		})
	}))
})

const coordinatorConfigSGLang = `log_level: 5
server:
  listen_addr: ":8080"
  shutdown_timeout: 1s
gateway:
  address: "http://envoy.${NAMESPACE}.svc:8081"
  timeout: 60s
pipeline:
  use_openai_format: true
  kv_connector: kv-sglang
  steps:
  - type: prefill
  - type: decode
`

const eppConfigSGLang = `apiVersion: llm-d.ai/v1
kind: EndpointPickerConfig
plugins:
- type: sglanghttp-parser
- type: sglang-bootstrap-handler
- type: header-profile-handler
- type: prefill-filter
- type: label-selector-filter
  name: decode-filter
  parameters:
    matchLabels:
      llm-d.ai/role: decode
- type: max-score-picker
- type: endpoint-notification-source
- type: inflight-load-producer
- type: concurrency-detector
requestHandler:
  parsers:
  - pluginRef: sglanghttp-parser
dataLayer:
  injectDefaults: false
  sources:
  - pluginRef: endpoint-notification-source
    extractors:
    - pluginRef: inflight-load-producer
flowControl:
  saturationDetector:
    pluginRef: concurrency-detector
schedulingProfiles:
- name: prefill
  plugins:
  - pluginRef: prefill-filter
  - pluginRef: max-score-picker
- name: decode
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
`
