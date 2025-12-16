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
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
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

// ============================================================================
// Telemetry Context for PD Request Tracking
// ============================================================================

// pdTelemetryContext tracks request lifecycle for telemetry collection in PD mode
type pdTelemetryContext struct {
	// Pod identification
	prefillPod string
	decodePod  string

	// Timing tracking
	requestReceived time.Time
	prefillStart    time.Time
	decodeStart     time.Time
	firstToken      time.Time
	lastToken       time.Time
	tokenCount      int

	// Scheduling result references (for live metric refresh)
	schedulingResult   *schedulingtypes.SchedulingResult
	prefillProfileName string
	decodeProfileName  string

	// Cached metrics (refreshed periodically)
	lastSeenMetrics map[string]*backendmetrics.MetricsState

	// Token sampling for TPOT predictions
	tokenSampler *tokenSampler

	// TPOT prediction tracking
	avgTPOT                   float64
	avgPredictedTPOT          float64
	tpotObservations          []float64
	predictedTPOTObservations []float64
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

// getInputTokenLength extracts input token count from request
// Uses word count as approximation (same as slo-aware-router)
func getInputTokenLength(request *schedulingtypes.LLMRequest) int {
	if request.Body == nil || request.Body.Completions == nil || request.Body.Completions.Prompt == "" {
		return 0
	}
	return len(strings.Fields(request.Body.Completions.Prompt))
}

// refreshLastSeenMetrics updates the cached metrics from the scheduling result.
// This queries potentially fresh metrics that may have been updated by background refresh loops.
func refreshLastSeenMetrics(ctx context.Context, telCtx *pdTelemetryContext) {
	logger := log.FromContext(ctx)

	if telCtx.schedulingResult == nil {
		logger.V(logutil.DEBUG).Info("No scheduling result available for metric refresh")
		return
	}

	if telCtx.lastSeenMetrics == nil {
		telCtx.lastSeenMetrics = make(map[string]*backendmetrics.MetricsState)
	}

	// Refresh metrics for all profiles in the scheduling result
	for profileName, profileResult := range telCtx.schedulingResult.ProfileResults {
		if profileResult != nil && profileResult.TargetPods != nil && len(profileResult.TargetPods) > 0 {
			// Get potentially fresh metrics from the pod
			// The GetMetrics() call returns current state which may have been updated by background refresh
			if metrics := profileResult.TargetPods[0].GetMetrics(); metrics != nil {
				// Clone to prevent concurrent modification issues
				telCtx.lastSeenMetrics[profileName] = metrics.Clone()
			}
		}
	}

	logger.V(logutil.TRACE).Info("Refreshed metrics from scheduling result",
		"profileCount", len(telCtx.lastSeenMetrics))
}

// getLatestMetricsForProfile retrieves the most recently refreshed metrics for a specific profile
func getLatestMetricsForProfile(telCtx *pdTelemetryContext, profileName string) (*backendmetrics.MetricsState, error) {
	if len(telCtx.lastSeenMetrics) == 0 {
		return nil, fmt.Errorf("no cached metrics available")
	}

	if metrics, exists := telCtx.lastSeenMetrics[profileName]; exists {
		return metrics, nil
	}

	return nil, fmt.Errorf("no metrics found for profile %s", profileName)
}
