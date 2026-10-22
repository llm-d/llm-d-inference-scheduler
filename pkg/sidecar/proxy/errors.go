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
	"errors"
	"net/http"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
)

var errInvalidJSON = errors.New("invalid JSON")

// errDecodeAborted reports a decode request that aborted before relaying any of
// its response, leaving the caller to write a status on its behalf.
var errDecodeAborted = errors.New("decode aborted before sending a response")

func errorJSONInvalid(w http.ResponseWriter, r *http.Request, err error) error {
	return sendError(w, r, http.StatusBadRequest, err.Error())
}

func errorBadGateway(w http.ResponseWriter, r *http.Request, err error) error {
	return sendError(w, r, http.StatusBadGateway, err.Error())
}

func errorInternalServerError(w http.ResponseWriter, r *http.Request, err error) error {
	return sendError(w, r, http.StatusInternalServerError, err.Error())
}

// sendError writes msg in the error envelope of the API r was sent to. A path
// that is not an inference route, which only the decoder catch-all serves, gets
// the OpenAI envelope.
func sendError(w http.ResponseWriter, r *http.Request, status int, msg string) error {
	return reqcommon.WriteAPIError(w, reqcommon.DetectAPIType(r.URL.Path), status, msg)
}
