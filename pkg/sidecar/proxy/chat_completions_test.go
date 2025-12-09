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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
)

func TestServer_chatCompletionsHandler(t *testing.T) {
	tests := []struct {
		name     string
		sampling bool
		r        *http.Request

		expectedCode             int
		expectedPrefillHostPorts []string
		expectedPassthrough      bool
	}{
		{
			name: "passthrough by default",
			r:    &http.Request{},

			expectedPassthrough: true,
		},
		{
			name: "passthrough with no header value",
			r:    &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{}}},

			expectedPassthrough: true,
		},
		{
			name: "default prefill to one header value",
			r:    &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a"}}},

			expectedCode:             200,
			expectedPrefillHostPorts: []string{"a"},
		},
		{
			name: "default prefill to first header value",
			r:    &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a,b"}}},

			expectedCode:             200,
			expectedPrefillHostPorts: []string{"a"},
		},
		{
			name:     "sample from comma delimited header",
			r:        &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a,b"}}},
			sampling: true,

			expectedCode:             200,
			expectedPrefillHostPorts: []string{"a", "b"},
		},
		{
			name:     "sample from comma delimited header with whitespace",
			r:        &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{" a, b"}}},
			sampling: true,

			expectedCode:             200,
			expectedPrefillHostPorts: []string{"a", "b"},
		},
		{
			name:     "sample from duplicate values",
			r:        &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a,a"}}},
			sampling: true,

			expectedCode:             200,
			expectedPrefillHostPorts: []string{"a"},
		},
		{
			name:     "sample from multiple header values",
			r:        &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a", "b"}}},
			sampling: true,

			expectedCode:             200,
			expectedPrefillHostPorts: []string{"a", "b"},
		},
		{
			name:     "sample from empty header value",
			r:        &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{""}}},
			sampling: true,

			expectedPassthrough: true,
		},
		{
			name:     "sample from multiple empty header values",
			r:        &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"", ""}}},
			sampling: true,

			expectedPassthrough: true,
		},
	}
	for _, tt := range tests {
		maxAttempts := len(tt.expectedPrefillHostPorts) + 1

		for i := 0; i < maxAttempts; i++ {
			t.Run(fmt.Sprintf("%s_%d", tt.name, i), func(t *testing.T) {
				s := NewProxy("8000", nil, Config{EnablePrefillerSampling: tt.sampling})
				s.allowlistValidator = &AllowlistValidator{}
				// return a predictable sequence of values
				s.prefillSamplerFn = func(n int) int { return i % n }
				// verify the hostPort value
				var hostPort string
				s.runConnectorProtocol = func(_ http.ResponseWriter, _ *http.Request, selectedHostPort string) { hostPort = selectedHostPort }
				var passthrough bool
				s.decoderProxy = http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					passthrough = true
				})
				s.dataParallelProxies = make(map[string]http.Handler)
				recorder := httptest.NewRecorder()
				recorder.Code = 0
				s.chatCompletionsHandler(recorder, tt.r)

				resp := recorder.Result()
				if passthrough {
					if !tt.expectedPassthrough {
						t.Errorf("unexpected passthrough to decode")
					}
					if recorder.Code != 0 || recorder.Body.Len() > 0 || len(resp.Header) > 0 {
						t.Errorf("unexpected write to recorder during passthrough: %#v %#v", recorder, resp)
					}
					if len(hostPort) > 0 {
						t.Errorf("unexpected hostPort set")
					}
				} else {
					if tt.expectedPassthrough {
						t.Fatal("unexpected handled request")
					}
					if resp.StatusCode != tt.expectedCode {
						t.Errorf("unexpected code: %d", resp.StatusCode)
					}
					expected, actual := tt.expectedPrefillHostPorts[i%len(tt.expectedPrefillHostPorts)], hostPort
					if expected != actual {
						t.Errorf("expected=%s actual=%s", expected, actual)
					}
				}
			})
		}
	}
}
