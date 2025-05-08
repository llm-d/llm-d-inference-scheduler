package pd

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/handlers"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/picker"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
)

const (
	prefillPodHeader = "x-prefiller-url"
)

// Scheduler implements the disaggreagted P/D scheduling logic
type Scheduler struct {
	threshold  int
	targetPort int32
	store      scheduling.Datastore
	prefill    handlers.Scheduler
	decode     handlers.Scheduler
}

var _ handlers.Scheduler = &Scheduler{} // validate interface conformance

// Datastore portion used by scheduler
type Datastore interface {
	// InferencePool operations
	PoolGet() (*v1alpha2.InferencePool, error)
	// PodMetrics operations
	PodGetAll() []backendmetrics.PodMetrics
}

// NewScheduler returns a new disaggregated Prefill/Decode filter, using the
// provided prompt length threshold.
func NewScheduler(threshold int, ds Datastore) (*Scheduler, error) {
	pool, err := ds.PoolGet()
	if err != nil {
		return nil, err
	}

	scheduler := &Scheduler{
		threshold:  threshold,
		targetPort: pool.Spec.TargetPortNumber,
		store:      ds,
	}

	scheduler.prefill = scheduling.NewSchedulerWithConfig(ds, scheduling.NewSchedulerConfig(
		[]plugins.PreSchedule{},
		[]plugins.Filter{&filter.PrefillFilter{}},
		map[plugins.Scorer]int{}, // TODO: KVCacheAware, LoadBased and weights
		picker.NewMaxScorePicker(),
		[]plugins.PostSchedule{},
	))
	scheduler.decode = scheduling.NewSchedulerWithConfig(ds, scheduling.NewSchedulerConfig(
		[]plugins.PreSchedule{},
		[]plugins.Filter{&filter.DecodeFilter{}},
		map[plugins.Scorer]int{}, // TODO: KVCacheAware, LoadBased
		picker.NewMaxScorePicker(),
		[]plugins.PostSchedule{},
	))
	return scheduler, nil
}

// Schedule uses (up to) two internal schedulers to process requests.
// If the request prompt is short (as defined by the configured threshold)
// the scheduler use the default behavior ("Decode scheduler").
// If the request prompt is long enough to warrant disaggregated prefill-decode,
// both the Prefill and Decode schedulers are invoked. In the case of the
// Prefill scheduler, the selected Pod's URL is saved in a header
// and communicated back to the inference gateway.
func (s *Scheduler) Schedule(ctx context.Context, req *types.LLMRequest) (*types.Result, error) {
	logger := log.FromContext(ctx).WithName("PD").WithValues("request", req)
	loggerDebug := logger.V(logutil.DEBUG)

	scheduleStart := time.Now()
	defer func() {
		metrics.RecordSchedulerE2ELatency(time.Since(scheduleStart))
	}()

	if len(req.Prompt) < s.threshold { // schedule on decode only
		fmt.Println("==== short prompt, decode only")
		return s.decode.Schedule(ctx, req)
	}

	// prompt is over threshold - schedule both prefill and decode workers
	sCtx := types.NewSchedulingContext(ctx, req, types.ToSchedulerPodMetrics(s.store.PodGetAll()))
	loggerDebug.Info(fmt.Sprintf("Scheduling a request, Metrics: %+v", sCtx.PodsSnapshot))

	// prefill pod
	res, err := s.prefill.Schedule(sCtx, req)
	if err != nil {
		return nil, err
	}

	prefillURL := ""
	if res.TargetPod != nil {
		// TODO: should the scheme be conifgurable (e.g., https://)?
		prefillURL = fmt.Sprintf("http://%s:%d", res.TargetPod.GetPod().Address, s.targetPort)
	}

	// decode pod
	res, err = s.decode.Schedule(sCtx, req)
	if err != nil {
		return nil, err
	}
	if prefillURL != "" { // update decode worker of prefill URL
		sCtx.MutatedHeaders[prefillPodHeader] = prefillURL
	}
	return res, nil
}
