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
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

const sseEventDelimiter = "\n\n"

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

type flushableResponseWriter interface {
	http.ResponseWriter
	http.Flusher
}

// responseWriterWithBuffer wraps an http.ResponseWriter to buffer initial writes.
// Start in buffer mode to inspect the first chunk, then call flushBufferAndGoDirect()
// to write buffered content and switch to direct pass-through mode.
type responseWriterWithBuffer struct {
	writerFlusher flushableResponseWriter

	// buffering is checked atomically to allow lock-free fast paths
	// in direct mode (Write and Flush).
	buffering atomic.Bool

	// mu protects buffer, statusCode, and wroteHeader during buffering mode
	// and during the transition to direct mode.
	mu          sync.Mutex
	buffer      strings.Builder
	statusCode  int
	wroteHeader bool

	// ready receives an error (or nil) when the first Write happens,
	// signaling that there's data available for inspection or an error occurred.
	ready     chan struct{}
	readyOnce sync.Once
}

// newResponseWriterWithBuffer creates a new writer starting in buffer mode.
func newResponseWriterWithBuffer(w flushableResponseWriter) *responseWriterWithBuffer {
	rw := &responseWriterWithBuffer{
		writerFlusher: w,
		ready:         make(chan struct{}, 1), // buffered to avoid blocking sender
	}
	rw.buffering.Store(true)
	return rw
}

// FirstChunkReady returns a channel that receives nil when the first chunk of
// body data is available in the buffer, or an error if the write failed.
func (w *responseWriterWithBuffer) FirstChunkReady() <-chan struct{} {
	return w.ready
}

func (w *responseWriterWithBuffer) Header() http.Header {
	return w.writerFlusher.Header()
}

func (w *responseWriterWithBuffer) Write(b []byte) (int, error) {
	if !w.buffering.Load() {
		return w.writerFlusher.Write(b)
	}

	// Buffering mode, need lock to protect buffer
	w.mu.Lock()
	defer w.mu.Unlock()

	// Double-check after acquiring lock (may have transitioned to direct mode)
	if !w.buffering.Load() {
		return w.writerFlusher.Write(b)
	}

	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}

	// Write() always returns a nil error
	n, _ := w.buffer.Write(b)

	// Signal ready when buffer contains at least 2 complete SSE events.
	// For SSE streaming, the first chunk is just the role announcement with
	// finish_reason:null. We need the second chunk to see if cache_threshold
	// was returned (early abort) or if decode is proceeding normally.
	if shouldSignal(w.buffer.String()) {
		w.signalReady()
	}

	return n, nil
}

func (w *responseWriterWithBuffer) WriteHeader(statusCode int) {
	if !w.buffering.Load() {
		w.writerFlusher.WriteHeader(statusCode)
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.buffering.Load() {
		w.writerFlusher.WriteHeader(statusCode)
		return
	}

	if w.statusCode == 0 {
		w.statusCode = statusCode
	}
}

func (w *responseWriterWithBuffer) Flush() {
	if w.buffering.Load() {
		// Apply same logic as Write(): only signal when we have at least 2 SSE events.
		w.mu.Lock()
		shouldSignal := shouldSignal(w.buffer.String())
		w.mu.Unlock()
		if shouldSignal {
			w.signalReady()
		}
		return
	}
	w.writerFlusher.Flush()
}

// firstChunkReady returns a channel that receives nil when the first complete
// chunk of body data is available in the buffer. For SSE streaming responses,
// this is signaled when the buffer contains "\n\n" (complete SSE event).
// As a fallback, Flush() also signals readiness.
func (w *responseWriterWithBuffer) firstChunkReady() <-chan struct{} {
	return w.ready
}

func (w *responseWriterWithBuffer) signalReady() {
	w.readyOnce.Do(func() {
		w.ready <- struct{}{}
		close(w.ready)
	})
}

func (w *responseWriterWithBuffer) writeHeaderOnce() {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if w.statusCode != 0 {
		w.writerFlusher.WriteHeader(w.statusCode)
	}
}

// buffered returns the currently buffered content for inspection.
func (w *responseWriterWithBuffer) buffered() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

// getStatusCode returns the status code that was set (0 if not set).
func (w *responseWriterWithBuffer) getStatusCode() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.statusCode
}

// flushBufferAndGoDirect writes any buffered content to the underlying writer
// and switches to direct mode for all subsequent writes.
func (w *responseWriterWithBuffer) flushBufferAndGoDirect() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.buffering.Load() {
		return nil // already in direct mode
	}

	w.writeHeaderOnce()

	// Write buffered content to underlying writer
	if w.buffer.Len() > 0 {
		_, err := w.writerFlusher.Write([]byte(w.buffer.String()))
		if err != nil {
			return err
		}
	}

	// Flush BEFORE switching to direct mode.
	// This prevents concurrent Flush() calls on the underlying writer,
	// since the proxy goroutine's Flush() will no-op while buffering=true.
	w.writerFlusher.Flush()

	// Switch to direct mode. After this, the proxy goroutine handles
	// all writes and flushes directly (no concurrency with us).
	w.buffering.Store(false)

	return nil
}

func shouldSignal(data string) bool {
	return strings.Count(data, sseEventDelimiter) >= 2
}
