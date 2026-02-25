package programaware

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	requestcontrol "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	scheduling "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// --- PrepareDataPlugin interface ---

// Produces declares what data this plugin produces. No endpoint attributes are produced.
func (p *ProgramAwarePlugin) Produces() map[string]any {
	return map[string]any{}
}

// Consumes declares what data this plugin requires from other plugins. None.
func (p *ProgramAwarePlugin) Consumes() map[string]any {
	return map[string]any{}
}

// PrepareRequestData reads the program ID from the fairness header, increments the
// program's total request count, and records a timestamp for wait time calculation.
func (p *ProgramAwarePlugin) PrepareRequestData(ctx context.Context, request *scheduling.LLMRequest, _ []scheduling.Endpoint) error {
	programID := request.Headers[fairnessIDHeader]
	if programID == "" {
		log.FromContext(ctx).V(logutil.DEBUG).Info("No fairness ID header found, skipping program-aware metrics update",
			"requestId", request.RequestId)
		return nil
	}

	metrics := p.getOrCreateMetrics(programID)
	metrics.IncrementRequests()

	p.requestTimestamps.Store(request.RequestId, time.Now())

	log.FromContext(ctx).V(logutil.TRACE).Info("PrepareData: recorded program request",
		"requestId", request.RequestId, "programId", programID,
		"totalRequests", metrics.TotalRequests())

	return nil
}

// --- PreRequest interface ---

// PreRequest is called after scheduling and before the request is sent to the model server.
// It calculates the wait time and updates the program's EWMA.
func (p *ProgramAwarePlugin) PreRequest(ctx context.Context, request *scheduling.LLMRequest, _ *scheduling.SchedulingResult) {
	programID := request.Headers[fairnessIDHeader]
	if programID == "" {
		return
	}

	metrics := p.getOrCreateMetrics(programID)
	metrics.IncrementDispatched()

	if tsRaw, ok := p.requestTimestamps.Load(request.RequestId); ok {
		ts := tsRaw.(time.Time)
		waitMs := float64(time.Since(ts).Milliseconds())
		metrics.RecordWaitTime(waitMs)

		log.FromContext(ctx).V(logutil.TRACE).Info("PreRequest: recorded wait time",
			"requestId", request.RequestId, "programId", programID,
			"waitMs", waitMs, "avgWaitMs", metrics.AverageWaitTime())
	}
}

// --- ResponseReceived interface ---

// ResponseReceived sets the x-program-id response header so the client can identify the program association.
func (p *ProgramAwarePlugin) ResponseReceived(_ context.Context, request *scheduling.LLMRequest, response *requestcontrol.Response, _ *datalayer.EndpointMetadata) {
	programID := request.Headers[fairnessIDHeader]
	if programID == "" {
		return
	}
	if response.Headers == nil {
		response.Headers = make(map[string]string)
	}
	response.Headers["x-program-id"] = programID
}

// --- ResponseComplete interface ---

// ResponseComplete records token usage and cleans up per-request state.
func (p *ProgramAwarePlugin) ResponseComplete(ctx context.Context, request *scheduling.LLMRequest, response *requestcontrol.Response, _ *datalayer.EndpointMetadata) {
	programID := request.Headers[fairnessIDHeader]

	// Cleanup per-request timestamp regardless of program ID presence.
	p.requestTimestamps.Delete(request.RequestId)

	if programID == "" {
		return
	}

	if response != nil {
		metrics := p.getOrCreateMetrics(programID)
		metrics.RecordTokens(int64(response.Usage.PromptTokens), int64(response.Usage.CompletionTokens))

		log.FromContext(ctx).V(logutil.TRACE).Info("ResponseComplete: recorded tokens",
			"requestId", request.RequestId, "programId", programID,
			"promptTokens", response.Usage.PromptTokens, "completionTokens", response.Usage.CompletionTokens)
	}
}
