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

	httperrors "github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/http_errors"
)

var decoderServiceUnavailableResponseJSON []byte

func init() {
	response := httperrors.ErrorResponse{
		Object:  "error",
		Message: "The decode node is not ready. Please check that the vLLM service is running and the port configuration is correct.",
		Type:    "ServiceUnavailable",
		Code:    http.StatusServiceUnavailable,
	}
	decoderServiceUnavailableResponseJSON, _ = json.Marshal(response)
}
