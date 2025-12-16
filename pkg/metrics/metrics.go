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

	// Metric types
	MetricTypeTTFT = "ttft"
	MetricTypeTPOT = "tpot"
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

	// PDSLOHeadroomSeconds records the actual SLO headroom in seconds.
	// Positive values = pod has headroom (can meet SLO)
	// Negative values = pod violates SLO
	PDSLOHeadroomSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "pd_slo_headroom_seconds",
			Help:      metrics.HelpMsgWithStability("SLO headroom in seconds (positive=meeting SLO, negative=violating SLO)", compbasemetrics.ALPHA),
			// Buckets cover -1s to +1s with focus on -100ms to +500ms range
			Buckets: []float64{-1.0, -0.5, -0.2, -0.1, -0.05, -0.02, -0.01, 0, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1.0},
		},
		[]string{"pod_type", "metric_type"}, // pod_type: prefill|decode, metric_type: ttft|tpot
	)
)

// GetCollectors returns all custom collectors for the llm-d-inference-scheduler.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		SchedulerPDDecisionCount,
		PDSLOPodSelectionsTotal,
		PDSLOPredictorCallsTotal,
		PDSLOTelemetryRecordedTotal,
		PDSLOHeadroomSeconds,
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

// RecordPDSLOHeadroom records the actual headroom value in seconds.
// Positive values indicate the pod can meet SLO, negative values indicate SLO violation.
func RecordPDSLOHeadroom(podType, metricType string, headroomMs float64) {
	// Convert milliseconds to seconds for Prometheus convention
	headroomSeconds := headroomMs / 1000.0
	PDSLOHeadroomSeconds.WithLabelValues(podType, metricType).Observe(headroomSeconds)
}
