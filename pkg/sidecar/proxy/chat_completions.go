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
	"strings"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var disaggregatedPrefillTracer = otel.Tracer("github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy")

var (
	// ChatCompletionsPath is the OpenAI chat completions path
	ChatCompletionsPath = "/v1/chat/completions"

	// CompletionsPath is the legacy completions path
	CompletionsPath = "/v1/completions"

	// ResponsesPath is the OpenAI Responses API path
	ResponsesPath = "/v1/responses"

	// ConversationsPath is the OpenAI Conversations API path
	ConversationsPath = "/v1/conversations"
)

func openAIAPIAttr(apiType APIType) attribute.KeyValue {
	switch apiType {
	case APITypeResponses:
		return attribute.String("openai.api", "responses")
	case APITypeConversations:
		return attribute.String("openai.api", "conversations")
	default:
		return attribute.String("openai.api", "chat_completions")
	}
}

// disaggregatedPrefillHandler routes OpenAI-style requests through P/D prefill when the
// prefill pod header is set; otherwise forwards to the decoder (or data-parallel path).
func (s *Server) disaggregatedPrefillHandler(apiType APIType, skipDisaggregatedLog string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, span := disaggregatedPrefillTracer.Start(r.Context(), "proxy.DisaggregatedPrefill")
		defer span.End()
		span.SetAttributes(openAIAPIAttr(apiType))
		r = r.WithContext(ctx)

		prefillHostPorts := r.Header.Values(common.PrefillPodHeader)

		// https://datatracker.ietf.org/doc/html/rfc7230#section-3.2.2 specifies proxies
		// may combine multiple header values with a comma. Accept either one host per
		// header line OR one line with multiple header values.
		if len(prefillHostPorts) == 1 {
			prefillHostPorts = strings.Split(prefillHostPorts[0], ",")
		}

		numHosts := len(prefillHostPorts)
		var prefillHostPort string
		if numHosts > 0 {
			if s.config.EnablePrefillerSampling {
				prefillHostPort = strings.TrimSpace(prefillHostPorts[s.prefillSamplerFn(numHosts)])
			} else {
				prefillHostPort = strings.TrimSpace(prefillHostPorts[0])
			}
		}

		if len(prefillHostPort) == 0 {
			s.logger.V(4).Info(skipDisaggregatedLog)

			if !s.forwardDataParallel || !s.dataParallelHandler(w, r) {
				s.decoderProxy.ServeHTTP(w, r)
			}
			return
		}

		if !s.allowlistValidator.IsAllowed(prefillHostPort) {
			s.logger.Error(nil, "SSRF protection: prefill target not in allowlist",
				"target", prefillHostPort,
				"clientIP", r.RemoteAddr,
				"userAgent", r.Header.Get("User-Agent"),
				"requestPath", r.URL.Path)
			http.Error(w, "Forbidden: prefill target not allowed by SSRF protection", http.StatusForbidden)
			return
		}

		s.logger.V(4).Info("SSRF protection: prefill target allowed", "target", prefillHostPort)
		fields := tokenLimitFieldsForAPIType(apiType)
		s.runConnectorProtocol(w, r, prefillHostPort, fields)
	}
}
