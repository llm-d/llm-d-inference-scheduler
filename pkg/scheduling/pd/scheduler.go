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
	giefilter "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/filter"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/picker"
	giescorer "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/scorer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	envutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
	externalhttp "github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/plugins/external/http"
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
func NewScheduler(ctx context.Context, schedulerConfig *config.Config, ds Datastore) (*Scheduler, error) {
	prefixConfig := scorer.DefaultPrefixStoreConfig()
	prefixConfig.BlockSize = schedulerConfig.PrefixBlockSize

	scheduler := &Scheduler{
		threshold:    schedulerConfig.PDThreshold,
		pdEnabled:    schedulerConfig.PDEnabled,
		store:        ds,
		prefixScorer: scorer.NewPrefixAwareScorer(ctx, prefixConfig),
	}

	scheduler.prefill = scheduling.NewSchedulerWithConfig(
		ds,
		scheduler.generateSchedulerConfig(ctx, schedulerConfig.PrefillSchedulerPlugins, schedulerConfig.PrefillSchedulerExternalPlugins,
			&filter.PrefillFilter{}),
	)

	scheduler.decode = scheduling.NewSchedulerWithConfig(
		ds,
		scheduler.generateSchedulerConfig(ctx, schedulerConfig.DecodeSchedulerPlugins, schedulerConfig.DecodeSchedulerExternalPlugins,
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
			plugins[giefilter.NewLeastKVCacheFilter()] = pluginWeight
		case config.GIELeastQueueFilterName:
			plugins[giefilter.NewLeastQueueFilter()] = pluginWeight
		case config.GIELoraAffinityFilterName:
			plugins[giefilter.NewLoraAffinityFilter()] = pluginWeight
		case config.GIELowQueueFilterName:
			plugins[giefilter.NewLowQueueFilter()] = pluginWeight
		case config.GIESheddableCapacityFilterName:
			plugins[giefilter.NewSheddableCapacityFilter()] = pluginWeight
		case config.GIEKVCacheUtilizationScorerName:
			plugins[&giescorer.KVCacheScorer{}] = pluginWeight
		case config.GIEPrefixScorerName:
			// For now use the default configuration
			prefixConfig := prefix.Config{
				HashBlockSize:          envutil.GetEnvInt("PREFIX_CACHE_HASH_BLOCK_SIZE", prefix.DefaultHashBlockSize, logger),
				MaxPrefixBlocksToMatch: envutil.GetEnvInt("PREFIX_CACHE_MAX_PREFIX_BLOCKS", prefix.DefaultMaxPrefixBlocks, logger),
				LRUIndexerCapacity:     envutil.GetEnvInt("PREFIX_CACHE_LRU_CAPACITY", prefix.DefaultLRUIndexerCapacity, logger),
			}
			plugins[prefix.New(prefixConfig)] = pluginWeight
		case config.GIEQueueScorerName:
			plugins[&giescorer.QueueScorer{}] = pluginWeight
		}
	}

	// only in case pd is enabled and prefix scorer was not enabled for decode scheduler
	// add prefix scorer to list of all scorers to collect information used for decision if PD should be acrivated
	if s.pdEnabled && !prefixWasAdded {
		plugins[s.prefixScorer] = 0.0
	}

	return plugins
}

func externalFiltersFromConfig(ctx context.Context, info []config.ExternalPluginInfo) []plugins.Filter {
	logger := log.FromContext(ctx)
	filters := make([]plugins.Filter, 0)

	for _, extPluginInfo := range info {
		filters = append(filters, externalhttp.NewFilter(ctx, extPluginInfo.Name, extPluginInfo.URL))
	}

	logger.Info(fmt.Sprintf("Created %d external filters", len(filters)))
	return filters
}

func externalPreSchedulesFromConfig(ctx context.Context, info []config.ExternalPluginInfo) []plugins.PreSchedule {
	logger := log.FromContext(ctx)
	preSchedules := []plugins.PreSchedule{}

	for _, extPluginInfo := range info {
		preSchedules = append(preSchedules, externalhttp.NewPreSchedule(ctx, extPluginInfo.Name, extPluginInfo.URL))
	}

	logger.Info(fmt.Sprintf("Created %d external pre-schedules", len(preSchedules)))
	return preSchedules
}

func externalPostSchedulesFromConfig(ctx context.Context, info []config.ExternalPluginInfo) []plugins.PostSchedule {
	logger := log.FromContext(ctx)
	postSchedules := []plugins.PostSchedule{}

	for _, extPluginInfo := range info {
		postSchedules = append(postSchedules, externalhttp.NewPostSchedule(ctx, extPluginInfo.Name, extPluginInfo.URL))
	}

	logger.Info(fmt.Sprintf("Created %d external post-schedules", len(postSchedules)))
	return postSchedules
}

func externalScorersFromConfig(ctx context.Context, info []config.ExternalPluginInfo) []*giescorer.WeightedScorer {
	logger := log.FromContext(ctx)
	scorers := []*giescorer.WeightedScorer{}

	for _, extPluginInfo := range info {
		scorers = append(scorers, giescorer.NewWeightedScorer(externalhttp.NewScorer(ctx, extPluginInfo.Name, extPluginInfo.URL), extPluginInfo.Weight))
	}

	logger.Info(fmt.Sprintf("Created %d external scorers", len(scorers)))
	return scorers
}

func (s *Scheduler) generateSchedulerConfig(ctx context.Context, pluginsConfig map[string]int,
	externalPlugins config.ExternalPlugins, extraFilters ...plugins.Filter) *scheduling.SchedulerConfig {

	thePlugins := s.pluginsFromConfig(ctx, pluginsConfig)
	preSchedulePlugins := []plugins.PreSchedule{}
	filters := []plugins.Filter{}
	scorers := []*giescorer.WeightedScorer{}
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
			scorers = append(scorers, giescorer.NewWeightedScorer(scorer, pluginWeight))
		}
		if postSchedule, ok := plugin.(plugins.PostSchedule); ok {
			postSchedulePlugins = append(postSchedulePlugins, postSchedule)
		}
		if postResponse, ok := plugin.(plugins.PostResponse); ok {
			postResponsePlugins = append(postResponsePlugins, postResponse)
		}
	}

	// add external plugins
	preSchedulePlugins = append(preSchedulePlugins, externalPreSchedulesFromConfig(ctx, externalPlugins.PreSchedulers)...)
	filters = append(filters, externalFiltersFromConfig(ctx, externalPlugins.Filters)...)
	scorers = append(scorers, externalScorersFromConfig(ctx, externalPlugins.Scorers)...)
	postSchedulePlugins = append(postSchedulePlugins, externalPostSchedulesFromConfig(ctx, externalPlugins.PostSchedulers)...)
	// postResponsePlugins = append(postResponsePlugins, postResponse)

	return scheduling.NewSchedulerConfig().
		WithPreSchedulePlugins(preSchedulePlugins...).
		WithFilters(filters...).
		WithScorers(scorers...).
		WithPicker(picker.NewMaxScorePicker()).
		WithPostSchedulePlugins(postSchedulePlugins...).
		WithPostResponsePlugins(postResponsePlugins...)
}
