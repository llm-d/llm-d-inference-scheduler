/*
Copyright 2025 The llm-d Authors

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

package proxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"k8s.io/utils/set"
)

const (
	inferencePoolResource = "inferencepools"
	resyncPeriod          = 30 * time.Second
)

// candidateGVRs in order of preference
//
// We maintain a prioritized list of GroupVersionResource (GVR) candidates to support
// environments where either the legacy or the new InferencePool CRD may be installed:
//
// 1. inference.networking.k8s.io/v1         ← Preferred (new official API group)
// 2. inference.networking.x-k8s.io/v1alpha2 ← Fallback (legacy experimental API group)

// The validator automatically detects which API is available by using the Kubernetes
// discovery API, selecting the first supported GVR in this list.
//
// This approach aligns with upstream Ingress Gateway (IGW) behavior, which also supports
// both API versions concurrently (see issue #462).
var candidateGVRs = []schema.GroupVersionResource{
	{Group: "inference.networking.k8s.io", Version: "v1", Resource: inferencePoolResource},
	{Group: "inference.networking.x-k8s.io", Version: "v1alpha2", Resource: inferencePoolResource},
}

// AllowlistValidator manages allowed prefill targets based on InferencePool resources
type AllowlistValidator struct {
	logger        logr.Logger
	dynamicClient dynamic.Interface
	namespace     string
	poolName      string
	enabled       bool

	gvr schema.GroupVersionResource // detected GVR

	// allowedTargets maps hostport -> bool for allowed prefill targets
	allowedTargets   set.Set[string]
	allowedTargetsMu sync.RWMutex

	// watchers for cleanup
	poolInformer   cache.SharedInformer
	podInformers   map[string]cache.SharedInformer
	podStopChans   map[string]chan struct{} // individual stop channels for pod informers
	podInformersMu sync.RWMutex
	stopCh         chan struct{}
}

// NewAllowlistValidator creates a new SSRF protection validator
func NewAllowlistValidator(enabled bool, namespace string, poolName string) (*AllowlistValidator, error) {
	if !enabled {
		return &AllowlistValidator{
			enabled: false,
		}, nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		overrides,
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes config (ensure running in a pod with proper RBAC): %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	var detectedGVR schema.GroupVersionResource
	for _, gvr := range candidateGVRs {
		supported, err := isGVRSupported(discoveryClient, gvr)
		if err != nil {
			return nil, fmt.Errorf("error checking GVR %s: %w", gvr.String(), err)
		}
		if supported {
			detectedGVR = gvr
			break
		}
	}

	if detectedGVR.Empty() {
		return nil, fmt.Errorf("no supported InferencePool API found; tried: %v", candidateGVRs)
	}

	return &AllowlistValidator{
		enabled:        true,
		dynamicClient:  dynamicClient,
		namespace:      namespace,
		poolName:       poolName,
		gvr:            detectedGVR,
		allowedTargets: set.New[string](),
		podInformers:   make(map[string]cache.SharedInformer),
		podStopChans:   make(map[string]chan struct{}),
		stopCh:         make(chan struct{}),
	}, nil
}

func isGVRSupported(discoveryClient discovery.DiscoveryInterface, gvr schema.GroupVersionResource) (bool, error) {
	apiResourceList, err := discoveryClient.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		// If the group/version doesn't exist, Kubernetes returns a "NotFound" error
		if errors.IsNotFound(err) {
			return false, nil // GroupVersion not supported
		}
		return false, fmt.Errorf("failed to discover resources for %s: %w", gvr.String(), err)
	}

	for _, resource := range apiResourceList.APIResources {
		if resource.Name == gvr.Resource {
			return true, nil
		}
	}
	return false, nil
}

// Start begins watching InferencePool resources and managing the allowlist
func (av *AllowlistValidator) Start(ctx context.Context) error {
	if !av.enabled {
		return nil
	}

	av.logger = klog.FromContext(ctx).WithName("allowlist-validator")
	av.logger.Info("starting SSRF protection allowlist validator",
		"namespace", av.namespace, "poolName", av.poolName, "gvr", av.gvr.String())

	// Create informer for the specific InferencePool resource
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			// List with field selector to get only the specific InferencePool
			options.FieldSelector = "metadata.name=" + av.poolName
			return av.dynamicClient.Resource(av.gvr).Namespace(av.namespace).List(ctx, options)
		},
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			// Watch the specific InferencePool by name using field selector
			options.FieldSelector = "metadata.name=" + av.poolName
			return av.dynamicClient.Resource(av.gvr).Namespace(av.namespace).Watch(ctx, options)
		},
	}

	av.poolInformer = cache.NewSharedInformer(lw, &unstructured.Unstructured{}, resyncPeriod)

	// Add event handlers
	_, _ = av.poolInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    av.onInferencePoolAdd,
		UpdateFunc: av.onInferencePoolUpdate,
		DeleteFunc: av.onInferencePoolDelete,
	})

	// Start the informer
	go av.poolInformer.Run(av.stopCh)

	// Wait for cache sync
	if !cache.WaitForCacheSync(av.stopCh, av.poolInformer.HasSynced) {
		return fmt.Errorf("failed to sync InferencePool cache within timeout (check RBAC permissions for inferencepools.%s and that pool '%s' exists)", av.gvr.Group, av.poolName)
	}

	av.logger.Info("allowlist validator started successfully")
	return nil
}

// Stop stops all watchers and cleans up resources
func (av *AllowlistValidator) Stop() {
	if !av.enabled {
		return
	}

	av.logger.Info("stopping allowlist validator")

	// Stop all pod informers first
	av.podInformersMu.Lock()
	for poolName, stopCh := range av.podStopChans {
		av.logger.V(4).Info("stopping pod informer", "pool", poolName)
		close(stopCh)
	}
	// Clear the maps
	av.podStopChans = make(map[string]chan struct{})
	av.podInformers = make(map[string]cache.SharedInformer)
	av.podInformersMu.Unlock()

	// Stop the main pool informer
	close(av.stopCh)
}

// IsAllowed checks if a given host:port combination is in the allowlist
func (av *AllowlistValidator) IsAllowed(hostPort string) bool {
	if !av.enabled {
		// If SSRF protection is disabled, allow all requests (backward compatibility)
		return true
	}

	// Clean up the hostPort input
	hostPort = av.normalizeHostPort(hostPort)

	av.allowedTargetsMu.RLock()
	defer av.allowedTargetsMu.RUnlock()

	allowed := av.allowedTargets.Has(hostPort)
	av.logger.V(4).Info("allowlist check", "hostPort", hostPort, "allowed", allowed)
	return allowed
}

// normalizeHostPort extracts the host part from a host:port string
func (av *AllowlistValidator) normalizeHostPort(hostPort string) string {
	// Use net.SplitHostPort to handle IPv6 addresses and ports
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		// If net.SplitHostPort fails, it's likely just a hostname without port
		av.logger.V(5).Info("could not parse host:port, treating as hostname",
			"input", hostPort,
			"error", err.Error())
		return hostPort
	}
	return host
}

// onInferencePoolAdd handles new InferencePool resources
func (av *AllowlistValidator) onInferencePoolAdd(obj interface{}) {
	pool := obj.(*unstructured.Unstructured)
	av.logger.Info("InferencePool added", "name", pool.GetName())
	av.updatePodsForPool(pool)
}

// onInferencePoolUpdate handles updated InferencePool resources
func (av *AllowlistValidator) onInferencePoolUpdate(_, newObj interface{}) {
	pool := newObj.(*unstructured.Unstructured)
	av.logger.Info("InferencePool updated", "name", pool.GetName())
	av.updatePodsForPool(pool)
}

// onInferencePoolDelete handles deleted InferencePool resources
func (av *AllowlistValidator) onInferencePoolDelete(obj interface{}) {
	pool := obj.(*unstructured.Unstructured)
	poolName := pool.GetName()
	av.logger.Info("InferencePool deleted", "name", poolName)

	// Stop watching pods for this pool
	av.podInformersMu.Lock()
	if stopCh, exists := av.podStopChans[poolName]; exists {
		close(stopCh) // properly stop the informer
		delete(av.podStopChans, poolName)
	}
	delete(av.podInformers, poolName)
	av.podInformersMu.Unlock()

	// Remove targets associated with this pool (simplified - removes all and rebuilds)
	av.rebuildAllowlist()
}

// updatePodsForPool starts or updates pod watching for a specific InferencePool
func (av *AllowlistValidator) updatePodsForPool(poolObj *unstructured.Unstructured) {
	poolName := poolObj.GetName()

	// Parse the pool spec to get selector
	spec, found, err := unstructured.NestedMap(poolObj.Object, "spec")
	if err != nil || !found {
		av.logger.Error(err, "InferencePool missing or invalid spec field", "name", poolName, "found", found)
		return
	}

	selectorData, found, err := unstructured.NestedMap(spec, "selector")
	if err != nil || !found {
		av.logger.Error(err, "InferencePool missing or invalid selector field", "name", poolName, "found", found)
		return
	}

	// Convert to labels.Selector
	labelSelector := labels.Set{}
	for k, v := range selectorData {
		labelSelector[k] = fmt.Sprintf("%v", v)
	}

	// Create or update pod informer for this selector
	av.createPodInformer(poolName, labelSelector.AsSelector())
}

// createPodInformer creates a new pod informer for the given selector
func (av *AllowlistValidator) createPodInformer(poolName string, selector labels.Selector) {
	av.podInformersMu.Lock()
	defer av.podInformersMu.Unlock()

	// Stop existing informer if it exists
	if _, exists := av.podInformers[poolName]; exists {
		if stopCh, stopExists := av.podStopChans[poolName]; stopExists {
			close(stopCh) // stop the existing informer
			delete(av.podStopChans, poolName)
		}
		delete(av.podInformers, poolName)
	}

	// Create new pod informer
	podLW := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.LabelSelector = selector.String()
			return av.dynamicClient.Resource(schema.GroupVersionResource{
				Group:    "",
				Version:  "v1",
				Resource: "pods",
			}).Namespace(av.namespace).List(context.TODO(), options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.LabelSelector = selector.String()
			return av.dynamicClient.Resource(schema.GroupVersionResource{
				Group:    "",
				Version:  "v1",
				Resource: "pods",
			}).Namespace(av.namespace).Watch(context.TODO(), options)
		},
	}

	podInformer := cache.NewSharedInformer(podLW, &unstructured.Unstructured{}, resyncPeriod)

	// Add event handlers
	_, _ = podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    av.onPodAdd,
		UpdateFunc: av.onPodUpdate,
		DeleteFunc: av.onPodDelete,
	})

	// Create individual stop channel for this informer
	podStopCh := make(chan struct{})

	av.podInformers[poolName] = podInformer
	av.podStopChans[poolName] = podStopCh

	// Start the informer with its own stop channel
	go podInformer.Run(podStopCh)
}

// onPodAdd handles new pods matching our selectors
func (av *AllowlistValidator) onPodAdd(obj interface{}) {
	pod := obj.(*unstructured.Unstructured)
	podIP, _, _ := unstructured.NestedString(pod.Object, "status", "podIP")
	av.logger.V(4).Info("Pod added", "name", pod.GetName(), "ip", podIP)
	av.rebuildAllowlist()
}

// onPodUpdate handles updated pods
func (av *AllowlistValidator) onPodUpdate(_, newObj interface{}) {
	pod := newObj.(*unstructured.Unstructured)
	podIP, _, _ := unstructured.NestedString(pod.Object, "status", "podIP")
	av.logger.V(4).Info("Pod updated", "name", pod.GetName(), "ip", podIP)
	av.rebuildAllowlist()
}

// onPodDelete handles deleted pods
func (av *AllowlistValidator) onPodDelete(obj interface{}) {
	pod := obj.(*unstructured.Unstructured)
	av.logger.V(4).Info("Pod deleted", "name", pod.GetName())
	av.rebuildAllowlist()
}

// rebuildAllowlist rebuilds the entire allowlist from current pod state
func (av *AllowlistValidator) rebuildAllowlist() {
	av.allowedTargetsMu.Lock()
	defer av.allowedTargetsMu.Unlock()

	// Clear existing allowlist
	av.allowedTargets = set.New[string]()

	av.podInformersMu.RLock()
	defer av.podInformersMu.RUnlock()
	// Rebuild from all pod informers
	for poolName, informer := range av.podInformers {
		store := informer.GetStore()
		for _, obj := range store.List() {
			pod := obj.(*unstructured.Unstructured)

			// Get pod phase and IP
			podIP, _, _ := unstructured.NestedString(pod.Object, "status", "podIP")

			// Only include pods with valid IPs
			if podIP != "" {
				// Add both IP and hostname variants
				av.addPodToAllowlist(pod, poolName)
			}
		}
	}

	av.logger.Info("rebuilt allowlist", "targetCount", len(av.allowedTargets), "targets", av.allowedTargets)
}

// addPodToAllowlist adds a pod's endpoints to the allowlist
func (av *AllowlistValidator) addPodToAllowlist(pod *unstructured.Unstructured, poolName string) {
	podIP, _, _ := unstructured.NestedString(pod.Object, "status", "podIP")
	if podIP != "" {
		av.allowedTargets.Insert(podIP)
	}

	podName := pod.GetName()
	if podName != "" {
		av.allowedTargets.Insert(podName)
	}

	av.logger.V(5).Info("added pod to allowlist", "pod", podName, "ip", podIP, "pool", poolName)
}
