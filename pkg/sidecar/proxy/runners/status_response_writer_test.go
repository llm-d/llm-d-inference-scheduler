/*
Copyright 2026 The llm-d Authors.

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

package runners_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/runners"
	. "github.com/onsi/ginkgo/v2" //nolint:revive
	. "github.com/onsi/gomega"    //nolint:revive
)

var _ = Describe("BufferedResponseWriter", func() {
	It("should append writes to buffer", func() {
		w := &runners.BufferedResponseWriter{}
		n, err := w.Write([]byte("hello "))
		Expect(err).ToNot(HaveOccurred())
		Expect(n).To(Equal(6))

		n, err = w.Write([]byte("world"))
		Expect(err).ToNot(HaveOccurred())
		Expect(n).To(Equal(5))
	})

	It("should implement http.ResponseWriter", func() {
		w := &runners.BufferedResponseWriter{}
		w.Write([]byte("data")) //nolint:errcheck
		w.WriteHeader(http.StatusNotFound)
	})

	It("should return headers map", func() {
		w := &runners.BufferedResponseWriter{}
		w.Header().Set("X-Test", "value")
		Expect(w.Header().Get("X-Test")).To(Equal("value"))
	})
})

var _ = Describe("ResponseWriterWithBuffer", func() {
	var (
		underlying *httptest.ResponseRecorder
		rw         *runners.ResponseWriterWithBuffer
	)

	BeforeEach(func() {
		underlying = httptest.NewRecorder()
		rw = runners.NewResponseWriterWithBuffer(underlying)
	})

	It("should signal FirstChunkReady after 2 SSE events", func() {
		rw.Write([]byte("data: {\"choices\":[{\"finish_reason\":null}]}\n\n")) //nolint:errcheck
		select {
		case <-rw.FirstChunkReady():
			Fail("should not signal after only 1 SSE event")
		default:
		}

		rw.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")) //nolint:errcheck
		Eventually(rw.FirstChunkReady()).Should(BeClosed())
	})

	It("should flush buffer to underlying writer and switch to direct mode", func() {
		rw.Write([]byte("buffered data")) //nolint:errcheck
		err := rw.FlushBufferAndGoDirect()
		Expect(err).ToNot(HaveOccurred())
		Expect(underlying.Body.String()).To(Equal("buffered data"))
	})

	It("should write directly after switching to direct mode", func() {
		err := rw.FlushBufferAndGoDirect()
		Expect(err).ToNot(HaveOccurred())

		rw.Write([]byte("direct data")) //nolint:errcheck
		Expect(underlying.Body.String()).To(Equal("direct data"))
	})

	It("should return status code via GetStatusCode", func() {
		Expect(rw.GetStatusCode()).To(Equal(0))
		rw.WriteHeader(http.StatusBadGateway)
		Expect(rw.GetStatusCode()).To(Equal(http.StatusBadGateway))
	})

	It("should return buffered content via Buffered()", func() {
		rw.Write([]byte("test content")) //nolint:errcheck
		Expect(rw.Buffered()).To(Equal("test content"))
	})
})

var _ = Describe("ShouldSignal", func() {
	It("should return true when data contains at least 2 SSE event delimiters", func() {
		Expect(runners.ShouldSignal("data: a\n\ndata: b\n\n")).To(BeTrue())
	})

	It("should return false when data contains fewer than 2 SSE event delimiters", func() {
		Expect(runners.ShouldSignal("data: a\n\n")).To(BeFalse())
		Expect(runners.ShouldSignal("data: a")).To(BeFalse())
		Expect(runners.ShouldSignal("")).To(BeFalse())
	})
})
