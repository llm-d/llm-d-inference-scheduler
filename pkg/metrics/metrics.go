// Package metrics provides metrics registration for the epp.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/metrics"
)

const (
	// SchedulerSubsystem is the metric prefix of the package.
	SchedulerSubsystem = "llm_d_inference_scheduler"

	// DecisionTypeDecodeOnly is for requests that are routed to decode instance only.
	DecisionTypeDecodeOnly = "decode-only"
	// DecisionTypePrefillDecode is for requests that are gone through P/D.
	DecisionTypePrefillDecode = "prefill-decode"
	// DecisionTypeEncodeDecode is for requests that are gone through E/D.
	DecisionTypeEncodeDecode = "encode-decode"
)

var (
	// SchedulerPDDecisionCount records request P/D decision.
	SchedulerPDDecisionCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_decision_total",
			Help:      metrics.HelpMsgWithStability("Total number of P/D disaggregation decisions made", compbasemetrics.ALPHA),
		},
		[]string{"model_name", "decision_type"}, // "decode-only" or "prefill-decode"
	)
)

// GetCollectors returns all custom collectors for the llm-d-inference-scheduler.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		SchedulerPDDecisionCount,
	}
}

// RecordPDDecision increments the counter for a specific P/D routing decision.
// The decisionType must be one of the DecisionType* constants (e.g., DecisionTypeDecodeOnly).
// The model parameter should be the target model name (e.g., from request.TargetModel);
// if empty, the caller should pass a placeholder like "unknown" to avoid empty labels.
func RecordPDDecision(modelName, decisionType string) {
	if modelName == "" {
		modelName = "unknown"
	}
	SchedulerPDDecisionCount.WithLabelValues(modelName, decisionType).Inc()
}
