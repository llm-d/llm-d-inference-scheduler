package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/metrics"
)

const (
	SchedulerSubsystem = "llm_d_inference_scheduler"
)

var (
	SchedulerRequestCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: SchedulerSubsystem,
			Name:      "requests_total",
			Help:      metrics.HelpMsgWithStability("Total number of requests processed by the scheduler.", compbasemetrics.ALPHA),
		},
		[]string{"result"},
	)
)

// GetCollectors returns all custom collectors for the llm-d-inference-scheduler.
func GetCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		SchedulerRequestCount,
	}
}
