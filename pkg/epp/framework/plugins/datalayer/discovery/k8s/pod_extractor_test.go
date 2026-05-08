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

package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	podutil "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/util/pod"
)

func makePod(name, namespace, ip string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Status: corev1.PodStatus{
			PodIP: ip,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func TestPodEndpointID(t *testing.T) {
	id := podEndpointID("my-pod", "default", 0)
	assert.Equal(t, types.NamespacedName{Name: "my-pod-rank-0", Namespace: "default"}, id)

	id2 := podEndpointID("my-pod", "default", 2)
	assert.Equal(t, "my-pod-rank-2", id2.Name)
}

func TestAllPodEndpointIDs(t *testing.T) {
	ids := allPodEndpointIDs("pod", "ns", []int{8080, 8081, 8082})
	require.Len(t, ids, 3)
	assert.Equal(t, "pod-rank-0", ids[0].Name)
	assert.Equal(t, "pod-rank-1", ids[1].Name)
	assert.Equal(t, "pod-rank-2", ids[2].Name)
	for _, id := range ids {
		assert.Equal(t, "ns", id.Namespace)
	}
}

func TestPodToEndpointMetadata_SinglePort(t *testing.T) {
	pod := makePod("vllm-0", "default", "10.0.0.1", map[string]string{"app": "vllm"})
	metas := podToEndpointMetadata(pod, []int{8080})

	require.Len(t, metas, 1)
	m := metas[0]
	assert.Equal(t, "vllm-0-rank-0", m.NamespacedName.Name)
	assert.Equal(t, "default", m.NamespacedName.Namespace)
	assert.Equal(t, "10.0.0.1", m.Address)
	assert.Equal(t, "8080", m.Port)
	assert.Equal(t, "vllm-0", m.PodName)
	assert.Contains(t, m.MetricsHost, "10.0.0.1")
}

func TestPodToEndpointMetadata_MultiplePorts(t *testing.T) {
	pod := makePod("vllm-0", "default", "10.0.0.1", nil)
	metas := podToEndpointMetadata(pod, []int{8080, 8081})

	require.Len(t, metas, 2)
	assert.Equal(t, "8080", metas[0].Port)
	assert.Equal(t, "8081", metas[1].Port)
	assert.Equal(t, "vllm-0-rank-0", metas[0].NamespacedName.Name)
	assert.Equal(t, "vllm-0-rank-1", metas[1].NamespacedName.Name)
}

func TestPodToEndpointMetadata_LabelsPreserved(t *testing.T) {
	labels := map[string]string{"model": "llama", "gpu": "h100"}
	pod := makePod("vllm-0", "ns", "10.0.0.2", labels)
	metas := podToEndpointMetadata(pod, []int{8000})

	require.Len(t, metas, 1)
	assert.Equal(t, labels, metas[0].Labels)
}

func TestPodToEndpointMetadata_ActivePortsAnnotation(t *testing.T) {
	pod := makePod("vllm-0", "default", "10.0.0.1", nil)
	pod.Annotations = map[string]string{podutil.ActivePortsAnnotation: "8080"}
	metas := podToEndpointMetadata(pod, []int{8080, 8081})

	require.Len(t, metas, 1, "only active port should produce an endpoint")
	assert.Equal(t, "8080", metas[0].Port)
}

// ---- ExtractNotification tests ---------------------------------------------

type fakePool struct {
	synced      bool
	labelsMatch bool
	targetPorts []int
}

func (f *fakePool) PoolHasSynced() bool                          { return f.synced }
func (f *fakePool) PoolLabelsMatch(labels map[string]string) bool { return f.labelsMatch }
func (f *fakePool) PoolTargetPorts() []int                        { return f.targetPorts }

type trackingNotifier struct {
	upserted []*fwkdl.EndpointMetadata
	deleted  []types.NamespacedName
}

func (n *trackingNotifier) Upsert(m *fwkdl.EndpointMetadata)    { n.upserted = append(n.upserted, m) }
func (n *trackingNotifier) Delete(id types.NamespacedName)       { n.deleted = append(n.deleted, id) }

func podToUnstructured(pod *corev1.Pod) *unstructured.Unstructured {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetName(pod.Name)
	u.SetNamespace(pod.Namespace)
	return u
}

func TestExtractNotification_NotSynced(t *testing.T) {
	pool := &fakePool{synced: false, targetPorts: []int{8080}}
	n := &trackingNotifier{}
	e := newPodDiscoveryExtractor(pool, n)

	err := e.ExtractNotification(context.Background(), fwkdl.NotificationEvent{
		Type:   fwkdl.EventAddOrUpdate,
		Object: podToUnstructured(makePod("p", "ns", "1.2.3.4", nil)),
	})
	require.NoError(t, err)
	assert.Empty(t, n.upserted)
	assert.Empty(t, n.deleted)
}

func TestExtractNotification_Delete(t *testing.T) {
	pool := &fakePool{synced: true, targetPorts: []int{8080, 8081}}
	n := &trackingNotifier{}
	e := newPodDiscoveryExtractor(pool, n)

	err := e.ExtractNotification(context.Background(), fwkdl.NotificationEvent{
		Type:   fwkdl.EventDelete,
		Object: podToUnstructured(makePod("pod-0", "ns", "", nil)),
	})
	require.NoError(t, err)
	assert.Empty(t, n.upserted)
	assert.Len(t, n.deleted, 2)
}

func TestExtractNotification_PodNotReady(t *testing.T) {
	pool := &fakePool{synced: true, labelsMatch: true, targetPorts: []int{8080}}
	n := &trackingNotifier{}
	e := newPodDiscoveryExtractor(pool, n)

	pod := makePod("pod-0", "ns", "1.2.3.4", nil)
	pod.Status.Conditions = nil // not ready

	err := e.ExtractNotification(context.Background(), fwkdl.NotificationEvent{
		Type:   fwkdl.EventAddOrUpdate,
		Object: podToUnstructured(pod),
	})
	require.NoError(t, err)
	assert.Empty(t, n.upserted)
	assert.Len(t, n.deleted, 1)
}

func TestExtractNotification_LabelsMismatch(t *testing.T) {
	pool := &fakePool{synced: true, labelsMatch: false, targetPorts: []int{8080}}
	n := &trackingNotifier{}
	e := newPodDiscoveryExtractor(pool, n)

	err := e.ExtractNotification(context.Background(), fwkdl.NotificationEvent{
		Type:   fwkdl.EventAddOrUpdate,
		Object: podToUnstructured(makePod("pod-0", "ns", "1.2.3.4", nil)),
	})
	require.NoError(t, err)
	assert.Empty(t, n.upserted)
	assert.Len(t, n.deleted, 1)
}

func TestExtractNotification_HappyPath(t *testing.T) {
	pool := &fakePool{synced: true, labelsMatch: true, targetPorts: []int{8080, 8081}}
	n := &trackingNotifier{}
	e := newPodDiscoveryExtractor(pool, n)

	err := e.ExtractNotification(context.Background(), fwkdl.NotificationEvent{
		Type:   fwkdl.EventAddOrUpdate,
		Object: podToUnstructured(makePod("pod-0", "ns", "1.2.3.4", map[string]string{"app": "vllm"})),
	})
	require.NoError(t, err)
	assert.Len(t, n.upserted, 2)
	assert.Empty(t, n.deleted)
	assert.Equal(t, "1.2.3.4", n.upserted[0].Address)
}

func TestExtractNotification_ActivePortsAnnotation(t *testing.T) {
	pool := &fakePool{synced: true, labelsMatch: true, targetPorts: []int{8080, 8081}}
	n := &trackingNotifier{}
	e := newPodDiscoveryExtractor(pool, n)

	pod := makePod("pod-0", "ns", "1.2.3.4", nil)
	pod.Annotations = map[string]string{podutil.ActivePortsAnnotation: "8080"}

	err := e.ExtractNotification(context.Background(), fwkdl.NotificationEvent{
		Type:   fwkdl.EventAddOrUpdate,
		Object: podToUnstructured(pod),
	})
	require.NoError(t, err)
	assert.Len(t, n.upserted, 1, "only active port should be upserted")
	assert.Equal(t, "8080", n.upserted[0].Port)
}
