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
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
)

func TestServer_chatCompletionsHandler(t *testing.T) {
	tests := []struct {
		name     string
		port     string
		sampling bool
		r        *http.Request

		expectedCode        int
		expectedPrefillerIn []string
		expectedPassthrough bool
	}{
		{r: &http.Request{}, expectedPassthrough: true},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{}}}, expectedPassthrough: true},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a"}}}, expectedPrefillerIn: []string{"a"}},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a,b"}}}, expectedPrefillerIn: []string{"a"}},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a,b"}}}, sampling: true, expectedPrefillerIn: []string{"a", "b"}},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{" a, b"}}}, sampling: true, expectedPrefillerIn: []string{"a", "b"}},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a,a"}}}, sampling: true, expectedPrefillerIn: []string{"a"}},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"a", "b"}}}, sampling: true, expectedPrefillerIn: []string{"a", "b"}},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{""}}}, sampling: true, expectedPassthrough: true},
		{r: &http.Request{Header: http.Header{http.CanonicalHeaderKey(common.PrefillPodHeader): []string{"", ""}}}, sampling: true, expectedPassthrough: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			s, err := NewProxy(tt.port, nil, Config{EnablePrefillerSampling: tt.sampling})
			if err != nil {
				t.Fatalf("could not construct receiver type: %v", err)
			}
			for i := 0; i < max(1, len(tt.expectedPrefillerIn)*3); i++ {
				var prefiller string
				s.runConnectorProtocol = func(_ http.ResponseWriter, _ *http.Request, hostPort string) { prefiller = hostPort }
				var passthrough bool
				s.decoderProxy = http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					passthrough = true
				})
				recorder := httptest.NewRecorder()
				recorder.Code = 0
				s.chatCompletionsHandler(recorder, tt.r)
				if passthrough {
					if !tt.expectedPassthrough {
						t.Errorf("unexpected passthrough to decode")
					}
					if recorder.Body.Len() > 0 || recorder.Code != 0 || len(recorder.Header()) > 0 {
						t.Errorf("unexpected write to response: %#v", recorder)
					}
				} else {
					if tt.expectedPassthrough {
						t.Fatal("unexpected handled request")
					}
					if recorder.Code != tt.expectedCode {
						t.Errorf("unexpected code: %d", recorder.Code)
					}
					if !slices.Contains(tt.expectedPrefillerIn, prefiller) {
						t.Errorf("unexpected prefiller %s", prefiller)
					}
				}
			}
		})
	}
}
