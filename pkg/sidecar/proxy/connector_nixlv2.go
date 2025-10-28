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
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

func (s *Server) runNIXLProtocolV2(w http.ResponseWriter, r *http.Request, prefillPodHostPort string) {
	s.logger.V(4).Info("running NIXL protocol V2", "url", prefillPodHostPort)

	// Read request body
	defer r.Body.Close() //nolint:all
	original, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest) // TODO: check FastAPI error code when failing to read body
		w.Write([]byte(err.Error()))         //nolint:all
		return
	}

	// Parse completion request
	var completionRequest map[string]any
	if err := json.Unmarshal(original, &completionRequest); err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// Generate unique request UUID
	uuid, err := uuid.NewUUID()
	if err != nil {
		if err := errorBadGateway(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	uuidStr := uuid.String()

	// Prefill Stage

	// 1. Prepare prefill request
	ctx := r.Context()
	preq := r.Clone(ctx)

	preq.Header.Add(requestHeaderRequestID, uuidStr)

	streamValue, streamOk := completionRequest[requestFieldStream]
	streamOptionsValue, streamOptionsOk := completionRequest[requestFieldStreamOptions]
	maxTokensValue, maxTokensOk := completionRequest[requestFieldMaxTokens]

	completionRequest[requestFieldKVTransferParams] = map[string]any{
		requestFieldDoRemoteDecode:  true,
		requestFieldDoRemotePrefill: false,
		requestFieldRemoteEngineID:  nil,
		requestFieldRemoteBlockIDs:  nil,
		requestFieldRemoteHost:      nil,
		requestFieldRemotePort:      nil,
	}

	completionRequest[requestFieldStream] = false
	delete(completionRequest, requestFieldStreamOptions)
	completionRequest[requestFieldMaxTokens] = 1

	pbody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	preq.Body = io.NopCloser(strings.NewReader(string(pbody)))
	preq.ContentLength = int64(len(pbody))

	prefillHandler, err := s.prefillerProxyHandler(prefillPodHostPort)
	if err != nil {
		if err := errorBadGateway(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// 2. Forward request to prefiller
	s.logger.V(5).Info("sending request to prefiller", "url", prefillPodHostPort, "body", string(pbody))
	pw := &bufferedResponseWriter{}
	prefillHandler.ServeHTTP(pw, preq)

	if pw.statusCode < 200 || pw.statusCode >= 300 {
		s.logger.Error(err, "request failed", "code", pw.statusCode)
		w.WriteHeader(pw.statusCode)
		return
	}

	// Process response - extract p/d fields
	var prefillerResponse map[string]any
	if err := json.Unmarshal([]byte(pw.buffer.String()), &prefillerResponse); err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// 3. Verify response

	pKVTransferParams, ok := prefillerResponse[requestFieldKVTransferParams]
	if !ok {
		s.logger.Info("warning: missing 'kv_transfer_params' field in prefiller response")
	}

	s.logger.V(5).Info("received prefiller response", requestFieldKVTransferParams, pKVTransferParams)

	// Decode Stage

	// 1. Prepare decode request
	dreq := r.Clone(ctx)

	dreq.Header.Add(requestHeaderRequestID, uuidStr)

	delete(completionRequest, requestFieldStream)
	if streamOk {
		completionRequest[requestFieldStream] = streamValue
	}
	if streamOptionsOk {
		completionRequest[requestFieldStreamOptions] = streamOptionsValue
	}
	delete(completionRequest, requestFieldMaxTokens)
	if maxTokensOk {
		completionRequest[requestFieldMaxTokens] = maxTokensValue
	}
	completionRequest[requestFieldKVTransferParams] = pKVTransferParams

	dbody, err := json.Marshal(completionRequest)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}
	dreq.Body = io.NopCloser(strings.NewReader(string(dbody)))
	dreq.ContentLength = int64(len(dbody))

	// 2. Forward to local decoder.

	s.logger.V(5).Info("sending request to decoder", "body", string(dbody))
	s.decoderProxy.ServeHTTP(w, dreq)
}
