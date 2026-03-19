// Package metrics provides metrics registration for the epp.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/metrics"
)

var (
	// SchedulerPDDecisionCount records request P/D decision.
	//
	// Deprecated: Use SchedulerDisaggDecisionCount instead.
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
//
// Deprecated: Use GetDisaggCollectors instead.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{SchedulerPDDecisionCount}
}

// RecordPDDecision increments the counter for a specific P/D routing decision.
//
// Deprecated: Use RecordDisaggDecision instead.
func RecordPDDecision(modelName, decisionType string) {
	if modelName == "" {
		modelName = "unknown"
	}
	SchedulerPDDecisionCount.WithLabelValues(modelName, decisionType).Inc()
}
