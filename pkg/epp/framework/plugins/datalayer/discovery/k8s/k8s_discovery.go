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

// Package k8s provides EndpointDiscovery implementations that discover inference
// endpoints by watching Kubernetes pods. Both plugins own their ctrl.Manager
// internally and use datalayer.BindNotificationSource with a podDiscoveryExtractor
// to translate pod lifecycle events into DiscoveryNotifier calls.
package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/llm-d/llm-d-inference-scheduler/apix/v1alpha2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	k8sdiscovery "k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	v1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/controller"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/datalayer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/datastore"
	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/discovery/inject"
	dlnotifications "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/source/notifications"
)

const (
	InferencePoolPluginType  = "inference-pool-discovery"
	StaticSelectorPluginType = "static-selector-discovery"
)

var pluginScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(pluginScheme))
	utilruntime.Must(v1alpha2.Install(pluginScheme))
	utilruntime.Must(v1.Install(pluginScheme))
}

// DatastoreProvider and DatalayerBinder are defined in the inject package so
// that both K8s and non-K8s plugins can implement them without importing this
// leaf package.
type (
	DatastoreProvider = inject.DatastoreProvider
	DatalayerBinder   = inject.DatalayerBinder
)

// ---- baseK8sDiscovery -- shared state and behaviour -----------------------

// baseK8sDiscovery holds the fields and methods common to both K8s discovery
// plugins, eliminating duplication of ds, dlRuntime, and the final manager
// start sequence.
type baseK8sDiscovery struct {
	ds        datastore.Datastore
	dlRuntime *datalayer.Runtime
}

func (b *baseK8sDiscovery) SetDatastore(ds datastore.Datastore) { b.ds = ds }
func (b *baseK8sDiscovery) BindDatalayer(rt *datalayer.Runtime) { b.dlRuntime = rt }

// runManager executes the steps that are identical in every K8s discovery plugin:
// bind pod discovery, bind notification sources if a datalayer runtime is
// configured, then start the manager.
func (b *baseK8sDiscovery) runManager(ctx context.Context, mgr ctrl.Manager, notifier fwkdl.DiscoveryNotifier, pluginName string) error {
	podSrc := dlnotifications.NewK8sNotificationSource("k8s-notification", "pod-discovery", podDiscoveryGVK)
	podExt := newPodDiscoveryExtractor(b.ds, notifier)
	if err := datalayer.BindNotificationSource(podSrc, []fwkdl.NotificationExtractor{podExt}, mgr); err != nil {
		return fmt.Errorf("%s: pod discovery: %w", pluginName, err)
	}
	// If a datalayer runtime is configured, bind K8s notification sources
	// (push-based metrics) to this manager before starting it.
	if b.dlRuntime != nil {
		if err := b.dlRuntime.Start(ctx, mgr); err != nil {
			return fmt.Errorf("%s: failed to bind datalayer notification sources: %w", pluginName, err)
		}
	}
	return mgr.Start(ctx)
}

// ---- InferencePoolDiscoveryPlugin ------------------------------------------

type inferencePoolParams struct {
	PoolName       string `json:"poolName"`
	PoolNamespace  string `json:"poolNamespace"`
	PoolGroup      string `json:"poolGroup"`
	LeaderElection bool   `json:"leaderElection"`
}

// InferencePoolDiscoveryPlugin discovers endpoints via an InferencePool CRD.
type InferencePoolDiscoveryPlugin struct {
	baseK8sDiscovery
	typedName     fwkplugin.TypedName
	poolName      string
	poolNamespace string
	poolGroup     string
	leaderElect   bool
}

var _ fwkdl.EndpointDiscovery = (*InferencePoolDiscoveryPlugin)(nil)
var _ DatastoreProvider = (*InferencePoolDiscoveryPlugin)(nil)
var _ DatalayerBinder = (*InferencePoolDiscoveryPlugin)(nil)

// GetPoolName returns the InferencePool name this plugin is configured to watch.
func (k *InferencePoolDiscoveryPlugin) GetPoolName() string { return k.poolName }

// SetPoolName sets the InferencePool name, used by the runner to inject --pool-name
// when the factory parameter was omitted.
func (k *InferencePoolDiscoveryPlugin) SetPoolName(name string) { k.poolName = name }

// GetPoolNamespace returns the namespace this plugin is configured to watch.
func (k *InferencePoolDiscoveryPlugin) GetPoolNamespace() string { return k.poolNamespace }

// SetPoolNamespace sets the namespace, used by the runner to inject --pool-namespace / NAMESPACE
// when the factory parameter was omitted.
func (k *InferencePoolDiscoveryPlugin) SetPoolNamespace(ns string) { k.poolNamespace = ns }

func NewInferencePoolDiscoveryPlugin(poolName, poolNamespace, poolGroup string, leaderElect bool) *InferencePoolDiscoveryPlugin {
	return &InferencePoolDiscoveryPlugin{
		typedName:     fwkplugin.TypedName{Type: InferencePoolPluginType, Name: InferencePoolPluginType},
		poolName:      poolName,
		poolNamespace: poolNamespace,
		poolGroup:     poolGroup,
		leaderElect:   leaderElect,
	}
}

func InferencePoolFactory(name string, parameters json.RawMessage, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	p := &inferencePoolParams{PoolGroup: "inference.networking.k8s.io"}
	if len(parameters) > 0 {
		if err := json.Unmarshal(parameters, p); err != nil {
			return nil, fmt.Errorf("%s: failed to parse parameters: %w", InferencePoolPluginType, err)
		}
	}
	if name == "" {
		name = InferencePoolPluginType
	}
	return &InferencePoolDiscoveryPlugin{
		typedName:     fwkplugin.TypedName{Type: InferencePoolPluginType, Name: name},
		poolName:      p.PoolName,
		poolNamespace: p.PoolNamespace,
		poolGroup:     p.PoolGroup,
		leaderElect:   p.LeaderElection,
	}, nil
}

func (k *InferencePoolDiscoveryPlugin) TypedName() fwkplugin.TypedName { return k.typedName }

func (k *InferencePoolDiscoveryPlugin) Start(ctx context.Context, notifier fwkdl.DiscoveryNotifier) error {
	if k.ds == nil {
		return errors.New("inference-pool-discovery: datastore not set; call SetDatastore before Start")
	}
	if k.poolName == "" {
		return errors.New("inference-pool-discovery: poolName is required; set poolName in plugin parameters or provide --pool-name on the command line")
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("inference-pool-discovery: failed to get K8s REST config: %w", err)
	}

	namespace := k.poolNamespace
	if namespace == "" {
		namespace = "default"
	}
	gknn := common.GKNN{
		NamespacedName: types.NamespacedName{Name: k.poolName, Namespace: namespace},
		GroupKind:      schema.GroupKind{Group: k.poolGroup, Kind: "InferencePool"},
	}

	dc, err := k8sdiscovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("inference-pool-discovery: failed to create discovery client: %w", err)
	}
	hasObjective := gvkInstalled(dc, v1alpha2.GroupVersion.Group, v1alpha2.GroupVersion.Version, "InferenceObjective")
	hasModelRewrite := gvkInstalled(dc, v1alpha2.GroupVersion.Group, v1alpha2.GroupVersion.Version, "InferenceModelRewrite")

	cacheOpts := cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {Namespaces: map[string]cache.Config{namespace: {}}},
			&v1.InferencePool{}: {Namespaces: map[string]cache.Config{namespace: {
				FieldSelector: fields.SelectorFromSet(fields.Set{"metadata.name": k.poolName}),
			}}},
		},
	}
	if hasObjective {
		cacheOpts.ByObject[&v1alpha2.InferenceObjective{}] = cache.ByObject{Namespaces: map[string]cache.Config{namespace: {}}}
	}
	if hasModelRewrite {
		cacheOpts.ByObject[&v1alpha2.InferenceModelRewrite{}] = cache.ByObject{Namespaces: map[string]cache.Config{namespace: {}}}
	}

	mgrOpts := ctrl.Options{
		Scheme:  pluginScheme,
		Cache:   cacheOpts,
		Metrics: metricsserver.Options{BindAddress: "0"},
	}
	if k.leaderElect {
		mgrOpts.LeaderElection = true
		mgrOpts.LeaderElectionResourceLock = "leases"
		mgrOpts.LeaderElectionID = fmt.Sprintf("epp-%s-%s.inference-pool-discovery", namespace, k.poolName)
		mgrOpts.LeaderElectionNamespace = namespace
		mgrOpts.LeaderElectionReleaseOnCancel = true
	}

	mgr, err := ctrl.NewManager(cfg, mgrOpts)
	if err != nil {
		return fmt.Errorf("inference-pool-discovery: failed to create manager: %w", err)
	}

	// TODO: convert to NotificationExtractor via BindNotificationSource for
	// consistency with pod-discovery.
	if err := (&controller.InferencePoolReconciler{
		Datastore: k.ds,
		Reader:    mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("inference-pool-discovery: InferencePoolReconciler: %w", err)
	}

	if hasObjective {
		// TODO: convert to NotificationExtractor via BindNotificationSource for
		// consistency with pod-discovery.
		if err := (&controller.InferenceObjectiveReconciler{
			Datastore: k.ds, Reader: mgr.GetClient(), PoolGKNN: gknn,
		}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("inference-pool-discovery: InferenceObjectiveReconciler: %w", err)
		}
	}
	if hasModelRewrite {
		// TODO: convert to NotificationExtractor via BindNotificationSource for
		// consistency with pod-discovery.
		if err := (&controller.InferenceModelRewriteReconciler{
			Datastore: k.ds, Reader: mgr.GetClient(), PoolGKNN: gknn,
		}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("inference-pool-discovery: InferenceModelRewriteReconciler: %w", err)
		}
	}

	return k.runManager(ctx, mgr, notifier, InferencePoolPluginType)
}

// ---- StaticSelectorDiscoveryPlugin -----------------------------------------

type staticSelectorParams struct {
	EndpointSelector    string `json:"endpointSelector"`
	EndpointTargetPorts []int  `json:"endpointTargetPorts"`
	Namespace           string `json:"namespace"`
}

// StaticSelectorDiscoveryPlugin discovers endpoints by watching pods matching a
// fixed label selector from plugin parameters.
type StaticSelectorDiscoveryPlugin struct {
	baseK8sDiscovery
	typedName   fwkplugin.TypedName
	selectorMap labels.Set
	targetPorts []int
	namespace   string
}

var _ fwkdl.EndpointDiscovery = (*StaticSelectorDiscoveryPlugin)(nil)
var _ DatastoreProvider = (*StaticSelectorDiscoveryPlugin)(nil)
var _ DatalayerBinder = (*StaticSelectorDiscoveryPlugin)(nil)

func NewStaticSelectorDiscoveryPlugin(selector, namespace string, targetPorts []int) (*StaticSelectorDiscoveryPlugin, error) {
	selectorMap, err := labels.ConvertSelectorToLabelsMap(selector)
	if err != nil {
		return nil, fmt.Errorf("static-selector-discovery: invalid endpointSelector: %w", err)
	}
	return &StaticSelectorDiscoveryPlugin{
		typedName:   fwkplugin.TypedName{Type: StaticSelectorPluginType, Name: StaticSelectorPluginType},
		selectorMap: selectorMap,
		targetPorts: targetPorts,
		namespace:   namespace,
	}, nil
}

func StaticSelectorFactory(name string, parameters json.RawMessage, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	p := &staticSelectorParams{Namespace: "default"}
	if len(parameters) > 0 {
		if err := json.Unmarshal(parameters, p); err != nil {
			return nil, fmt.Errorf("%s: failed to parse parameters: %w", StaticSelectorPluginType, err)
		}
	}
	if p.EndpointSelector == "" {
		return nil, fmt.Errorf("%s: 'endpointSelector' parameter is required", StaticSelectorPluginType)
	}
	if len(p.EndpointTargetPorts) == 0 {
		return nil, fmt.Errorf("%s: 'endpointTargetPorts' parameter is required", StaticSelectorPluginType)
	}
	selectorMap, err := labels.ConvertSelectorToLabelsMap(p.EndpointSelector)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid endpointSelector: %w", StaticSelectorPluginType, err)
	}
	if name == "" {
		name = StaticSelectorPluginType
	}
	return &StaticSelectorDiscoveryPlugin{
		typedName:   fwkplugin.TypedName{Type: StaticSelectorPluginType, Name: name},
		selectorMap: selectorMap,
		targetPorts: p.EndpointTargetPorts,
		namespace:   p.Namespace,
	}, nil
}

func (s *StaticSelectorDiscoveryPlugin) TypedName() fwkplugin.TypedName { return s.typedName }

func (s *StaticSelectorDiscoveryPlugin) Start(ctx context.Context, notifier fwkdl.DiscoveryNotifier) error {
	if s.ds == nil {
		return errors.New("static-selector-discovery: datastore not set; call SetDatastore before Start")
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("static-selector-discovery: failed to get K8s REST config: %w", err)
	}

	pool := datalayer.NewEndpointPool(s.namespace, StaticSelectorPluginType)
	pool.Selector = s.selectorMap
	pool.TargetPorts = s.targetPorts
	s.ds.WithEndpointPool(pool)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  pluginScheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Pod{}: {Namespaces: map[string]cache.Config{s.namespace: {}}},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("static-selector-discovery: failed to create manager: %w", err)
	}

	return s.runManager(ctx, mgr, notifier, StaticSelectorPluginType)
}

// ---- helpers ---------------------------------------------------------------

func gvkInstalled(dc k8sdiscovery.DiscoveryInterface, group, version, kind string) bool {
	list, err := dc.ServerResourcesForGroupVersion(group + "/" + version)
	if err != nil {
		return false
	}
	for _, r := range list.APIResources {
		if r.Kind == kind {
			return true
		}
	}
	return false
}
