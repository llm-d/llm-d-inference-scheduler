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

	"github.com/google/uuid"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func (s *Server) runNIXLProtocolV2(w http.ResponseWriter, r *http.Request, prefillPodHostPort string) {
	s.logger.V(4).Info("running NIXL protocol V2", "url", prefillPodHostPort)

	// Read request body
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

	streamValue, streamOk := completionRequest[requestFieldStream]
	streamOptionsValue, streamOptionsOk := completionRequest[requestFieldStreamOptions]
	maxTokensValue, maxTokensOk := completionRequest[requestFieldMaxTokens]
	maxCompletionTokensValue, maxCompletionTokensOk := completionRequest[requestFieldMaxCompletionTokens]

	// Determine if client wants streaming
	streamBool, streamBoolOk := streamValue.(bool)
	clientWantsStreaming := streamOk && streamBoolOk && streamBool

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
			r.Body = io.NopCloser(strings.NewReader(string(original)))
			s.decoderProxy.ServeHTTP(w, r)
		} else {
			for key, values := range pw.Header() {
				for _, v := range values {
					w.Header().Add(key, v)
				}
			}
			w.WriteHeader(pw.statusCode)
			_, err := w.Write([]byte(pw.buffer.String()))
			if err != nil {
				s.logger.Error(err, "failed to send error response to client")
			}
		}
		return
	}
	prefillSpan.End()

	// Process response - extract p/d fields
	var prefillerResponse map[string]any
	if err := json.Unmarshal([]byte(pw.buffer.String()), &prefillerResponse); err != nil {
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

	s.logger.V(5).Info("received prefiller response", requestFieldKVTransferParams, pKVTransferParams)

	// 4. Send first token to client if streaming
	// Channel to signal when first token has been sent and receive the timestamp
	// Also carries any error that occurred and whether headers were sent
	type firstTokenResult struct {
		sentTime    time.Time
		err         error
		headersSent bool
	}
	firstTokenSent := make(chan firstTokenResult, 1)

	if clientWantsStreaming {
		// Remove kv_transfer_params before sending to user (do this before goroutine to avoid race)
		delete(prefillerResponse, requestFieldKVTransferParams)

		// Send first token in goroutine to allow parallel execution with decode prep
		go func() {
			streamChunk, err := json.Marshal(prefillerResponse)
			if err != nil {
				s.logger.Error(err, "failed to marshal streaming chunk")
				firstTokenSent <- firstTokenResult{err: err, headersSent: false}
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)

			_, err = w.Write([]byte("data: "))
			if err != nil {
				s.logger.Error(err, "failed to write SSE prefix")
				firstTokenSent <- firstTokenResult{err: err, headersSent: true}
				return
			}
			_, err = w.Write(streamChunk)
			if err != nil {
				s.logger.Error(err, "failed to write prefill chunk to client")
				firstTokenSent <- firstTokenResult{err: err, headersSent: true}
				return
			}
			_, err = w.Write([]byte("\n\n"))
			if err != nil {
				s.logger.Error(err, "failed to write SSE suffix")
				firstTokenSent <- firstTokenResult{err: err, headersSent: true}
				return
			}

			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}

			sentTime := time.Now()
			s.logger.V(4).Info("sent first token to client immediately after prefill")
			firstTokenSent <- firstTokenResult{sentTime: sentTime, headersSent: true}
		}()
	} else {
		// For non-streaming, signal immediately that we don't need to wait
		firstTokenSent <- firstTokenResult{}
	}

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

	// 5. Prepare decode request
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
	delete(completionRequest, requestFieldMaxTokens)
	if maxTokensOk {
		if clientWantsStreaming {
			// Decrement by 1 since we already sent 1 token to streaming client
			if val, ok := maxTokensValue.(float64); ok && val > 0 {
				completionRequest[requestFieldMaxTokens] = val - 1
			} else {
				completionRequest[requestFieldMaxTokens] = maxTokensValue
			}
		} else {
			completionRequest[requestFieldMaxTokens] = maxTokensValue
		}
	}
	delete(completionRequest, requestFieldMaxCompletionTokens)
	if maxCompletionTokensOk {
		if clientWantsStreaming {
			// Decrement by 1 since we already sent 1 token to streaming client
			if val, ok := maxCompletionTokensValue.(float64); ok && val > 0 {
				completionRequest[requestFieldMaxCompletionTokens] = val - 1
			} else {
				completionRequest[requestFieldMaxCompletionTokens] = maxCompletionTokensValue
			}
		} else {
			completionRequest[requestFieldMaxCompletionTokens] = maxCompletionTokensValue
		}
	}
	completionRequest[requestFieldKVTransferParams] = pKVTransferParams

	dbody, err := json.Marshal(completionRequest)
	if err != nil {
		// Wait for goroutine to complete before returning
		result := <-firstTokenSent
		if result.headersSent {
			// Headers already sent, can't send error response
			s.logger.Error(err, "failed to marshal decode request, but response already started")
			return
		}
		// Safe to send error response - headers not sent yet
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	dreq.Body = io.NopCloser(strings.NewReader(string(dbody)))
	dreq.ContentLength = int64(len(dbody))

	// 6. Forward to local decoder.
	// Wait for first token to be sent before forwarding to decoder
	result := <-firstTokenSent
	if result.err != nil {
		// First token send failed
		if result.headersSent {
			// Partial response already sent to client, can't recover
			s.logger.Error(result.err, "failed to send first token after headers sent, aborting request")
		} else {
			// Headers not sent yet, try to send error response
			s.logger.Error(result.err, "failed to send first token before headers sent")
			if err := errorJSONInvalid(result.err, w); err != nil {
				s.logger.Error(err, "failed to send error response to client")
			}
		}
		return
	}
	firstTokenSentTime := result.sentTime

	// Use wrapper to prevent duplicate WriteHeader for streaming
	decodeResponseWriter := w
	if clientWantsStreaming {
		decodeResponseWriter = &headersSentWriter{ResponseWriter: w}
	}

	s.logger.V(5).Info("sending request to decoder", "body", string(dbody))
	dataParallelUsed := s.forwardDataParallel && s.dataParallelHandler(decodeResponseWriter, dreq)
	decodeSpan.SetAttributes(attribute.Bool("llm_d.pd_proxy.decode.data_parallel", dataParallelUsed))

	if !dataParallelUsed {
		s.logger.V(4).Info("sending request to decoder", "to", s.decoderURL.Host)
		decodeSpan.SetAttributes(attribute.String("llm_d.pd_proxy.decode.target", s.decoderURL.Host))
		s.decoderProxy.ServeHTTP(decodeResponseWriter, dreq)
	}

	decodeDuration := time.Since(decodeStart)
	decodeSpan.SetAttributes(attribute.Float64("llm_d.pd_proxy.decode.duration_ms", float64(decodeDuration.Milliseconds())))

	// Calculate end-to-end P/D timing metrics.
	// True TTFT captures time from gateway request start to when the first token reaches the user.
	// For streaming clients: measures until first token is sent (after prefill).
	// For non-streaming clients: measures until decode starts (includes KV transfer).
	if currentSpan := trace.SpanFromContext(ctx); currentSpan.SpanContext().IsValid() {
		var totalDuration time.Duration
		var trueTTFT time.Duration
		if requestStartValue := ctx.Value(requestStartTimeKey); requestStartValue != nil {
			if requestStart, ok := requestStartValue.(time.Time); ok {
				totalDuration = time.Since(requestStart)
				// Use actual first token sent time for streaming, decode start for non-streaming
				if !firstTokenSentTime.IsZero() {
					trueTTFT = firstTokenSentTime.Sub(requestStart)
				} else {
					trueTTFT = decodeStart.Sub(requestStart)
				}
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

// headersSentWriter wraps a ResponseWriter to prevent duplicate WriteHeader calls
// Used when headers have already been sent for streaming first token
type headersSentWriter struct {
	http.ResponseWriter
}

func (w *headersSentWriter) WriteHeader(statusCode int) {
	// No-op: headers already sent
}

func (w *headersSentWriter) Write(b []byte) (int, error) {
	// Write directly without calling WriteHeader
	return w.ResponseWriter.Write(b)
}
