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
	"math"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// SLO header keys
	ttftSLOHeaderKey = "x-slo-ttft-ms"
	tpotSLOHeaderKey = "x-slo-tpot-ms"

	// Cycle state keys for storing selected pair
	selectedPrefillPodKey = "pd-slo-selected-prefill-pod"
	selectedDecodePodKey  = "pd-slo-selected-decode-pod"
)

// StringStateData is a StateData wrapper for string values
type StringStateData struct {
	Value string
}

// Clone implements plugins.StateData
func (s *StringStateData) Clone() plugins.StateData {
	return &StringStateData{Value: s.Value}
}

// pairResult holds the evaluation result for a (prefill, decode) pod pair
type pairResult struct {
	prefillPod       schedulingtypes.Pod
	decodePod        schedulingtypes.Pod
	prefillTTFT      float64 // Prefill processing time (ms)
	decodeTTFT       float64 // Decode queue wait + startup (ms)
	decodeTPOT       float64 // Decode per-token latency (ms)
	jointTTFT        float64 // prefillTTFT + decodeTTFT + transfer
	ttftValid        bool    // True if jointTTFT meets SLO
	tpotValid        bool    // True if decodeTPOT meets SLO
	ttftHeadroom     float64 // bufferedTTFT - jointTTFT
	tpotHeadroom     float64 // bufferedTPOT - decodeTPOT
	blendedHeadroom  float64 // Weighted sum of headrooms
	prefixCacheScore float64 // Prefix cache hit rate
}

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
	positive = make([]pairResult, 0)
	negative = make([]pairResult, 0)

	for _, pair := range pairs {
		if pair.ttftValid && pair.tpotValid {
			positive = append(positive, pair)
		} else {
			negative = append(negative, pair)
		}
	}

	return positive, negative
}

// selectPairWeightedRandom selects a pair from the given list using weighted random selection.
// Pairs with higher blended headroom have higher probability of being selected.
func selectPairWeightedRandom(pairs []pairResult) *pairResult {
	if len(pairs) == 0 {
		return nil
	}

	if len(pairs) == 1 {
		return &pairs[0]
	}

	// Find min headroom to shift all values to positive
	minHeadroom := pairs[0].blendedHeadroom
	for _, pair := range pairs[1:] {
		if pair.blendedHeadroom < minHeadroom {
			minHeadroom = pair.blendedHeadroom
		}
	}

	// Calculate weights (shift to make all positive, add epsilon to avoid zeros)
	weights := make([]float64, len(pairs))
	totalWeight := 0.0
	epsilon := 1.0 // Add small constant to ensure non-zero weights

	for i, pair := range pairs {
		weight := pair.blendedHeadroom - minHeadroom + epsilon
		weights[i] = weight
		totalWeight += weight
	}

	// Weighted random selection
	source := rand.NewSource(time.Now().UnixNano())
	r := rand.New(source)
	randomValue := r.Float64() * totalWeight
	cumulative := 0.0

	for i, weight := range weights {
		cumulative += weight
		if randomValue <= cumulative {
			return &pairs[i]
		}
	}

	// Fallback to last pair (should never reach here)
	return &pairs[len(pairs)-1]
}

// normalizeHeadroom normalizes headroom values to [0, 1] range for comparison
func normalizeHeadroom(headroom, minHeadroom, maxHeadroom float64) float64 {
	if math.Abs(maxHeadroom-minHeadroom) < 1e-6 {
		return 0.5 // All headrooms are equal
	}
	normalized := (headroom - minHeadroom) / (maxHeadroom - minHeadroom)
	return math.Max(0.0, math.Min(1.0, normalized))
}

// storeSelectedPairInState stores the selected pod pair in cycle state for the profile handler to use
func storeSelectedPairInState(cycleState *schedulingtypes.CycleState, pair *pairResult) {
	if pair == nil {
		return
	}

	// Store pod names
	cycleState.Write(plugins.StateKey(selectedPrefillPodKey), &StringStateData{Value: pair.prefillPod.GetPod().String()})
	cycleState.Write(plugins.StateKey(selectedDecodePodKey), &StringStateData{Value: pair.decodePod.GetPod().String()})
}

// getSelectedPairFromState retrieves the selected pod pair from cycle state
func getSelectedPairFromState(cycleState *schedulingtypes.CycleState) (prefillPodName, decodePodName string, err error) {
	prefillData, err := cycleState.Read(plugins.StateKey(selectedPrefillPodKey))
	if err != nil {
		return "", "", fmt.Errorf("failed to read selected prefill pod from cycle state: %w", err)
	}

	decodeData, err := cycleState.Read(plugins.StateKey(selectedDecodePodKey))
	if err != nil {
		return "", "", fmt.Errorf("failed to read selected decode pod from cycle state: %w", err)
	}

	prefillStringData, ok := prefillData.(*StringStateData)
	if !ok {
		return "", "", fmt.Errorf("selected prefill pod is not StringStateData")
	}

	decodeStringData, ok := decodeData.(*StringStateData)
	if !ok {
		return "", "", fmt.Errorf("selected decode pod is not StringStateData")
	}

	return prefillStringData.Value, decodeStringData.Value, nil
}

// findPodByName finds a pod in the list by its string representation
func findPodByName(pods []schedulingtypes.Pod, podName string) *schedulingtypes.Pod {
	for _, pod := range pods {
		if pod.GetPod().String() == podName {
			return &pod
		}
	}
	return nil
}

// findMinMaxHeadroom finds the min and max blended headroom values in a slice of pairs
func findMinMaxHeadroom(pairs []pairResult) (min, max float64) {
	if len(pairs) == 0 {
		return 0, 0
	}

	min = pairs[0].blendedHeadroom
	max = pairs[0].blendedHeadroom

	for _, pair := range pairs[1:] {
		if pair.blendedHeadroom < min {
			min = pair.blendedHeadroom
		}
		if pair.blendedHeadroom > max {
			max = pair.blendedHeadroom
		}
	}

	return min, max
}

// ============================================================================
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
