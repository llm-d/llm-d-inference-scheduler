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
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
)

func (s *Server) runLMCacheProtocol(w http.ResponseWriter, r *http.Request, prefillPodHostPort string) {
	s.logger.Info("running LMCache protocol")

	defer r.Body.Close() //nolint:all
	original, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest) // TODO: check FastAPI error code when failing to read body
		w.Write([]byte(err.Error()))         //nolint:all
		return
	}

	var completionRequest map[string]any
	if err := json.Unmarshal(original, &completionRequest); err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	if s.forwardDataParallel && s.dataParallelHandler(w, r) {
		if err := s.prefill(w, r, prefillPodHostPort, completionRequest); err != nil {
			s.logger.Error(err, "prefill failed")
		}
		return
	}

	// If "cache_hit_threshold" is present in the request, we try to decode first. The decode node must mit the cache hit threshold in order to execute.
	// If the decode node is below the threshold, it won't process the request and return a "cache_threshold" finish reason. In that case, we need to prefill.
	// For more infromation refer to the RFC https://github.com/vllm-project/vllm/issues/24256
	if cacheHitThreshold, hasCacheHitThreshold := completionRequest[requestFieldCacheHitThreshold]; hasCacheHitThreshold {
		needsPrefill, err := s.tryDecode(w, r)
		if err != nil {
			return
		}
		if !needsPrefill {
			s.logger.V(4).Info("decode succeeded without prefill")
			return
		}
		s.logger.V(4).Info("decode failed due to insufficient cache hit threshold.", requestFieldCacheHitThreshold, cacheHitThreshold)
	}

	if err := s.prefill(w, r, prefillPodHostPort, completionRequest); err != nil {
		s.logger.Error(err, "prefill failed")
		return
	}

	s.logger.V(4).Info("forwarding to decoder after prefill")
	r.Body = io.NopCloser(strings.NewReader(string(original)))
	s.decoderProxy.ServeHTTP(w, r)
}

// tryDecode attempts to decode and returns whether prefill is needed
func (s *Server) tryDecode(w http.ResponseWriter, r *http.Request) (bool, error) {
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
	if choices, ok := response[responseFieldChoices].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if finishReason, ok := choice[responseFieldFinishReason].(string); ok {
				if finishReason == finishReasonCacheThreshold {
					return true, nil
				}
			}

		}
	}

	// Decode succeeded, write response to client
	maps.Copy(w.Header(), dw.headers)
	w.Write([]byte(dw.buffer.String())) //nolint:all

	return false, nil
}

// prefill routes a request to a preill node
func (s *Server) prefill(w http.ResponseWriter, r *http.Request, prefillPodHostPort string, completionRequest map[string]any) error {
	ctx := r.Context()
	preq := r.Clone(ctx)

	// Prepare prefill request
	completionRequest[requestFieldMaxTokens] = 1
	completionRequest[requestFieldMaxCompletionTokens] = 1

	pbody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return err
	}
	preq.Body = io.NopCloser(strings.NewReader(string(pbody)))
	preq.ContentLength = int64(len(pbody))

	prefillHandler, err := s.prefillerProxyHandler(prefillPodHostPort)
	if err != nil {
		if err := errorBadGateway(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
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
		return err
	}

	s.logger.V(4).Info("prefill completed successfully")
	return nil
}
