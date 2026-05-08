/*
Copyright 2026 The Kubernetes Authors.

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

package k8s

import (
	"context"
	"net"
	"reflect"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	podutil "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/util/pod"
)

var _ fwkdl.NotificationExtractor = (*podDiscoveryExtractor)(nil)

var podDiscoveryGVK = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}

// PoolInfoProvider is the narrow interface podDiscoveryExtractor requires from the datastore.
// Implemented by datastore.Datastore.
type PoolInfoProvider interface {
	PoolHasSynced() bool
	PoolLabelsMatch(labels map[string]string) bool
	PoolTargetPorts() []int
}

type podDiscoveryExtractor struct {
	typedName fwkplugin.TypedName
	pool      PoolInfoProvider
	notifier  fwkdl.DiscoveryNotifier
}

func newPodDiscoveryExtractor(pool PoolInfoProvider, notifier fwkdl.DiscoveryNotifier) *podDiscoveryExtractor {
	return &podDiscoveryExtractor{
		typedName: fwkplugin.TypedName{Type: "pod-discovery-extractor", Name: "pod-discovery"},
		pool:      pool,
		notifier:  notifier,
	}
}

func (e *podDiscoveryExtractor) TypedName() fwkplugin.TypedName { return e.typedName }

func (e *podDiscoveryExtractor) GVK() schema.GroupVersionKind { return podDiscoveryGVK }

func (e *podDiscoveryExtractor) ExpectedInputType() reflect.Type {
	return reflect.TypeFor[fwkdl.NotificationEvent]()
}

func (e *podDiscoveryExtractor) ExtractNotification(ctx context.Context, event fwkdl.NotificationEvent) error {
	if !e.pool.PoolHasSynced() {
		return nil
	}

	// Snapshot once so all branches in this call see a consistent port list,
	// even if InferencePoolReconciler fires PoolSet concurrently.
	targetPorts := e.pool.PoolTargetPorts()

	name := event.Object.GetName()
	namespace := event.Object.GetNamespace()

	if event.Type == fwkdl.EventDelete {
		for _, id := range allPodEndpointIDs(name, namespace, targetPorts) {
			e.notifier.Delete(id)
		}
		return nil
	}

	pod := &corev1.Pod{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(event.Object.Object, pod); err != nil {
		return err
	}

	if !podutil.IsPodReady(pod) || !e.pool.PoolLabelsMatch(pod.Labels) {
		for _, id := range allPodEndpointIDs(name, namespace, targetPorts) {
			e.notifier.Delete(id)
		}
		return nil
	}

	for _, meta := range podToEndpointMetadata(pod, targetPorts) {
		e.notifier.Upsert(meta)
	}
	return nil
}

func podToEndpointMetadata(pod *corev1.Pod, targetPorts []int) []*fwkdl.EndpointMetadata {
	activePorts := podutil.ExtractActivePorts(pod, targetPorts)
	metas := make([]*fwkdl.EndpointMetadata, 0, len(targetPorts))
	for idx, port := range targetPorts {
		if !activePorts.Has(port) {
			continue
		}
		metas = append(metas, &fwkdl.EndpointMetadata{
			NamespacedName: podEndpointID(pod.Name, pod.Namespace, idx),
			PodName:        pod.Name,
			Address:        pod.Status.PodIP,
			Port:           strconv.Itoa(port),
			MetricsHost:    net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(port)),
			Labels:         pod.GetLabels(),
		})
	}
	return metas
}

func podEndpointID(podName, namespace string, idx int) types.NamespacedName {
	return types.NamespacedName{Name: podName + "-rank-" + strconv.Itoa(idx), Namespace: namespace}
}

func allPodEndpointIDs(podName, namespace string, targetPorts []int) []types.NamespacedName {
	ids := make([]types.NamespacedName, len(targetPorts))
	for idx := range targetPorts {
		ids[idx] = podEndpointID(podName, namespace, idx)
	}
	return ids
}
