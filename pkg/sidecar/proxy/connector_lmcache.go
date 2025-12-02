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
	ctx, prefillSpan := tracer.Start(ctx, "pd_sidecar.prefill",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	prefillSpan.SetAttributes(
		attribute.String("pd_sidecar.prefill_target", prefillPodHostPort),
		attribute.String("pd_sidecar.connector", "lmcache"),
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
		attribute.Int("pd_sidecar.prefill.status_code", pw.statusCode),
		attribute.Float64("pd_sidecar.prefill.duration_ms", float64(prefillDuration.Milliseconds())),
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
	ctx, decodeSpan := tracer.Start(ctx, "pd_sidecar.decode",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer decodeSpan.End()

	decodeSpan.SetAttributes(attribute.String("pd_sidecar.connector", "lmcache"))
	decodeStart := time.Now()

	// Forward original request to local decoder

	r = r.WithContext(ctx)
	r.Body = io.NopCloser(strings.NewReader(string(original)))
	dataParallelUsed := s.forwardDataParallel && s.dataParallelHandler(w, r)
	decodeSpan.SetAttributes(attribute.Bool("pd_sidecar.decode.data_parallel", dataParallelUsed))

	if !dataParallelUsed {
		s.logger.V(4).Info("sending request to decoder", "to", s.decoderURL.Host)
		decodeSpan.SetAttributes(attribute.String("pd_sidecar.decode.target", s.decoderURL.Host))
		s.decoderProxy.ServeHTTP(w, r)
	}

	decodeDuration := time.Since(decodeStart)
	decodeSpan.SetAttributes(attribute.Float64("pd_sidecar.decode.duration_ms", float64(decodeDuration.Milliseconds())))
	decodeSpan.SetStatus(codes.Ok, "")
}
