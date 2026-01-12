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
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
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

// getPrefixCacheScoreForPod reads the prefix cache score for a pod from cycle state
// Exactly matches GAIE's getPrefixCacheScoreForPod implementation
func getPrefixCacheScoreForPod(ctx context.Context, cycleState *schedulingtypes.CycleState, pod schedulingtypes.Pod) float64 {
	logger := log.FromContext(ctx)
	logger.V(logutil.DEBUG).Info("Running getPrefixCacheScoreForPod, getting prefix cache score for pod", "pod", pod.GetPod().String())

	plugintype := prefix.PrefixCachePluginType
	pluginname := prefix.PrefixCachePluginType
	cycleStateKey := (plugins.TypedName{Type: plugintype, Name: pluginname}).String()
	stateData, err := cycleState.Read(plugins.StateKey(cycleStateKey))

	logger.V(logutil.DEBUG).Info("Reading prefix cache state from cycle state", "stateKey", cycleStateKey)

	if err != nil {
		// The prefix cache plugin might not be enabled, which is a valid scenario.
		logger.V(logutil.DEBUG).Info("prefix cache state not found in cycle state, returning prefix cache score of 0.0", "pod", pod.GetPod().String())
		return 0.0
	}

	prefixCacheState, ok := stateData.(*prefix.SchedulingContextState)
	if !ok {
		// This should not happen if the plugin is configured correctly.
		logger.Error(fmt.Errorf("unexpected state type: %T", stateData), "failed to read prefix cache state")
		return 0.0
	}

	total := len(prefixCacheState.PrefixHashes)
	if total == 0 {
		// if the request has no prefixes, return 0.0
		logger.V(logutil.DEBUG).Info("No prefixes found in request, returning prefix cache score of 0.0")
		return 0.0
	}

	matchLen := prefixCacheState.PrefixCacheServers[prefix.ServerID(pod.GetPod().NamespacedName)]
	logger.V(logutil.DEBUG).Info("Prefix cache score for pod", "pod", pod.GetPod().String(), "matchLen", matchLen, "totalPrefixes", total)
	return float64(matchLen) / float64(total)
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

	// Cached metrics (refreshed periodically) - using sync.Map for thread-safe concurrent access
	lastSeenMetrics sync.Map

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

	// Count profiles for logging
	profileCount := 0

	// Refresh metrics for all profiles in the scheduling result
	for profileName, profileResult := range telCtx.schedulingResult.ProfileResults {
		if profileResult != nil && profileResult.TargetPods != nil && len(profileResult.TargetPods) > 0 {
			// Get potentially fresh metrics from the pod
			// The GetMetrics() call returns current state which may have been updated by background refresh
			if metrics := profileResult.TargetPods[0].GetMetrics(); metrics != nil {
				// Clone to prevent concurrent modification issues
				// Use sync.Map.Store for thread-safe concurrent write
				telCtx.lastSeenMetrics.Store(profileName, metrics.Clone())
				profileCount++
			}
		}
	}

	logger.V(logutil.TRACE).Info("Refreshed metrics from scheduling result",
		"profileCount", profileCount)
}

// getLatestMetricsForProfile retrieves the most recently refreshed metrics for a specific profile
func getLatestMetricsForProfile(telCtx *pdTelemetryContext, profileName string) (*backendmetrics.MetricsState, error) {
	// Use sync.Map.Load for thread-safe concurrent read
	if value, exists := telCtx.lastSeenMetrics.Load(profileName); exists {
		if metrics, ok := value.(*backendmetrics.MetricsState); ok {
			return metrics, nil
		}
		return nil, fmt.Errorf("invalid metrics type for profile %s", profileName)
	}

	return nil, fmt.Errorf("no metrics found for profile %s", profileName)
}

// ============================================================================
// Running Request Priority Queue (for per-pod min TPOT SLO tracking)
// ============================================================================

// runningRequest tracks SLO requirements for a running request on a pod
type runningRequest struct {
	requestID string
	ttft      float64 // TTFT SLO
	tpot      float64 // TPOT SLO
}

// requestPriorityQueue maintains running requests sorted by strictest TPOT SLO
type requestPriorityQueue struct {
	requests []*runningRequest
}

// newRequestPriorityQueue creates a new priority queue
func newRequestPriorityQueue() *requestPriorityQueue {
	return &requestPriorityQueue{
		requests: make([]*runningRequest, 0),
	}
}

// Push adds a request to the queue
func (q *requestPriorityQueue) Push(req *runningRequest) {
	q.requests = append(q.requests, req)
}

// Remove removes a request by ID
func (q *requestPriorityQueue) Remove(requestID string) {
	for i, req := range q.requests {
		if req.requestID == requestID {
			q.requests = append(q.requests[:i], q.requests[i+1:]...)
			return
		}
	}
}

// Peek returns the request with strictest (minimum) TPOT SLO
func (q *requestPriorityQueue) Peek() *runningRequest {
	if len(q.requests) == 0 {
		return nil
	}

	// Find minimum TPOT SLO
	minReq := q.requests[0]
	for _, req := range q.requests[1:] {
		if req.tpot > 0 && (minReq.tpot == 0 || req.tpot < minReq.tpot) {
			minReq = req
		}
	}
	return minReq
}

// GetSize returns the number of running requests
func (q *requestPriorityQueue) GetSize() int {
	return len(q.requests)
}
