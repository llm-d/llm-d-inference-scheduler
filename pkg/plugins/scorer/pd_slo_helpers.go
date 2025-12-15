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

package scorer

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// SLO header keys
	ttftSLOHeaderKey = "x-slo-ttft-ms"
	tpotSLOHeaderKey = "x-slo-tpot-ms"
)

// parseSLOHeaders extracts TTFT and TPOT SLO values from request headers.
// Returns (ttftSLO, tpotSLO, error). Returns 0,0,nil if headers not present.
func parseSLOHeaders(request *schedulingtypes.LLMRequest) (float64, float64, error) {
	var ttftSLO, tpotSLO float64

	// Parse TTFT SLO
	if ttftStr, ok := request.Headers[ttftSLOHeaderKey]; ok {
		val, err := strconv.ParseFloat(ttftStr, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid %s header value '%s': %w", ttftSLOHeaderKey, ttftStr, err)
		}
		if val <= 0 {
			return 0, 0, fmt.Errorf("%s must be positive, got %f", ttftSLOHeaderKey, val)
		}
		ttftSLO = val
	}

	// Parse TPOT SLO
	if tpotStr, ok := request.Headers[tpotSLOHeaderKey]; ok {
		val, err := strconv.ParseFloat(tpotStr, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid %s header value '%s': %w", tpotSLOHeaderKey, tpotStr, err)
		}
		if val <= 0 {
			return 0, 0, fmt.Errorf("%s must be positive, got %f", tpotSLOHeaderKey, val)
		}
		tpotSLO = val
	}

	return ttftSLO, tpotSLO, nil
}

// hasSLOHeaders checks if the request has at least one SLO header
func hasSLOHeaders(request *schedulingtypes.LLMRequest) bool {
	_, hasTTFT := request.Headers[ttftSLOHeaderKey]
	_, hasTPOT := request.Headers[tpotSLOHeaderKey]
	return hasTTFT || hasTPOT
}

// filterPodsByRole separates pods into prefill and decode based on their role label
func filterPodsByRole(pods []schedulingtypes.Pod) (prefillPods, decodePods []schedulingtypes.Pod) {
	prefillPods = make([]schedulingtypes.Pod, 0)
	decodePods = make([]schedulingtypes.Pod, 0)

	for _, pod := range pods {
		// Check pod labels for role
		role, hasRole := pod.GetPod().Labels["llm-d.ai/role"]
		if !hasRole {
			continue
		}

		switch role {
		case "prefill":
			prefillPods = append(prefillPods, pod)
		case "decode", "both":
			decodePods = append(decodePods, pod)
		}
	}

	return prefillPods, decodePods
}

// classifyPairsByHeadroom separates pairs into positive and negative headroom groups
func classifyPairsByHeadroom(pairs []pairResult) (positive, negative []pairResult) {
// Telemetry Context for PD Request Tracking
// ============================================================================

// pdTelemetryContext tracks request lifecycle for telemetry collection in PD mode
type pdTelemetryContext struct {
	prefillPod      string
	decodePod       string
	requestReceived time.Time
	prefillStart    time.Time
	decodeStart     time.Time
	firstToken      time.Time
	lastToken       time.Time
	tokenCount      int
	prefillMetrics  *backendmetrics.MetricsState
	decodeMetrics   *backendmetrics.MetricsState
}

// Store telemetry contexts per request ID
var (
	telemetryContexts sync.Map // map[requestID]*pdTelemetryContext
)

// getTelemetryContext retrieves or creates telemetry context for a request
func getTelemetryContext(requestID string) *pdTelemetryContext {
	if ctx, exists := telemetryContexts.Load(requestID); exists {
		return ctx.(*pdTelemetryContext)
	}
	ctx := &pdTelemetryContext{
		requestReceived: time.Now(),
	}
	telemetryContexts.Store(requestID, ctx)
	return ctx
}

// deleteTelemetryContext removes telemetry context after request completes
func deleteTelemetryContext(requestID string) {
	telemetryContexts.Delete(requestID)
}

// isPrefillPod checks if the given pod is the prefill pod for this request
// prefillAddr is in format "host:port" from x-prefiller-host-port header
func isPrefillPod(pod *backend.Pod, prefillAddr string) bool {
	if prefillAddr == "" {
		return false
	}
	// Construct pod address from pod info
	podAddress := net.JoinHostPort(pod.Address, pod.Port)
	return podAddress == prefillAddr
}
