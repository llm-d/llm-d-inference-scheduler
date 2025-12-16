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
	PredictorTypePrefill = "prefill"
	PredictorTypeDecode  = "decode"

	// Pod types
	PodTypePrefill = "prefill"
	PodTypeDecode  = "decode"

	// Predictor status
	PredictorStatusSuccess = "success"
	PredictorStatusError   = "error"
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

	// PDSLOPodSelectionsTotal records PD-SLO pod selections by type and outcome.
	PDSLOPodSelectionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_slo_pod_selections_total",
			Help:      metrics.HelpMsgWithStability("Total PD-SLO pod selections by type and headroom outcome", compbasemetrics.ALPHA),
		},
		[]string{"pod_type", "outcome"}, // pod_type: prefill|decode, outcome: positive_headroom|negative_headroom
	)

	// PDSLOPredictorCallsTotal records predictor calls by type and status.
	PDSLOPredictorCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_slo_predictor_calls_total",
			Help:      metrics.HelpMsgWithStability("Total predictor calls by type and status", compbasemetrics.ALPHA),
		},
		[]string{"predictor", "status"}, // predictor: prefill|decode, status: success|error
	)

	// PDSLOTelemetryRecordedTotal records telemetry data sent to training servers.
	PDSLOTelemetryRecordedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_slo_telemetry_recorded_total",
			Help:      metrics.HelpMsgWithStability("Total telemetry data points recorded by pod type", compbasemetrics.ALPHA),
		},
		[]string{"pod_type"}, // pod_type: prefill|decode
	)
)

// GetCollectors returns all custom collectors for the llm-d-inference-scheduler.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		SchedulerPDDecisionCount,
		PDSLOPodSelectionsTotal,
		PDSLOPredictorCallsTotal,
		PDSLOTelemetryRecordedTotal,
	}
}

// RecordPDDecision records the type of P/D disaggregation decision made.
func RecordPDDecision(decisionType string) {
	SchedulerPDDecisionCount.WithLabelValues(decisionType).Inc()
}

// RecordPDSLOPodSelection records a PD-SLO pod selection by type and outcome.
func RecordPDSLOPodSelection(podType, outcome string) {
	PDSLOPodSelectionsTotal.WithLabelValues(podType, outcome).Inc()
}

// RecordPDSLOPredictorCall records a predictor call.
func RecordPDSLOPredictorCall(predictorType, status string) {
	PDSLOPredictorCallsTotal.WithLabelValues(predictorType, status).Inc()
}

// RecordPDSLOTelemetry records telemetry data sent to training server.
func RecordPDSLOTelemetry(podType string) {
	PDSLOTelemetryRecordedTotal.WithLabelValues(podType).Inc()
}
