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

package runners

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

	"github.com/go-logr/logr"
	httperrors "github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/http_errors"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/keys"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/manager"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/runners/types"
)

var (
	sglangBootstrapPort int
)

func init() {
	// Default SGLang bootstrap port
	sglangBootstrapPort = 8998

	// Override from environment variable if set
	if portStr := os.Getenv("SGLANG_BOOTSTRAP_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			sglangBootstrapPort = port
		}
	}
}

// SGLangRunner implements P/D disaggregation for SGLang backends.
// Unlike VLLMRunner, SGLangRunner sends prefill and decode requests concurrently
// and uses SGLang's built-in bootstrap coordination mechanism.
type SGLangRunner struct {
	proxyManager *manager.ProxyManager
}

// NewSGLangRunner creates a new SGLangRunner for SGLang backends.
func NewSGLangRunner(proxyManager *manager.ProxyManager) types.ProtocolRunner {
	return &SGLangRunner{
		proxyManager: proxyManager,
	}
}

// Run executes the P/D disaggregation workflow using the SGLang connector protocol.
func (sr *SGLangRunner) Run(w http.ResponseWriter, r *http.Request, prefillPodHostPort string, logger logr.Logger) {
	logger = logger.WithValues("connector", "sglang", "prefillHost", prefillPodHostPort)
	logger.V(4).Info("running protocol")

	// Make Request
	requestData, err := sr.parseSGLangRequest(r)

	if err != nil {
		if err := httperrors.ErrorJSONInvalid(err, w); err != nil {
			logger.Error(err, "failed to send error response to client")
		}
		return
	}

	roomID := sr.generateSGLangRoomID()

	// Inject bootstrap info for both prefill and decode
	bootstrapInfo := sr.addSGLangBootstrapInfo(requestData, prefillPodHostPort, roomID, logger)

	body, err := json.Marshal(bootstrapInfo)
	if err != nil {
		if err := httperrors.ErrorJSONInvalid(err, w); err != nil {
			logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// Send concurrent prefill and decode requests
	sr.sendSGLangConcurrentRequests(w, r, body, prefillPodHostPort, logger)
}

func (sr *SGLangRunner) sendSGLangConcurrentRequests(w http.ResponseWriter, r *http.Request, body []byte, prefillHost string, logger logr.Logger) {
	// Create separate requests for prefill and decode
	prefillReq := cloneWithJSONBody(r, body)
	decodeReq := cloneWithJSONBody(r, body)

	prefillHandler, err := sr.proxyManager.PrefillerProxyHandler(prefillHost, logger)
	if err != nil {
		if err := httperrors.ErrorBadGateway(err, w); err != nil {
			logger.Error(err, "failed to send error response to client")
		}
		return
	}

	// Send prefill request asynchronously
	go func() {
		pw := &bufferedResponseWriter{}
		prefillHandler.ServeHTTP(pw, prefillReq)
		logger.V(5).Info("prefill request completed", "status", pw.statusCode)
	}()

	// Send decode request synchronously
	sr.proxyManager.DecoderProxy.ServeHTTP(w, decodeReq)
}

func cloneWithJSONBody(r *http.Request, body []byte) *http.Request {
	req := r.Clone(r.Context())
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return req
}

func (sr *SGLangRunner) addSGLangBootstrapInfo(requestData map[string]interface{}, prefillHostPort string, roomID int64, logger logr.Logger) map[string]interface{} {
	modifiedRequest := make(map[string]interface{})
	for k, v := range requestData {
		modifiedRequest[k] = v
	}

	// Generate bootstrap host from prefill host
	bootstrapHost := sr.getBootstrapHost(prefillHostPort)

	// Add bootstrap information
	modifiedRequest[keys.RequestFieldBootstrapHost] = bootstrapHost
	modifiedRequest[keys.RequestFieldBootstrapPort] = sglangBootstrapPort
	modifiedRequest[keys.RequestFieldBootstrapRoom] = roomID

	logger.V(5).Info("bootstrap info added",
		"bootstrap_host", bootstrapHost,
		"bootstrap_port", sglangBootstrapPort,
		"bootstrap_room", roomID)

	return modifiedRequest
}

func (sr *SGLangRunner) parseSGLangRequest(r *http.Request) (map[string]interface{}, error) {
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

func (sr *SGLangRunner) generateSGLangRoomID() int64 {
	return time.Now().UnixNano() + int64(rand.Intn(1000))
}

func (sr *SGLangRunner) getBootstrapHost(prefillHostPort string) string {
	// Extract hostname from prefill host
	parts := strings.Split(prefillHostPort, ":")
	return parts[0]
}
