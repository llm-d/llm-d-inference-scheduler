package requestcontrol

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/activator/datastore"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"k8s.io/client-go/scale"
)

const (
	scaleDownDelayKey = "activator.llm-d.ai/scale-down-delay" // Optional annotation
)

// Deactivator handles scaling down InferencePools to zero replicas after a period of idleness.
type Deactivator struct {
	DynamicClient *dynamic.DynamicClient
	ScaleClient   scale.ScalesGetter
	Mapper        meta.RESTMapper
	datastore     *datastore.Datastore
}

// DeactivatorWithConfig creates a new Deactivator with the provided REST config and Datastore.
func DeactivatorWithConfig(config *rest.Config, datastore *datastore.Datastore) (*Deactivator, error) {
	scaleClient, mapper, err := initScaleClient(config)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Deactivator{
		datastore:     datastore,
		DynamicClient: dynamicClient,
		Mapper:        mapper,
		ScaleClient:   scaleClient}, nil
}

// MonitorInferencePoolIdleness monitors the InferencePool for idleness and scales it down to zero replicas
func (da *Deactivator) MonitorInferencePoolIdleness(ctx context.Context) {
	logger := log.FromContext(ctx)
	ds := *(da.datastore)

	ds.ResetTicker(DefaultScaleDownDelay)
	defer ds.StopTicker()

	ticker := ds.GetTicker()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Context cancelled, stopping deactivator")
			return
		case <-ticker.C:
			logger.V(logutil.DEBUG).Info("Deactivator Time check for inferencePool idleness: " + time.Now().Format("15:04:05"))

			// Get InferencePool Info
			pool, err := ds.PoolGet()
			if err != nil {
				logger.V(logutil.TRACE).Info("InferencePool found", "name", pool.Name, "namespace", pool.Namespace)
				continue
			}

			// Verify required inferencePool annotations
			valid := verifyPoolObjectAnnotations(logger, pool)
			if !valid {
				logger.V(logutil.TRACE).Info("InferencePool missing required annotations for pool", "name", pool.Name, "namespace", pool.Namespace)
				continue
			}

			gvr, err := GetResourceForKind(da.Mapper, pool.Annotations[objectAPIVersionKey], pool.Annotations[objectkindKey])
			if err != nil {
				logger.Error(err, "Failed to parse Group, Version, Kind, Resource", "apiVersion", pool.Annotations[objectAPIVersionKey], "kind", pool.Annotations[objectkindKey])
				continue
			}

			gr := gvr.GroupResource()

			scaleObject, err := da.ScaleClient.Scales(pool.Namespace).Get(ctx, gr, pool.Annotations[objectNameKey], metav1.GetOptions{})
			if err != nil {
				logger.Error(err, "Error getting scale subresource object")
				continue
			}

			// Scale inferencePool to zero replicas
			scaleObject.Spec.Replicas = 0
			_, err = da.ScaleClient.Scales(pool.Namespace).Update(ctx, gr, scaleObject, metav1.UpdateOptions{})
			if err != nil {
				logger.Error(err, "InferencePool was not successfully scale down to zero replica")
				continue
			}

			logger.V(logutil.DEBUG).Info(fmt.Sprintf("InferencePool '%s' was successfully scale down to zero replica", pool.Name))
		}
	}
}
