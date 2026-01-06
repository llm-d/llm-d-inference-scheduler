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
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
	latencypredictor "sigs.k8s.io/gateway-api-inference-extension/sidecars/latencypredictorasync"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	schedulermetrics "github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
)

const (
	// PdSLOOptimizerType is the plugin type identifier
	PdSLOOptimizerType = "pd-slo-optimizer"
)

// Compile-time type assertion
var _ framework.Scorer = &PdSLOOptimizer{}

// pdSLOOptimizerConfig holds configuration parameters for the optimizer
type pdSLOOptimizerConfig struct {
	SLOBufferFactor       float64 `json:"sloBufferFactor"`       // Safety margin for SLOs (default: 0.9)
	Epsilon               float64 `json:"epsilon"`               // Exploration rate for epsilon-greedy (default: 0.1)
	NegHeadroomTTFTWeight float64 `json:"negHeadroomTTFTWeight"` // Weight for TTFT deficit in negative headroom (default: 0.8)
	NegHeadroomTPOTWeight float64 `json:"negHeadroomTPOTWeight"` // Weight for TPOT deficit in negative headroom (default: 0.2)
	HeadroomTTFTWeight    float64 `json:"headroomTTFTWeight"`    // Weight for TTFT in positive headroom (default: 0.8)
	HeadroomTPOTWeight    float64 `json:"headroomTPOTWeight"`    // Weight for TPOT in positive headroom (default: 0.2)
	EpsilonExploreNeg     float64 `json:"epsilonExploreNeg"`     // Exploration rate for negative headroom tier (default: 0.01)
	HeadroomStrategy      string  `json:"headroomStrategy"`      // Selection strategy: "least", "most", "weighted" (default: "weighted")
}

// Default configuration values
var defaultPdSLOConfig = pdSLOOptimizerConfig{
	SLOBufferFactor:       0.9,
	Epsilon:               0.1,  // 10% exploration for weighted random
	NegHeadroomTTFTWeight: 0.8,  // TTFT more important when violating SLOs
	NegHeadroomTPOTWeight: 0.2,  // TPOT less important in negative tier
	HeadroomTTFTWeight:    0.8,  // TTFT weight for positive headroom
	HeadroomTPOTWeight:    0.2,  // TPOT weight for positive headroom
	EpsilonExploreNeg:     0.01, // 1% exploration in negative tier
	HeadroomStrategy:      "weighted",
}

// validate checks if the configuration is valid
func (c *pdSLOOptimizerConfig) validate() error {
	if c.SLOBufferFactor <= 0 || c.SLOBufferFactor > 1.0 {
		return fmt.Errorf("sloBufferFactor must be between 0 and 1.0, got %f", c.SLOBufferFactor)
	}
	if c.Epsilon < 0 || c.Epsilon > 1.0 {
		return fmt.Errorf("epsilon must be between 0 and 1.0, got %f", c.Epsilon)
	}
	if c.NegHeadroomTTFTWeight < 0 || c.NegHeadroomTPOTWeight < 0 ||
		c.HeadroomTTFTWeight < 0 || c.HeadroomTPOTWeight < 0 {
		return fmt.Errorf("all headroom weights must be >= 0")
	}
	if c.EpsilonExploreNeg < 0 || c.EpsilonExploreNeg > 1.0 {
		return fmt.Errorf("epsilonExploreNeg must be between 0 and 1.0, got %f", c.EpsilonExploreNeg)
	}
	validStrategies := map[string]bool{"least": true, "most": true, "weighted": true}
	if c.HeadroomStrategy != "" && !validStrategies[c.HeadroomStrategy] {
		return fmt.Errorf("headroomStrategy must be one of: least, most, weighted; got %s", c.HeadroomStrategy)
	}
	return nil
}

// PdSLOOptimizer implements independent prefill and decode pod optimization
// based on SLO headroom calculations using trained latency predictors
type PdSLOOptimizer struct {
	typedName           plugins.TypedName
	config              pdSLOOptimizerConfig
	predictor           latencypredictor.PredictorInterface
	rand                *rand.Rand
	initialized         bool
	runningRequestLists map[string]*requestPriorityQueue // pod name -> running requests
	mu                  sync.RWMutex                      // protects runningRequestLists
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

	// Initialize single predictor (handles both prefill and decode via pod_type feature)
	predictorConfig := latencypredictor.ConfigFromEnv()
	predictor := latencypredictor.New(predictorConfig, ctrl.Log.WithName("pd-slo-predictor"))

	// Start predictor
	if err := predictor.Start(handle.Context()); err != nil {
		return nil, fmt.Errorf("failed to start predictor: %w", err)
	}

	// Stop predictor on context cancellation
	go func() {
		<-handle.Context().Done()
		predictor.Stop()
	}()

	return NewPdSLOOptimizer(config, predictor).WithName(name), nil
}

// NewPdSLOOptimizer creates a new PdSLOOptimizer instance
func NewPdSLOOptimizer(config pdSLOOptimizerConfig, predictor latencypredictor.PredictorInterface) *PdSLOOptimizer {
	source := rand.NewSource(time.Now().UnixNano())
	return &PdSLOOptimizer{
		typedName:           plugins.TypedName{Type: PdSLOOptimizerType},
		config:              config,
		predictor:           predictor,
		rand:                rand.New(source),
		initialized:         true,
		runningRequestLists: make(map[string]*requestPriorityQueue),
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
		// No SLOs specified - use queue-based scoring as fallback
		// This enables non-SLO/baseline requests to benefit from load balancing
		logger.V(logutil.DEBUG).Info("No SLO headers found, using queue-based fallback scoring")
		return s.scoreByQueueDepth(ctx, pods)
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

	if s.predictor == nil {
		logger.V(logutil.DEBUG).Info("No predictor available")
		return nil
	}

	scores := make(map[schedulingtypes.Pod]float64)
	bufferedTTFTSLO := ttftSLO * s.config.SLOBufferFactor

	// Track pods with headroom breakdown
	var candidatePods []podWithHeadroom

	for _, pod := range decodePods {
		metrics := pod.GetMetrics()
		if metrics == nil {
			logger.V(logutil.DEBUG).Info("No metrics for decode pod", "pod", pod.GetPod().String())
			continue
		}

		// Check per-pod min TPOT SLO from running requests
		podMinTPOTSLO := s.getPodMinTPOTSLO(pod)
		bufferedTPOTSLO := tpotSLO * s.config.SLOBufferFactor

		// Adjust buffered TPOT SLO to respect running requests
		if podMinTPOTSLO > 0 {
			if podMinTPOTSLO < tpotSLO {
				logger.V(logutil.DEBUG).Info("Pod min TPOT SLO is stricter than request SLO",
					"pod", pod.GetPod().String(),
					"podMinTPOTSLO", podMinTPOTSLO,
					"requestTPOTSLO", tpotSLO)
			}
			// Use the stricter (minimum) of the two
			if podMinTPOTSLO*s.config.SLOBufferFactor < bufferedTPOTSLO {
				bufferedTPOTSLO = podMinTPOTSLO * s.config.SLOBufferFactor
			}
		}

		// Predict decode TTFT and TPOT
		inputTokens := getInputTokenLength(request)
		predReq := latencypredictor.PredictionRequest{
			KVCachePercentage:  metrics.KVCacheUsagePercent,
			InputTokenLength:   inputTokens,
			NumRequestWaiting:  metrics.WaitingQueueSize,
			NumRequestRunning:  metrics.RunningRequestsSize,
			PrefixCacheScore:   0,
			PodType:            "decode", // Decode pod handles both TTFT and TPOT
		}

		predResp, err := s.predictor.Predict(ctx, predReq)
		if err != nil {
			schedulermetrics.RecordPDSLOPredictorCall(schedulermetrics.PredictorTypeDecode, schedulermetrics.PredictorStatusError)
			logger.V(logutil.DEBUG).Error(err, "Failed to predict decode latency", "pod", pod.GetPod().String())
			continue
		}
		schedulermetrics.RecordPDSLOPredictorCall(schedulermetrics.PredictorTypeDecode, schedulermetrics.PredictorStatusSuccess)

		predictedTTFT := predResp.TTFT
		predictedTPOT := predResp.TPOT

		// Calculate headroom
		ttftHeadroom := bufferedTTFTSLO - predictedTTFT
		tpotHeadroom := bufferedTPOTSLO - predictedTPOT

		// Record actual headroom values for observability
		schedulermetrics.RecordPDSLOHeadroom(schedulermetrics.PodTypeDecode, schedulermetrics.MetricTypeTTFT, ttftHeadroom)
		schedulermetrics.RecordPDSLOHeadroom(schedulermetrics.PodTypeDecode, schedulermetrics.MetricTypeTPOT, tpotHeadroom)

		logger.Info("Decode pod scored",
			"pod", pod.GetPod().String(),
			"predictedTTFT", predictedTTFT,
			"predictedTPOT", predictedTPOT,
			"ttftSLO", bufferedTTFTSLO,
			"tpotSLO", bufferedTPOTSLO,
			"ttftHeadroom", ttftHeadroom,
			"tpotHeadroom", tpotHeadroom)

		candidatePods = append(candidatePods, podWithHeadroom{
			pod:          pod,
			ttftHeadroom: ttftHeadroom,
			tpotHeadroom: tpotHeadroom,
			totalScore:   ttftHeadroom + tpotHeadroom,
		})
	}

	if len(candidatePods) == 0 {
		return nil
	}

	// Tiered selection: positive vs negative headroom
	selectedPod := s.selectPodTiered(ctx, candidatePods)
	if selectedPod == nil {
		return nil
	}

	// Record selection outcome metric
	outcome := schedulermetrics.HeadroomOutcomePositive
	if selectedPod.totalScore < 0 {
		outcome = schedulermetrics.HeadroomOutcomeNegative
	}
	schedulermetrics.RecordPDSLOPodSelection(schedulermetrics.PodTypeDecode, outcome)

	logger.Info("Selected decode pod",
		"pod", selectedPod.pod.GetPod().String(),
		"ttftHeadroom", selectedPod.ttftHeadroom,
		"tpotHeadroom", selectedPod.tpotHeadroom,
		"totalScore", selectedPod.totalScore)

	// Assign scores: 1.0 to selected pod, 0.0 to others
	for _, p := range candidatePods {
		if p.pod.GetPod().String() == selectedPod.pod.GetPod().String() {
			scores[p.pod] = 1.0
		} else {
			scores[p.pod] = 0.0
		}
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

	if s.predictor == nil {
		logger.V(logutil.DEBUG).Info("No predictor available")
		return nil
	}

	scores := make(map[schedulingtypes.Pod]float64)
	bufferedTTFTSLO := ttftSLO * s.config.SLOBufferFactor

	// Track pods with headroom breakdown
	var candidatePods []podWithHeadroom

	for _, pod := range prefillPods {
		metrics := pod.GetMetrics()
		if metrics == nil {
			logger.V(logutil.DEBUG).Info("No metrics for prefill pod", "pod", pod.GetPod().String())
			continue
		}

		// Predict prefill TTFT (TPOT is always 0 for prefill)
		inputTokens := getInputTokenLength(request)
		predReq := latencypredictor.PredictionRequest{
			KVCachePercentage: metrics.KVCacheUsagePercent,
			InputTokenLength:  inputTokens,
			NumRequestWaiting: metrics.WaitingQueueSize,
			NumRequestRunning: metrics.RunningRequestsSize,
			PrefixCacheScore:  0,
			PodType:           "prefill", // Prefill pod only handles TTFT
		}

		predResp, err := s.predictor.Predict(ctx, predReq)
		if err != nil {
			schedulermetrics.RecordPDSLOPredictorCall(schedulermetrics.PredictorTypePrefill, schedulermetrics.PredictorStatusError)
			logger.V(logutil.DEBUG).Error(err, "Failed to predict prefill latency", "pod", pod.GetPod().String())
			continue
		}
		schedulermetrics.RecordPDSLOPredictorCall(schedulermetrics.PredictorTypePrefill, schedulermetrics.PredictorStatusSuccess)

		predictedTTFT := predResp.TTFT

		// Calculate headroom
		ttftHeadroom := bufferedTTFTSLO - predictedTTFT

		// Record actual headroom value for observability
		schedulermetrics.RecordPDSLOHeadroom(schedulermetrics.PodTypePrefill, schedulermetrics.MetricTypeTTFT, ttftHeadroom)

		logger.Info("Prefill pod scored",
			"pod", pod.GetPod().String(),
			"predictedTTFT", predictedTTFT,
			"ttftSLO", bufferedTTFTSLO,
			"ttftHeadroom", ttftHeadroom)

		// For prefill, TPOT is N/A (prefill doesn't generate output tokens)
		// Set tpotHeadroom to infinity to indicate it's always satisfied
		candidatePods = append(candidatePods, podWithHeadroom{
			pod:          pod,
			ttftHeadroom: ttftHeadroom,
			tpotHeadroom: 1e9, // Infinity - TPOT not applicable for prefill
			totalScore:   ttftHeadroom,
		})
	}

	if len(candidatePods) == 0 {
		return nil
	}

	// Tiered selection: positive vs negative headroom (based on TTFT only for prefill)
	selectedPod := s.selectPodTiered(ctx, candidatePods)
	if selectedPod == nil {
		return nil
	}

	// Record selection outcome metric
	outcome := schedulermetrics.HeadroomOutcomePositive
	if selectedPod.ttftHeadroom < 0 {
		outcome = schedulermetrics.HeadroomOutcomeNegative
	}
	schedulermetrics.RecordPDSLOPodSelection(schedulermetrics.PodTypePrefill, outcome)

	logger.Info("Selected prefill pod",
		"pod", selectedPod.pod.GetPod().String(),
		"ttftHeadroom", selectedPod.ttftHeadroom,
		"totalScore", selectedPod.totalScore)

	// Assign scores: 1.0 to selected pod, 0.0 to others
	for _, p := range candidatePods {
		if p.pod.GetPod().String() == selectedPod.pod.GetPod().String() {
			scores[p.pod] = 1.0
		} else {
			scores[p.pod] = 0.0
		}
	}

	return scores
}

// scoreByQueueDepth provides fallback scoring for non-SLO requests based on queue depth.
// Uses the same min-max normalization as the standard queue-scorer.
// Scores pods inversely to their waiting queue size (fewer waiting requests = higher score).
func (s *PdSLOOptimizer) scoreByQueueDepth(
	ctx context.Context,
	pods []schedulingtypes.Pod,
) map[schedulingtypes.Pod]float64 {
	logger := log.FromContext(ctx)
	scores := make(map[schedulingtypes.Pod]float64)

	if len(pods) == 0 {
		return scores
	}

	// Find min and max queue sizes
	minQueueSize := int(^uint(0) >> 1) // MaxInt
	maxQueueSize := int(-1 << 63)      // MinInt

	for _, pod := range pods {
		metrics := pod.GetMetrics()
		if metrics == nil {
			continue
		}
		queueSize := metrics.WaitingQueueSize
		if queueSize < minQueueSize {
			minQueueSize = queueSize
		}
		if queueSize > maxQueueSize {
			maxQueueSize = queueSize
		}
	}

	// Score each pod using min-max normalization
	// Higher score for lower queue depth
	for _, pod := range pods {
		metrics := pod.GetMetrics()
		if metrics == nil {
			logger.V(logutil.DEBUG).Info("No metrics for pod (queue fallback)", "pod", pod.GetPod().String())
			continue
		}

		var score float64
		if maxQueueSize == minQueueSize {
			// All pods have same queue size - return neutral score
			score = 1.0
		} else {
			// Normalize: score ranges from 0.0 (max queue) to 1.0 (min queue)
			score = float64(maxQueueSize-metrics.WaitingQueueSize) / float64(maxQueueSize-minQueueSize)
		}

		scores[pod] = score

		logger.V(logutil.DEBUG).Info("Queue-based fallback score",
			"pod", pod.GetPod().String(),
			"waitingQueue", metrics.WaitingQueueSize,
			"score", score)
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

	// Handle nil request (defensive check)
	if request == nil {
		logger.V(4).Info("Skipping PreRequest: request is nil")
		return
	}

	requestID := request.Headers[requtil.RequestIdHeaderKey]
	if requestID == "" {
		logger.V(logutil.DEBUG).Info("No request ID")
		return
	}

	// Track running requests for per-pod min TPOT SLO checking
	ttftSLO, tpotSLO, err := parseSLOHeaders(request)
	if err == nil && (ttftSLO > 0 || tpotSLO > 0) {
		s.trackRunningRequest(ctx, requestID, schedulingResult, ttftSLO, tpotSLO)
	}

	// Only track telemetry if we have both prefill and decode pods (PD mode active)
	prefillAddr := request.Headers[common.PrefillPodHeader]
	if prefillAddr == "" {
		logger.V(logutil.DEBUG).Info("No prefill header, skipping PD telemetry")
		return
	}

	telCtx := getTelemetryContext(requestID)
	telCtx.prefillPod = prefillAddr

	// Store scheduling result reference for live metric refresh
	if schedulingResult != nil && len(schedulingResult.ProfileResults) > 0 {
		telCtx.schedulingResult = schedulingResult

		// Identify decode profile (primary profile)
		telCtx.decodeProfileName = schedulingResult.PrimaryProfileName
		primaryProfile := schedulingResult.ProfileResults[telCtx.decodeProfileName]
		if primaryProfile != nil && len(primaryProfile.TargetPods) > 0 {
			decodePodInfo := primaryProfile.TargetPods[0].GetPod()
			telCtx.decodePod = decodePodInfo.NamespacedName.String()
		}

		// Identify prefill profile (non-primary profile)
		for profileName := range schedulingResult.ProfileResults {
			if profileName != schedulingResult.PrimaryProfileName {
				telCtx.prefillProfileName = profileName
				break
			}
		}

		// Initialize metrics cache and do initial refresh
		telCtx.lastSeenMetrics = make(map[string]*backendmetrics.MetricsState)
		refreshLastSeenMetrics(ctx, telCtx)
	}

	logger.V(logutil.DEBUG).Info("PD telemetry initialized with scheduling result",
		"requestID", requestID,
		"prefillPod", telCtx.prefillPod,
		"decodePod", telCtx.decodePod,
		"prefillProfile", telCtx.prefillProfileName,
		"decodeProfile", telCtx.decodeProfileName,
		"cachedMetricsCount", len(telCtx.lastSeenMetrics))
}

// trackRunningRequest adds a request to the running request queue for the target pod
func (s *PdSLOOptimizer) trackRunningRequest(
	ctx context.Context,
	requestID string,
	schedulingResult *schedulingtypes.SchedulingResult,
	ttftSLO, tpotSLO float64,
) {
	logger := log.FromContext(ctx)

	if schedulingResult == nil || len(schedulingResult.ProfileResults) == 0 {
		return
	}

	// Get primary profile (decode pod for PD, or single pod for non-PD)
	primaryProfile := schedulingResult.ProfileResults[schedulingResult.PrimaryProfileName]
	if primaryProfile == nil || len(primaryProfile.TargetPods) == 0 {
		return
	}

	targetPod := primaryProfile.TargetPods[0].GetPod().NamespacedName.String()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.runningRequestLists[targetPod] == nil {
		s.runningRequestLists[targetPod] = newRequestPriorityQueue()
	}

	s.runningRequestLists[targetPod].Push(&runningRequest{
		requestID: requestID,
		ttft:      ttftSLO,
		tpot:      tpotSLO,
	})

	logger.V(logutil.DEBUG).Info("Tracked running request",
		"requestID", requestID,
		"pod", targetPod,
		"ttftSLO", ttftSLO,
		"tpotSLO", tpotSLO,
		"queueSize", s.runningRequestLists[targetPod].GetSize())
}

// ResponseReceived is called when response headers are received
func (s *PdSLOOptimizer) ResponseReceived(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	response *requestcontrol.Response,
	targetPod *backend.Pod,
) {
	logger := log.FromContext(ctx)

	// Handle nil request (can happen for responses without associated request context)
	if request == nil {
		logger.V(4).Info("Skipping ResponseReceived: request is nil")
		return
	}

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

	// Handle nil request (can happen for responses without associated request context)
	if request == nil {
		logger.V(4).Info("Skipping ResponseStreaming: request is nil")
		return
	}

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

		// Calculate actual TTFT for decode pod
		decodeTTFT := now.Sub(telCtx.decodeStart).Milliseconds()

		// Initialize token sampler for TPOT predictions
		if telCtx.tokenSampler == nil {
			telCtx.tokenSampler = newTokenSampler(requestID, DefaultSamplingMean, MaxSampledTokens)
			logger.V(logutil.DEBUG).Info("Initialized token sampler",
				"requestID", requestID,
				"nextSampleToken", telCtx.tokenSampler.getNextSampleToken())
		}

		// Refresh metrics before recording training data
		refreshLastSeenMetrics(ctx, telCtx)

		// Record TTFT training data with fresh metrics
		s.recordDecodeTTFT(ctx, request, telCtx, float64(decodeTTFT))

		// Predict first TPOT for decode
		s.predictFirstTPOT(ctx, request, telCtx)

		// Advance timestamp and refresh again
		telCtx.lastToken = now
		refreshLastSeenMetrics(ctx, telCtx)

		logger.V(logutil.DEBUG).Info("First token from decode processed",
			"requestID", requestID,
			"decodeTTFT_ms", decodeTTFT,
			"avgPredictedTPOT", telCtx.avgPredictedTPOT)
	} else {
		// Subsequent tokens - track for TPOT
		interTokenLatency := now.Sub(telCtx.lastToken).Milliseconds()
		telCtx.tokenCount++

		// Initialize sampler if not yet created (defensive)
		if telCtx.tokenSampler == nil {
			telCtx.tokenSampler = newTokenSampler(requestID, DefaultSamplingMean, MaxSampledTokens)
			logger.V(logutil.DEBUG).Info("Initialized token sampler for subsequent tokens",
				"requestID", requestID,
				"nextSampleToken", telCtx.tokenSampler.getNextSampleToken())
		}

		// Track actual TPOT for sampled tokens
		if telCtx.tokenCount == 2 || telCtx.tokenSampler.shouldPredict(telCtx.tokenCount) {
			telCtx.tpotObservations = append(telCtx.tpotObservations, float64(interTokenLatency))
			telCtx.avgTPOT = calculateRunningAverage(telCtx.avgTPOT, float64(interTokenLatency),
				len(telCtx.tpotObservations))
		}

		// Refresh metrics before recording training data
		refreshLastSeenMetrics(ctx, telCtx)

		// Record TPOT training data (always, for training)
		s.recordDecodeTPOT(ctx, request, telCtx, float64(interTokenLatency), telCtx.tokenCount)

		// Predict TPOT at sampled intervals
		if telCtx.tokenSampler.shouldPredict(telCtx.tokenCount) {
			s.predictSubsequentTPOT(ctx, request, telCtx)
			telCtx.tokenSampler.recordPrediction(telCtx.tokenCount)
		}

		// Advance timestamp and refresh metrics again
		telCtx.lastToken = now
		refreshLastSeenMetrics(ctx, telCtx)

		logger.V(logutil.TRACE).Info("Token from decode",
			"requestID", requestID,
			"tokenCount", telCtx.tokenCount,
			"interTokenLatency_ms", interTokenLatency,
			"shouldPredict", telCtx.tokenSampler.shouldPredict(telCtx.tokenCount))
	}
}

// ResponseComplete is called when the response is fully sent
func (s *PdSLOOptimizer) ResponseComplete(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	response *requestcontrol.Response,
	targetPod *backend.Pod,
) {
	logger := log.FromContext(ctx)

	// Handle nil request (can happen for responses without associated request context)
	if request == nil {
		logger.V(4).Info("Skipping ResponseComplete: request is nil")
		return
	}

	requestID := request.Headers[requtil.RequestIdHeaderKey]
	if requestID == "" {
		logger.Info("ResponseComplete: no request ID")
		return
	}

	// Remove request from running queue
	podKey := targetPod.NamespacedName.String()
	s.mu.Lock()
	if queue, exists := s.runningRequestLists[podKey]; exists {
		queue.Remove(requestID)
		logger.V(logutil.DEBUG).Info("Removed request from running queue",
			"requestID", requestID,
			"pod", podKey,
			"remainingRequests", queue.GetSize())
	}
	s.mu.Unlock()

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

	// Get predictor
	if s.predictor == nil {
		logger.V(logutil.DEBUG).Info("No predictor available for telemetry")
		return
	}

	// Get latest metrics for prefill profile
	metrics, err := getLatestMetricsForProfile(telCtx, telCtx.prefillProfileName)
	if err != nil {
		logger.V(logutil.DEBUG).Info("No metrics available for prefill telemetry", "error", err)
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
		PodType:            "prefill",
	}

	if err := s.predictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to send prefill TTFT telemetry")
	} else {
		schedulermetrics.RecordPDSLOTelemetry(schedulermetrics.PodTypePrefill)
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

	if s.predictor == nil {
		logger.V(logutil.DEBUG).Info("No predictor available for telemetry")
		return
	}

	// Get latest metrics for decode profile
	metrics, err := getLatestMetricsForProfile(telCtx, telCtx.decodeProfileName)
	if err != nil {
		logger.V(logutil.DEBUG).Info("No metrics available for decode TTFT telemetry", "error", err)
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
		PodType:            "decode",
	}

	if err := s.predictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to send decode TTFT telemetry")
	} else {
		schedulermetrics.RecordPDSLOTelemetry(schedulermetrics.PodTypeDecode)
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

	if s.predictor == nil {
		return
	}

	// Get latest metrics for decode profile
	metrics, err := getLatestMetricsForProfile(telCtx, telCtx.decodeProfileName)
	if err != nil {
		logger.V(logutil.TRACE).Info("No metrics available for decode TPOT telemetry", "error", err)
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
		PodType:            "decode",
	}

	if err := s.predictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.TRACE).Error(err, "Failed to send decode TPOT telemetry")
	} else {
		schedulermetrics.RecordPDSLOTelemetry(schedulermetrics.PodTypeDecode)
	}
}

// calculateRunningAverage computes the running average given the old average, new value, and count
func calculateRunningAverage(oldAvg, newValue float64, count int) float64 {
	if count <= 0 {
		return newValue
	}
	return oldAvg + (newValue-oldAvg)/float64(count)
}

// predictFirstTPOT predicts the first TPOT for the decode pod after TTFT
func (s *PdSLOOptimizer) predictFirstTPOT(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	telCtx *pdTelemetryContext,
) {
	logger := log.FromContext(ctx)

	if s.predictor == nil {
		logger.V(logutil.DEBUG).Info("No predictor available for TPOT prediction")
		return
	}

	// Get latest metrics for decode profile
	metrics, err := getLatestMetricsForProfile(telCtx, telCtx.decodeProfileName)
	if err != nil {
		logger.V(logutil.DEBUG).Info("Skipping first TPOT prediction due to missing metrics",
			"error", err)
		return
	}

	// Build prediction request
	predReq := latencypredictor.PredictionRequest{
		KVCachePercentage:  metrics.KVCacheUsagePercent,
		InputTokenLength:   getInputTokenLength(request),
		NumRequestWaiting:  metrics.WaitingQueueSize,
		NumRequestRunning:  metrics.RunningRequestsSize,
		NumTokensGenerated: telCtx.tokenCount,
		PrefixCacheScore:   0,
		PodType:            "decode",
	}

	start := time.Now()
	predResp, err := s.predictor.Predict(ctx, predReq)
	dur := time.Since(start)

	if err != nil || predResp == nil {
		logger.V(logutil.DEBUG).Error(err, "First TPOT predict failed",
			"duration_ms", dur.Milliseconds())
		telCtx.predictedTPOTObservations = append(telCtx.predictedTPOTObservations, 0)
		telCtx.avgPredictedTPOT = calculateRunningAverage(telCtx.avgPredictedTPOT, 0,
			len(telCtx.predictedTPOTObservations))
	} else {
		logger.V(logutil.DEBUG).Info("First TPOT predict succeeded",
			"value_ms", predResp.TPOT,
			"duration_ms", dur.Milliseconds())
		telCtx.predictedTPOTObservations = append(telCtx.predictedTPOTObservations, predResp.TPOT)
		telCtx.avgPredictedTPOT = calculateRunningAverage(telCtx.avgPredictedTPOT, predResp.TPOT,
			len(telCtx.predictedTPOTObservations))
	}
}

// predictSubsequentTPOT predicts TPOT for subsequent tokens at sampled intervals
func (s *PdSLOOptimizer) predictSubsequentTPOT(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	telCtx *pdTelemetryContext,
) {
	logger := log.FromContext(ctx)

	if s.predictor == nil {
		return
	}

	// Get latest metrics for decode profile
	metrics, err := getLatestMetricsForProfile(telCtx, telCtx.decodeProfileName)
	if err != nil {
		logger.V(logutil.DEBUG).Info("Skipping TPOT prediction due to missing metrics",
			"error", err)
		return
	}

	// Build prediction request
	predReq := latencypredictor.PredictionRequest{
		KVCachePercentage:  metrics.KVCacheUsagePercent,
		InputTokenLength:   getInputTokenLength(request),
		NumRequestWaiting:  metrics.WaitingQueueSize,
		NumRequestRunning:  metrics.RunningRequestsSize,
		NumTokensGenerated: telCtx.tokenCount,
		PrefixCacheScore:   0, // TPOT doesn't use prefix cache score
		PodType:            "decode",
	}

	start := time.Now()
	predResp, err := s.predictor.Predict(ctx, predReq)
	dur := time.Since(start)

	if err != nil || predResp == nil {
		logger.V(logutil.DEBUG).Error(err, "TPOT predict failed",
			"duration_ms", dur.Milliseconds())
		telCtx.predictedTPOTObservations = append(telCtx.predictedTPOTObservations, 0)
		telCtx.avgPredictedTPOT = calculateRunningAverage(telCtx.avgPredictedTPOT, 0,
			len(telCtx.predictedTPOTObservations))
	} else {
		logger.V(logutil.DEBUG).Info("TPOT predict succeeded",
			"value_ms", predResp.TPOT,
			"duration_ms", dur.Milliseconds(),
			"tokenCount", telCtx.tokenCount)
		telCtx.predictedTPOTObservations = append(telCtx.predictedTPOTObservations, predResp.TPOT)
		telCtx.avgPredictedTPOT = calculateRunningAverage(telCtx.avgPredictedTPOT, predResp.TPOT,
			len(telCtx.predictedTPOTObservations))
	}
}
