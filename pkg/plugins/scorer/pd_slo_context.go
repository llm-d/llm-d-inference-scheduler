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
	"sync"

	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// pdSLOContext holds SLO and prediction context for a request
// Similar to GAIE's sloRequestContext
type pdSLOContext struct {
	// SLO values
	ttftSLO float64
	tpotSLO float64

	// Prediction tracking
	predictorBasedScheduling     bool
	prefixCacheScoresForPods     map[string]float64 // pod key -> prefix cache score
	predictedTTFTForScheduling   map[string]float64 // pod key -> predicted TTFT
	predictedTPOTForScheduling   map[string]float64 // pod key -> predicted TPOT

	// Validation flags
	hasValidPod bool // Whether any pod can meet SLOs
}

// newPDSLOContext creates a new SLO context for a request
func newPDSLOContext() *pdSLOContext {
	return &pdSLOContext{
		prefixCacheScoresForPods:   make(map[string]float64),
		predictedTTFTForScheduling: make(map[string]float64),
		predictedTPOTForScheduling: make(map[string]float64),
		hasValidPod:                true, // Assume valid until proven otherwise
	}
}

// Store SLO contexts per request ID
var (
	pdSLOContexts sync.Map // map[requestID]*pdSLOContext
)

// getOrMakePDSLOContext retrieves or creates an SLO context for a request
func getOrMakePDSLOContext(request *schedulingtypes.LLMRequest) *pdSLOContext {
	requestID := request.RequestId

	// Try to get existing context
	if ctx, exists := pdSLOContexts.Load(requestID); exists {
		if sloCtx, ok := ctx.(*pdSLOContext); ok {
			return sloCtx
		}
	}

	// Create new context
	sloCtx := newPDSLOContext()
	pdSLOContexts.Store(requestID, sloCtx)
	return sloCtx
}

// setPDSLOContextForRequest stores the SLO context for a request
func setPDSLOContextForRequest(request *schedulingtypes.LLMRequest, sloCtx *pdSLOContext) {
	pdSLOContexts.Store(request.RequestId, sloCtx)
}

// deletePDSLOContext removes the SLO context for a request (cleanup)
func deletePDSLOContext(requestID string) {
	pdSLOContexts.Delete(requestID)
}

// updatePDSLOContextWithPredictions stores predicted TTFT/TPOT values in the context
// Similar to GAIE's updateRequestContextWithPredictions
func updatePDSLOContextWithPredictions(
	sloCtx *pdSLOContext,
	pods []schedulingtypes.Pod,
	ttftPredictions []float64,
	tpotPredictions []float64,
) {
	if sloCtx == nil {
		return
	}

	for i, pod := range pods {
		podKey := pod.GetPod().String()
		if i < len(ttftPredictions) {
			sloCtx.predictedTTFTForScheduling[podKey] = ttftPredictions[i]
		}
		if i < len(tpotPredictions) {
			sloCtx.predictedTPOTForScheduling[podKey] = tpotPredictions[i]
		}
	}
}
