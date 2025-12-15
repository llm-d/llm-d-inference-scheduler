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
	"errors"
	"fmt"
	"math/rand"
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
	// PdSLOPairOptimizerType is the plugin type identifier
	PdSLOPairOptimizerType = "pd-slo-pair-optimizer"
)

// Compile-time type assertion
var _ framework.Scorer = &PdSLOPairOptimizer{}

// pdSLOPairOptimizerConfig holds configuration parameters for the pair optimizer
type pdSLOPairOptimizerConfig struct {
	TTFTWeight           float64 `json:"ttftWeight"`           // Weight for TTFT headroom (default: 0.8)
	TPOTWeight           float64 `json:"tpotWeight"`           // Weight for TPOT headroom (default: 0.2)
	TransferOverheadMs   float64 `json:"transferOverheadMs"`   // KV transfer overhead in ms (default: 5.0)
	SLOBufferFactor      float64 `json:"sloBufferFactor"`      // Safety margin for SLOs (default: 0.9)
	SelectionStrategy    string  `json:"selectionStrategy"`    // "weighted_random" or "best_headroom"
	NegativeHeadroomProb float64 `json:"negativeHeadroomProb"` // Exploration probability (default: 0.01)
}

// Default configuration values
var defaultPdSLOConfig = pdSLOPairOptimizerConfig{
	TTFTWeight:           0.8,
	TPOTWeight:           0.2,
	TransferOverheadMs:   5.0,
	SLOBufferFactor:      0.9,
	SelectionStrategy:    "weighted_random",
	NegativeHeadroomProb: 0.01,
}

// PdSLOPairOptimizer implements joint (prefill, decode) pod pair optimization
// based on SLO headroom calculations
type PdSLOPairOptimizer struct {
	typedName   plugins.TypedName
	config      pdSLOPairOptimizerConfig
	predictors  *predictors.PDPredictorSet
	initialized bool
}

// PdSLOPairOptimizerFactory creates a new PdSLOPairOptimizer instance
func PdSLOPairOptimizerFactory(name string, rawParameters json.RawMessage, handle plugins.Handle) (plugins.Plugin, error) {
	config := defaultPdSLOConfig

	// Parse parameters if provided
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &config); err != nil {
			return nil, fmt.Errorf("failed to parse parameters for '%s': %w", PdSLOPairOptimizerType, err)
		}
	}

	// Validate configuration
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration for '%s': %w", PdSLOPairOptimizerType, err)
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

	return NewPdSLOPairOptimizer(config, predictorSet).WithName(name), nil
}

// NewPdSLOPairOptimizer creates a new PdSLOPairOptimizer instance
func NewPdSLOPairOptimizer(config pdSLOPairOptimizerConfig, predictors *predictors.PDPredictorSet) *PdSLOPairOptimizer {
	return &PdSLOPairOptimizer{
		typedName:   plugins.TypedName{Type: PdSLOPairOptimizerType},
		config:      config,
		predictors:  predictors,
		initialized: true,
	}
}

// TypedName returns the plugin's typed name
func (s *PdSLOPairOptimizer) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the plugin instance name
func (s *PdSLOPairOptimizer) WithName(name string) *PdSLOPairOptimizer {
	s.typedName.Name = name
	return s
}

// Score evaluates all (prefill, decode) pod pairs and selects the optimal one
// based on joint TTFT and TPOT predictions
func (s *PdSLOPairOptimizer) Score(
	ctx context.Context,
	cycleState *schedulingtypes.CycleState,
	request *schedulingtypes.LLMRequest,
	pods []schedulingtypes.Pod,
) map[schedulingtypes.Pod]float64 {
	logger := log.FromContext(ctx)

	if !s.initialized {
		logger.V(logutil.DEBUG).Info("PdSLOPairOptimizer not initialized, returning nil scores")
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

	if len(prefillPods) == 0 || len(decodePods) == 0 {
		logger.V(logutil.DEBUG).Info("Insufficient pods for PD optimization",
			"prefillPods", len(prefillPods),
			"decodePods", len(decodePods))
		return nil
	}

	logger.V(logutil.DEBUG).Info("Evaluating PD pod pairs",
		"prefillCandidates", len(prefillPods),
		"decodeCandidates", len(decodePods),
		"totalPairs", len(prefillPods)*len(decodePods))

	// Evaluate all (prefill, decode) pairs
	pairResults := s.evaluateAllPairs(ctx, request, prefillPods, decodePods, ttftSLO, tpotSLO)

	if len(pairResults) == 0 {
		logger.Error(errors.New("no valid pairs"), "Failed to evaluate any pod pairs")
		return nil
	}

	// Classify pairs by headroom
	positivePairs, negativePairs := classifyPairsByHeadroom(pairResults)

	logger.Info("Pair headroom distribution",
		"positiveHeadroomPairs", len(positivePairs),
		"negativeHeadroomPairs", len(negativePairs))

	// Select optimal pair
	selectedPair := s.selectOptimalPair(positivePairs, negativePairs)

	if selectedPair == nil {
		logger.Error(errors.New("no valid pairs"), "Failed to select optimal pair")
		return nil
	}

	// Store selected pair in cycle state for profile handler
	storeSelectedPairInState(cycleState, selectedPair)

	logger.Info("Selected optimal PD pair",
		"prefillPod", selectedPair.prefillPod.GetPod().String(),
		"decodePod", selectedPair.decodePod.GetPod().String(),
		"prefillTTFT", selectedPair.prefillTTFT,
		"decodeTTFT", selectedPair.decodeTTFT,
		"decodeTPOT", selectedPair.decodeTPOT,
		"jointTTFT", selectedPair.jointTTFT,
		"ttftHeadroom", selectedPair.ttftHeadroom,
		"tpotHeadroom", selectedPair.tpotHeadroom,
		"blendedHeadroom", selectedPair.blendedHeadroom,
		"ttftValid", selectedPair.ttftValid,
		"tpotValid", selectedPair.tpotValid)

	// Return scores: selected pods get 1.0, all others get 0.0
	scores := make(map[schedulingtypes.Pod]float64)
	for _, pod := range pods {
		if pod.GetPod().String() == selectedPair.prefillPod.GetPod().String() ||
			pod.GetPod().String() == selectedPair.decodePod.GetPod().String() {
			scores[pod] = 1.0
		} else {
			scores[pod] = 0.0
		}
	}

	return scores
}

// evaluateAllPairs evaluates all (prefill, decode) combinations and returns scored results
func (s *PdSLOPairOptimizer) evaluateAllPairs(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	prefillPods, decodePods []schedulingtypes.Pod,
	ttftSLO, tpotSLO float64,
) []pairResult {
	logger := log.FromContext(ctx)
	results := make([]pairResult, 0, len(prefillPods)*len(decodePods))

	// Apply buffer factor to SLOs
	bufferedTTFTSLO := ttftSLO * s.config.SLOBufferFactor
	bufferedTPOTSLO := tpotSLO * s.config.SLOBufferFactor

	for _, prefillPod := range prefillPods {
		for _, decodePod := range decodePods {
			// Get predictions for this pair
			prefillTTFT, decodeTTFT, decodeTPOT := s.predictPairLatencies(ctx, prefillPod, decodePod, request)

			// Calculate joint TTFT
			jointTTFT := prefillTTFT + decodeTTFT + s.config.TransferOverheadMs

			// Calculate headrooms
			ttftHeadroom := bufferedTTFTSLO - jointTTFT
			tpotHeadroom := bufferedTPOTSLO - decodeTPOT

			// Validate against SLOs
			ttftValid := ttftHeadroom > 0
			tpotValid := tpotHeadroom > 0

			// Calculate blended headroom
			blendedHeadroom := s.config.TTFTWeight*ttftHeadroom + s.config.TPOTWeight*tpotHeadroom

			result := pairResult{
				prefillPod:      prefillPod,
				decodePod:       decodePod,
				prefillTTFT:     prefillTTFT,
				decodeTTFT:      decodeTTFT,
				decodeTPOT:      decodeTPOT,
				jointTTFT:       jointTTFT,
				ttftValid:       ttftValid,
				tpotValid:       tpotValid,
				ttftHeadroom:    ttftHeadroom,
				tpotHeadroom:    tpotHeadroom,
				blendedHeadroom: blendedHeadroom,
			}

			results = append(results, result)

			logger.V(logutil.DEBUG).Info("Evaluated PD pair",
				"prefillPod", prefillPod.GetPod().String(),
				"decodePod", decodePod.GetPod().String(),
				"prefillTTFT_ms", prefillTTFT,
				"decodeTTFT_ms", decodeTTFT,
				"decodeTPOT_ms", decodeTPOT,
				"jointTTFT_ms", jointTTFT,
				"ttftHeadroom_ms", ttftHeadroom,
				"tpotHeadroom_ms", tpotHeadroom,
				"blendedHeadroom", blendedHeadroom,
				"valid", ttftValid && tpotValid)
		}
	}

	return results
}

// predictPairLatencies calls the two predictors to get latency predictions for a pod pair
func (s *PdSLOPairOptimizer) predictPairLatencies(
	ctx context.Context,
	prefillPod, decodePod schedulingtypes.Pod,
	request *schedulingtypes.LLMRequest,
) (prefillTTFT, decodeTTFT, decodeTPOT float64) {
	logger := log.FromContext(ctx)

	// Build prediction request for prefill (input_tokens, prefix_cache focused)
	prefillReq := s.buildPrefillPredictionRequest(prefillPod, request)

	// Predict prefill TTFT
	if resp, err := s.predictors.PrefillPredictor.Predict(ctx, prefillReq); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Prefill prediction failed, using fallback")
		prefillTTFT = s.fallbackPrefillTTFT(request)
	} else if resp != nil {
		prefillTTFT = resp.TTFT
	} else {
		prefillTTFT = s.fallbackPrefillTTFT(request)
	}

	// Build prediction request for decode (queue, running requests, kv_cache focused)
	decodeReq := s.buildDecodePredictionRequest(decodePod, request)

	// Predict decode TTFT and TPOT (single call returns both)
	if resp, err := s.predictors.DecodePredictor.Predict(ctx, decodeReq); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Decode prediction failed, using fallback")
		decodeTTFT = s.fallbackDecodeTTFT(decodePod)
		decodeTPOT = s.fallbackDecodeTPOT(decodePod)
	} else if resp != nil {
		decodeTTFT = resp.TTFT
		decodeTPOT = resp.TPOT
	} else {
		decodeTTFT = s.fallbackDecodeTTFT(decodePod)
		decodeTPOT = s.fallbackDecodeTPOT(decodePod)
	}

	return prefillTTFT, decodeTTFT, decodeTPOT
}

// buildPrefillPredictionRequest constructs a prediction request for prefill pod
// Focuses on: input tokens, prefix cache score
func (s *PdSLOPairOptimizer) buildPrefillPredictionRequest(
	prefillPod schedulingtypes.Pod,
	request *schedulingtypes.LLMRequest,
) latencypredictor.PredictionRequest {
	// Get input token length from request (dominant feature for prefill)
	inputTokenLength := getInputTokenLength(request)

	// Get prefill pod metrics (if available)
	metrics := prefillPod.GetMetrics()
	kvCachePercentage := 0.0
	numRequestWaiting := 0
	numRequestRunning := 0

	if metrics != nil {
		kvCachePercentage = metrics.KVCacheUsagePercent
		numRequestWaiting = metrics.WaitingQueueSize
		numRequestRunning = metrics.RunningRequestsSize
	}

	return latencypredictor.PredictionRequest{
		KVCachePercentage: kvCachePercentage,
		InputTokenLength:  inputTokenLength,
		NumRequestWaiting: numRequestWaiting,
		NumRequestRunning: numRequestRunning,
		PrefixCacheScore:  0.0, // TODO: Get from cycle state
	}
}

// buildDecodePredictionRequest constructs a prediction request for decode pod
// Focuses on: queue depth, running requests, kv cache usage
func (s *PdSLOPairOptimizer) buildDecodePredictionRequest(
	decodePod schedulingtypes.Pod,
	request *schedulingtypes.LLMRequest,
) latencypredictor.PredictionRequest {
	// Extract metrics from decode pod (dominant features for decode)
	decodeMetrics := decodePod.GetMetrics()
	kvCachePercentage := 0.0
	numRequestWaiting := 0
	numRequestRunning := 0

	if decodeMetrics != nil {
		kvCachePercentage = decodeMetrics.KVCacheUsagePercent
		numRequestWaiting = decodeMetrics.WaitingQueueSize
		numRequestRunning = decodeMetrics.RunningRequestsSize
	}

	// Get input token length (less important for decode, but included)
	inputTokenLength := getInputTokenLength(request)

	return latencypredictor.PredictionRequest{
		KVCachePercentage: kvCachePercentage,
		InputTokenLength:  inputTokenLength,
		NumRequestWaiting: numRequestWaiting,
		NumRequestRunning: numRequestRunning,
		PrefixCacheScore:  0.0,
	}
}

// selectOptimalPair chooses the best pair using the configured selection strategy
func (s *PdSLOPairOptimizer) selectOptimalPair(positivePairs, negativePairs []pairResult) *pairResult {
	source := rand.NewSource(time.Now().UnixNano())
	r := rand.New(source)

	// Weighted random strategy: 99% positive headroom, 1% negative (exploration)
	if len(positivePairs) > 0 {
		if r.Float64() >= s.config.NegativeHeadroomProb {
			// Select from positive headroom pairs
			return selectPairWeightedRandom(positivePairs)
		}
	}

	// Fallback to negative headroom pairs or exploration
	if len(negativePairs) > 0 {
		return selectPairWeightedRandom(negativePairs)
	}

	// No valid pairs at all
	return nil
}

// Fallback heuristics when predictions fail

func (s *PdSLOPairOptimizer) fallbackPrefillTTFT(request *schedulingtypes.LLMRequest) float64 {
	// Simple heuristic: ~0.5ms per token
	inputTokens := float64(getInputTokenLength(request))
	return inputTokens * 0.5
}

func (s *PdSLOPairOptimizer) fallbackDecodeTTFT(decodePod schedulingtypes.Pod) float64 {
	// Simple heuristic: ~10ms per queued request
	metrics := decodePod.GetMetrics()
	if metrics != nil {
		return float64(metrics.WaitingQueueSize) * 10.0
	}
	return 10.0 // Default minimal wait
}

func (s *PdSLOPairOptimizer) fallbackDecodeTPOT(decodePod schedulingtypes.Pod) float64 {
	// Simple heuristic: ~20ms base + congestion penalty
	baseTpot := 20.0
	congestion := 0.0

	metrics := decodePod.GetMetrics()
	if metrics != nil {
		congestion = float64(metrics.RunningRequestsSize) * 5.0
		congestion += metrics.KVCacheUsagePercent * 10.0
	}

	return baseTpot + congestion
}

// Helper functions

func (c *pdSLOPairOptimizerConfig) validate() error {
	var errs []error

	if c.TTFTWeight < 0 || c.TPOTWeight < 0 {
		errs = append(errs, errors.New("weights must be non-negative"))
	}

	if c.TransferOverheadMs < 0 {
		errs = append(errs, errors.New("transferOverheadMs must be non-negative"))
	}

	if c.SLOBufferFactor <= 0 || c.SLOBufferFactor > 1 {
		errs = append(errs, errors.New("sloBufferFactor must be in (0, 1]"))
	}

	if c.NegativeHeadroomProb < 0 || c.NegativeHeadroomProb > 1 {
		errs = append(errs, errors.New("negativeHeadroomProb must be in [0, 1]"))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func getInputTokenLength(request *schedulingtypes.LLMRequest) int {
	// Try to get from request body
	if request.Body.Completions != nil && request.Body.Completions.Prompt != "" {
		// Rough estimate: 4 characters per token
		return len(request.Body.Completions.Prompt) / 4
	}

	if request.Body.ChatCompletions != nil && len(request.Body.ChatCompletions.Messages) > 0 {
		totalChars := 0
		for _, msg := range request.Body.ChatCompletions.Messages {
			totalChars += len(msg.Content.Raw)
		}
		return totalChars / 4
	}

	return 100 // Default fallback
}

// ============================================================================
// RequestControl Hook Implementations for Telemetry Collection
// ============================================================================

// Compile-time assertions for requestcontrol interfaces
var _ requestcontrol.PreRequest = &PdSLOPairOptimizer{}
var _ requestcontrol.ResponseReceived = &PdSLOPairOptimizer{}
var _ requestcontrol.ResponseStreaming = &PdSLOPairOptimizer{}
var _ requestcontrol.ResponseComplete = &PdSLOPairOptimizer{}

// PreRequest is called after scheduling, before sending request to pod
func (s *PdSLOPairOptimizer) PreRequest(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	schedulingResult *schedulingtypes.SchedulingResult,
) {
	logger := log.FromContext(ctx)
	logger.Info("PdSLOPairOptimizer.PreRequest called")

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
func (s *PdSLOPairOptimizer) ResponseReceived(
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
func (s *PdSLOPairOptimizer) ResponseStreaming(
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
func (s *PdSLOPairOptimizer) ResponseComplete(
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

	// If this is prefill completion, record prefill TTFT
	if isPrefillPod(targetPod, telCtx.prefillPod) {
		if !telCtx.prefillStart.IsZero() {
			prefillTTFT := time.Since(telCtx.prefillStart).Milliseconds()
			s.recordPrefillTTFT(ctx, request, telCtx, float64(prefillTTFT))

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
func (s *PdSLOPairOptimizer) recordPrefillTTFT(
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
func (s *PdSLOPairOptimizer) recordDecodeTTFT(
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
func (s *PdSLOPairOptimizer) recordDecodeTPOT(
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
