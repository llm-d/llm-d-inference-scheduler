package sidecar

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPDProxyCoordinatorOverheadMilliseconds(t *testing.T) {
	connector := "lmcache"

	// Observe some values
	PDProxyCoordinatorOverheadMilliseconds.WithLabelValues(connector).Observe(3.0)
	PDProxyCoordinatorOverheadMilliseconds.WithLabelValues(connector).Observe(7.0)
	PDProxyCoordinatorOverheadMilliseconds.WithLabelValues(connector).Observe(25.0)

	expected := `
		# HELP llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds [ALPHA] Sidecar coordination overhead between prefill and decode HTTP requests (JSON processing only, excludes KV cache transfer which happens inside vLLM)
		# TYPE llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds histogram
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="1"} 0
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="2"} 0
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="5"} 1
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="10"} 2
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="20"} 2
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="50"} 3
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="100"} 3
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="200"} 3
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="500"} 3
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_bucket{connector="lmcache",le="+Inf"} 3
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_sum{connector="lmcache"} 35
		llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds_count{connector="lmcache"} 3
	`

	if err := testutil.CollectAndCompare(PDProxyCoordinatorOverheadMilliseconds, strings.NewReader(expected),
		"llm_d_inference_scheduler_pd_proxy_coordinator_overhead_milliseconds"); err != nil {
		t.Errorf("PDProxyCoordinatorOverheadMilliseconds test failed: %v", err)
	}
}
