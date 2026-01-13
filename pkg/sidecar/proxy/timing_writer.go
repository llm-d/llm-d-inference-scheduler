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
	"fmt"
	"net/http"
)

const (
	// responseHeaderPrefillTTFTMs reports the actual prefill TTFT in milliseconds to EPP
	// for training data collection. EPP's SLOAwareRouter extracts this header in the
	// ResponseReceived hook and records training data for the prefill pod.
	responseHeaderPrefillTTFTMs = "x-prefill-ttft-ms"

	// responseHeaderPrefillPodURL reports which prefill pod was used for this request.
	// This helps EPP correlate the timing with the correct pod for training.
	responseHeaderPrefillPodURL = "x-prefill-pod-url"
)

// timingResponseWriter wraps http.ResponseWriter to inject prefill timing headers
// into the response. This allows EPP to collect training data for prefill pods
// even though the prefill request/response happens inside the decode sidecar and
// is not directly visible to EPP.
//
// The wrapper intercepts WriteHeader and Write calls to ensure headers are set
// before the response body is written.
type timingResponseWriter struct {
	http.ResponseWriter
	prefillLatencyMs float64 // Actual prefill TTFT in milliseconds
	prefillPodHost   string  // Prefill pod host:port
	headersWritten   bool    // Track if headers have been written
}

// WriteHeader intercepts the WriteHeader call to inject prefill timing headers
// before the status code is written.
func (w *timingResponseWriter) WriteHeader(statusCode int) {
	if !w.headersWritten {
		// Inject prefill timing headers before writing response
		w.Header().Set(responseHeaderPrefillTTFTMs, fmt.Sprintf("%.2f", w.prefillLatencyMs))
		w.Header().Set(responseHeaderPrefillPodURL, w.prefillPodHost)
		w.headersWritten = true
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write intercepts the Write call to ensure headers are written before body content.
// This handles cases where WriteHeader is not called explicitly.
func (w *timingResponseWriter) Write(b []byte) (int, error) {
	if !w.headersWritten {
		// WriteHeader wasn't called explicitly, call it with 200 OK
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher to support streaming responses.
// This is important for SSE (Server-Sent Events) responses.
func (w *timingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
