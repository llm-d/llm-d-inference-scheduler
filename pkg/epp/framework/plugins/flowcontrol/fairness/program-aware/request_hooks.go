package programaware

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	requestcontrol "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requestcontrol"
	scheduling "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

// --- DataProducer interface ---

// Produces declares what data this plugin produces. No endpoint attributes are produced.
func (p *ProgramAwarePlugin) Produces() map[string]any {
	return map[string]any{}
}

// Consumes declares what data this plugin requires from other plugins. None.
func (p *ProgramAwarePlugin) Consumes() map[string]any {
	return map[string]any{}
}

// PrepareRequestData reads the program ID from the fairness header and increments
// the program's total request count. The enqueue timestamp for wait time calculation
// is recorded by Pick() (in flow control), not here, since PrepareData runs after
// the request has already left the flow control queue.
func (p *ProgramAwarePlugin) PrepareRequestData(ctx context.Context, request *scheduling.InferenceRequest, _ []scheduling.Endpoint) error {
	programID := request.Headers[fairnessIDHeader]
	if programID == "" {
		programID = defaultFairnessID
	}

	metrics := p.getOrCreateMetrics(programID)
	metrics.IncrementRequests()
	requestsTotal.WithLabelValues(programID).Inc()

	log.FromContext(ctx).V(logutil.TRACE).Info("PrepareData: recorded program request",
		"requestId", request.RequestId, "programId", programID,
		"totalRequests", metrics.TotalRequests())

	return nil
}

// --- PreRequest interface ---

// PreRequest is called after scheduling and before the request is sent to the model server.
// It calculates the wait time (enqueue → now) and updates the program's EWMA.
// The enqueue timestamp was recorded by Pick() during flow control dispatch.
func (p *ProgramAwarePlugin) PreRequest(ctx context.Context, request *scheduling.InferenceRequest, _ *scheduling.SchedulingResult) {
	programID := request.Headers[fairnessIDHeader]
	if programID == "" {
		programID = defaultFairnessID
	}

	metrics := p.getOrCreateMetrics(programID)
	p.getStrategy().OnPreRequest(metrics, request)

	metrics.IncrementDispatched()
	dispatchedTotal.WithLabelValues(programID).Inc()

	if enqueueTimeRaw, ok := p.requestTimestamps.Load(request.RequestId); ok {
		enqueueTime := enqueueTimeRaw.(time.Time)
		waitMs := float64(time.Since(enqueueTime).Milliseconds())
		metrics.RecordWaitTime(waitMs)
		waitTimeMs.WithLabelValues(programID).Observe(waitMs)
		ewmaWaitTimeMs.WithLabelValues(programID).Set(metrics.AverageWaitTime())

		log.FromContext(ctx).V(logutil.TRACE).Info("PreRequest: recorded wait time",
			"requestId", request.RequestId, "programId", programID,
			"waitMs", waitMs, "avgWaitMs", metrics.AverageWaitTime())
	}
}

// --- ResponseComplete interface ---

// ResponseBody records token usage and cleans up per-request state.
func (p *ProgramAwarePlugin) ResponseBody(ctx context.Context, request *scheduling.InferenceRequest, response *requestcontrol.Response, _ *datalayer.EndpointMetadata) {
	if request == nil {
		return
	}
	programID := request.Headers[fairnessIDHeader]
	if programID == "" {
		programID = defaultFairnessID
	}

	// Clean up the enqueue timestamp stored by Pick().
	p.requestTimestamps.Delete(request.RequestId)

	if response != nil {
		metrics := p.getOrCreateMetrics(programID)
		promptTokens := int64(response.Usage.PromptTokens)
		completionTokens := int64(response.Usage.CompletionTokens)

		metrics.RecordTokens(promptTokens, completionTokens)
		inputTokensTotal.WithLabelValues(programID).Add(float64(promptTokens))
		outputTokensTotal.WithLabelValues(programID).Add(float64(completionTokens))

		p.getStrategy().OnCompleted(metrics, request, response)
		attainedServiceTokens.WithLabelValues(programID).Set(metrics.AttainedService())

		// Update service rate for fairness index (weighted tokens/sec EWMA).
		cost := float64(weightInputToken*promptTokens + weightOutputToken*completionTokens)
		metrics.RecordServiceRate(cost, time.Now())
		serviceRateTokensPerSec.WithLabelValues(programID).Set(metrics.ServiceRate())

		log.FromContext(ctx).V(logutil.TRACE).Info("ResponseComplete: recorded tokens",
			"requestId", request.RequestId, "programId", programID,
			"promptTokens", promptTokens, "completionTokens", completionTokens,
			"attainedService", metrics.AttainedService())
	}
}
