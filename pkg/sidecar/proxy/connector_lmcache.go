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

	sidecarmetrics "github.com/llm-d/llm-d-inference-scheduler/pkg/metrics/sidecar"
)

func (s *Server) runLMCacheProtocol(w http.ResponseWriter, r *http.Request, prefillPodHostPort string) {
	s.logger.Info("running LMCache protocol")

	// Timing: capture request start for end-to-end metrics
	requestStart := time.Now()

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

	// Create prefiller request. Set max_tokens to 1.
	// Prefill Stage
	prefillStart := time.Now()

	ctx := r.Context()
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

	if pw.statusCode < 200 || pw.statusCode >= 300 {
		s.logger.Error(err, "request failed", "code", pw.statusCode)
		w.WriteHeader(pw.statusCode)
		return
	}

	// Decode Stage
	decodeStart := time.Now()

	// Forward original request to local decoder
	r.Body = io.NopCloser(strings.NewReader(string(original)))
	if !s.forwardDataParallel || !s.dataParallelHandler(w, r) {
		s.logger.V(4).Info("sending request to decoder", "to", s.decoderURL.Host)
		s.decoderProxy.ServeHTTP(w, r)
	}

	decodeDuration := time.Since(decodeStart)

	// Calculate and record P/D coordinator metrics
	totalDuration := time.Since(requestStart)
	coordinatorOverhead := decodeStart.Sub(prefillStart.Add(prefillDuration))

	// Record Prometheus metrics for dashboard aggregation
	sidecarmetrics.PDProxyCoordinatorOverheadMilliseconds.WithLabelValues("lmcache").Observe(float64(coordinatorOverhead.Milliseconds()))
	sidecarmetrics.PDProxyPrefillDurationMilliseconds.WithLabelValues("lmcache").Observe(float64(prefillDuration.Milliseconds()))
	sidecarmetrics.PDProxyDecodeDurationMilliseconds.WithLabelValues("lmcache").Observe(float64(decodeDuration.Milliseconds()))
	sidecarmetrics.PDProxyTotalDurationMilliseconds.WithLabelValues("lmcache").Observe(float64(totalDuration.Milliseconds()))
}
