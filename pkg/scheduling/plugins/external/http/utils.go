package http

import (
	"maps"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// podsToExtPods converts upstream pod object to pod object used for external plugins http calls
func podsToExtPods(pods []types.Pod) []pod {
	extPods := make([]pod, len(pods))

	for i, p := range pods {
		extPods[i] = pod{
			Pod:     podInfoFromBackend(p.GetPod()),
			Metrics: metricsFromBackend(p.GetMetrics()),
		}
	}

	return extPods
}

// podInfoFromBackend converts upstream backend.Pod to podInfo object used for external plugins http calls
func podInfoFromBackend(bkPod *backend.Pod) *podInfo {
	if bkPod == nil {
		return nil
	}

	return &podInfo{
		NamespacedName: namespacedName{Name: bkPod.NamespacedName.Name, Namespace: bkPod.NamespacedName.Namespace},
		Address:        bkPod.Address,
		Labels:         maps.Clone(bkPod.Labels),
	}
}

// metricsFromBackend converts upstream backendmetrics.MetricsState to podMetrics object used for external plugins http calls
func metricsFromBackend(bkMetrics *backendmetrics.MetricsState) *podMetrics {
	if bkMetrics == nil {
		return nil
	}

	return &podMetrics{
		ActiveModels:            maps.Clone(bkMetrics.ActiveModels),
		WaitingModels:           maps.Clone(bkMetrics.WaitingModels),
		MaxActiveModels:         bkMetrics.MaxActiveModels,
		RunningQueueSize:        bkMetrics.RunningQueueSize,
		WaitingQueueSize:        bkMetrics.WaitingQueueSize,
		KVCacheUsagePercent:     bkMetrics.KVCacheUsagePercent,
		KVCacheMaxTokenCapacity: bkMetrics.KvCacheMaxTokenCapacity,
		UpdateTime:              bkMetrics.UpdateTime.Format("2006-01-02 15:04:05"),
	}
}

// namespacedNameToString creates full pod name based on namespace and name
func namespacedNameToString(name string, namespace string) string {
	return namespace + "/" + name
}
