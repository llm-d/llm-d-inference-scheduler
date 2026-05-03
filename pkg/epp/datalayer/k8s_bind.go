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

package datalayer

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	podutil "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/util/pod"
)

// BindNotificationSource registers a watcher/reconciler for the source's GVK.
// The framework core owns the cache and reconciliation; the source only receives
// deep-copied events via Notify.
func BindNotificationSource(src fwkdl.NotificationSource, extractors []fwkdl.NotificationExtractor, mgr ctrl.Manager) error {
	gvk := src.GVK()
	log := mgr.GetLogger().WithName("notification-controller").WithValues("gvk", gvk.Kind)

	reconciler := &notificationReconciler{
		client:     mgr.GetClient(),
		src:        src,
		extractors: extractors,
		gvk:        gvk,
		log:        log,
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	// use the source's name to make the controller name unique
	// This allows multiple notification sources for the same GVK
	// (needed in tests, Configure() sources still imposes
	// one source per GVK).
	controllerName := "notify_" + strings.ToLower(gvk.Kind) + "_" + src.TypedName().Name

	return ctrl.NewControllerManagedBy(mgr).
		// Naming the controller allows you to see specific metrics/logs for this watch
		Named(controllerName).
		For(obj).
		// ResourceVersionChanged is safer for generic notifications than GenerationChanged,
		// as it catches metadata and status updates that the consumer might need.
		WithEventFilter(predicate.ResourceVersionChangedPredicate{}).
		Complete(reconciler)
}

// Reconciler for notifications. This is a generic reconciler that can be used for any GVK.
type notificationReconciler struct {
	client     client.Client
	src        fwkdl.NotificationSource
	extractors []fwkdl.NotificationExtractor
	gvk        schema.GroupVersionKind
	log        logr.Logger
}

// Reconciler carries out the actual notification logic.
func (rn *notificationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := rn.log.WithValues("resource", req.NamespacedName, "gvk", rn.gvk.String())

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(rn.gvk)

	event := &fwkdl.NotificationEvent{
		Type:   fwkdl.EventAddOrUpdate,
		Object: u,
	}

	err := rn.client.Get(ctx, req.NamespacedName, u)
	if err != nil {
		if apierrors.IsNotFound(err) {
			u.SetName(req.Name)
			u.SetNamespace(req.Namespace)
			event.Type = fwkdl.EventDelete
		} else {
			log.Error(err, "failed to fetch resource from cache")
			return ctrl.Result{}, err
		}
	}

	return rn.dispatch(ctx, log, event)
}

func (rn *notificationReconciler) dispatch(ctx context.Context, log logr.Logger, event *fwkdl.NotificationEvent) (ctrl.Result, error) {
	log.V(logging.TRACE).Info("processing notification", "eventType", event.Type)

	processed, err := rn.src.Notify(ctx, *event)
	if err != nil {
		log.Error(err, "notifier failed to process event")
		return ctrl.Result{}, err
	}
	if processed == nil {
		return ctrl.Result{}, nil
	}

	for _, ext := range rn.extractors {
		if err := ext.ExtractNotification(ctx, *processed); err != nil {
			log.Error(err, "extractor failed", "extractor", ext.TypedName())
		}
	}

	return ctrl.Result{}, nil
}

// ---- Pod discovery binding -------------------------------------------------

// PoolInfoProvider gives the pod discovery reconciler the pool state it needs
// to filter pods and extract port information. Implemented by the datastore.
type PoolInfoProvider interface {
	PoolHasSynced() bool
	PoolLabelsMatch(labels map[string]string) bool
	PoolTargetPorts() []int
}

// BindPodDiscovery registers a pod-watching controller that translates pod
// lifecycle events into DiscoveryNotifier.Upsert/Delete calls.
// This centralises the controller-runtime wiring so K8s discovery plugins
// do not duplicate the reconciler pattern from BindNotificationSource.
func BindPodDiscovery(pool PoolInfoProvider, notifier fwkdl.DiscoveryNotifier, mgr ctrl.Manager) error {
	r := &podDiscoveryReconciler{
		client:   mgr.GetClient(),
		pool:     pool,
		notifier: notifier,
	}

	filter := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return pool.PoolLabelsMatch(e.Object.GetLabels()) },
		UpdateFunc:  func(e event.UpdateEvent) bool {
			return pool.PoolLabelsMatch(e.ObjectOld.GetLabels()) || pool.PoolLabelsMatch(e.ObjectNew.GetLabels())
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return pool.PoolLabelsMatch(e.Object.GetLabels()) },
		GenericFunc: func(e event.GenericEvent) bool { return pool.PoolLabelsMatch(e.Object.GetLabels()) },
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("pod-discovery").
		For(&corev1.Pod{}).
		WithEventFilter(filter).
		Complete(r)
}

type podDiscoveryReconciler struct {
	client   client.Client
	pool     PoolInfoProvider
	notifier fwkdl.DiscoveryNotifier
}

func (r *podDiscoveryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !r.pool.PoolHasSynced() {
		return ctrl.Result{}, nil
	}

	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			for _, id := range allPodEndpointIDs(req.Name, req.Namespace, r.pool.PoolTargetPorts()) {
				r.notifier.Delete(id)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("unable to get pod: %w", err)
	}

	if !podutil.IsPodReady(pod) || !r.pool.PoolLabelsMatch(pod.Labels) {
		for _, id := range allPodEndpointIDs(pod.Name, pod.Namespace, r.pool.PoolTargetPorts()) {
			r.notifier.Delete(id)
		}
		return ctrl.Result{}, nil
	}

	for _, meta := range podToEndpointMetadata(pod, r.pool.PoolTargetPorts()) {
		r.notifier.Upsert(meta)
	}
	return ctrl.Result{}, nil
}

// podToEndpointMetadata converts a ready pod into one EndpointMetadata per active port.
func podToEndpointMetadata(pod *corev1.Pod, targetPorts []int) []*fwkdl.EndpointMetadata {
	metas := make([]*fwkdl.EndpointMetadata, 0, len(targetPorts))
	for idx, port := range targetPorts {
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
