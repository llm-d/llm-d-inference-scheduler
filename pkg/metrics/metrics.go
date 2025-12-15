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
)

var (
	// SchedulerPDDecisionCount records request P/D decision.
	SchedulerPDDecisionCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_decision_total",
			Help:      metrics.HelpMsgWithStability("Total number of P/D disaggregation decisions made", compbasemetrics.ALPHA),
		},
		[]string{"decision_type"}, // "decode-only" or "prefill-decode"
	)
	Retries = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: SchedulerSubsystem, Name: "batch_request_retries_total",
		Help: "Total number of batch request retries.",
	})

	BatchReqs = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: SchedulerSubsystem, Name: "batch_request_total",
		Help: "Total number of batch requests.",
	})
	ExceededDeadlineReqs = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: SchedulerSubsystem, Name: "batch_ßexceeded_deadline_requests_total",
		Help: "Total number of batch requests that exceeded their deadline.",
	})
	FailedReqs = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: SchedulerSubsystem, Name: "batch_failed_requests_total",
		Help: "Total number of batch requests that failed.",
	})
	SuccessfulReqs = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: SchedulerSubsystem, Name: "batch_successful_requests_total",
		Help: "Total number of batch requests that succeeded.",
	})
	SheddedRequests = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: SchedulerSubsystem, Name: "batch_shedded_requests_total",
		Help: "Total number of batch requests that were shedded.",
	})
)

// GetCollectors returns all custom collectors for the llm-d-inference-scheduler.
func GetEPPCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		SchedulerPDDecisionCount,
	}
}

// GetCollectors returns all custom collectors for the batch processor.
func GetBatchCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		Retries, BatchReqs, ExceededDeadlineReqs, FailedReqs, SuccessfulReqs, SheddedRequests,
	}
}

// RecordPDDecision records the type of P/D disaggregation decision made.
func RecordPDDecision(decisionType string) {
	SchedulerPDDecisionCount.WithLabelValues(decisionType).Inc()
}
