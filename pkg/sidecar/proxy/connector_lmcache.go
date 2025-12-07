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
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func (s *Server) runLMCacheProtocol(w http.ResponseWriter, r *http.Request, prefillPodHostPort string) {
	s.logger.Info("running LMCache protocol")

	tracer := telemetry.Tracer()
	ctx := r.Context()

	// Read and parse request body
	defer r.Body.Close() //nolint:all
	original, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest) // TODO: check FastAPI error code when failing to read body
		w.Write([]byte(err.Error()))         //nolint:all
		return
	}

	// Parse completion request
	var completionRequest map[string]any
	if err := json.Unmarshal(original, &completionRequest); err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// Prefill Stage
	ctx, prefillSpan := tracer.Start(ctx, "llm_d.pd_proxy.prefill",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	prefillSpan.SetAttributes(
		attribute.String("llm_d.pd_proxy.prefill_target", prefillPodHostPort),
		attribute.String("llm_d.pd_proxy.connector", "lmcache"),
	)
	prefillStart := time.Now()

	// Create prefiller request. Set max_tokens to 1.

	preq := r.Clone(ctx)

	completionRequest[requestFieldMaxTokens] = 1
	completionRequest[requestFieldMaxCompletionTokens] = 1

	pbody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	preq.Body = io.NopCloser(strings.NewReader(string(pbody)))
	preq.ContentLength = int64(len(pbody))

	// Forward request to prefiller

	prefillHandler, err := s.prefillerProxyHandler(prefillPodHostPort)
	if err != nil {
		if err := errorBadGateway(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	s.logger.V(4).Info("sending prefill request", "to", prefillPodHostPort)
	pw := &bufferedResponseWriter{}
	prefillHandler.ServeHTTP(pw, preq)

	prefillDuration := time.Since(prefillStart)
	prefillSpan.SetAttributes(
		attribute.Int("llm_d.pd_proxy.prefill.status_code", pw.statusCode),
		attribute.Float64("llm_d.pd_proxy.prefill.duration_ms", float64(prefillDuration.Milliseconds())),
	)

	if pw.statusCode < 200 || pw.statusCode >= 300 {
		s.logger.Error(err, "request failed", "code", pw.statusCode)
		prefillSpan.SetStatus(codes.Error, "prefill request failed")
		prefillSpan.End()
		w.WriteHeader(pw.statusCode)
		return
	}
	prefillSpan.SetStatus(codes.Ok, "")
	prefillSpan.End()

	// Decode Stage
	ctx, decodeSpan := tracer.Start(ctx, "llm_d.pd_proxy.decode",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer decodeSpan.End()

	decodeSpan.SetAttributes(attribute.String("llm_d.pd_proxy.connector", "lmcache"))
	decodeStart := time.Now()

	// Forward original request to local decoder

	r = r.WithContext(ctx)
	r.Body = io.NopCloser(strings.NewReader(string(original)))
	dataParallelUsed := s.forwardDataParallel && s.dataParallelHandler(w, r)
	decodeSpan.SetAttributes(attribute.Bool("llm_d.pd_proxy.decode.data_parallel", dataParallelUsed))

	if !dataParallelUsed {
		s.logger.V(4).Info("sending request to decoder", "to", s.decoderURL.Host)
		decodeSpan.SetAttributes(attribute.String("llm_d.pd_proxy.decode.target", s.decoderURL.Host))
		s.decoderProxy.ServeHTTP(w, r)
	}

	decodeDuration := time.Since(decodeStart)
	decodeSpan.SetAttributes(attribute.Float64("llm_d.pd_proxy.decode.duration_ms", float64(decodeDuration.Milliseconds())))
	decodeSpan.SetStatus(codes.Ok, "")

	// Calculate end-to-end P/D metrics and add to decode span
	// These metrics represent the "true" TTFT and latency from the coordinator's perspective
	// Note: After tracer.Start() above, ctx contains the decode span, so SpanFromContext returns it
	if currentSpan := trace.SpanFromContext(ctx); currentSpan.SpanContext().IsValid() {
		// Get request start time from context
		var totalDuration time.Duration
		var trueTTFT time.Duration
		if requestStartValue := ctx.Value("request_start_time"); requestStartValue != nil {
			if requestStart, ok := requestStartValue.(time.Time); ok {
				totalDuration = time.Since(requestStart)

				// The "true TTFT" in P/D mode is the time until the decoder can start generating
				// This includes: gateway routing + scheduling + prefill time + KV transfer coordination overhead
				// The decode vLLM will report a low TTFT (since KV is already transferred),
				// but this captures the real end-to-end TTFT from the client's perspective
				//
				// True TTFT = time from gateway request start to decode start
				// This includes all coordinator overhead that vLLM-level metrics miss
				trueTTFT = decodeStart.Sub(requestStart)
			}
		}

		// KV transfer overhead: time between prefill completion and decode start
		kvTransferOverhead := decodeStart.Sub(prefillStart.Add(prefillDuration))

		currentSpan.SetAttributes(
			// End-to-end P/D timing metrics
			attribute.Float64("llm_d.pd_proxy.total_duration_ms", float64(totalDuration.Milliseconds())),
			attribute.Float64("llm_d.pd_proxy.true_ttft_ms", float64(trueTTFT.Milliseconds())),

			// Component breakdown
			attribute.Float64("llm_d.pd_proxy.prefill_duration_ms", float64(prefillDuration.Milliseconds())),
			attribute.Float64("llm_d.pd_proxy.decode_duration_ms", float64(decodeDuration.Milliseconds())),

			// Coordination overhead
			attribute.Float64("llm_d.pd_proxy.kv_transfer_overhead_ms", float64(kvTransferOverhead.Milliseconds())),
		)
	}
}
