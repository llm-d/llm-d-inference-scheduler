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
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
)

func (s *Server) runLMCacheProtocol(w http.ResponseWriter, r *http.Request, prefillPodHostPort string) {
	s.logger.Info("running LMCache protocol")

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
			s.logger.Error(err, "failed to send Invalid JSON error response to Client")
		}
		return
	}

	// If "cache_hit_threshold" is present in the request, we try to decode first. The decode node must meet the cache hit threshold in order to execute.
	// If the decode node is below the threshold, it won't process the request and return a "cache_threshold" finish reason. In that case,
	// we fall back to P/D disaggregation: perform prefill and then decode.
	// For more information refer to the RFC https://github.com/vllm-project/vllm/issues/24256
	if cacheHitThreshold, hasCacheHitThreshold := completionRequest[requestFieldCacheHitThreshold]; hasCacheHitThreshold {
		s.logger.V(4).Info("cache_hit_threshold field found in the request, trying to decode first", requestFieldCacheHitThreshold, cacheHitThreshold)
		decodeReq := r.Clone(ctx)
		decodeReq.Body = io.NopCloser(bytes.NewReader(original))
		decodeReq.ContentLength = int64(len(original))
		isStreaming, _ := completionRequest[requestFieldStream].(bool)
		needsPrefill, err := s.tryDecode(w, decodeReq, isStreaming)
		if err != nil {
			return
		}
		if !needsPrefill {
			s.logger.V(4).Info("decode succeeded without prefill")
			return
		}
		s.logger.V(4).Info("decode failed due to failing to meet the cache hit threshold", requestFieldCacheHitThreshold, cacheHitThreshold)
	}

	// we clone the completion request to avoid modifying the original request
	prefillRequest := maps.Clone(completionRequest)
	if err := s.prefill(w, r, prefillPodHostPort, prefillRequest); err != nil {
		s.logger.Error(err, "prefill failed")
		return
	}

	s.logger.V(4).Info("forwarding to decoder after prefill")
	completionRequest[requestFieldCacheHitThreshold] = 0
	decodeRequestBody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send Invalid JSON error response to Client")
		}
		return
	}

	decodeReq := r.Clone(ctx)
	decodeReq.Body = io.NopCloser(bytes.NewReader(decodeRequestBody))
	decodeReq.ContentLength = int64(len(decodeRequestBody))
	s.decoderProxy.ServeHTTP(w, decodeReq)
}

// tryDecode attempts to decode and returns whether prefill is needed.
func (s *Server) tryDecode(w http.ResponseWriter, r *http.Request, isStreaming bool) (bool, error) {
	if isStreaming {
		if flusher, ok := w.(flushableResponseWriter); ok {
			bw := newResponseWriterWithBuffer(flusher)
			return s.tryDecodeStreaming(bw, r)
		}
	}
	return s.tryDecodeBuffered(w, r)
}

// tryDecodeBuffered handles non-streaming decode attempts.
// It buffers the entire response before inspecting it.
func (s *Server) tryDecodeBuffered(w http.ResponseWriter, r *http.Request) (bool, error) {
	dw := &bufferedResponseWriter{}
	s.decoderProxy.ServeHTTP(dw, r)

	// Check for non-success status codes
	if dw.statusCode < 200 || dw.statusCode >= 300 {
		w.WriteHeader(dw.statusCode)
		if dw.buffer.Len() > 0 {
			w.Write([]byte(dw.buffer.String())) //nolint:all
		}
		return false, fmt.Errorf("decode request failed with status code: %d", dw.statusCode)
	}

	// Parse response to check finish_reason
	var response map[string]any
	if err := json.Unmarshal([]byte(dw.buffer.String()), &response); err != nil {
		s.logger.Error(err, "failed to unmarshal decode response", "response", dw.buffer.String())

		if err := errorInternalServerError(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return false, err
	}

	// Check for cache_threshold finish reason
	if s.hasCacheThresholdFinishReason(response) {
		return true, nil
	}

	// Decode succeeded, write response to client
	maps.Copy(w.Header(), dw.headers)
	w.Write([]byte(dw.buffer.String())) //nolint:all

	return false, nil
}

// tryDecodeStreaming handles streaming decode attempts.
// It buffers the initial response to check for cache_threshold, then switches
// to direct streaming mode if decode succeeds.
func (s *Server) tryDecodeStreaming(w *responseWriterWithBuffer, r *http.Request) (bool, error) {
	// Run ServeHTTP in a goroutine so we can inspect the initial choice to determine if we need to prefill.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.decoderProxy.ServeHTTP(w, r)
	}()

	// Wait for either:
	// - firstChunkReady(): first body data is available in buffer
	// - done: request completed (possibly with no body, e.g., error response)
	select {
	case <-w.firstChunkReady():
	case <-done:
		s.logger.V(4).Info("request completed without body data")
	}

	statusCode := w.getStatusCode()
	if statusCode < 200 || statusCode >= 300 {
		if err := w.flushBufferAndGoDirect(); err != nil {
			s.logger.Error(err, "failed to flush buffer to client")
			return false, err
		}
		return false, fmt.Errorf("decode request failed with status code: %d", statusCode)
	}

	// Check buffered SSE content for cache_threshold finish reason.
	if s.checkBufferedResponseForCacheThreshold(w.buffered()) {
		s.logger.V(4).Info("finish reason cache_threshold detected, needs prefill")
		return true, nil
	}

	// No cache_threshold finish reasonfound, flush buffer and switch to direct mode
	// to let the rest of the response stream through.
	s.logger.V(4).Info("first response for request shows success without cache_threshold finishreason")
	if err := w.flushBufferAndGoDirect(); err != nil {
		s.logger.Error(err, "failed to flush buffer to client and switch to direct mode")
		return false, err
	}
	<-done
	return false, nil
}

// hasCacheThresholdFinishReason checks if a parsed response contains cache_threshold finish reason.
func (s *Server) hasCacheThresholdFinishReason(response map[string]any) bool {
	choices, ok := response[responseFieldChoices].([]any)
	if !ok || len(choices) == 0 {
		return false
	}

	choice, ok := choices[0].(map[string]any)
	if !ok {
		return false
	}

	finishReason, ok := choice[responseFieldFinishReason].(string)
	return ok && finishReason == finishReasonCacheThreshold
}

// checkBufferedResponseForCacheThreshold checks the buffered SSE response for cache_threshold finish reason.
// This is only called for streaming responses, so data is always in SSE format.
func (s *Server) checkBufferedResponseForCacheThreshold(data string) bool {
	// Parse SSE format: "data: {...json...}\n\ndata: {...json...}\n\n"
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "data: [DONE]" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		var response map[string]any
		if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
			s.logger.V(4).Info("skipping malformed SSE chunk", "chunk", jsonData)
			continue
		}

		if s.hasCacheThresholdFinishReason(response) {
			return true
		}
	}
	return false
}

// prefill routes a request to a prefill node
func (s *Server) prefill(w http.ResponseWriter, r *http.Request, prefillPodHostPort string, completionRequest map[string]any) error {
	ctx := r.Context()
	preq := r.Clone(ctx)

	// Prepare prefill request
	completionRequest[requestFieldMaxTokens] = 1
	completionRequest[requestFieldMaxCompletionTokens] = 1
	completionRequest[requestFieldCacheHitThreshold] = 0

	pbody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send Invalid JSON error response to Client")
		}
		return err
	}
	preq.Body = io.NopCloser(strings.NewReader(string(pbody)))
	preq.ContentLength = int64(len(pbody))

	prefillHandler, err := s.prefillerProxyHandler(prefillPodHostPort)
	if err != nil {
		if err := errorBadGateway(err, w); err != nil {
			s.logger.Error(err, "failed to send Bad Gateway error response to Client")
		}
		return err
	}

	// send prefill request
	s.logger.V(4).Info("sending prefill request", "to", prefillPodHostPort)
	pw := &bufferedResponseWriter{}
	prefillHandler.ServeHTTP(pw, preq)

	if pw.statusCode < 200 || pw.statusCode >= 300 {
		s.logger.Error(nil, "prefill request failed", "code", pw.statusCode)
		w.WriteHeader(pw.statusCode)
		if pw.buffer.Len() > 0 {
			w.Write([]byte(pw.buffer.String())) //nolint:all
		}
		return fmt.Errorf("prefill request failed with status code: %d", pw.statusCode)
	}

	s.logger.V(4).Info("prefill completed successfully")
	return nil
}
