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
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
	latencypredictor "sigs.k8s.io/gateway-api-inference-extension/sidecars/latencypredictorasync"

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

// predictPairLatencies calls the three predictors to get latency predictions for a pod pair
func (s *PdSLOPairOptimizer) predictPairLatencies(
	ctx context.Context,
	prefillPod, decodePod schedulingtypes.Pod,
	request *schedulingtypes.LLMRequest,
) (prefillTTFT, decodeTTFT, decodeTPOT float64) {
	logger := log.FromContext(ctx)

	// Build prediction request (common features)
	predReq := s.buildPredictionRequest(prefillPod, decodePod, request)

	// Predict prefill TTFT
	if resp, err := s.predictors.PrefillTTFTPredictor.Predict(ctx, predReq); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Prefill TTFT prediction failed, using fallback")
		prefillTTFT = s.fallbackPrefillTTFT(request)
	} else if resp != nil {
		prefillTTFT = resp.TTFT
	} else {
		prefillTTFT = s.fallbackPrefillTTFT(request)
	}

	// Predict decode TTFT (queue wait + startup)
	if resp, err := s.predictors.DecodeTTFTPredictor.Predict(ctx, predReq); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Decode TTFT prediction failed, using fallback")
		decodeTTFT = s.fallbackDecodeTTFT(decodePod)
	} else if resp != nil {
		decodeTTFT = resp.TTFT
	} else {
		decodeTTFT = s.fallbackDecodeTTFT(decodePod)
	}

	// Predict decode TPOT
	if resp, err := s.predictors.DecodeTPOTPredictor.Predict(ctx, predReq); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Decode TPOT prediction failed, using fallback")
		decodeTPOT = s.fallbackDecodeTPOT(decodePod)
	} else if resp != nil {
		decodeTPOT = resp.TPOT
	} else {
		decodeTPOT = s.fallbackDecodeTPOT(decodePod)
	}

	return prefillTTFT, decodeTTFT, decodeTPOT
}

// buildPredictionRequest constructs a prediction request from pod metrics and request info
func (s *PdSLOPairOptimizer) buildPredictionRequest(
	prefillPod, decodePod schedulingtypes.Pod,
	request *schedulingtypes.LLMRequest,
) latencypredictor.PredictionRequest {
	// Extract metrics from decode pod (used for all predictions)
	decodeMetrics := decodePod.GetMetrics()
	kvCachePercentage := 0.0
	numRequestWaiting := 0
	numRequestRunning := 0

	if decodeMetrics != nil {
		kvCachePercentage = decodeMetrics.KVCacheUsagePercent
		numRequestWaiting = decodeMetrics.WaitingQueueSize
		numRequestRunning = decodeMetrics.RunningRequestsSize
	}

	// Get input token length from request
	inputTokenLength := getInputTokenLength(request)

	return latencypredictor.PredictionRequest{
		KVCachePercentage: kvCachePercentage,
		InputTokenLength:  inputTokenLength,
		NumRequestWaiting: numRequestWaiting,
		NumRequestRunning: numRequestRunning,
		PrefixCacheScore:  0.0, // TODO: Get from cycle state
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
