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

package pod

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	// ActivePortsAnnotation specifies which ports on a pod should receive inference traffic.
	// Value is a comma-separated list of port numbers, e.g. "8000,8001".
	ActivePortsAnnotation = "llm-d.ai/active-ports"

	// LegacyGAIEActivePortsAnnotation is kept for backward compatibility.
	//
	// Deprecated: use ActivePortsAnnotation instead.
	LegacyGAIEActivePortsAnnotation = "inference.networking.k8s.io/active-ports"
)

// ExtractActivePorts returns the subset of targetPorts that the pod has declared
// active via the llm-d.ai/active-ports (or legacy inference.networking.k8s.io/active-ports)
// annotation. If neither annotation is present, all targetPorts are considered active.
func ExtractActivePorts(pod *corev1.Pod, targetPorts []int) sets.Set[int] {
	allPorts := sets.New(targetPorts...)
	annotations := pod.GetAnnotations()
	portsAnnotation, ok := annotations[ActivePortsAnnotation]
	if !ok {
		portsAnnotation, ok = annotations[LegacyGAIEActivePortsAnnotation]
		if !ok {
			return allPorts
		}
	}

	activePorts := sets.New[int]()
	for _, portStr := range strings.Split(portsAnnotation, ",") {
		var portNum int
		if _, err := fmt.Sscanf(strings.TrimSpace(portStr), "%d", &portNum); err == nil && portNum > 0 && allPorts.Has(portNum) {
			activePorts.Insert(portNum)
		}
	}
	return activePorts
}

func IsPodReady(pod *corev1.Pod) bool {
	if !pod.DeletionTimestamp.IsZero() {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			if condition.Status == corev1.ConditionTrue {
				return true
			}
			break
		}
	}
	return false
}
