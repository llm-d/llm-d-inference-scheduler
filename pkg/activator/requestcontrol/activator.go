// Package requestcontrol implements the activator logic for controlling request flow
package requestcontrol

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/datastore"
	v1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	errutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/error"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	autoscaling "k8s.io/api/autoscaling/v1"
	"k8s.io/client-go/discovery"
	cached "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/scale"
)

const (
	objectAPIVersionKey         = "activator.llm-d.ai/target-apiversion"
	objectkindKey               = "activator.llm-d.ai/target-kind"
	objectNameKey               = "activator.llm-d.ai/target-name"
	scaleFromZeroGracePeriodKey = "activator.llm-d.ai/scale-from-zero-grace-period" // Optional annotation

	// DefaultScaleFromZeroGracePeriod is the time we will wait for a scale-from-zero decision to complete
	DefaultScaleFromZeroGracePeriod = 60 * time.Second

	// DefaultScaleDownDelay is the amount of time that must pass before a scale-down decision is applied
	DefaultScaleDownDelay = 120 * time.Second

	// ScaleToZeroRequestRetentionPeriod it is the amount of time we will wait before releasing the request after a scale from zero event
	ScaleToZeroRequestRetentionPeriod = 5 * time.Second
)

type scaledObjectData struct {
	name             string
	scaleGracePeriod time.Duration
	numReplicas      int32
	scaleObject      *autoscaling.Scale
}

// Activator implements the logic for activating replicas based on incoming requests.
type Activator struct {
	DynamicClient *dynamic.DynamicClient
	ScaleClient   scale.ScalesGetter
	Mapper        meta.RESTMapper
	datastore     datastore.Datastore

	scalingUp           bool
	guard               chan struct{}
	scalingUpAndGuardMu sync.Mutex
}

// NewActivatorWithConfig creates a new Activator with the provided REST config and Datastore.
func NewActivatorWithConfig(config *rest.Config, datastore datastore.Datastore) (*Activator, error) {
	scaleClient, mapper, err := initScaleClient(config)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Activator{
		datastore:     datastore,
		DynamicClient: dynamicClient,
		Mapper:        mapper,
		ScaleClient:   scaleClient}, nil
}

// MayActivate checks if the inferencePool associated with the request is scaled to one or more replicas
func (a *Activator) MayActivate(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// Get InferencePool Info
	pool, err := a.datastore.PoolGet()
	if err != nil {
		return err
	}

	logger.V(logutil.TRACE).Info("InferencePool found", "name", pool.Name, "namespace", pool.Namespace)

	// First: check if the inferencePool is currently scaling up from zero replicas
	if scalingUp, guard := a.isScalingUp(); scalingUp {
		logger.V(logutil.DEBUG).Info("InferencePool is currently scaling up. Waiting for it to be done.")

		a.waitOnGuard(guard, DefaultScaleFromZeroGracePeriod)
		return nil // After scaling up is done, allow the request to proceed even if scaling failed
	}

	// Then: block until the inferencePool has enough replicas and is ready
	if ready := a.InferencePoolReady(ctx, pool); !ready {
		return errutil.Error{Code: errutil.ServiceUnavailable, Msg: "failed to find active candidate pods in the inferencePool for serving the request"}
	}

	// Reset the Deactivator ticker for scale to zero monitoring
	scaleDownDelay := DefaultScaleDownDelay
	if value, found := getOptionalPoolAnnotation(logger, scaleDownDelayKey, pool); found {
		scaleDownDelay, _ = time.ParseDuration(value)
	}

	a.datastore.ResetTicker(scaleDownDelay)
	return nil
}

// InferencePoolReady checks if the inferencePool has enough replicas and is ready
func (a *Activator) InferencePoolReady(ctx context.Context, pool *v1.InferencePool) bool {
	logger := log.FromContext(ctx)
	namespace := pool.Namespace

	// TODO: store annotation values in datastore to avoid reading them every time
	// verify required inferencePool annotations
	valid := verifyPoolObjectAnnotations(logger, pool)
	if !valid {
		return false
	}

	// extract optional inferencePool annotation if it exists, otherwise use a default value
	scaleGracePeriod := DefaultScaleFromZeroGracePeriod
	if value, found := getOptionalPoolAnnotation(logger, scaleFromZeroGracePeriodKey, pool); found {
		scaleGracePeriod, _ = time.ParseDuration(value)
	}

	// Get the scale subresource for the target inferencePool object
	gvr, err := GetResourceForKind(a.Mapper, pool.Annotations[objectAPIVersionKey], pool.Annotations[objectkindKey])
	if err != nil {
		msg := "Failed to parse Group, Version, Kind, Resource"
		logger.Error(err, msg, "apiVersion", pool.Annotations[objectAPIVersionKey], "kind", pool.Annotations[objectkindKey])
		return false
	}

	gr := gvr.GroupResource()
	scaleObject, err := a.ScaleClient.Scales(namespace).Get(ctx, gr, pool.Annotations[objectNameKey], metav1.GetOptions{})
	if err != nil {
		logger.Error(err, "Error getting scale subresource object")
		return true
	}

	// Common case: enough replicas?
	if scaleObject.Spec.Replicas > 0 {
		if a.inferencePoolPodsReady(logger, namespace, pool.Annotations[objectNameKey], scaleObject.Spec.Replicas, scaleGracePeriod, gvr) {
			// Scale object exists and has no zero running replicas then do not scale it
			logger.V(logutil.DEBUG).Info(fmt.Sprintf("Scale Object %s have at least one replica ready. Skipping scaling from zero", scaleObject.Name))
			return true
		}
	}

	// Need to scale inferencePool workload from zero to one replicas
	numReplicas := int32(1)
	scaleData := scaledObjectData{name: pool.Annotations[objectNameKey], scaleGracePeriod: DefaultScaleFromZeroGracePeriod, numReplicas: numReplicas, scaleObject: scaleObject}
	return a.scaleInferencePool(ctx, logger, namespace, scaleData, gr, gvr)
}

func (a *Activator) inferencePoolPodsReady(logger logr.Logger, namespace, objname string, numReplicas int32, scaleGracePeriod time.Duration, gvr schema.GroupVersionResource) bool {
	// Don't inherit the parent context to avoid cancellation
	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, scaleGracePeriod, false, func(ctx context.Context) (done bool, err error) {

		a.datastore.ResetTicker(DefaultScaleDownDelay) // turn off the deactivator during scale from zero events

		unstructuredObj, err := a.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, objname, metav1.GetOptions{})
		if err != nil {
			logger.Error(err, "Error getting unstructured object")
			return false, nil // continue polling
		}

		// NOTE: this assumes that the target object has a status.readyReplicas field
		if readyReplicas, ok := unstructuredObj.Object["status"].(map[string]any)["readyReplicas"].(int64); ok {
			if numReplicas == int32(readyReplicas) {
				logger.V(logutil.DEBUG).Info("Candidate pods are READY")
				return true, nil
			}
			logger.V(logutil.DEBUG).Info("Candidate pods are NOT READY")
		} else {
			logger.V(logutil.DEBUG).Info("Object status.readyReplicas field is not set yet - candidate pods for serving the request are NOT READY ")
			return false, nil
		}

		return false, nil
	})

	return err == nil
}

func (a *Activator) scaleInferencePool(ctx context.Context, logger logr.Logger, namespace string, objData scaledObjectData, gr schema.GroupResource, gvr schema.GroupVersionResource) bool {
	a.beginScalingUp()
	defer a.endScalingUp()

	// Modify the desired replicas
	objData.scaleObject.Spec.Replicas = objData.numReplicas

	// Update the Scale object
	_, err := a.ScaleClient.Scales(namespace).Update(ctx, gr, objData.scaleObject, metav1.UpdateOptions{})
	if err != nil {
		logger.Error(err, "Error increasing Scale Object number of replicas to one")
		return false
	}
	logger.Info(fmt.Sprintf("Scale Object %s in namespace %s scaled up to %d replicas with scale grace period %d \n", objData.name, namespace, objData.numReplicas, int(objData.scaleGracePeriod)))

	// Wait for the pods to be ready
	ready := a.inferencePoolPodsReady(logger, namespace, objData.name, objData.numReplicas, objData.scaleGracePeriod, gvr)
	if ready {
		// Give some time for the Endpoint Picker to pick up the newly created pods
		time.Sleep(ScaleToZeroRequestRetentionPeriod)
		return true
	}
	return false
}

func initScaleClient(config *rest.Config) (scale.ScalesGetter, meta.RESTMapper, error) {
	clientset, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	cachedDiscoveryClient := cached.NewMemCacheClient(clientset)
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscoveryClient)

	return scale.New(
		clientset.RESTClient(), restMapper,
		dynamic.LegacyAPIPathResolverFunc,
		scale.NewDiscoveryScaleKindResolver(clientset),
	), restMapper, nil
}

func verifyPoolObjectAnnotations(logger logr.Logger, pool *v1.InferencePool) bool {
	if _, ok := pool.Annotations[objectAPIVersionKey]; !ok {
		logger.Info(fmt.Sprintf("Annotation '%s' not found on pool '%s'", objectAPIVersionKey, pool.Name))
		return false
	}
	if _, ok := pool.Annotations[objectkindKey]; !ok {
		logger.Info(fmt.Sprintf("Annotation '%s' not found on pool '%s'", objectkindKey, pool.Name))
		return false
	}
	if _, ok := pool.Annotations[objectNameKey]; !ok {
		logger.Info(fmt.Sprintf("Annotation '%s' not found on pool '%s'", objectNameKey, pool.Name))
		return false
	}
	return true
}

func getOptionalPoolAnnotation(logger logr.Logger, annotationKey string, pool *v1.InferencePool) (string, bool) {
	if value, ok := pool.Annotations[annotationKey]; ok {
		return value, true
	}
	logger.V(logutil.DEBUG).Info(fmt.Sprintf("Annotation '%s' not found on pool '%s'", annotationKey, pool.Name))
	return "", false
}

func (a *Activator) beginScalingUp() {
	a.scalingUpAndGuardMu.Lock()
	defer a.scalingUpAndGuardMu.Unlock()

	a.scalingUp = true
	a.guard = make(chan struct{})
}

func (a *Activator) endScalingUp() {
	a.scalingUpAndGuardMu.Lock()
	defer a.scalingUpAndGuardMu.Unlock()

	a.scalingUp = false
	close(a.guard)
	a.guard = nil
}

func (a *Activator) isScalingUp() (bool, chan struct{}) {
	a.scalingUpAndGuardMu.Lock()
	defer a.scalingUpAndGuardMu.Unlock()

	return a.scalingUp, a.guard
}

// waitOnGuard blocks until the InferencePool is marked as ready or the timeout is reached.
func (a *Activator) waitOnGuard(guard <-chan struct{}, timeout time.Duration) {
	select {
	case <-time.After(timeout):
		return
	case <-guard:
	}
}
