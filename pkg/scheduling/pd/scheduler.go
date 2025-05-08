package pd

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/picker"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
)

const (
	// PrefillPodHeader is the HTTP header name used to indicate Prefill worker
	PrefillPodHeader = "x-prefiller-url"
)

// Scheduler implements the disaggreagted P/D scheduling logic
type Scheduler struct {
	threshold  int
	targetPort int32
	store      scheduling.Datastore
	prefill    requestcontrol.Scheduler
	decode     requestcontrol.Scheduler
}

var _ requestcontrol.Scheduler = &Scheduler{} // validate interface conformance

// Datastore portion used by scheduler
type Datastore interface {
	// InferencePool operations
	PoolGet() (*v1alpha2.InferencePool, error)
	// PodMetrics operations
	PodGetAll() []backendmetrics.PodMetrics
}

// NewScheduler returns a new disaggregated Prefill/Decode filter, using the
// provided prompt length threshold.
// TODO: accept a configuration object with pd-enable, threshold,
// scorers and their weights, etc. (tracked in issue #9).
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
	debugLog := logger.V(logutil.DEBUG)

	scheduleStart := time.Now()
	defer func() {
		metrics.RecordSchedulerE2ELatency(time.Since(scheduleStart))
	}()

	if len(req.Prompt) < s.threshold { // schedule on decode only (TODO: or p/d disabled)
		debugLog.Info("Scheduling to decode worker only")
		return s.decode.Schedule(ctx, req)
	}

	debugLog.Info("Scheduling to separate Prefill and Decode workers")

	res, err := s.prefill.Schedule(ctx, req) // prefill pod
	if err != nil {
		return nil, err
	}

	if res.TargetPod != nil { // record the prefill worker
		// TODO: should the scheme be conifgurable (e.g., https://)?
		prefillURL := fmt.Sprintf("http://%s:%d", res.TargetPod.GetPod().Address, s.targetPort)
		if req.Headers == nil { // TODO should always be populated?
			req.Headers = make(map[string]string)
		}
		req.Headers[PrefillPodHeader] = prefillURL
	}

	return s.decode.Schedule(ctx, req) // decode pod
}
