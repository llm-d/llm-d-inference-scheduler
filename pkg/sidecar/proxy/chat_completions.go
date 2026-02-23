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
	"net/http"
	"strings"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const requestStartTimeKey contextKey = "request_start_time"

var (
	// ChatCompletionsPath is the OpenAI chat completions path
	ChatCompletionsPath = "/v1/chat/completions"

	// CompletionsPath is the legacy completions path
	CompletionsPath = "/v1/completions"
)

func (s *Server) chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	requestStart := time.Now()
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(r.Context(), "llm_d.pd_proxy.request",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	// Update request context with span and start time
	ctx = context.WithValue(ctx, requestStartTimeKey, requestStart)
	r = r.WithContext(ctx)

	span.SetAttributes(
		attribute.String("llm_d.pd_proxy.connector", s.config.Connector),
		attribute.String("llm_d.pd_proxy.request_path", r.URL.Path),
	)

	var prefillHostPorts []string
	prefillHostPorts = r.Header.Values(common.PrefillPodHeader)

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
			// Sample a host value from the list
			prefillHostPort = strings.TrimSpace(prefillHostPorts[s.prefillSamplerFn(numHosts)])
		} else if numHosts > 0 {
			// Select only the first header value, consistent with previous behavior
			prefillHostPort = strings.TrimSpace(prefillHostPorts[0])
		}
	}

	if len(prefillHostPort) == 0 {
		s.logger.V(4).Info("skip disaggregated prefill")
		span.SetAttributes(
			attribute.Bool("llm_d.pd_proxy.disaggregation_used", false),
			attribute.String("llm_d.pd_proxy.reason", "no_prefill_header"),
		)

		if !s.forwardDataParallel || !s.dataParallelHandler(w, r) {
			s.decoderProxy.ServeHTTP(w, r)
		}
		return
	}

	span.SetAttributes(
		attribute.Bool("llm_d.pd_proxy.disaggregation_used", true),
		attribute.String("llm_d.pd_proxy.prefill_target", prefillHostPort),
		attribute.Int("llm_d.pd_proxy.prefill_candidates", numHosts),
	)

	// SSRF Protection: Check if the prefill target is allowed
	if !s.allowlistValidator.IsAllowed(prefillHostPort) {
		s.logger.Error(nil, "SSRF protection: prefill target not in allowlist",
			"target", prefillHostPort,
			"clientIP", r.RemoteAddr,
			"userAgent", r.Header.Get("User-Agent"),
			"requestPath", r.URL.Path)
		span.SetAttributes(
			attribute.String("llm_d.pd_proxy.error", "ssrf_protection_denied"),
			attribute.String("llm_d.pd_proxy.denied_target", prefillHostPort),
		)
		span.SetStatus(codes.Error, "SSRF protection: prefill target not in allowlist")
		http.Error(w, "Forbidden: prefill target not allowed by SSRF protection", http.StatusForbidden)
		return
	}

	s.logger.V(4).Info("SSRF protection: prefill target allowed", "target", prefillHostPort)
	s.runConnectorProtocol(w, r, prefillHostPort)
}
