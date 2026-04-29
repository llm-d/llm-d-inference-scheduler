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
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

func (s *Server) runNIXLProtocolV2(w http.ResponseWriter, r *http.Request, prefillPodHostPort string, apiType APIType) {
	tokenLimitFields := tokenLimitFieldsForAPIType(apiType)
	s.logger.V(4).Info("running NIXL protocol V2", "url", prefillPodHostPort, "tokenLimitFields", tokenLimitFields)

	// Read request body
	defer r.Body.Close() //nolint:errcheck
	original, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest) // TODO: check FastAPI error code when failing to read body
		w.Write([]byte(err.Error()))         //nolint:errcheck
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

	// Generate unique request UUID
	uuid, err := uuid.NewUUID()
	if err != nil {
		if err := errorBadGateway(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	uuidStr := uuid.String()

	// Prefill Stage
	tracer := telemetry.Tracer()
	ctx := r.Context()

	ctx, prefillSpan := tracer.Start(ctx, "llm_d.pd_proxy.prefill",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	prefillSpan.SetAttributes(
		attribute.String("llm_d.pd_proxy.request_id", uuidStr),
		attribute.String("llm_d.pd_proxy.prefill_target", prefillPodHostPort),
		attribute.String("llm_d.pd_proxy.connector", "nixlv2"),
	)
	prefillStart := time.Now()

	// 1. Prepare prefill request
	preq := r.Clone(ctx)

	preq.Header.Add(requestHeaderRequestID, uuidStr)

	// Save original values based on API type
	streamValue, streamOk := completionRequest[requestFieldStream]
	streamOptionsValue, streamOptionsOk := completionRequest[requestFieldStreamOptions]

	// Save and override token limit fields for prefill
	type savedField struct {
		field   string
		val     any
		present bool
	}
	var savedTokenValues [2]savedField
	for i, field := range tokenLimitFields {
		if v, ok := completionRequest[field]; ok {
			savedTokenValues[i] = savedField{field: field, val: v, present: true}
		} else {
			savedTokenValues[i] = savedField{field: field}
		}
	}

	completionRequest[requestFieldKVTransferParams] = map[string]any{
		requestFieldDoRemoteDecode:  true,
		requestFieldDoRemotePrefill: false,
		requestFieldRemoteEngineID:  nil,
		requestFieldRemoteBlockIDs:  nil,
		requestFieldRemoteHost:      nil,
		requestFieldRemotePort:      nil,
	}

	completionRequest[requestFieldStream] = false
	delete(completionRequest, requestFieldStreamOptions)

	for _, field := range tokenLimitFields {
		completionRequest[field] = 1
	}

	pbody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	preq.Body = io.NopCloser(bytes.NewReader(pbody))
	preq.ContentLength = int64(len(pbody))

	prefillHandler, err := s.prefillerProxyHandler(prefillPodHostPort)
	if err != nil {
		if err := errorBadGateway(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// 2. Forward request to prefiller
	s.logger.V(4).Info("sending prefill request", "to", prefillPodHostPort)
	s.logger.V(5).Info("Prefill request", "body", string(pbody))
	pw := &bufferedResponseWriter{}
	prefillHandler.ServeHTTP(pw, preq)

	prefillDuration := time.Since(prefillStart)
	prefillSpan.SetAttributes(
		attribute.Int("llm_d.pd_proxy.prefill.status_code", pw.statusCode),
		attribute.Float64("llm_d.pd_proxy.prefill.duration_ms", float64(prefillDuration.Milliseconds())),
	)

	if isHTTPError(pw.statusCode) {
		s.logger.Error(err, "request failed", "code", pw.statusCode, "body", pw.buffer.String())
		prefillSpan.SetStatus(codes.Error, "prefill request failed")
		prefillSpan.End()

		if shouldFallbackToDecode(pw) {
			s.logger.Info("fallback to decode", "request_id", uuidStr)
			r.Body = io.NopCloser(bytes.NewReader(original))
			s.decoderProxy.ServeHTTP(w, r)
		} else {
			for key, values := range pw.Header() {
				for _, v := range values {
					w.Header().Add(key, v)
				}
			}
			w.WriteHeader(pw.statusCode)
			_, err := w.Write(pw.bodyBytes())
			if err != nil {
				s.logger.Error(err, "failed to send error response to client")
			}
		}
		return
	}
	prefillSpan.End()

	// Process response - extract p/d fields
	var prefillerResponse map[string]any
	if err := json.Unmarshal(pw.bodyBytes(), &prefillerResponse); err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// 3. Verify response

	pKVTransferParams, ok := prefillerResponse[requestFieldKVTransferParams]
	if !ok {
		s.logger.Info("warning: missing 'kv_transfer_params' field in prefiller response")
	}
	pCachedTokens, hasPCachedTokens := extractCachedTokens(prefillerResponse)

	s.logger.V(5).Info("received prefiller response", requestFieldKVTransferParams, pKVTransferParams)

	// Decode Stage

	ctx, decodeSpan := tracer.Start(ctx, "llm_d.pd_proxy.decode",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer decodeSpan.End()

	decodeSpan.SetAttributes(
		attribute.String("llm_d.pd_proxy.request_id", uuidStr),
		attribute.String("llm_d.pd_proxy.connector", "nixlv2"),
	)
	decodeStart := time.Now()

	// 1. Prepare decode request
	dreq := r.Clone(ctx)

	dreq.Header.Add(requestHeaderRequestID, uuidStr)

	delete(completionRequest, requestFieldStream)
	streamingEnabled := false
	if streamOk {
		completionRequest[requestFieldStream] = streamValue
		if streamBool, ok := streamValue.(bool); ok {
			streamingEnabled = streamBool
		}
	}
	decodeSpan.SetAttributes(attribute.Bool("llm_d.pd_proxy.decode.streaming", streamingEnabled))
	if streamOptionsOk {
		completionRequest[requestFieldStreamOptions] = streamOptionsValue
	}

	for i := range savedTokenValues[:len(tokenLimitFields)] {
		sv := &savedTokenValues[i]
		delete(completionRequest, sv.field)
		if sv.present {
			completionRequest[sv.field] = sv.val
		}
	}

	completionRequest[requestFieldKVTransferParams] = pKVTransferParams

	dbody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	dreq.Body = io.NopCloser(bytes.NewReader(dbody))
	dreq.ContentLength = int64(len(dbody))

	// 2. Forward to local decoder.

	s.logger.V(5).Info("sending request to decoder", "body", string(dbody))
	decodeWriter := newCachedTokensResponseWriter(w, pCachedTokens, hasPCachedTokens)
	dataParallelUsed := s.forwardDataParallel && s.dataParallelHandler(decodeWriter, dreq)
	decodeSpan.SetAttributes(attribute.Bool("llm_d.pd_proxy.decode.data_parallel", dataParallelUsed))

	if !dataParallelUsed {
		s.logger.V(4).Info("sending request to decoder", "to", s.config.DecoderURL.Host)
		decodeSpan.SetAttributes(attribute.String("llm_d.pd_proxy.decode.target", s.config.DecoderURL.Host))
		s.decoderProxy.ServeHTTP(decodeWriter, dreq)
	}

	decodeDuration := time.Since(decodeStart)
	decodeSpan.SetAttributes(attribute.Float64("llm_d.pd_proxy.decode.duration_ms", float64(decodeDuration.Milliseconds())))

	// Calculate end-to-end P/D timing metrics.
	// True TTFT captures time from gateway request start to decode start, including
	// gateway routing, scheduling, prefill, and coordination overhead that
	// per-instance vLLM metrics miss.
	if currentSpan := trace.SpanFromContext(ctx); currentSpan.SpanContext().IsValid() {
		var totalDuration time.Duration
		var trueTTFT time.Duration
		if requestStartValue := ctx.Value(requestStartTimeKey); requestStartValue != nil {
			if requestStart, ok := requestStartValue.(time.Time); ok {
				totalDuration = time.Since(requestStart)
				trueTTFT = decodeStart.Sub(requestStart)
			}
		}

		coordinatorOverhead := decodeStart.Sub(prefillStart.Add(prefillDuration))

		currentSpan.SetAttributes(
			attribute.Float64("llm_d.pd_proxy.total_duration_ms", float64(totalDuration.Milliseconds())),
			attribute.Float64("llm_d.pd_proxy.true_ttft_ms", float64(trueTTFT.Milliseconds())),
			attribute.Float64("llm_d.pd_proxy.prefill_duration_ms", float64(prefillDuration.Milliseconds())),
			attribute.Float64("llm_d.pd_proxy.decode_duration_ms", float64(decodeDuration.Milliseconds())),
			attribute.Float64("llm_d.pd_proxy.coordinator_overhead_ms", float64(coordinatorOverhead.Milliseconds())),
		)
	}
}

type cachedTokensResponseWriter struct {
	http.ResponseWriter
	cachedTokens int
	enabled      bool
	wroteHeader  bool
}

func newCachedTokensResponseWriter(w http.ResponseWriter, cachedTokens int, enabled bool) http.ResponseWriter {
	if !enabled {
		return w
	}
	return &cachedTokensResponseWriter{
		ResponseWriter: w,
		cachedTokens:   cachedTokens,
		enabled:        enabled,
	}
}

func (w *cachedTokensResponseWriter) WriteHeader(statusCode int) {
	if w.enabled {
		w.Header().Del("Content-Length")
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *cachedTokensResponseWriter) Write(body []byte) (int, error) {
	if !w.enabled {
		return w.ResponseWriter.Write(body)
	}
	updated := replaceCachedTokens(body, w.cachedTokens)
	if !w.wroteHeader {
		w.Header().Del("Content-Length")
	}
	_, err := w.ResponseWriter.Write(updated)
	if err != nil {
		return 0, err
	}
	return len(body), nil
}

func (w *cachedTokensResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func extractCachedTokens(response map[string]any) (int, bool) {
	usage, ok := response["usage"].(map[string]any)
	if !ok {
		return 0, false
	}
	return cachedTokensFromUsage(usage)
}

func cachedTokensFromUsage(usage map[string]any) (int, bool) {
	for _, field := range []string{"prompt_tokens_details", "prompt_token_details"} {
		details, ok := usage[field].(map[string]any)
		if !ok {
			continue
		}
		return intValue(details["cached_tokens"])
	}
	return 0, false
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

func replaceCachedTokens(body []byte, cachedTokens int) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	if updated, ok := replaceCachedTokensJSON(body, cachedTokens); ok {
		return updated
	}
	if updated, ok := replaceCachedTokensSSE(body, cachedTokens); ok {
		return updated
	}
	return body
}

func replaceCachedTokensJSON(body []byte, cachedTokens int) ([]byte, bool) {
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, false
	}
	if !setCachedTokens(response, cachedTokens) {
		return body, true
	}
	updated, err := json.Marshal(response)
	if err != nil {
		return body, true
	}
	return updated, true
}

func replaceCachedTokensSSE(body []byte, cachedTokens int) ([]byte, bool) {
	lines := bytes.SplitAfter(body, []byte("\n"))
	updated := make([]byte, 0, len(body))
	changed := false
	processed := false

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		trimmedLine := bytes.TrimRight(line, "\r\n")
		lineEnding := line[len(trimmedLine):]
		data, ok := bytes.CutPrefix(trimmedLine, []byte("data: "))
		if !ok {
			updated = append(updated, line...)
			continue
		}
		processed = true
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			updated = append(updated, line...)
			continue
		}
		replacedData, isJSON := replaceCachedTokensJSON(data, cachedTokens)
		if !isJSON {
			updated = append(updated, line...)
			continue
		}
		if !bytes.Equal(replacedData, data) {
			changed = true
		}
		updated = append(updated, []byte("data: ")...)
		updated = append(updated, replacedData...)
		updated = append(updated, lineEnding...)
	}

	if !processed {
		return nil, false
	}
	if !changed {
		return body, true
	}
	return updated, true
}

func setCachedTokens(response map[string]any, cachedTokens int) bool {
	usage, ok := response["usage"].(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for _, field := range []string{"prompt_tokens_details", "prompt_token_details"} {
		details, ok := usage[field].(map[string]any)
		if !ok {
			continue
		}
		if _, ok := details["cached_tokens"]; !ok {
			continue
		}
		if current, ok := intValue(details["cached_tokens"]); !ok || current != cachedTokens {
			details["cached_tokens"] = cachedTokens
			changed = true
		}
	}
	return changed
}
