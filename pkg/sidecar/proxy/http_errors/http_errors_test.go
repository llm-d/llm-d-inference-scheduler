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
package httperrors_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"

	httperrors "github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/http_errors"
	. "github.com/onsi/ginkgo/v2" //nolint:revive
	. "github.com/onsi/gomega"    //nolint:revive
)

var _ = Describe("HTTP Error Responses", func() {
	Describe("ErrorJSONInvalid", func() {
		It("should return status 400 with BadRequestError type", func() {
			w := httptest.NewRecorder()
			err := httperrors.ErrorJSONInvalid(errors.New("invalid json input"), w)
			Expect(err).ToNot(HaveOccurred())

			Expect(w.Code).To(Equal(http.StatusBadRequest))

			var resp httperrors.ErrorResponse
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp.Type).To(Equal("BadRequestError"))
			Expect(resp.Message).To(Equal("invalid json input"))
			Expect(resp.Code).To(Equal(http.StatusBadRequest))
			Expect(resp.Object).To(Equal("error"))
		})
	})

	Describe("ErrorBadGateway", func() {
		It("should return status 502 with BadGateway type", func() {
			w := httptest.NewRecorder()
			err := httperrors.ErrorBadGateway(errors.New("upstream failed"), w)
			Expect(err).ToNot(HaveOccurred())

			Expect(w.Code).To(Equal(http.StatusBadGateway))

			var resp httperrors.ErrorResponse
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp.Type).To(Equal("BadGateway"))
			Expect(resp.Message).To(Equal("upstream failed"))
			Expect(resp.Code).To(Equal(http.StatusBadGateway))
		})
	})

	Describe("ErrorInternalServerError", func() {
		It("should return status 500 with InternalServerError type", func() {
			w := httptest.NewRecorder()
			err := httperrors.ErrorInternalServerError(errors.New("something broke"), w)
			Expect(err).ToNot(HaveOccurred())

			Expect(w.Code).To(Equal(http.StatusInternalServerError))

			var resp httperrors.ErrorResponse
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp.Type).To(Equal("InternalServerError"))
			Expect(resp.Message).To(Equal("something broke"))
			Expect(resp.Code).To(Equal(http.StatusInternalServerError))
		})
	})
})
