package pd

import (
	"context"
	"errors"
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

	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/config"
	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/scorer"
)

const (
	// PrefillPodHeader is the HTTP header name used to indicate Prefill worker
	PrefillPodHeader = "x-prefiller-url"
)

// Scheduler implements the disaggreagted P/D scheduling logic
type Scheduler struct {
	threshold int
	pdEnabled bool
	store     Datastore
	prefill   requestcontrol.Scheduler
	decode    requestcontrol.Scheduler

	// prefixScorer is a prefix scorer which will be used for decission if prefill step is required
	// if pd is enabled, prefix scorers should be the same instance in all:
	// prefill scheduler, decode scheduler and prefixScorer
	prefixScorer *scorer.PrefixAwareScorer
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
// provided configuration.
func NewScheduler(ctx context.Context, schedCfg *config.Config, ds Datastore) (*Scheduler, error) {
	scheduler := &Scheduler{
		threshold:    schedCfg.PDThreshold,
		pdEnabled:    schedCfg.PDEnabled,
		store:        ds,
		prefixScorer: scorer.NewPrefixAwareScorer(ctx, nil),
	}

	scheduler.prefill = scheduling.NewSchedulerWithConfig(ds, scheduling.NewSchedulerConfig(
		[]plugins.PreSchedule{},
		[]plugins.Filter{&filter.PrefillFilter{}},
		scheduler.scorersFromConfig(ctx, schedCfg.PrefillSchedulerScorers),
		picker.NewMaxScorePicker(),
		[]plugins.PostSchedule{},
		[]plugins.PostResponse{},
	))

	scheduler.decode = scheduling.NewSchedulerWithConfig(ds, scheduling.NewSchedulerConfig(
		[]plugins.PreSchedule{},
		[]plugins.Filter{&filter.DecodeFilter{}},
		scheduler.scorersFromConfig(ctx, schedCfg.DecodeSchedulerScorers),
		picker.NewMaxScorePicker(),
		[]plugins.PostSchedule{},
		[]plugins.PostResponse{},
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

	if !s.pdEnabled {
		debugLog.Info("Disagregated prefill/decode disabled - scheduling to decode worker only")
		return s.decode.Schedule(ctx, req)
	}

	// find the best pod for decode
	// assumes that prefix scorer was activated
	decodeRes, err := s.decode.Schedule(ctx, req)

	if decodeRes.TargetPod == nil {
		logger.Info("No decode pod found, skipping scheduling")
		return nil, errors.New("no decode pod found")
	}

	// if the request is short enough, use the default scheduler
	hitPercentage := s.prefixScorer.GetCachedPercentage(decodeRes.TargetPod.GetPod().NamespacedName.String(), req.Prompt)
	if hitPercentage > 0 && (1.0-hitPercentage)*float64(len(req.Prompt)) < float64(s.threshold) {
		logger.Info("Non-cached suffix is smaller than threshold, using decode scheduler",
			"hitPercentage", hitPercentage)
		return decodeRes, err
	}

	logger.Info("Non-cached suffix is larger than threshold, using PD scheduler",
		"hitPercentage", hitPercentage)
	prefillRes, prefillErr := s.prefill.Schedule(ctx, req)

	if prefillErr == nil && prefillRes.TargetPod != nil { // record the prefill worker
		pool, err := s.store.PoolGet()
		if err != nil {
			debugLog.Error(err, "Get inference pool failed - scheduling to decode worker only")
			return s.decode.Schedule(ctx, req)
		}

		// TODO: should the scheme be conifgurable (e.g., https://)?
		prefillURL := fmt.Sprintf("http://%s:%d", prefillRes.TargetPod.GetPod().Address, pool.Spec.TargetPortNumber)
		if req.Headers == nil { // TODO should always be populated?
			req.Headers = make(map[string]string)
		}
		req.Headers[PrefillPodHeader] = prefillURL
	}

	debugLog.Info("Scheduling to separate Prefill and Decode workers")

	return decodeRes, nil // decode pod
}

// OnResponse normally processes all LLMResponses it is a no-op for the P/D
// scheduler.
func (s *Scheduler) OnResponse(_ context.Context, _ *types.LLMResponse, _ string) {
	// no-op
}

func (s *Scheduler) scorersFromConfig(ctx context.Context, scorersConfig map[string]int) map[plugins.Scorer]int {
	logger := log.FromContext(ctx)

	scorers := map[plugins.Scorer]int{}
	prefixWasAdded := false

	for scorerName, scorerWeight := range scorersConfig {
		switch scorerName {
		case config.KVCacheScorerName:
			scorer, err := scorer.NewKVCacheAwareScorer(ctx)
			if err == nil {
				scorers[scorer] = scorerWeight
			} else {
				logger.Error(err, "KVCache scorer creation failed")
			}
		case config.LoadAwareScorerName:
			scorers[&scorer.LoadAwareScorer{}] = scorerWeight
		case config.PrefixScorerName:
			// TODO - create config? based on what? - issue #55
			// use the same instance
			scorers[s.prefixScorer] = scorerWeight
			prefixWasAdded = true
		case config.SessionAwareScorerName:
			scorers[scorer.NewSessionAffinity()] = scorerWeight
		}
	}

	if !prefixWasAdded {
		scorers[s.prefixScorer] = 0.0
	}

	return scorers
}
