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
	"strings"
)

// bufferedResponseWriter receives responses from prefillers
type bufferedResponseWriter struct {
	headers    http.Header
	buffer     strings.Builder
	statusCode int
}

func (w *bufferedResponseWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}

func (w *bufferedResponseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.buffer.Write(b)
}

func (w *bufferedResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

// headerInjectorResponseWriter wraps an http.ResponseWriter to inject custom headers
// before forwarding the response. Used to add prefill timing telemetry to decode responses.
type headerInjectorResponseWriter struct {
	http.ResponseWriter
	prefillDurationMs int64
	headerWritten     bool
}

func (w *headerInjectorResponseWriter) WriteHeader(statusCode int) {
	if !w.headerWritten {
		w.ResponseWriter.Header().Set("x-prefill-ttft-ms", fmt.Sprintf("%d", w.prefillDurationMs))
		w.headerWritten = true
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *headerInjectorResponseWriter) Write(b []byte) (int, error) {
	if !w.headerWritten {
		w.ResponseWriter.Header().Set("x-prefill-ttft-ms", fmt.Sprintf("%d", w.prefillDurationMs))
		w.headerWritten = true
	}
	return w.ResponseWriter.Write(b)
}
