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

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
)

var (
	// ChatCompletionsPath is the OpenAI chat completions path
	ChatCompletionsPath = "/v1/chat/completions"

	// CompletionsPath is the legacy completions path
	CompletionsPath = "/v1/completions"
)

func (s *Server) chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	prefillPodHostPort := r.Header.Get(common.PrefillPodHeader)

	if prefillPodHostPort == "" {
		// backward compatible behavior: to remove in next release
		prefillPodHostPort = r.Header.Get(requestHeaderPrefillURL)
	}

	if prefillPodHostPort == "" {
		s.logger.V(4).Info("skip disaggregated prefill")
		s.decoderProxy.ServeHTTP(w, r)
		return
	}

	// SSRF Protection: Check if the prefill target is allowed
	if !s.allowlistValidator.IsAllowed(prefillPodHostPort) {
		s.logger.Error(nil, "SSRF protection: prefill target not in allowlist",
			"target", prefillPodHostPort,
			"clientIP", r.RemoteAddr,
			"userAgent", r.Header.Get("User-Agent"),
			"requestPath", r.URL.Path)
		http.Error(w, "Forbidden: prefill target not allowed by SSRF protection", http.StatusForbidden)
		return
	}

	s.logger.V(4).Info("SSRF protection: prefill target allowed", "target", prefillPodHostPort)
	s.runConnectorProtocol(w, r, prefillPodHostPort)
}
