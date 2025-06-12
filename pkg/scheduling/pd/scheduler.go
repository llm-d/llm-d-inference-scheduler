package pd

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	giefilter "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/filter"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/picker"
	gieprofile "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/profile"
	giescorer "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/scorer"
	envutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/datastore"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/profile"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

// CreatePDScheduler returns a new disaggregated Prefill/Decode scheduler, using the provided configuration.
func CreatePDScheduler(ctx context.Context, pdConfig *config.Config, ds datastore.Datastore) (*scheduling.Scheduler, error) {
	loggerDebug := log.FromContext(ctx).WithName("pd-Scheduler").V(logutil.DEBUG)

	// decode profile plugins creation
	decodePlugins := pluginsFromConfig(ctx, pdConfig.DecodeSchedulerPlugins)

	if !pdConfig.PDEnabled { // no PD, create scheduler with SingleProfileHandler (handling only decode profile)
		decodeProfile, err := createDecodeSchedulerProfile(decodePlugins)
		if err != nil {
			return nil, fmt.Errorf("falied to create scheduler - %w", err)
		}
		loggerDebug.Info("Disagregated prefill/decode disabled - scheduler configured to work with decode profile only")
		return scheduling.NewSchedulerWithConfig(ds, scheduling.NewSchedulerConfig(gieprofile.NewSingleProfileHandler(), map[string]*framework.SchedulerProfile{
			"decode": decodeProfile})), nil
	}
	// if we're here, PD is enabled.
	// always initialize prefix scorer, which is used in the decision making of whether PD should be called or not.
	// The prefix scorer is always used in profile handler and should always be used in decode profile even when PD is
	// disabled (in such case add with weight 0).
	prefixConfig := scorer.DefaultPrefixStoreConfig()
	prefixConfig.BlockSize = pdConfig.PrefixBlockSize
	prefixScorer := scorer.NewPrefixAwareScorer(ctx, prefixConfig)

	// in case pd is enabled and prefix scorer was not enabled for decode profile
	// add prefix scorer to list of all scorers to collect information used for the decision if prefill should be called.
	if _, exist := pdConfig.DecodeSchedulerPlugins[config.PrefixScorerName]; !exist {
		decodePlugins = append(decodePlugins, framework.NewWeightedScorer(prefixScorer, 0))
	}
	decodeProfile, err := createDecodeSchedulerProfile(decodePlugins)
	if err != nil {
		return nil, fmt.Errorf("falied to create scheduler - %w", err)
	}

	// prefil profile creation
	prefilProfile := framework.NewSchedulerProfile().
		WithFilters(&filter.PrefillFilter{}).
		WithPicker(picker.NewMaxScorePicker())
	if err := prefilProfile.AddPlugins(pluginsFromConfig(ctx, pdConfig.PrefillSchedulerPlugins)...); err != nil {
		return nil, fmt.Errorf("falied to create prefil scheduler profile - %w", err)
	}

	pdProfileHandler := profile.NewPdProfileHandler(pdConfig.PDThreshold, prefixScorer, ds)
	return scheduling.NewSchedulerWithConfig(ds, scheduling.NewSchedulerConfig(pdProfileHandler, map[string]*framework.SchedulerProfile{
		"decode":  decodeProfile,
		"prefill": prefilProfile,
	})), nil
}

func createDecodeSchedulerProfile(decodePlugins []plugins.Plugin) (*framework.SchedulerProfile, error) {
	decodeProfile := framework.NewSchedulerProfile().
		WithFilters(&filter.DecodeFilter{}).
		WithPicker(picker.NewMaxScorePicker())
	if err := decodeProfile.AddPlugins(decodePlugins...); err != nil {
		return nil, fmt.Errorf("falied to create decode scheduler profile - %w", err)
	}

	return decodeProfile, nil
}

func pluginsFromConfig(ctx context.Context, pluginsConfig map[string]int) []plugins.Plugin {
	logger := log.FromContext(ctx)

	plugins := []plugins.Plugin{}
	for pluginName, pluginWeight := range pluginsConfig {
		switch pluginName {
		case config.KVCacheScorerName:
			if scorer, err := scorer.NewKVCacheAwareScorer(ctx); err == nil {
				plugins = append(plugins, framework.NewWeightedScorer(scorer, pluginWeight))
			} else {
				logger.Error(err, "KVCache scorer creation failed")
			}
		case config.LoadAwareScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(scorer.NewLoadAwareScorer(ctx), pluginWeight))
		case config.SessionAwareScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(scorer.NewSessionAffinity(), pluginWeight))

		// Plugins from upstream

		case config.GIELeastKVCacheFilterName:
			plugins = append(plugins, giefilter.NewLeastKVCacheFilter())
		case config.GIELeastQueueFilterName:
			plugins = append(plugins, giefilter.NewLeastQueueFilter())
		case config.GIELoraAffinityFilterName:
			plugins = append(plugins, giefilter.NewLoraAffinityFilter())
		case config.GIELowQueueFilterName:
			plugins = append(plugins, giefilter.NewLowQueueFilter())
		case config.GIEKVCacheUtilizationScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(&giescorer.KVCacheScorer{}, pluginWeight))
		case config.GIEPrefixScorerName:
			// For now use the default configuration
			prefixConfig := prefix.Config{
				HashBlockSize:          envutil.GetEnvInt("PREFIX_CACHE_HASH_BLOCK_SIZE", prefix.DefaultHashBlockSize, logger),
				MaxPrefixBlocksToMatch: envutil.GetEnvInt("PREFIX_CACHE_MAX_PREFIX_BLOCKS", prefix.DefaultMaxPrefixBlocks, logger),
				LRUIndexerCapacity:     envutil.GetEnvInt("PREFIX_CACHE_LRU_CAPACITY", prefix.DefaultLRUIndexerCapacity, logger),
			}
			plugins = append(plugins, framework.NewWeightedScorer(prefix.New(prefixConfig), pluginWeight))
		case config.GIEQueueScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(&giescorer.QueueScorer{}, pluginWeight))
		}
	}

	return plugins
}
