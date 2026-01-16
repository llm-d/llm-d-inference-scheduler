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

package connectors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	httperrors "github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/http_errors"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/keys"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/manager"
)

// DecodeFirstRunner is a decode first protocol runner
type DecodeFirstRunner struct {
	requestBuilder RequestBuilderFactory
	labelName      string
	proxyManager   *manager.ProxyManager
}

// NewDecodeFirstRunner creates a new DecodeFirstRunner
func NewDecodeFirstRunner(requestBuilder RequestBuilderFactory, labelName string, proxyManager *manager.ProxyManager) ProtocolRunner {
	return &DecodeFirstRunner{
		requestBuilder: requestBuilder,
		labelName:      labelName,
		proxyManager:   proxyManager,
	}
}

// Run runs the decode first protocol
func (dfr *DecodeFirstRunner) Run(w http.ResponseWriter, r *http.Request, prefillPodHostPort string, logger logr.Logger) {
	logger = logger.WithValues("protocol", dfr.labelName, "url", prefillPodHostPort)
	logger.V(4).Info("running protocol")

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
		if err := httperrors.ErrorJSONInvalid(err, w); err != nil {
			logger.Error(err, "failed to send Invalid JSON error response to client")
		}
		return
	}

	// Generate unique request UUID
	uuid, err := uuid.NewUUID()
	if err != nil {
		if err := httperrors.ErrorBadGateway(err, w); err != nil {
			logger.Error(err, "failed to send error response to client")
		}
		return
	}
	uuidStr := uuid.String()

	r.Header.Add(keys.RequestHeaderRequestID, uuidStr)

	// If "cache_hit_threshold" is present in the request, we try to decode first. The decode node must meet the cache hit threshold in order to execute.
	// If the decode node is below the threshold, it won't process the request and return a "cache_threshold" finish reason. In that case,
	// we fall back to P/D disaggregation: perform prefill and then decode.
	// For more information refer to the RFC https://github.com/vllm-project/vllm/issues/24256
	if cacheHitThreshold, hasCacheHitThreshold := completionRequest[keys.RequestFieldCacheHitThreshold]; hasCacheHitThreshold {
		logger.V(4).Info("cache_hit_threshold field found in the request, trying to decode first", keys.RequestFieldCacheHitThreshold, cacheHitThreshold)
		decodeReq := cloneRequestWithBody(r, original)
		needsPrefill, err := dfr.tryDecode(w, decodeReq, completionRequest, logger)
		if err != nil {
			logger.Error(err, "decode failed")
			return
		}
		if !needsPrefill {
			logger.V(4).Info("decode succeeded without prefill")
			return
		}
		logger.V(4).Info("decode failed due to failing to meet the cache hit threshold", keys.RequestFieldCacheHitThreshold, cacheHitThreshold)
	}

	requestBuilder := dfr.requestBuilder.New()
	prefillRequest := requestBuilder.PreparePrefillRequest(completionRequest)

	prefillResponse, err := dfr.prefill(w, r, prefillPodHostPort, prefillRequest, logger)
	if err != nil {
		logger.Error(err, "prefill failed")
		return
	}

	logger.V(4).Info("forwarding to decoder after prefill")
	decodeRequest := requestBuilder.PrepareDecodeRequest(completionRequest, prefillResponse)
	decodeRequestBody, err := json.Marshal(decodeRequest)
	if err != nil {
		if err := httperrors.ErrorJSONInvalid(err, w); err != nil {
			logger.Error(err, "failed to send Invalid JSON error response to client")
		}
		return
	}

	decodeReq := cloneRequestWithBody(r, decodeRequestBody)
	dfr.proxyManager.DecoderProxy.ServeHTTP(w, decodeReq)
}

// tryDecode attempts to decode and returns whether prefill is needed.
func (dfr *DecodeFirstRunner) tryDecode(w http.ResponseWriter, r *http.Request, completionRequest map[string]any, logger logr.Logger) (bool, error) {
	if isStreaming, _ := completionRequest[keys.RequestFieldStream].(bool); isStreaming {
		if flusher, ok := w.(flushableResponseWriter); ok {
			bw := newResponseWriterWithBuffer(flusher)
			return dfr.tryDecodeStreaming(bw, r, logger)
		}

		return false, errors.New("failed to cast response writer to flushable response writer")
	}
	return dfr.tryDecodeBuffered(w, r, logger)
}

// tryDecodeBuffered handles non-streaming decode attempts.
// It buffers the entire response before inspecting it.
func (dfr *DecodeFirstRunner) tryDecodeBuffered(w http.ResponseWriter, r *http.Request, logger logr.Logger) (bool, error) {
	dw := &bufferedResponseWriter{}
	dfr.proxyManager.DecoderProxy.ServeHTTP(dw, r)

	if isHTTPError(dw.statusCode) {

		w.WriteHeader(dw.statusCode)
		if dw.buffer.Len() > 0 {
			w.Write([]byte(dw.buffer.String())) //nolint:all
		}

		err := fmt.Errorf("decode request failed with status code: %d", dw.statusCode)
		logger.Error(err, "decode request failed")

		return false, err
	}

	// Parse response to check finish_reason
	var response map[string]any
	if err := json.Unmarshal([]byte(dw.buffer.String()), &response); err != nil {
		if err := httperrors.ErrorInternalServerError(err, w); err != nil {
			logger.Error(err, "failed to unmarshal decode response", "response", dw.buffer.String())
			return false, err
		}
	}

	// Check for cache_threshold finish reason
	if dfr.hasCacheThresholdFinishReason(response) {
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
func (dfr *DecodeFirstRunner) tryDecodeStreaming(w *responseWriterWithBuffer, r *http.Request, logger logr.Logger) (bool, error) {
	// Run ServeHTTP in a goroutine so we can inspect the initial choice to determine if we need to prefill.
	done := make(chan struct{})
	go func() {
		defer close(done)
		dfr.proxyManager.DecoderProxy.ServeHTTP(w, r)
	}()

	// Wait for either:
	// - firstChunkReady(): first body data is available in buffer
	// - done: request completed (possibly with no body, e.g., error response)
	select {
	case <-w.firstChunkReady():
	case <-done:
		logger.V(4).Info("request completed without body data")
	}

	statusCode := w.getStatusCode()
	if isHTTPError(statusCode) {
		if err := w.flushBufferAndGoDirect(); err != nil {
			logger.Error(err, "failed to flush buffer to client")
			return false, err
		}
		return false, fmt.Errorf("decode request failed with status code: %d", statusCode)
	}

	// Check buffered SSE content for cache_threshold finish reason.
	if dfr.checkBufferedResponseForCacheThreshold(w.buffered(), logger) {
		logger.V(4).Info("finish reason cache_threshold detected, needs prefill")
		return true, nil
	}

	// No cache_threshold finish reason found, flush buffer and switch to direct mode
	// to let the rest of the response stream through.
	logger.V(4).Info("first response for request shows success without cache_threshold finish reason")
	if err := w.flushBufferAndGoDirect(); err != nil {
		logger.Error(err, "failed to flush buffer to client and switch to direct mode")
		return false, err
	}
	<-done
	return false, nil
}

// hasCacheThresholdFinishReason checks if a parsed response contains cache_threshold finish reason.
func (dfr *DecodeFirstRunner) hasCacheThresholdFinishReason(response map[string]any) bool {
	choices, ok := response[keys.ResponseFieldChoices].([]any)
	if !ok || len(choices) == 0 {
		return false
	}

	choice, ok := choices[0].(map[string]any)
	if !ok {
		return false
	}

	finishReason, ok := choice[keys.ResponseFieldFinishReason].(string)
	return ok && finishReason == keys.FinishReasonCacheThreshold
}

// checkBufferedResponseForCacheThreshold checks the buffered SSE response for cache_threshold finish reason.
// This is only called for streaming responses, so data is always in SSE format.
func (dfr *DecodeFirstRunner) checkBufferedResponseForCacheThreshold(data string, logger logr.Logger) bool {
	// Parse SSE format: "data: {...json...}\n\ndata: {...json...}\n\n"
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "data: [DONE]" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		var response map[string]any
		if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
			logger.V(4).Info("skipping malformed SSE chunk", "chunk", jsonData)
			continue
		}

		if dfr.hasCacheThresholdFinishReason(response) {
			return true
		}
	}
	return false
}

// prefill routes a request to a prefill node
func (dfr *DecodeFirstRunner) prefill(w http.ResponseWriter, r *http.Request, prefillPodHostPort string, completionRequest map[string]any, logger logr.Logger) (map[string]any, error) {
	pbody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := httperrors.ErrorJSONInvalid(err, w); err != nil {
			logger.Error(err, "failed to send Invalid JSON error response to client")
		}
		return nil, err
	}
	preq := cloneRequestWithBody(r, pbody)

	prefillHandler, err := dfr.proxyManager.PrefillerProxyHandler(prefillPodHostPort, logger)
	if err != nil {
		if err := httperrors.ErrorBadGateway(err, w); err != nil {
			logger.Error(err, "failed to send Bad Gateway error response to client")
		}
		return nil, err
	}

	// send prefill request
	logger.V(4).Info("sending prefill request", "to", prefillPodHostPort)
	pw := &bufferedResponseWriter{}
	prefillHandler.ServeHTTP(pw, preq)

	if isHTTPError(pw.statusCode) {
		logger.Error(nil, "prefill request failed", "code", pw.statusCode)
		w.WriteHeader(pw.statusCode)
		if pw.buffer.Len() > 0 {
			w.Write([]byte(pw.buffer.String())) //nolint:all
		}
		return nil, fmt.Errorf("prefill request failed with status code: %d", pw.statusCode)
	}

	var prefillResponse map[string]any
	if err := json.Unmarshal([]byte(pw.buffer.String()), &prefillResponse); err != nil {
		return nil, err
	}

	logger.V(4).Info("prefill completed successfully")

	return prefillResponse, nil
}

func cloneRequestWithBody(r *http.Request, body []byte) *http.Request {
	cloned := r.Clone(r.Context())
	cloned.Body = io.NopCloser(bytes.NewReader(body))
	cloned.ContentLength = int64(len(body))
	return cloned
}
