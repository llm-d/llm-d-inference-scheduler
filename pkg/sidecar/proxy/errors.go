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
	"net/http"
)

// vLLM error response
type errorResponse struct {
	Object  string `json:"object"`
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    int    `json:"code"`
}

func errorJSONInvalid(err error, w http.ResponseWriter) error {
	// Simulate vLLM error

	// Example:
	//{
	//	"object": "error",
	//	"message": "[{'type': 'json_invalid', 'loc': ('body', 167), 'msg': 'JSON decode error', 'input': {}, 'ctx': {'error': 'Invalid control character at'}}]",
	//	"type": "BadRequestError",
	//	"param": null,
	//	"code": 400
	//  }

	er := errorResponse{
		Object:  "error",
		Message: err.Error(),
		Type:    "BadRequestError",
		Code:    http.StatusBadRequest,
	}

	b, err := json.Marshal(er)
	if err != nil {
		return err
	}

	w.WriteHeader(http.StatusBadRequest)
	_, err = w.Write(b)
	return err
}

func errorBadGateway(err error, w http.ResponseWriter) error {
	er := errorResponse{
		Object:  "error",
		Message: err.Error(),
		Type:    "BadGateway",
		Code:    http.StatusBadGateway,
	}

	b, err := json.Marshal(er)
	if err != nil {
		return err
	}

	w.WriteHeader(http.StatusBadGateway)
	_, err = w.Write(b)
	return err
}
