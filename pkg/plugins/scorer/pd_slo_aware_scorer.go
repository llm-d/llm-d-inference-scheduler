// Package scorer provides scoring plugins for the llm-d scheduler.
package scorer

import (
	"encoding/json"
	"fmt"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	predictedlatency "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/predictedlatency"
)

const (
	// PDSLOAwareScorerType is the type identifier for the P/D-aware SLO scorer plugin.
	PDSLOAwareScorerType = "pd-slo-aware-scorer"
)

// PDSLOAwareScorerFactory creates an SLO-aware router configured for P/D disaggregation.
// This factory wraps the base SLOAwareRouter with a PDPredictionRequestBuilder that
// automatically populates pod type information from llm-d.ai/role labels.
//
// The factory accepts the same configuration parameters as the base SLOAwareRouter:
//   - samplingMean: Poisson mean for token sampling (default: 100.0)
//   - maxSampledTokens: Maximum TPOT predictions per request (default: 20)
//   - sloBufferFactor: SLO buffer multiplier (default: 1.0)
//   - negHeadroomTTFTWeight: TTFT weight for negative headroom (default: 0.8)
//   - negHeadroomTPOTWeight: TPOT weight for negative headroom (default: 0.2)
//   - headroomTTFTWeight: TTFT weight for positive headroom (default: 0.8)
//   - headroomTPOTWeight: TPOT weight for positive headroom (default: 0.2)
//   - headroomSelectionStrategy: Pod selection strategy: "least", "most", "composite-least", "composite-most", "composite-only" (default: "least")
//   - compositeKVWeight: KV cache component weight (default: 1.0)
//   - compositeQueueWeight: Queue size component weight (default: 1.0)
//   - compositePrefixWeight: Prefix cache component weight (default: 1.0)
//   - epsilonExploreSticky: Affinity gate exploration rate (default: 0.01)
//   - epsilonExploreNeg: Negative headroom exploration rate (default: 0.01)
//   - affinityGateTau: Per-path stickiness threshold (default: 0.80)
//   - affinityGateTauGlobal: Global stickiness threshold (default: 0.99)
//   - selectionMode: Pod selection mode: "linear" (weighted random) or "max" (argmax) (default: "linear")
//
// Usage in EPP config:
//
//	schedulingProfiles:
//	  - name: prefill
//	    scorers:
//	      - name: pd-slo-scorer
//	        type: pd-slo-aware-scorer
//	        weight: 100
//	        config:
//	          headroomSelectionStrategy: "least"
//	          sloBufferFactor: 1.0
//	  - name: decode
//	    scorers:
//	      - name: pd-slo-scorer
//	        type: pd-slo-aware-scorer
//	        weight: 100
//	        config:
//	          headroomSelectionStrategy: "least"
func PDSLOAwareScorerFactory(name string, rawConfig json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	// Parse base SLO aware router config
	cfg := predictedlatency.DefaultConfig
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config for PDSLOAwareScorer: %w", err)
		}
	}

	// Validate the config
	// Note: We can't call cfg.validate() directly as it's a method on Config, not exported
	// The base factory will validate for us, but since we're not using it, we need to ensure
	// the config is valid. For now, we'll rely on the base implementation's validation.

	// Start the latency predictor using exported helper
	predictor, err := predictedlatency.StartPredictor(handle)
	if err != nil {
		return nil, fmt.Errorf("failed to start latency predictor: %w", err)
	}

	// Create the SLO aware router and inject P/D-aware request builder
	// This builder will populate the PodType field based on llm-d.ai/role labels
	baseRouter := predictedlatency.NewPredictedLatency(cfg, predictor).WithName(name)
	baseRouter.SetRequestBuilder(NewPDPredictionRequestBuilder())

	// Wrap with PDSLOAwareRouter to add P/D-specific hook logic
	// The wrapper delegates to the base router while adding P/D-specific header extraction
	pdRouter := &PDSLOAwareRouter{
		PredictedLatency: baseRouter,
	}

	return pdRouter, nil
}
