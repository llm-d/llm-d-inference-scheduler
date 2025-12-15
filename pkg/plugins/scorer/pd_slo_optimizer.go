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
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
	latencypredictor "sigs.k8s.io/gateway-api-inference-extension/sidecars/latencypredictorasync"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/predictors"
)

const (
	// PdSLOOptimizerType is the plugin type identifier
	PdSLOOptimizerType = "pd-slo-optimizer"
)

// Compile-time type assertion
var _ framework.Scorer = &PdSLOOptimizer{}

// pdSLOOptimizerConfig holds configuration parameters for the optimizer
type pdSLOOptimizerConfig struct {
	SLOBufferFactor float64 `json:"sloBufferFactor"` // Safety margin for SLOs (default: 0.9)
}

// Default configuration values
var defaultPdSLOConfig = pdSLOOptimizerConfig{
	SLOBufferFactor: 0.9,
}

// validate checks if the configuration is valid
func (c *pdSLOOptimizerConfig) validate() error {
	if c.SLOBufferFactor <= 0 || c.SLOBufferFactor > 1.0 {
		return fmt.Errorf("sloBufferFactor must be between 0 and 1.0, got %f", c.SLOBufferFactor)
	}
	return nil
}

// PdSLOOptimizer implements independent prefill and decode pod optimization
// based on SLO headroom calculations using trained latency predictors
type PdSLOOptimizer struct {
	typedName   plugins.TypedName
	config      pdSLOOptimizerConfig
	predictors  *predictors.PDPredictorSet
	initialized bool
}

// PdSLOOptimizerFactory creates a new PdSLOOptimizer instance
func PdSLOOptimizerFactory(name string, rawParameters json.RawMessage, handle plugins.Handle) (plugins.Plugin, error) {
	config := defaultPdSLOConfig

	// Parse parameters if provided
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &config); err != nil {
			return nil, fmt.Errorf("failed to parse parameters for '%s': %w", PdSLOOptimizerType, err)
		}
	}

	// Validate configuration
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration for '%s': %w", PdSLOOptimizerType, err)
	}

	// Initialize predictor set
	predictorSet, err := predictors.NewPDPredictorSet(ctrl.Log.WithName("pd-predictors"))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize PD predictor set: %w", err)
	}

	// Start predictors
	if err := predictorSet.Start(handle.Context()); err != nil {
		return nil, fmt.Errorf("failed to start PD predictor set: %w", err)
	}

	// Stop predictors on context cancellation
	go func() {
		<-handle.Context().Done()
		predictorSet.Stop()
	}()

	return NewPdSLOOptimizer(config, predictorSet).WithName(name), nil
}

// NewPdSLOOptimizer creates a new PdSLOOptimizer instance
func NewPdSLOOptimizer(config pdSLOOptimizerConfig, predictors *predictors.PDPredictorSet) *PdSLOOptimizer {
	return &PdSLOOptimizer{
		typedName:   plugins.TypedName{Type: PdSLOOptimizerType},
		config:      config,
		predictors:  predictors,
		initialized: true,
	}
}

// TypedName returns the plugin's typed name
func (s *PdSLOOptimizer) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the plugin instance name
func (s *PdSLOOptimizer) WithName(name string) *PdSLOOptimizer {
	s.typedName.Name = name
	return s
}

// Score evaluates pods independently using their respective predictors.
// Prefill pods are scored based on TTFT predictions, decode pods on TTFT+TPOT predictions.
func (s *PdSLOOptimizer) Score(
	ctx context.Context,
	cycleState *schedulingtypes.CycleState,
	request *schedulingtypes.LLMRequest,
	pods []schedulingtypes.Pod,
) map[schedulingtypes.Pod]float64 {
	logger := log.FromContext(ctx)

	if !s.initialized {
		logger.V(logutil.DEBUG).Info("PdSLOOptimizer not initialized, returning nil scores")
		return nil
	}

	// Parse SLO headers
	ttftSLO, tpotSLO, err := parseSLOHeaders(request)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to parse SLO headers")
		return nil
	}

	if ttftSLO == 0 && tpotSLO == 0 {
		// No SLOs specified, let other scorers handle it
		logger.V(logutil.DEBUG).Info("No SLO headers found, skipping PD-SLO optimization")
		return nil
	}

	logger.V(logutil.DEBUG).Info("PD-SLO optimization triggered",
		"ttftSLO", ttftSLO,
		"tpotSLO", tpotSLO,
		"totalPods", len(pods))

	// Separate pods by role
	prefillPods, decodePods := filterPodsByRole(pods)

	// Independent scoring mode: score each pod type independently using its predictor
	if len(prefillPods) == 0 && len(decodePods) > 0 {
		logger.V(logutil.DEBUG).Info("Decode-only scoring mode",
			"decodePods", len(decodePods))
		return s.scoreDecodePods(ctx, request, decodePods, ttftSLO, tpotSLO)
	}

	if len(decodePods) == 0 && len(prefillPods) > 0 {
		logger.V(logutil.DEBUG).Info("Prefill-only scoring mode",
			"prefillPods", len(prefillPods))
		return s.scorePrefillPods(ctx, request, prefillPods, ttftSLO)
	}

	// Joint optimization not implemented - return nil to let other scorers handle it
	logger.V(logutil.DEBUG).Info("Both prefill and decode pods present - joint optimization not implemented, skipping",
		"prefillPods", len(prefillPods),
		"decodePods", len(decodePods))
	return nil
}

// scoreDecodePods scores decode pods independently using decode predictor
func (s *PdSLOOptimizer) scoreDecodePods(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	decodePods []schedulingtypes.Pod,
	ttftSLO, tpotSLO float64,
) map[schedulingtypes.Pod]float64 {
	logger := log.FromContext(ctx)

	if s.predictors == nil || s.predictors.DecodePredictor == nil {
		logger.V(logutil.DEBUG).Info("No decode predictor available")
		return nil
	}

	scores := make(map[schedulingtypes.Pod]float64)
	bufferedTTFTSLO := ttftSLO * s.config.SLOBufferFactor
	bufferedTPOTSLO := tpotSLO * s.config.SLOBufferFactor

	bestScore := -1e9
	var bestPod schedulingtypes.Pod

	for _, pod := range decodePods {
		metrics := pod.GetMetrics()
		if metrics == nil {
			logger.V(logutil.DEBUG).Info("No metrics for decode pod", "pod", pod.GetPod().String())
			continue
		}

		// Predict decode TTFT and TPOT
		inputTokens := float64(getInputTokenLength(request))
		predictedTTFT, predictedTPOT, err := s.predictors.DecodePredictor.PredictLatency(
			metrics.KVCacheUsagePercent,
			inputTokens,
			float64(metrics.WaitingQueueSize),
			float64(metrics.RunningRequestsSize),
			0, // prefix cache score
		)

		if err != nil {
			logger.V(logutil.DEBUG).Error(err, "Failed to predict decode latency", "pod", pod.GetPod().String())
			continue
		}

		// Calculate headroom
		ttftHeadroom := bufferedTTFTSLO - predictedTTFT
		tpotHeadroom := bufferedTPOTSLO - predictedTPOT

		// Combined score: favor pods with positive headroom
		score := ttftHeadroom + tpotHeadroom

		logger.Info("Decode pod scored",
			"pod", pod.GetPod().String(),
			"predictedTTFT", predictedTTFT,
			"predictedTPOT", predictedTPOT,
			"ttftSLO", bufferedTTFTSLO,
			"tpotSLO", bufferedTPOTSLO,
			"ttftHeadroom", ttftHeadroom,
			"tpotHeadroom", tpotHeadroom,
			"score", score)

		if score > bestScore {
			bestScore = score
			bestPod = pod
		}
	}

	if bestPod != nil {
		for _, pod := range decodePods {
			if pod.GetPod().String() == bestPod.GetPod().String() {
				scores[pod] = 1.0
			} else {
				scores[pod] = 0.0
			}
		}
		logger.Info("Selected best decode pod", "pod", bestPod.GetPod().String(), "score", bestScore)
	}

	return scores
}

// scorePrefillPods scores prefill pods independently using prefill predictor
func (s *PdSLOOptimizer) scorePrefillPods(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	prefillPods []schedulingtypes.Pod,
	ttftSLO float64,
) map[schedulingtypes.Pod]float64 {
	logger := log.FromContext(ctx)

	if s.predictors == nil || s.predictors.PrefillPredictor == nil {
		logger.V(logutil.DEBUG).Info("No prefill predictor available")
		return nil
	}

	scores := make(map[schedulingtypes.Pod]float64)
	bufferedTTFTSLO := ttftSLO * s.config.SLOBufferFactor

	bestScore := -1e9
	var bestPod schedulingtypes.Pod

	for _, pod := range prefillPods {
		metrics := pod.GetMetrics()
		if metrics == nil {
			logger.V(logutil.DEBUG).Info("No metrics for prefill pod", "pod", pod.GetPod().String())
			continue
		}

		// Predict prefill TTFT (TPOT is always 0 for prefill)
		inputTokens := float64(getInputTokenLength(request))
		predictedTTFT, _, err := s.predictors.PrefillPredictor.PredictLatency(
			metrics.KVCacheUsagePercent,
			inputTokens,
			float64(metrics.WaitingQueueSize),
			float64(metrics.RunningRequestsSize),
			0, // prefix cache score
		)

		if err != nil {
			logger.V(logutil.DEBUG).Error(err, "Failed to predict prefill latency", "pod", pod.GetPod().String())
			continue
		}

		// Calculate headroom
		ttftHeadroom := bufferedTTFTSLO - predictedTTFT

		// Score is just the headroom
		score := ttftHeadroom

		logger.Info("Prefill pod scored",
			"pod", pod.GetPod().String(),
			"predictedTTFT", predictedTTFT,
			"ttftSLO", bufferedTTFTSLO,
			"ttftHeadroom", ttftHeadroom,
			"score", score)

		if score > bestScore {
			bestScore = score
			bestPod = pod
		}
	}

	if bestPod != nil {
		for _, pod := range prefillPods {
			if pod.GetPod().String() == bestPod.GetPod().String() {
				scores[pod] = 1.0
			} else {
				scores[pod] = 0.0
			}
		}
		logger.Info("Selected best prefill pod", "pod", bestPod.GetPod().String(), "score", bestScore)
	}

	return scores
}


// Compile-time assertions for requestcontrol interfaces
var _ requestcontrol.PreRequest = &PdSLOOptimizer{}
var _ requestcontrol.ResponseReceived = &PdSLOOptimizer{}
var _ requestcontrol.ResponseStreaming = &PdSLOOptimizer{}
var _ requestcontrol.ResponseComplete = &PdSLOOptimizer{}

// PreRequest is called after scheduling, before sending request to pod
func (s *PdSLOOptimizer) PreRequest(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	schedulingResult *schedulingtypes.SchedulingResult,
) {
	logger := log.FromContext(ctx)
	logger.Info("PdSLOOptimizer.PreRequest called")

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

	// Extract decode pod and metrics from scheduling result
	if schedulingResult != nil && len(schedulingResult.ProfileResults) > 0 {
		// Get decode pod from primary profile (should be "decode" profile)
		primaryProfile := schedulingResult.ProfileResults[schedulingResult.PrimaryProfileName]
		if primaryProfile != nil && len(primaryProfile.TargetPods) > 0 {
			decodePodInfo := primaryProfile.TargetPods[0].GetPod()
			telCtx.decodePod = decodePodInfo.NamespacedName.String()
			// Store decode metrics
			if metrics := primaryProfile.TargetPods[0].GetMetrics(); metrics != nil {
				telCtx.decodeMetrics = metrics.Clone()
			}
		}

		// Get prefill metrics from prefill profile
		for profileName, profileResult := range schedulingResult.ProfileResults {
			if profileResult != nil && len(profileResult.TargetPods) > 0 {
				// Check if this is the prefill profile (not the primary/decode profile)
				if profileName != schedulingResult.PrimaryProfileName {
					if metrics := profileResult.TargetPods[0].GetMetrics(); metrics != nil {
						telCtx.prefillMetrics = metrics.Clone()
					}
					break
				}
			}
		}
	}

	logger.V(logutil.DEBUG).Info("PD telemetry initialized",
		"requestID", requestID,
		"prefillPod", telCtx.prefillPod,
		"decodePod", telCtx.decodePod,
		"hasPrefillMetrics", telCtx.prefillMetrics != nil,
		"hasDecodeMetrics", telCtx.decodeMetrics != nil)
}

// ResponseReceived is called when response headers are received
func (s *PdSLOOptimizer) ResponseReceived(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	response *requestcontrol.Response,
	targetPod *backend.Pod,
) {
	logger := log.FromContext(ctx)
	requestID := request.Headers[requtil.RequestIdHeaderKey]
	if requestID == "" {
		return
	}

	telCtx := getTelemetryContext(requestID)

	podAddr := net.JoinHostPort(targetPod.Address, targetPod.Port)
	isPrefill := isPrefillPod(targetPod, telCtx.prefillPod)

	// Track which phase this response is from
	if isPrefill {
		telCtx.prefillStart = time.Now()
		logger.Info("Prefill response received",
			"requestID", requestID,
			"pod", targetPod.NamespacedName.String(),
			"podAddr", podAddr,
			"prefillPod", telCtx.prefillPod)
	} else {
		telCtx.decodeStart = time.Now()

		// Check if decode response includes prefill timing from sidecar
		if prefillTTFTHeader, ok := response.Headers["x-prefill-ttft-ms"]; ok && prefillTTFTHeader != "" {
			if prefillTTFT, err := strconv.ParseFloat(prefillTTFTHeader, 64); err == nil {
				// Send prefill telemetry to training server
				s.recordPrefillTTFT(ctx, request, telCtx, prefillTTFT)
				logger.Info("Received prefill TTFT from sidecar",
					"requestID", requestID,
					"prefillTTFT_ms", prefillTTFT)
			}
		}

		logger.Info("Decode response received",
			"requestID", requestID,
			"pod", targetPod.NamespacedName.String(),
			"podAddr", podAddr)
	}
}

// ResponseStreaming is called for each token in the streaming response
func (s *PdSLOOptimizer) ResponseStreaming(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
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

	// Only track tokens from decode pod (final output)
	if isPrefillPod(targetPod, telCtx.prefillPod) {
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
		s.recordDecodeTTFT(ctx, request, telCtx, float64(decodeTTFT))

		logger.V(logutil.DEBUG).Info("First token from decode",
			"requestID", requestID,
			"decodeTTFT_ms", decodeTTFT)
	} else {
		// Subsequent tokens - track for TPOT
		telCtx.tokenCount++
		interTokenLatency := now.Sub(telCtx.lastToken).Milliseconds()

		// Send TPOT telemetry to decode training server
		s.recordDecodeTPOT(ctx, request, telCtx, float64(interTokenLatency), telCtx.tokenCount)

		logger.V(logutil.TRACE).Info("Token from decode",
			"requestID", requestID,
			"tokenCount", telCtx.tokenCount,
			"interTokenLatency_ms", interTokenLatency)
	}

	telCtx.lastToken = now
}

// ResponseComplete is called when the response is fully sent
func (s *PdSLOOptimizer) ResponseComplete(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	response *requestcontrol.Response,
	targetPod *backend.Pod,
) {
	logger := log.FromContext(ctx)
	requestID := request.Headers[requtil.RequestIdHeaderKey]
	if requestID == "" {
		logger.Info("ResponseComplete: no request ID")
		return
	}

	telCtx := getTelemetryContext(requestID)

	podAddr := net.JoinHostPort(targetPod.Address, targetPod.Port)
	logger.Info("ResponseComplete called",
		"requestID", requestID,
		"targetPod", targetPod.NamespacedName.String(),
		"podAddr", podAddr,
		"prefillPod", telCtx.prefillPod,
		"isPrefill", isPrefillPod(targetPod, telCtx.prefillPod))

	// If this is prefill completion, record prefill TTFT
	if isPrefillPod(targetPod, telCtx.prefillPod) {
		logger.Info("Processing prefill completion", "requestID", requestID)
		if !telCtx.prefillStart.IsZero() {
			prefillTTFT := time.Since(telCtx.prefillStart).Milliseconds()
			s.recordPrefillTTFT(ctx, request, telCtx, float64(prefillTTFT))

			logger.Info("Prefill complete",
				"requestID", requestID,
				"prefillTTFT_ms", prefillTTFT)
		} else {
			logger.Info("Prefill completion but prefillStart is zero", "requestID", requestID)
		}
	} else {
		// Decode completion - cleanup
		logger.Info("Decode complete",
			"requestID", requestID,
			"totalTokens", telCtx.tokenCount)
		deleteTelemetryContext(requestID)
	}
}

// recordPrefillTTFT sends prefill TTFT telemetry to prefill training server
func (s *PdSLOOptimizer) recordPrefillTTFT(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	telCtx *pdTelemetryContext,
	ttftMs float64,
) {
	logger := log.FromContext(ctx)

	// Get predictors
	if s.predictors == nil || s.predictors.PrefillPredictor == nil {
		logger.V(logutil.DEBUG).Info("No prefill predictor available for telemetry")
		return
	}

	// Build training entry using cached metrics
	metrics := telCtx.prefillMetrics
	if metrics == nil {
		logger.V(logutil.DEBUG).Info("No metrics available for prefill telemetry")
		return
	}

	entry := latencypredictor.TrainingEntry{
		KVCachePercentage:  metrics.KVCacheUsagePercent,
		InputTokenLength:   getInputTokenLength(request),
		ActualTTFT:         ttftMs,
		ActualTPOT:         0, // Prefill doesn't produce output tokens
		Timestamp:          time.Now(),
		NumRequestWaiting:  metrics.WaitingQueueSize,
		NumRequestRunning:  metrics.RunningRequestsSize,
		NumTokensGenerated: 0,
		PrefixCacheScore:   0, // TODO: Get from cycle state if available
	}

	if err := s.predictors.PrefillPredictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to send prefill TTFT telemetry")
	} else {
		logger.V(logutil.DEBUG).Info("Sent prefill TTFT telemetry", "ttft_ms", ttftMs)
	}
}

// recordDecodeTTFT sends decode TTFT telemetry to decode training server
func (s *PdSLOOptimizer) recordDecodeTTFT(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	telCtx *pdTelemetryContext,
	ttftMs float64,
) {
	logger := log.FromContext(ctx)

	if s.predictors == nil || s.predictors.DecodePredictor == nil {
		logger.V(logutil.DEBUG).Info("No decode predictor available for telemetry")
		return
	}

	metrics := telCtx.decodeMetrics
	if metrics == nil {
		logger.V(logutil.DEBUG).Info("No metrics available for decode TTFT telemetry")
		return
	}

	entry := latencypredictor.TrainingEntry{
		KVCachePercentage:  metrics.KVCacheUsagePercent,
		InputTokenLength:   getInputTokenLength(request),
		ActualTTFT:         ttftMs,
		ActualTPOT:         0,
		Timestamp:          time.Now(),
		NumRequestWaiting:  metrics.WaitingQueueSize,
		NumRequestRunning:  metrics.RunningRequestsSize,
		NumTokensGenerated: 0,
		PrefixCacheScore:   0,
	}

	if err := s.predictors.DecodePredictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to send decode TTFT telemetry")
	} else {
		logger.V(logutil.DEBUG).Info("Sent decode TTFT telemetry", "ttft_ms", ttftMs)
	}
}

// recordDecodeTPOT sends decode TPOT telemetry to decode training server
func (s *PdSLOOptimizer) recordDecodeTPOT(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	telCtx *pdTelemetryContext,
	tpotMs float64,
	tokenCount int,
) {
	logger := log.FromContext(ctx)

	if s.predictors == nil || s.predictors.DecodePredictor == nil {
		return
	}

	metrics := telCtx.decodeMetrics
	if metrics == nil {
		return
	}

	entry := latencypredictor.TrainingEntry{
		KVCachePercentage:  metrics.KVCacheUsagePercent,
		InputTokenLength:   getInputTokenLength(request),
		ActualTTFT:         0,
		ActualTPOT:         tpotMs,
		Timestamp:          time.Now(),
		NumRequestWaiting:  metrics.WaitingQueueSize,
		NumRequestRunning:  metrics.RunningRequestsSize,
		NumTokensGenerated: tokenCount - 1, // Previous token count
		PrefixCacheScore:   0,
	}

	if err := s.predictors.DecodePredictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.TRACE).Error(err, "Failed to send decode TPOT telemetry")
	}
}
