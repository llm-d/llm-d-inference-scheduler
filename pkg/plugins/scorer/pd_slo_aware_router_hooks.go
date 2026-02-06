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
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	predictedlatency "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/predictedlatency"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
	latencypredictor "sigs.k8s.io/gateway-api-inference-extension/sidecars/latencypredictorasync"
)

// PDSLOAwareRouter wraps the base PredictedLatency to add P/D-specific hook logic.
// This keeps P/D disaggregation concerns in llm-d-inference-scheduler rather than
// leaking them into the generic gateway-api-inference-extension.
type PDSLOAwareRouter struct {
	*predictedlatency.PredictedLatency
}

var _ requestcontrol.PreRequest = &PDSLOAwareRouter{}
var _ requestcontrol.ResponseReceived = &PDSLOAwareRouter{}
var _ requestcontrol.ResponseStreaming = &PDSLOAwareRouter{}
var _ requestcontrol.ResponseComplete = &PDSLOAwareRouter{}

// PreRequest tracks both prefill and decode pods in running request lists.
// The base router tracks the decode pod (primary profile), and we additionally
// track the prefill pod to ensure accurate load visibility during scoring.
func (p *PDSLOAwareRouter) PreRequest(ctx context.Context, request *schedulingtypes.LLMRequest, schedulingResult *schedulingtypes.SchedulingResult) {
	logger := log.FromContext(ctx)

	// Delegate to base router (tracks decode pod - primary profile)
	p.PredictedLatency.PreRequest(ctx, request, schedulingResult)

	// Guard against nil request or schedulingResult - base will have returned early
	if request == nil || schedulingResult == nil || len(schedulingResult.ProfileResults) == 0 {
		logger.V(logutil.DEBUG).Info("PDSLOAwareRouter.PreRequest: request or schedulingResult is nil/empty after base delegation, skipping P/D tracking")
		return
	}

	// P/D-specific: Also track prefill pod if it was selected
	if prefillResult, exists := schedulingResult.ProfileResults["prefill"]; exists && prefillResult != nil {
		if len(prefillResult.TargetEndpoints) > 0 {
			prefillPod := prefillResult.TargetEndpoints[0]
			requestID := request.Headers[requtil.RequestIdHeaderKey]

			// Get average TPOT SLO to determine priority
			avgTPOTSLO, err := p.PredictedLatency.GetAvgTPOTSLO(request)
			if err != nil {
				logger.V(logutil.DEBUG).Info("Could not get SLO context for prefill tracking", "error", err)
				return
			}

			// Track prefill pod in running requests
			p.PredictedLatency.AddToRunningRequests(
				prefillPod.GetMetadata().NamespacedName,
				requestID,
				avgTPOTSLO,
			)

			logger.V(logutil.DEBUG).Info("Tracked prefill pod in running requests",
				"prefillPod", prefillPod.GetMetadata().NamespacedName.Name,
				"requestID", requestID)
		}
	}
}

// ResponseReceived adds P/D-specific logic to extract prefill timing headers
// before delegating to the base router.
func (p *PDSLOAwareRouter) ResponseReceived(ctx context.Context, request *schedulingtypes.LLMRequest, response *requestcontrol.Response, targetPod *datalayer.EndpointMetadata) {
	logger := log.FromContext(ctx)

	// Guard against nil request (can happen during early request failures)
	if request == nil {
		logger.V(logutil.DEBUG).Info("PDSLOAwareRouter.ResponseReceived: request is nil, delegating to base")
		p.PredictedLatency.ResponseReceived(ctx, request, response, targetPod)
		return
	}

	// P/D-specific: Check for prefill timing headers from the decode sidecar
	if prefillTTFTStr, ok := response.Headers["x-prefill-ttft-ms"]; ok && prefillTTFTStr != "" {
		logger.V(logutil.DEBUG).Info("Detected prefill timing header",
			"ttft_ms", prefillTTFTStr,
			"requestID", request.Headers[requtil.RequestIdHeaderKey])

		// Parse prefill TTFT
		prefillTTFT, err := strconv.ParseFloat(prefillTTFTStr, 64)
		if err != nil {
			logger.V(logutil.DEBUG).Error(err, "Failed to parse prefill TTFT header", "value", prefillTTFTStr)
		} else {
			// Record training data for the prefill pod
			p.recordPrefillTrainingData(ctx, request, prefillTTFT)
		}
	}

	// Delegate to base router for decode prediction logic
	p.PredictedLatency.ResponseReceived(ctx, request, response, targetPod)
}

// ResponseStreaming delegates to the base router
func (p *PDSLOAwareRouter) ResponseStreaming(ctx context.Context, request *schedulingtypes.LLMRequest, response *requestcontrol.Response, pod *datalayer.EndpointMetadata) {
	p.PredictedLatency.ResponseStreaming(ctx, request, response, pod)
}

// ResponseComplete cleans up both prefill and decode pod tracking.
// We remove the prefill pod from running requests (if it was used) before
// delegating to the base router, which removes the decode pod.
func (p *PDSLOAwareRouter) ResponseComplete(ctx context.Context, request *schedulingtypes.LLMRequest, response *requestcontrol.Response, pod *datalayer.EndpointMetadata) {
	logger := log.FromContext(ctx)

	// Guard against nil request (can happen during early request failures)
	if request == nil {
		logger.V(logutil.DEBUG).Info("PDSLOAwareRouter.ResponseComplete: request is nil, delegating to base")
		p.PredictedLatency.ResponseComplete(ctx, request, response, pod)
		return
	}

	requestID := request.Headers[requtil.RequestIdHeaderKey]

	// P/D-specific: Remove prefill pod from tracking if it was used
	schedulingResult, err := p.PredictedLatency.GetSchedulingResult(request)
	if err == nil && schedulingResult != nil {
		if prefillResult, exists := schedulingResult.ProfileResults["prefill"]; exists && prefillResult != nil {
			if len(prefillResult.TargetEndpoints) > 0 {
				prefillPod := prefillResult.TargetEndpoints[0]
				p.PredictedLatency.RemoveFromRunningRequests(
					prefillPod.GetMetadata().NamespacedName,
					requestID,
				)

				logger.V(logutil.DEBUG).Info("Removed prefill pod from running requests",
					"prefillPod", prefillPod.GetMetadata().NamespacedName.Name,
					"requestID", requestID)
			}
		}
	}

	// Delegate to base router (removes decode pod)
	p.PredictedLatency.ResponseComplete(ctx, request, response, pod)
}

// recordPrefillTrainingData records training data for the prefill pod based on timing
// reported by the decode sidecar via x-prefill-ttft-ms header.
//
// This method is P/D-specific and lives in llm-d-inference-scheduler because it:
// - Assumes two-phase scheduling with "prefill" and "decode" profiles
// - Knows about the llm-d.ai/role label structure
// - Understands that prefill pods only handle TTFT (no TPOT)
func (p *PDSLOAwareRouter) recordPrefillTrainingData(
	ctx context.Context,
	request *schedulingtypes.LLMRequest,
	actualPrefillTTFT float64,
) {
	logger := log.FromContext(ctx)

	// Guard against nil request (defensive, should not happen)
	if request == nil {
		logger.V(logutil.DEBUG).Info("recordPrefillTrainingData: request is nil, skipping")
		return
	}

	// Get scheduling result for this request
	schedulingResult, err := p.PredictedLatency.GetSchedulingResult(request)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to get scheduling result for prefill training")
		return
	}

	// P/D-specific: Extract prefill pod from the "prefill" profile
	prefillResult, exists := schedulingResult.ProfileResults["prefill"]
	if !exists || prefillResult == nil || len(prefillResult.TargetEndpoints) == 0 {
		logger.V(logutil.DEBUG).Info("No prefill pod in scheduling result, skipping prefill training")
		return
	}

	prefillPod := prefillResult.TargetEndpoints[0]

	// Get metrics for the prefill pod
	lastSeenMetrics, err := p.PredictedLatency.GetLastSeenMetricsForRequest(request)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to get metrics for prefill training")
		return
	}

	prefillMetrics, exists := lastSeenMetrics["prefill"]
	if !exists || prefillMetrics == nil {
		logger.V(logutil.DEBUG).Info("No metrics available for prefill pod")
		return
	}

	// Get prefix cache score
	prefixCacheScores, err := p.PredictedLatency.GetPrefixCacheScoresForRequest(request)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to get prefix cache scores")
		return
	}
	prefixCacheScore := prefixCacheScores[prefillPod.GetMetadata().String()]

	// Get prompt
	prompt, err := p.PredictedLatency.GetRequestPrompt(request)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to get prompt for prefill training")
		return
	}

	// Build training entry using the PDPredictionRequestBuilder
	// This will automatically populate PodType="prefill" based on llm-d.ai/role label
	requestBuilder := p.PredictedLatency.GetRequestBuilder()
	entry := requestBuilder.BuildTrainingEntry(
		ctx,
		prefillPod.GetMetadata(),
		prefillMetrics,
		prompt,
		actualPrefillTTFT, // Actual TTFT from sidecar
		0,                  // TPOT not applicable for prefill
		time.Now(),
		0, // No tokens generated yet for prefill
		prefixCacheScore,
	)

	// Record training data (use safe type assertion to avoid panic)
	predictorInterface := p.PredictedLatency.GetLatencyPredictor()
	if predictorInterface == nil {
		logger.V(logutil.DEBUG).Info("Latency predictor is nil, skipping prefill training")
		return
	}

	latencyPredictor, ok := predictorInterface.(latencypredictor.PredictorInterface)
	if !ok {
		logger.V(logutil.DEBUG).Info("Latency predictor type mismatch, skipping prefill training")
		return
	}
	if err := latencyPredictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to record prefill training data")
	} else {
		logger.V(logutil.DEBUG).Info("Recorded prefill training data",
			"pod", prefillPod.GetMetadata().String(),
			"ttft_ms", actualPrefillTTFT,
			"pod_type", "prefill")
	}
}
