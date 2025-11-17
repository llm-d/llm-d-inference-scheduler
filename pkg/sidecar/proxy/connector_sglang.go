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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (s *Server) runSGLangProtocol(w http.ResponseWriter, r *http.Request, prefillPodHostPort string) {
	s.logger.V(4).Info("running SGLang protocol", "url", prefillPodHostPort)

	// Make Request
	requestData, err := s.parseSGLangRequest(r)

	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// Validate prefill host
	if prefillPodHostPort == "" {
		err := fmt.Errorf("prefill host required for SGLang P/D disaggregation")
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	roomID := s.generateSGLangRoomID()

	// Inject bootstrap info for both prefill and decode
	bootstrapInfo := s.addSGLangBootstrapInfo(requestData, prefillPodHostPort, roomID)

	body, err := json.Marshal(bootstrapInfo)
	if err != nil {
		if err := errorJSONInvalid(err, w); err != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// Send concurrent prefill and decode requests
	s.sendSGLangConcurrentRequests(w, r, body, prefillPodHostPort)
}

func (s *Server) sendSGLangConcurrentRequests(w http.ResponseWriter, r *http.Request, body []byte, prefillHost string) {
	// Create separate requests for prefill and decode
	prefillReq := cloneWithJSONBody(r, body)
	decodeReq := cloneWithJSONBody(r, body)

	// Send prefill request asynchronously
	go func() {
		prefillHandler, err := s.prefillerProxyHandler(prefillHost)
		if err != nil {
			s.logger.Error(err, "failed to get prefiller proxy handler", "prefill_host", prefillHost)
			return
		}
		pw := &bufferedResponseWriter{}

		prefillHandler.ServeHTTP(pw, prefillReq)
		s.logger.V(5).Info("prefill request completed", "status", pw.statusCode)
	}()
	// Send decode request synchronously
	s.decoderProxy.ServeHTTP(w, decodeReq)
}

func cloneWithJSONBody(r *http.Request, body []byte) *http.Request {
	req := r.Clone(r.Context())
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return req
}

func (s *Server) addSGLangBootstrapInfo(requestData map[string]interface{}, prefillHostPort string, roomID int64) map[string]interface{} {
	modifiedRequest := make(map[string]interface{})
	for k, v := range requestData {
		modifiedRequest[k] = v
	}

	// Generate bootstrap host from prefill host
	bootstrapHost, bootstrapPort := s.getBootstrapHost(prefillHostPort)

	// Add bootstrap information
	modifiedRequest[requestFieldBootstrapHost] = bootstrapHost
	modifiedRequest[requestFieldBootstrapPort] = bootstrapPort
	modifiedRequest[requestFieldBootstrapRoom] = roomID

	s.logger.V(5).Info("bootstrap info added",
		"bootstrap_host", bootstrapHost,
		"bootstrap_port", bootstrapPort,
		"bootstrap_room", roomID)

	return modifiedRequest
}

func (s *Server) parseSGLangRequest(r *http.Request) (map[string]interface{}, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}

	var requestData map[string]interface{}
	if err := json.Unmarshal(body, &requestData); err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	return requestData, nil
}

func (s *Server) generateSGLangRoomID() int64 {
	return time.Now().UnixNano() + int64(rand.Intn(1000))
}

func (s *Server) getBootstrapHost(prefillHostPort string) (string, int) {
	// Extract hostname from prefill host
	parts := strings.Split(prefillHostPort, ":")
	hostname := parts[0]
	// Get bootstrap port from environment variable
	bootstrapPort := 8998 // Default SGLang bootstrap port
	if portStr := os.Getenv("SGLANG_BOOTSTRAP_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			bootstrapPort = port
		}
	}
	return hostname, bootstrapPort
}
