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

package profile

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
	latencypredictor "sigs.k8s.io/gateway-api-inference-extension/sidecars/latencypredictorasync"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/predictors"
)

// Telemetry context for tracking request lifecycle in PD mode
type pdTelemetryContext struct {
	prefillPod      string
	decodePod       string
	requestReceived time.Time
	prefillStart    time.Time
	decodeStart     time.Time
	firstToken      time.Time
	lastToken       time.Time
	tokenCount      int
	metricsSnapshot *backendmetrics.MetricsState
}

// Store telemetry contexts per request
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
// podAddr is the pod's NamespacedName string
func isPrefillPod(pod *backend.Pod, prefillAddr string) bool {
	if prefillAddr == "" {
		return false
	}
	// Construct pod address from pod info
	podAddress := net.JoinHostPort(pod.Address, pod.Port)
	return podAddress == prefillAddr
}

// Implement requestcontrol hooks for telemetry collection
var _ requestcontrol.PreRequest = &PdSLOProfileHandler{}
var _ requestcontrol.ResponseReceived = &PdSLOProfileHandler{}
var _ requestcontrol.ResponseStreaming = &PdSLOProfileHandler{}
var _ requestcontrol.ResponseComplete = &PdSLOProfileHandler{}

// PreRequest is called after scheduling, before sending request to pod
func (h *PdSLOProfileHandler) PreRequest(
	ctx context.Context,
	request *types.LLMRequest,
	schedulingResult *types.SchedulingResult,
) {
	logger := log.FromContext(ctx)

	// Only track if we have both prefill and decode pods (PD mode active)
	prefillAddr := request.Headers[common.PrefillPodHeader]
	if prefillAddr == "" {
		logger.V(logutil.DEBUG).Info("No prefill header, skipping PD telemetry")
		return
	}

	requestID := request.Headers[requtil.RequestIdHeaderKey]
	if requestID == "" {
		logger.V(logutil.DEBUG).Info("No request ID, skipping PD telemetry")
		return
	}

	telCtx := getTelemetryContext(requestID)
	telCtx.prefillPod = prefillAddr

	// Decode pod is the target from scheduling result
	if schedulingResult != nil && len(schedulingResult.ProfileResults) > 0 {
		primaryProfile := schedulingResult.ProfileResults[schedulingResult.PrimaryProfileName]
		if primaryProfile != nil && len(primaryProfile.TargetPods) > 0 {
			decodePodInfo := primaryProfile.TargetPods[0].GetPod()
			telCtx.decodePod = decodePodInfo.NamespacedName.String()
		}
	}

	logger.V(logutil.DEBUG).Info("PD telemetry initialized",
		"requestID", requestID,
		"prefillPod", telCtx.prefillPod,
		"decodePod", telCtx.decodePod)
}

// ResponseReceived is called when response headers are received
func (h *PdSLOProfileHandler) ResponseReceived(
	ctx context.Context,
	request *types.LLMRequest,
	response *requestcontrol.Response,
	targetPod *backend.Pod,
) {
	logger := log.FromContext(ctx)
	requestID := request.Headers[requtil.RequestIdHeaderKey]
	if requestID == "" {
		return
	}

	telCtx := getTelemetryContext(requestID)

	// Track which phase this response is from
	if isPrefillPod(targetPod, telCtx.prefillPod) {
		telCtx.prefillStart = time.Now()
		logger.V(logutil.DEBUG).Info("Prefill response received", "requestID", requestID, "pod", targetPod.NamespacedName.String())
	} else {
		telCtx.decodeStart = time.Now()
		logger.V(logutil.DEBUG).Info("Decode response received", "requestID", requestID, "pod", targetPod.NamespacedName.String())
	}
}

// ResponseStreaming is called for each token in the streaming response
func (h *PdSLOProfileHandler) ResponseStreaming(
	ctx context.Context,
	request *types.LLMRequest,
	response *requestcontrol.Response,
	targetPod *backend.Pod,
) {
	logger := log.FromContext(ctx)
	requestID := request.Headers[requtil.RequestIdHeaderKey]
	if requestID == "" || response.EndOfStream {
		return
	}

	telCtx := getTelemetryContext(requestID)
	now := time.Now()
	podAddr := targetPod.NamespacedName.String()

	// Only track tokens from decode pod (final output)
	if isPrefillPod(podAddr, telCtx.prefillPod) {
		// Prefill doesn't generate output tokens, only processes input
		return
	}

	// First token from decode
	if telCtx.firstToken.IsZero() {
		telCtx.firstToken = now
		telCtx.tokenCount = 1

		// Calculate and record TTFT for decode pod
		decodeTTFT := now.Sub(telCtx.decodeStart).Milliseconds()

		// Send telemetry to decode training server
		h.recordDecodeTTFT(ctx, request, targetPod, float64(decodeTTFT))

		logger.V(logutil.DEBUG).Info("First token from decode",
			"requestID", requestID,
			"decodeTTFT_ms", decodeTTFT)
	} else {
		// Subsequent tokens - track for TPOT
		telCtx.tokenCount++
		interTokenLatency := now.Sub(telCtx.lastToken).Milliseconds()

		// Send TPOT telemetry to decode training server
		h.recordDecodeTPOT(ctx, request, targetPod, float64(interTokenLatency), telCtx.tokenCount)

		logger.V(logutil.TRACE).Info("Token from decode",
			"requestID", requestID,
			"tokenCount", telCtx.tokenCount,
			"interTokenLatency_ms", interTokenLatency)
	}

	telCtx.lastToken = now
}

// ResponseComplete is called when the response is fully sent
func (h *PdSLOProfileHandler) ResponseComplete(
	ctx context.Context,
	request *types.LLMRequest,
	response *requestcontrol.Response,
	targetPod *backend.Pod,
) {
	logger := log.FromContext(ctx)
	requestID := request.Headers[requtil.RequestIdHeaderKey]
	if requestID == "" {
		return
	}

	telCtx := getTelemetryContext(requestID)
	podAddr := targetPod.NamespacedName.String()

	// If this is prefill completion, record prefill TTFT
	if isPrefillPod(podAddr, telCtx.prefillPod) {
		if !telCtx.prefillStart.IsZero() {
			prefillTTFT := time.Since(telCtx.prefillStart).Milliseconds()
			h.recordPrefillTTFT(ctx, request, targetPod, float64(prefillTTFT))

			logger.V(logutil.DEBUG).Info("Prefill complete",
				"requestID", requestID,
				"prefillTTFT_ms", prefillTTFT)
		}
	} else {
		// Decode completion - cleanup
		logger.V(logutil.DEBUG).Info("Decode complete",
			"requestID", requestID,
			"totalTokens", telCtx.tokenCount)
		deleteTelemetryContext(requestID)
	}
}

// recordPrefillTTFT sends prefill TTFT telemetry to prefill training server
func (h *PdSLOProfileHandler) recordPrefillTTFT(
	ctx context.Context,
	request *types.LLMRequest,
	pod *backend.Pod,
	ttftMs float64,
) {
	logger := log.FromContext(ctx)

	// Get predictors from context or global
	predictorSet := h.getPredictorSet()
	if predictorSet == nil || predictorSet.PrefillPredictor == nil {
		logger.V(logutil.DEBUG).Info("No prefill predictor available for telemetry")
		return
	}

	// Build training entry
	metrics := pod.Metrics
	if metrics == nil {
		logger.V(logutil.DEBUG).Info("No metrics available for prefill telemetry")
		return
	}

	entry := latencypredictor.TrainingEntry{
		KVCachePercentage:  metrics.KVCacheUsagePercent,
		InputTokenLength:   len(strings.Fields(request.Body.Completions.Prompt)),
		ActualTTFT:         ttftMs,
		ActualTPOT:         0, // Prefill doesn't produce output tokens
		Timestamp:          time.Now(),
		NumRequestWaiting:  metrics.WaitingQueueSize,
		NumRequestRunning:  metrics.RunningRequestsSize,
		NumTokensGenerated: 0,
		PrefixCacheScore:   0, // TODO: Get from cycle state if available
	}

	if err := predictorSet.PrefillPredictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to send prefill TTFT telemetry")
	} else {
		logger.V(logutil.DEBUG).Info("Sent prefill TTFT telemetry", "ttft_ms", ttftMs)
	}
}

// recordDecodeTTFT sends decode TTFT telemetry to decode training server
func (h *PdSLOProfileHandler) recordDecodeTTFT(
	ctx context.Context,
	request *types.LLMRequest,
	pod *backend.Pod,
	ttftMs float64,
) {
	logger := log.FromContext(ctx)

	predictorSet := h.getPredictorSet()
	if predictorSet == nil || predictorSet.DecodePredictor == nil {
		logger.V(logutil.DEBUG).Info("No decode predictor available for telemetry")
		return
	}

	metrics := pod.Metrics
	if metrics == nil {
		logger.V(logutil.DEBUG).Info("No metrics available for decode TTFT telemetry")
		return
	}

	entry := latencypredictor.TrainingEntry{
		KVCachePercentage:  metrics.KVCacheUsagePercent,
		InputTokenLength:   len(strings.Fields(request.Body.Completions.Prompt)),
		ActualTTFT:         ttftMs,
		ActualTPOT:         0,
		Timestamp:          time.Now(),
		NumRequestWaiting:  metrics.WaitingQueueSize,
		NumRequestRunning:  metrics.RunningRequestsSize,
		NumTokensGenerated: 0,
		PrefixCacheScore:   0,
	}

	if err := predictorSet.DecodePredictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to send decode TTFT telemetry")
	} else {
		logger.V(logutil.DEBUG).Info("Sent decode TTFT telemetry", "ttft_ms", ttftMs)
	}
}

// recordDecodeTPOT sends decode TPOT telemetry to decode training server
func (h *PdSLOProfileHandler) recordDecodeTPOT(
	ctx context.Context,
	request *types.LLMRequest,
	pod *backend.Pod,
	tpotMs float64,
	tokenCount int,
) {
	logger := log.FromContext(ctx)

	predictorSet := h.getPredictorSet()
	if predictorSet == nil || predictorSet.DecodePredictor == nil {
		return
	}

	metrics := pod.Metrics
	if metrics == nil {
		return
	}

	entry := latencypredictor.TrainingEntry{
		KVCachePercentage:  metrics.KVCacheUsagePercent,
		InputTokenLength:   len(strings.Fields(request.Body.Completions.Prompt)),
		ActualTTFT:         0,
		ActualTPOT:         tpotMs,
		Timestamp:          time.Now(),
		NumRequestWaiting:  metrics.WaitingQueueSize,
		NumRequestRunning:  metrics.RunningRequestsSize,
		NumTokensGenerated: tokenCount - 1, // Previous token count
		PrefixCacheScore:   0,
	}

	if err := predictorSet.DecodePredictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.TRACE).Error(err, "Failed to send decode TPOT telemetry")
	}
}

// getPredictorSet retrieves the PD predictor set
func (h *PdSLOProfileHandler) getPredictorSet() *predictors.PDPredictorSet {
	return h.predictorSet
}
