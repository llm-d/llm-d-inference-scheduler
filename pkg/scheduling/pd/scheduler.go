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
	k8sfilter "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/filter"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/picker"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/prefix"
	k8sscorer "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/scorer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	envutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/plugins/scorer"
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

	scheduler.prefill = scheduling.NewSchedulerWithConfig(
		ds,
		scheduler.generateSchedulerConfig(ctx, schedCfg.PrefillSchedulerPlugins,
			&filter.PrefillFilter{}),
	)

	scheduler.decode = scheduling.NewSchedulerWithConfig(
		ds,
		scheduler.generateSchedulerConfig(ctx, schedCfg.DecodeSchedulerPlugins,
			&filter.DecodeFilter{}),
	)

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

	if decodeRes == nil || decodeRes.TargetPod == nil {
		logger.Info("No decode pod found, skipping scheduling")
		return nil, errors.New("no decode pod found")
	}

	// if the request is short enough, use the default scheduler
	hitPercentage := s.prefixScorer.GetCachedPercentage(decodeRes.TargetPod.GetPod().NamespacedName.String(), req.Prompt)
	if (1.0-hitPercentage)*float64(len(req.Prompt)) < float64(s.threshold) {
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

// OnResponse normally processes all LLMResponses - forwards all responses to the decode scheduler
func (s *Scheduler) OnResponse(ctx context.Context, resp *types.LLMResponse, targetPodName string) {
	// prefill scheduler will never get OnReponse, need to take care of plugin, issue #97
	s.decode.OnResponse(ctx, resp, targetPodName)
}

func (s *Scheduler) pluginsFromConfig(ctx context.Context, pluginsConfig map[string]int) map[plugins.Plugin]int {
	logger := log.FromContext(ctx)

	plugins := map[plugins.Plugin]int{}
	prefixWasAdded := false

	for pluginName, pluginWeight := range pluginsConfig {
		switch pluginName {
		case config.KVCacheScorerName:
			scorer, err := scorer.NewKVCacheAwareScorer(ctx)
			if err == nil {
				plugins[scorer] = pluginWeight
			} else {
				logger.Error(err, "KVCache scorer creation failed")
			}
		case config.LoadAwareScorerName:
			plugins[scorer.NewLoadAwareScorer(ctx)] = pluginWeight
		case config.PrefixScorerName:
			// TODO - create config? based on what? - issue #55
			// use the same instance
			plugins[s.prefixScorer] = pluginWeight
			prefixWasAdded = true
		case config.SessionAwareScorerName:
			plugins[scorer.NewSessionAffinity()] = pluginWeight

		// Plugins from upstream

		case config.GIELeastKVCacheFilterName:
			plugins[k8sfilter.NewLeastKVCacheFilter()] = pluginWeight
		case config.GIELeastQueueFilterName:
			plugins[k8sfilter.NewLeastQueueFilter()] = pluginWeight
		case config.GIELoraAffinityFilterName:
			plugins[k8sfilter.NewLoraAffinityFilter()] = pluginWeight
		case config.GIELowQueueFilterName:
			plugins[k8sfilter.NewLowQueueFilter()] = pluginWeight
		case config.GIESheddableCapacityFilterName:
			plugins[k8sfilter.NewSheddableCapacityFilter()] = pluginWeight
		case config.GIEKVCacheUtilizationScorerName:
			plugins[&k8sscorer.KVCacheScorer{}] = pluginWeight
		case config.K8SPrefixScorerName:
			// For now use the default configuration
			prefixConfig := prefix.Config{
				HashBlockSize:          envutil.GetEnvInt("PREFIX_CACHE_HASH_BLOCK_SIZE", prefix.DefaultHashBlockSize, logger),
				MaxPrefixBlocksToMatch: envutil.GetEnvInt("PREFIX_CACHE_MAX_PREFIX_BLOCKS", prefix.DefaultMaxPrefixBlocks, logger),
				LRUIndexerCapacity:     envutil.GetEnvInt("PREFIX_CACHE_LRU_CAPACITY", prefix.DefaultLRUIndexerCapacity, logger),
			}
			plugins[prefix.New(prefixConfig)] = pluginWeight
		case config.GIEQueueScorerName:
			plugins[&k8sscorer.QueueScorer{}] = pluginWeight
		}
	}

	// only in case pd is enabled and prefix scorer was not enabled for decode scheduler
	// add prefix scorer to list of all scorers to collect information used for decision if PD should be acrivated
	if s.pdEnabled && !prefixWasAdded {
		plugins[s.prefixScorer] = 0.0
	}

	return plugins
}

func (s *Scheduler) generateSchedulerConfig(ctx context.Context, pluginsConfig map[string]int, extraFilters ...plugins.Filter) *scheduling.SchedulerConfig {
	thePlugins := s.pluginsFromConfig(ctx, pluginsConfig)
	preSchedulePlugins := []plugins.PreSchedule{}
	filters := []plugins.Filter{}
	scorers := map[plugins.Scorer]int{}
	postSchedulePlugins := []plugins.PostSchedule{}
	postResponsePlugins := []plugins.PostResponse{}

	filters = append(filters, extraFilters...)

	for plugin, pluginWeight := range thePlugins {
		if preSchedule, ok := plugin.(plugins.PreSchedule); ok {
			preSchedulePlugins = append(preSchedulePlugins, preSchedule)
		}
		if filter, ok := plugin.(plugins.Filter); ok {
			filters = append(filters, filter)
		}
		if scorer, ok := plugin.(plugins.Scorer); ok {
			scorers[scorer] = pluginWeight
		}
		if postSchedule, ok := plugin.(plugins.PostSchedule); ok {
			postSchedulePlugins = append(postSchedulePlugins, postSchedule)
		}
		if postResponse, ok := plugin.(plugins.PostResponse); ok {
			postResponsePlugins = append(postResponsePlugins, postResponse)
		}
	}

	return scheduling.NewSchedulerConfig(
		preSchedulePlugins,
		filters,
		scorers,
		picker.NewMaxScorePicker(),
		postSchedulePlugins,
		postResponsePlugins,
	)
}
