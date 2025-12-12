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

	// PD-SLO headroom outcomes
	HeadroomOutcomePositive = "positive_headroom"
	HeadroomOutcomeNegative = "negative_headroom"

	// Predictor types
	PredictorTypePrefillTTFT = "prefill-ttft"
	PredictorTypeDecodeTTFT  = "decode-ttft"
	PredictorTypeDecodeTPOT  = "decode-tpot"
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

	// PDSLOPairSelectionsTotal records PD-SLO pair selections by outcome.
	PDSLOPairSelectionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_slo_pair_selections_total",
			Help:      metrics.HelpMsgWithStability("Total PD-SLO pair selections by headroom outcome", compbasemetrics.ALPHA),
		},
		[]string{"outcome"}, // "positive_headroom" or "negative_headroom"
	)

	// PDSLOPredictorCallsTotal records predictor calls by type and status.
	PDSLOPredictorCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_slo_predictor_calls_total",
			Help:      metrics.HelpMsgWithStability("Total predictor calls by type and status", compbasemetrics.ALPHA),
		},
		[]string{"predictor", "status"}, // predictor: prefill-ttft|decode-ttft|decode-tpot, status: success|error
	)

	// PDSLOPairsEvaluatedTotal records the number of pod pairs evaluated.
	PDSLOPairsEvaluatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_slo_pairs_evaluated_total",
			Help:      metrics.HelpMsgWithStability("Total number of (prefill, decode) pod pairs evaluated", compbasemetrics.ALPHA),
		},
		[]string{"request_id"},
	)
)

// GetCollectors returns all custom collectors for the llm-d-inference-scheduler.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		SchedulerPDDecisionCount,
		PDSLOPairSelectionsTotal,
		PDSLOPredictorCallsTotal,
		PDSLOPairsEvaluatedTotal,
	}
}

// RecordPDDecision records the type of P/D disaggregation decision made.
func RecordPDDecision(decisionType string) {
	SchedulerPDDecisionCount.WithLabelValues(decisionType).Inc()
}

// RecordPDSLOPairSelection records a PD-SLO pair selection outcome.
func RecordPDSLOPairSelection(outcome string) {
	PDSLOPairSelectionsTotal.WithLabelValues(outcome).Inc()
}

// RecordPDSLOPredictorCall records a predictor call.
func RecordPDSLOPredictorCall(predictorType, status string) {
	PDSLOPredictorCallsTotal.WithLabelValues(predictorType, status).Inc()
}

// RecordPDSLOPairsEvaluated records the number of pairs evaluated for a request.
func RecordPDSLOPairsEvaluated(requestID string, count int) {
	for i := 0; i < count; i++ {
		PDSLOPairsEvaluatedTotal.WithLabelValues(requestID).Inc()
	}
}
