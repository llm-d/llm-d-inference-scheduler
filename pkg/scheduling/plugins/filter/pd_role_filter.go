/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package filter

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// PrefillFilter - filters out pods that are not marked with role Prefill
type PrefillFilter struct{}

var _ plugins.Filter = &PrefillFilter{} // validate interface conformance

// Name returns the name of the filter
func (pf *PrefillFilter) Name() string {
	return "prefill-filter"
}

// Filter filters out all pods that are not marked as "prefill"
func (pf *PrefillFilter) Filter(_ *types.SchedulingContext, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		if pod.GetPod().Role == backend.Prefill {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods
}

// DecodeFilter - filters out pods that are not marked with role Decode or Both
type DecodeFilter struct{}

var _ plugins.Filter = &DecodeFilter{} // validate interface conformance

// Name returns the name of the filter
func (df *DecodeFilter) Name() string {
	return "decode-filter"
}

// decodeFilterFunc filters out all pods that are not marked as "decode" or "both"
func (pf *DecodeFilter) Filter(_ *types.SchedulingContext, pods []types.Pod) []types.Pod {
	filteredPods := []types.Pod{}

	for _, pod := range pods {
		if pod.GetPod().Role == backend.Decode || pod.GetPod().Role == backend.Both {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods
}
