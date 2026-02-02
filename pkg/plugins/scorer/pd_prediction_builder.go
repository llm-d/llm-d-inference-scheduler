// Package scorer provides scoring plugins for the llm-d scheduler.
package scorer

import (
	"context"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	predictedlatency "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/predictedlatency"
	latencypredictor "sigs.k8s.io/gateway-api-inference-extension/sidecars/latencypredictorasync"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
)

// PDPredictionRequestBuilder extends the default builder with P/D pod type awareness.
// This builder reads the llm-d.ai/role label from pods and populates the PodType field
// in prediction and training requests, enabling the latency predictor to learn separate
// models for prefill and decode workloads.
type PDPredictionRequestBuilder struct {
	predictedlatency.DefaultPredictionRequestBuilder
}

// NewPDPredictionRequestBuilder creates a new P/D-aware prediction request builder.
func NewPDPredictionRequestBuilder() *PDPredictionRequestBuilder {
	return &PDPredictionRequestBuilder{}
}

// extractPodType reads the llm-d.ai/role label from pod metadata and maps it to the predictor's pod_type field.
// Returns:
//   - "prefill" for pods with llm-d.ai/role=prefill
//   - "decode" for pods with llm-d.ai/role=decode
//   - "" (empty) for pods with llm-d.ai/role=both or no label (monolithic)
func (b *PDPredictionRequestBuilder) extractPodType(metadata *datalayer.EndpointMetadata) string {
	if metadata == nil {
		return "" // No metadata, treat as monolithic
	}

	labels := metadata.Labels
	if labels == nil {
		return "" // No labels, treat as monolithic
	}

	role, exists := labels[filter.RoleLabel] // "llm-d.ai/role"
	if !exists {
		return "" // No role label, treat as monolithic
	}

	// Map llm-d roles to predictor pod types
	switch role {
	case filter.RolePrefill:
		return "prefill"
	case filter.RoleDecode:
		return "decode"
	case filter.RoleBoth:
		// Pods that can do both are treated as monolithic
		// (predictor doesn't have a specialized model for this)
		return ""
	default:
		return ""
	}
}

// BuildPredictionRequest constructs a prediction request with pod type information.
// Extends the default implementation by populating the PodType field based on the pod's role label.
func (b *PDPredictionRequestBuilder) BuildPredictionRequest(
	ctx context.Context,
	targetEndpointMetadata *datalayer.EndpointMetadata,
	metrics *datalayer.Metrics,
	prompt string,
	generatedTokens int,
	prefixCacheScore float64,
) latencypredictor.PredictionRequest {
	// Get base request from parent implementation
	req := b.DefaultPredictionRequestBuilder.BuildPredictionRequest(
		ctx, targetEndpointMetadata, metrics, prompt, generatedTokens, prefixCacheScore,
	)

	// Customize with pod type from llm-d.ai/role label
	req.PodType = b.extractPodType(targetEndpointMetadata)

	return req
}

// BuildTrainingEntry constructs a training entry with pod type information.
// Extends the default implementation by populating the PodType field based on the pod's role label.
func (b *PDPredictionRequestBuilder) BuildTrainingEntry(
	ctx context.Context,
	targetEndpointMetadata *datalayer.EndpointMetadata,
	metrics *datalayer.Metrics,
	prompt string,
	actualTTFT float64,
	actualTPOT float64,
	timestamp time.Time,
	generatedTokens int,
	prefixCacheScore float64,
) latencypredictor.TrainingEntry {
	// Get base entry from parent implementation
	entry := b.DefaultPredictionRequestBuilder.BuildTrainingEntry(
		ctx, targetEndpointMetadata, metrics, prompt, actualTTFT, actualTPOT, timestamp, generatedTokens, prefixCacheScore,
	)

	// Customize with pod type from llm-d.ai/role label
	entry.PodType = b.extractPodType(targetEndpointMetadata)

	return entry
}
