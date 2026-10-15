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

package request

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIErrorType(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusBadRequest:            "invalid_request_error",
		http.StatusUnauthorized:          "authentication_error",
		http.StatusForbidden:             "permission_error",
		http.StatusNotFound:              "not_found_error",
		http.StatusPreconditionFailed:    "invalid_request_error",
		http.StatusRequestEntityTooLarge: "invalid_request_error",
		http.StatusUnprocessableEntity:   "invalid_request_error",
		http.StatusTooManyRequests:       "rate_limit_error",
		http.StatusInternalServerError:   "server_error",
		http.StatusBadGateway:            "server_error",
		http.StatusServiceUnavailable:    "server_error",
	} {
		assert.Equal(t, want, OpenAIErrorType(status), "status %d", status)
	}
}

func TestWriteOpenAIError(t *testing.T) {
	rec := httptest.NewRecorder()
	require.NoError(t, WriteOpenAIError(rec, http.StatusRequestEntityTooLarge, `body <too> large & rejected`))

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Header().Get(HeaderContentType), ContentTypeJSON)
	assert.Equal(t, ContentTypeOptionsNoSniff, rec.Header().Get(HeaderContentTypeOptions))
	assert.JSONEq(t, `{"error":{"message":"body <too> large & rejected","type":"invalid_request_error","code":413}}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), `\u003c`, "HTML characters must be written verbatim")
}
