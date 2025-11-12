package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/metrics"
)

const (
	SchedulerSubsystem = "llm_d_inference_scheduler"

	// Decision types for pd_decision_total metric
	DecisionTypeDecodeOnly    = "decode-only"
	DecisionTypePrefillDecode = "prefill-decode"
)

var (
	SchedulerPDDecisionCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_decision_total",
			Help:      metrics.HelpMsgWithStability("Total number of P/D disaggregation decisions made", compbasemetrics.ALPHA),
		},
		[]string{"decision_type"}, // "decode-only" or "prefill-decode"
	)
)

// GetCollectors returns all custom collectors for the llm-d-inference-scheduler.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		SchedulerPDDecisionCount,
	}
}

// RecordPDDecisionCounter records the type of P/D disaggregation decision made.
func RecordPDDecisionCounter(decisionType string) {
	SchedulerPDDecisionCount.WithLabelValues(decisionType).Inc()
}
