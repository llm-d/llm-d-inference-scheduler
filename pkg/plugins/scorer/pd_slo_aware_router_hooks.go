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

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	slo_aware_router "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/slo_aware_router"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
	latencypredictor "sigs.k8s.io/gateway-api-inference-extension/sidecars/latencypredictorasync"
)

// PDSLOAwareRouter wraps the base SLOAwareRouter to add P/D-specific hook logic.
// This keeps P/D disaggregation concerns in llm-d-inference-scheduler rather than
// leaking them into the generic gateway-api-inference-extension.
type PDSLOAwareRouter struct {
	*slo_aware_router.SLOAwareRouter
}

var _ requestcontrol.PreRequest = &PDSLOAwareRouter{}
var _ requestcontrol.ResponseReceived = &PDSLOAwareRouter{}
var _ requestcontrol.ResponseStreaming = &PDSLOAwareRouter{}
var _ requestcontrol.ResponseComplete = &PDSLOAwareRouter{}

// PreRequest delegates to the base router
func (p *PDSLOAwareRouter) PreRequest(ctx context.Context, request *schedulingtypes.LLMRequest, schedulingResult *schedulingtypes.SchedulingResult) {
	p.SLOAwareRouter.PreRequest(ctx, request, schedulingResult)
}

// ResponseReceived adds P/D-specific logic to extract prefill timing headers
// before delegating to the base router.
func (p *PDSLOAwareRouter) ResponseReceived(ctx context.Context, request *schedulingtypes.LLMRequest, response *requestcontrol.Response, targetPod *backend.Pod) {
	logger := log.FromContext(ctx)

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
	p.SLOAwareRouter.ResponseReceived(ctx, request, response, targetPod)
}

// ResponseStreaming delegates to the base router
func (p *PDSLOAwareRouter) ResponseStreaming(ctx context.Context, request *schedulingtypes.LLMRequest, response *requestcontrol.Response, pod *backend.Pod) {
	p.SLOAwareRouter.ResponseStreaming(ctx, request, response, pod)
}

// ResponseComplete delegates to the base router
func (p *PDSLOAwareRouter) ResponseComplete(ctx context.Context, request *schedulingtypes.LLMRequest, response *requestcontrol.Response, pod *backend.Pod) {
	p.SLOAwareRouter.ResponseComplete(ctx, request, response, pod)
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

	// Get scheduling result for this request
	schedulingResult, err := p.SLOAwareRouter.GetSchedulingResultForRequest(request)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to get scheduling result for prefill training")
		return
	}

	// P/D-specific: Extract prefill pod from the "prefill" profile
	prefillResult, exists := schedulingResult.ProfileResults["prefill"]
	if !exists || prefillResult == nil || len(prefillResult.TargetPods) == 0 {
		logger.V(logutil.DEBUG).Info("No prefill pod in scheduling result, skipping prefill training")
		return
	}

	prefillPod := prefillResult.TargetPods[0]

	// Get metrics for the prefill pod
	lastSeenMetrics, err := p.SLOAwareRouter.GetLastSeenMetricsForRequest(request)
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
	prefixCacheScores, err := p.SLOAwareRouter.GetPrefixCacheScoresForRequest(request)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to get prefix cache scores")
		return
	}
	prefixCacheScore := prefixCacheScores[prefillPod.GetPod().String()]

	// Get prompt
	prompt, err := p.SLOAwareRouter.GetRequestPrompt(request)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to get prompt for prefill training")
		return
	}

	// Build training entry using the PDPredictionRequestBuilder
	// This will automatically populate PodType="prefill" based on llm-d.ai/role label
	requestBuilder := p.SLOAwareRouter.GetRequestBuilder()
	entry := requestBuilder.BuildTrainingEntry(
		ctx,
		prefillPod,
		prefillMetrics,
		prompt,
		actualPrefillTTFT, // Actual TTFT from sidecar
		0,                  // TPOT not applicable for prefill
		time.Now(),
		0, // No tokens generated yet for prefill
		prefixCacheScore,
	)

	// Record training data
	latencyPredictor := p.SLOAwareRouter.GetLatencyPredictor().(latencypredictor.PredictorInterface)
	if err := latencyPredictor.AddTrainingDataBulk([]latencypredictor.TrainingEntry{entry}); err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to record prefill training data")
	} else {
		logger.V(logutil.DEBUG).Info("Recorded prefill training data",
			"pod", prefillPod.GetPod().String(),
			"ttft_ms", actualPrefillTTFT,
			"pod_type", "prefill")
	}
}
