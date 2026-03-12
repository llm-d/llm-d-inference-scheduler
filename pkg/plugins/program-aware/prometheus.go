package programaware

import (
	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"
	metricsutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/metrics"
)

const programAwareSubsystem = "program_aware"

// Package-level metrics that the plugin records to directly.
// These are registered at startup via GetCollectors() before the plugin is instantiated.
var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: programAwareSubsystem,
			Name:      "requests_total",
			Help:      metricsutil.HelpMsgWithStability("Total requests received per program", compbasemetrics.ALPHA),
		},
		[]string{"program_id"},
	)

	dispatchedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: programAwareSubsystem,
			Name:      "dispatched_total",
			Help:      metricsutil.HelpMsgWithStability("Total requests dispatched per program", compbasemetrics.ALPHA),
		},
		[]string{"program_id"},
	)

	waitTimeMs = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: programAwareSubsystem,
			Name:      "wait_time_milliseconds",
			Help:      metricsutil.HelpMsgWithStability("Flow control queue wait time per program in milliseconds", compbasemetrics.ALPHA),
			Buckets:   []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
		},
		[]string{"program_id"},
	)

	inputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: programAwareSubsystem,
			Name:      "input_tokens_total",
			Help:      metricsutil.HelpMsgWithStability("Total input (prompt) tokens consumed per program", compbasemetrics.ALPHA),
		},
		[]string{"program_id"},
	)

	outputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: programAwareSubsystem,
			Name:      "output_tokens_total",
			Help:      metricsutil.HelpMsgWithStability("Total output (completion) tokens produced per program", compbasemetrics.ALPHA),
		},
		[]string{"program_id"},
	)

	pickLatencyUs = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Subsystem: programAwareSubsystem,
			Name:      "pick_latency_microseconds",
			Help:      metricsutil.HelpMsgWithStability("Latency of the Pick() call in microseconds", compbasemetrics.ALPHA),
			Buckets:   []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 5000},
		},
	)
)

// GetCollectors returns all Prometheus collectors for the program-aware plugin.
// Called from pkg/metrics/metrics.go to register with the runner at startup.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		requestsTotal,
		dispatchedTotal,
		waitTimeMs,
		inputTokensTotal,
		outputTokensTotal,
		pickLatencyUs,
	}
}
