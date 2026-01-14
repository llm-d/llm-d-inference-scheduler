// Package sidecar provides Prometheus metrics for the P/D routing sidecar.
package sidecar

import (
	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/metrics"
)

const (
	// SidecarSubsystem is the metric prefix for sidecar metrics.
	SidecarSubsystem = "llm_d_inference_scheduler"
)

var (
	// PDProxyCoordinatorOverheadMilliseconds records sidecar coordination overhead between prefill and decode.
	// This measures time spent in sidecar processing (JSON parsing, parameter extraction, HTTP overhead).
	// Note: Actual KV cache transfer happens inside vLLM and is not measured by this metric.
	PDProxyCoordinatorOverheadMilliseconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: SidecarSubsystem,
			Name:      "pd_proxy_coordinator_overhead_milliseconds",
			Help:      metrics.HelpMsgWithStability("Sidecar coordination overhead between prefill and decode HTTP requests (JSON processing only, excludes KV cache transfer which happens inside vLLM)", compbasemetrics.ALPHA),
			Buckets:   []float64{1, 2, 5, 10, 20, 50, 100, 200, 500},
		},
		[]string{"connector"},
	)

	// PDProxyPrefillDurationMilliseconds records prefill stage duration.
	// This is the full HTTP round-trip time to the prefill instance.
	PDProxyPrefillDurationMilliseconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: SidecarSubsystem,
			Name:      "pd_proxy_prefill_duration_milliseconds",
			Help:      metrics.HelpMsgWithStability("Prefill stage HTTP round-trip duration in P/D disaggregation", compbasemetrics.ALPHA),
			Buckets:   []float64{5, 10, 20, 50, 100, 200, 500, 1000, 2000},
		},
		[]string{"connector"},
	)

	// PDProxyDecodeDurationMilliseconds records decode stage duration.
	// This is the full HTTP round-trip time to the decode instance (includes KV cache transfer inside vLLM).
	PDProxyDecodeDurationMilliseconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: SidecarSubsystem,
			Name:      "pd_proxy_decode_duration_milliseconds",
			Help:      metrics.HelpMsgWithStability("Decode stage HTTP round-trip duration in P/D disaggregation (includes KV cache transfer inside vLLM)", compbasemetrics.ALPHA),
			Buckets:   []float64{10, 50, 100, 200, 500, 1000, 2000, 5000, 10000},
		},
		[]string{"connector"},
	)

	// PDProxyTotalDurationMilliseconds records end-to-end request duration.
	PDProxyTotalDurationMilliseconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: SidecarSubsystem,
			Name:      "pd_proxy_total_duration_milliseconds",
			Help:      metrics.HelpMsgWithStability("Total end-to-end request duration from P/D coordinator perspective", compbasemetrics.ALPHA),
			Buckets:   []float64{20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000},
		},
		[]string{"connector"},
	)
)

// GetCollectors returns all custom collectors for the P/D routing sidecar.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		PDProxyCoordinatorOverheadMilliseconds,
		PDProxyPrefillDurationMilliseconds,
		PDProxyDecodeDurationMilliseconds,
		PDProxyTotalDurationMilliseconds,
	}
}
