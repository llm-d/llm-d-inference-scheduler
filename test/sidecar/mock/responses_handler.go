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

package mock

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// ResponsesHandler is a mock handler for OpenAI Responses API
type ResponsesHandler struct {
	Connector          string
	Role               Role
	RequestCount       atomic.Int32
	ResponsesRequests  []map[string]any
	ResponsesResponses []map[string]any
	mu                 sync.Mutex
}

func (rh *ResponsesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rh.RequestCount.Add(1)

	defer r.Body.Close() //nolint:all
	b, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error())) //nolint:all
		return
	}

	var responsesRequest map[string]any
	if err := json.Unmarshal(b, &responsesRequest); err != nil {
		w.Write([]byte(err.Error())) //nolint:all
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	rh.mu.Lock()
	rh.ResponsesRequests = append(rh.ResponsesRequests, responsesRequest)
	rh.mu.Unlock()

	var rawResponse string

	switch rh.Connector {
	case "nixlv2":
		switch rh.Role {
		case RoleDecode:
			// Decode response for Responses API
			rawResponse = `{"id":"resp_abc123","object":"response","created_at":1699000000,"status":"completed","output":[{"type":"message","id":"msg_abc123","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello! How can I help you today?"}]}]}`
		case RolePrefill:
			// 1. Verify Prefill Request
			kvTransferParams, ok := responsesRequest["kv_transfer_params"]

			if !ok || kvTransferParams == nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("expected kv_transfer_params:{...}")) //nolint:all
				return
			}
			kvTransferParamsMap, ok := kvTransferParams.(map[string]any)
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("expected kv_transfer_params:{...}")) //nolint:all
				return
			}

			if v, ok := kvTransferParamsMap["do_remote_decode"]; !ok || !v.(bool) {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("expected do_remote_decode:true")) //nolint:all
				return
			}
			if v, ok := kvTransferParamsMap["do_remote_prefill"]; !ok || v.(bool) {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("expected do_remote_prefill:false")) //nolint:all
				return
			}
			if v, ok := kvTransferParamsMap["remote_engine_id"]; !ok || v != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("expected remote_engine_id:null")) //nolint:all
				return
			}
			if v, ok := kvTransferParamsMap["remote_block_ids"]; !ok || v != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("expected remote_block_ids:null")) //nolint:all
				return
			}
			if v, ok := kvTransferParamsMap["remote_host"]; !ok || v != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("expected remote_host:null")) //nolint:all
				return
			}
			if v, ok := kvTransferParamsMap["remote_port"]; !ok || v != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("expected remote_port:null")) //nolint:all
				return
			}

			// 2. Produce Response with kv_transfer_params
			rawResponse = `{"kv_transfer_params":{"remote_block_ids":[1, 2, 3], "remote_engine_id": "5b5fb28f-3f30-4bdd-9a36-958d52459200", "remote_host":"ahost", "remote_port":4032}}`
		}

	case "lmcache":
		// LMCache protocol just returns empty response
		rawResponse = `{}`

	case "sglang":
		// SGLang returns a basic response
		rawResponse = `{"id":"resp_abc123","object":"response","created_at":1699000000,"status":"completed","output":[{"type":"message","id":"msg_abc123","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}]}`

	default:
		// Default case for unspecified connector (used for basic tests)
		rawResponse = `{"id":"resp_default","object":"response","created_at":1699000000,"status":"completed","output":[]}`
	}

	var responsesResponse map[string]any
	if err := json.Unmarshal([]byte(rawResponse), &responsesResponse); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error())) //nolint:all
		return
	}

	rh.mu.Lock()
	rh.ResponsesResponses = append(rh.ResponsesResponses, responsesResponse)
	rh.mu.Unlock()

	w.Write([]byte(rawResponse)) //nolint:all
}
